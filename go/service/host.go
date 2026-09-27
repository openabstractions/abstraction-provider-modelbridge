// Package service serves abstraction.storage/lend@1 on the shared
// identity-bound native transport, for the designated runtime program. The
// runtime decides the right for each application and forwards its call.
package service

import (
	"context"
	"errors"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
)

// MaxFrameBytes bounds one request or reply frame. A page of 256 ledger
// entries fits well inside it.
const MaxFrameBytes = 1 << 20

// callBudget bounds one call. A lend walks every store by stat and may copy a
// file when the caller asked for one.
const callBudget = 5 * time.Minute

// Host serves one Bridge. Every call rechecks the bound peer: an identity the
// transport could not obtain is the error code caller_unavailable, one below
// the Program proof is identity_required, and a bound peer of another account
// reads not_permitted.
type Host struct {
	bridge   *bridge.Bridge
	listener listen.Listener
	owner    string
	programs []string
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	workers  sync.WaitGroup
	slots    chan struct{}

	lifecycle sync.Mutex
	serving   bool
	// OnError, assigned before Serve, hears failed calls.
	OnError func(error)
}

// Listen binds endpoint for b and admits only the named runtime programs.
// A missing designation fails closed before the endpoint is opened.
func Listen(endpoint string, b *bridge.Bridge, programs ...string) (*Host, error) {
	if b == nil {
		return nil, errors.New("modelbridged: bridge required")
	}
	if len(programs) == 0 {
		return nil, errors.New("modelbridged: runtime program required")
	}
	allowed := make([]string, 0, len(programs))
	for _, program := range programs {
		if !identity.ValidSubjectProgram(program) || !filepath.IsAbs(program) {
			return nil, errors.New("modelbridged: runtime program must be an absolute executable path")
		}
		allowed = append(allowed, identity.CanonicalProgramPath(program))
	}
	owner, err := user.Current()
	if err != nil {
		return nil, err
	}
	if owner.Uid == "" {
		return nil, errors.New("modelbridged: service principal unavailable")
	}
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Host{bridge: b, listener: listen.Sessions(l, listen.SessionOptions{MaxSessions: 8}), owner: owner.Uid, programs: allowed, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 8)}, nil
}

// Close stops accepting and ends calls in progress.
func (h *Host) Close() error {
	var err error
	h.once.Do(func() { h.cancel(); err = h.listener.Close() })
	return err
}

// Serve answers calls until ctx ends or Close.
func (h *Host) Serve(ctx context.Context) error {
	h.lifecycle.Lock()
	if h.serving {
		h.lifecycle.Unlock()
		return errors.New("modelbridged: host already served")
	}
	h.serving = true
	h.lifecycle.Unlock()
	//unchecked: Close is idempotent (sync.Once); this async cancellation callback has no caller to report the error to, and Serve's own deferred Close below is the same no-op afterward
	stop := context.AfterFunc(ctx, func() { h.Close() })
	defer stop()
	defer h.workers.Wait()
	defer h.Close()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || h.ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case h.slots <- struct{}{}:
		default:
			//unchecked: dropping a connection because the worker slots are full; nothing here can act on a failed close of the connection it is already refusing
			conn.Close()
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer func() { <-h.slots }()
			defer conn.Close()
			callCtx, cancel := context.WithTimeout(h.ctx, callBudget)
			defer cancel()
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, MaxFrameBytes)
			if call != nil {
				defer call.Close()
			}
			if err == nil {
				dispatcher := wire.LendingDispatcher{Handler: &receiver{host: h, call: call}}
				var reply []byte
				reply, err = dispatcher.ExchangeFrame(call.Frame)
				if err == nil {
					err = call.Reply(reply)
				}
			}
			if err != nil && h.OnError != nil && h.ctx.Err() == nil {
				h.OnError(err)
			}
		}()
	}
}

type receiver struct {
	host *Host
	call *listen.FramedCall
}

// admit returns nil for the service's own account, forbidden for another
// bound account, and a ServiceError when no identity is established.
func (r *receiver) admit() (forbidden bool, err error) {
	peer, err := r.call.Peer()
	if err != nil {
		return false, &wire.ServiceError{Code: wire.ServiceErrorCodeCallerUnavailable}
	}
	if r.call.Recheck() != nil {
		return false, &wire.ServiceError{Code: wire.ServiceErrorCodeCallerUnavailable}
	}
	return r.host.admits(peer)
}

func (h *Host) admits(peer *identity.Peer) (forbidden bool, err error) {
	u, err := peer.User.AtLeast(listen.Program.User)
	if err != nil {
		return false, &wire.ServiceError{Code: wire.ServiceErrorCodeIdentityRequired}
	}
	principal := ""
	switch u.Kind {
	case "windows":
		principal = u.SID
	case "posix":
		principal = strconv.Itoa(u.UID)
	}
	if principal == "" || principal != h.owner {
		return true, nil
	}
	program, err := identity.SubjectProgram(peer, listen.Program.Path)
	if err != nil {
		return false, &wire.ServiceError{Code: wire.ServiceErrorCodeIdentityRequired}
	}
	for _, allowed := range h.programs {
		if program == allowed || runtime.GOOS == "windows" && strings.EqualFold(program, allowed) {
			return false, nil
		}
	}
	return true, nil
}

func (r *receiver) Lend(store, object, engine string, copy_ bool) (wire.LendResult, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return wire.LendResult{Outcome: wire.LendOutcomeNotPermitted, Detail: "runtime program required"}, err
	}
	return r.host.bridge.Lend(store, object, engine, copy_), nil
}

func (r *receiver) Unlend(entry string) (wire.UnlendResult, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeNotPermitted, Detail: "runtime program required"}, err
	}
	return r.host.bridge.Unlend(entry), nil
}

func (r *receiver) Lends(continuation string, limit int64) (wire.LendPage, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return wire.LendPage{Outcome: wire.LendsOutcomeNotPermitted, Lends: []wire.Lend{}}, err
	}
	return r.host.bridge.Lends(continuation, limit), nil
}
