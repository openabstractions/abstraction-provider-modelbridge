package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
)

func serviceEndpoint(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\modelbridge-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	return filepath.Join(t.TempDir(), "service.sock")
}

func TestOnlyDesignatedRuntimeCallsLending(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil || runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = identity.CanonicalProgramPath(filepath.Clean(self))
	ledger, err := bridge.OpenLedger(filepath.Join(t.TempDir(), "lends.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, designated string
		wantList         wire.LendsOutcome
		wantLend         wire.LendOutcome
	}{
		{"same account, other program", filepath.Join(t.TempDir(), "another-runtime"), wire.LendsOutcomeNotPermitted, wire.LendOutcomeNotPermitted},
		{"designated runtime", self, wire.LendsOutcomePage, wire.LendOutcomeInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := serviceEndpoint(t)
			h, err := Listen(endpoint, bridge.New(nil, ledger, bridge.Linker{}), tc.designated)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- h.Serve(ctx) }()
			defer func() { cancel(); <-done }()
			client := wire.NewLendingClient(listen.FrameClient{Endpoint: endpoint}.WithDefaults(5*time.Second, MaxFrameBytes))
			page, err := client.Lends("", 1)
			if err != nil || page.Outcome != tc.wantList {
				t.Fatalf("Lends outcome=%s, err=%v; want %s", page.Outcome, err, tc.wantList)
			}
			result, err := client.Lend("", "", "", false)
			if err != nil || result.Outcome != tc.wantLend {
				t.Fatalf("Lend outcome=%s, err=%v; want %s", result.Outcome, err, tc.wantLend)
			}
		})
	}
}

func TestListenRequiresRuntimeProgram(t *testing.T) {
	if h, err := Listen(serviceEndpoint(t), bridge.New(nil, nil, bridge.Linker{})); h != nil || err == nil {
		t.Fatalf("Listen without designation = %v, %v", h, err)
	}
}
