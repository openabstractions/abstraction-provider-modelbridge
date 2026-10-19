package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/listen"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func call(args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	status := run(args, &out, &errs, func(string) string { return "" })
	return status, out.String(), errs.String()
}

func TestRuntimeDesignationComesFromProviderInstallation(t *testing.T) {
	dir := t.TempDir()
	runtimeName := "openabstractions"
	if runtime.GOOS == "windows" {
		runtimeName += ".exe"
	}
	installed := filepath.Join(dir, runtimeName)
	if err := os.WriteFile(installed, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	programs, err := runtimeProgramsAt(filepath.Join(dir, "modelbridged"), "")
	if err != nil || len(programs) != 1 || programs[0] != identity.CanonicalProgramPath(installed) {
		t.Fatalf("installed programs = %v, %v", programs, err)
	}
	if runtime.GOOS == "windows" {
		windowless := filepath.Join(dir, "openabstractionsw.exe")
		if err := os.WriteFile(windowless, []byte("runtime"), 0o600); err != nil {
			t.Fatal(err)
		}
		programs, err = runtimeProgramsAt(filepath.Join(dir, "modelbridged.exe"), "")
		if err != nil || len(programs) != 2 || programs[1] != identity.CanonicalProgramPath(windowless) {
			t.Fatalf("windowless runtime = %v, %v", programs, err)
		}
	}
	explicit := filepath.Join(t.TempDir(), "custom-runtime")
	if err := os.WriteFile(explicit, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	programs, err = runtimeProgramsAt(filepath.Join(dir, "modelbridged"), explicit)
	if err != nil || len(programs) != 1 || programs[0] != identity.CanonicalProgramPath(explicit) {
		t.Fatalf("explicit program = %v, %v", programs, err)
	}
}

// No arguments and help print the same option list, which is how a person
// reads what this command takes.
func TestHelpIsTheDefault(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}, {"serve", "--help"}, {"lends", "--help"}} {
		status, out, errs := call(args...)
		if status != 0 || !strings.Contains(out, "modelbridged lends a model file") || errs != "" {
			t.Fatalf("%v: status %d, stderr %q, stdout %.80q", args, status, errs, out)
		}
		if !strings.Contains(out, "--lemonade-base URL") {
			t.Fatalf("%v: the help does not name --lemonade-base", args)
		}
	}
}

func TestHelpOutputFailureIsNotSuccess(t *testing.T) {
	if status := run([]string{"help"}, failedWriter{}, &bytes.Buffer{}, func(string) string { return "" }); status == 0 {
		t.Fatal("failed help output reported success")
	}
}

func TestMissingArgumentsAreUsageFailures(t *testing.T) {
	for _, c := range []struct{ args []string }{
		{[]string{"serve"}},
		{[]string{"lends"}},
		{[]string{"jump"}},
	} {
		if status, _, _ := call(c.args...); status != 2 {
			t.Errorf("%v: status %d, wanted 2", c.args, status)
		}
	}
}

// The diagnostic prints the ledger as JSON with an LF terminator, so a byte
// comparison reads the same on every platform.
func TestLendsPrintsTheLedgerAsJSON(t *testing.T) {
	state := t.TempDir()
	ledger, err := bridge.OpenLedger(filepath.Join(state, "lends.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Add(wire.Lend{ID: "lend-1", Engine: "lmstudio", Store: "huggingface", Object: "m", Name: "n",
		Link: "/l", Kind: wire.LinkHard, Size: 7, Created: "2026-09-22T00:00:00.000000Z"}); err != nil {
		t.Fatal(err)
	}
	status, out, errs := call("lends", "--state", state)
	if status != 0 || errs != "" {
		t.Fatalf("status %d, stderr %q", status, errs)
	}
	if strings.Contains(out, "\r") || !strings.HasSuffix(out, "\n") {
		t.Fatalf("the diagnostic is not LF-terminated: %q", out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["id"] != "lend-1" || rows[0]["kind"] != "hard" {
		t.Fatalf("the ledger did not survive the read: %v", rows)
	}
}

func TestLendsOutputFailureIsNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  interface{ Write([]byte) (int, error) }
		want string
	}{
		{"write error", failedWriter{}, "output unavailable"},
		{"short write", shortWriter{}, "short write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			status := runLends([]string{"--state", t.TempDir()}, tc.out, &diagnostics)
			if status == 0 || !strings.Contains(diagnostics.String(), tc.want) {
				t.Fatalf("status %d, stderr %q", status, diagnostics.String())
			}
		})
	}
}

// Naming is exercised through a real owned socket/pipe; no provider engine runs.
func TestLeafEndpointNamingAndReceiving(t *testing.T) {
	parent, err := os.MkdirTemp("", "st5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(parent); err != nil {
			t.Error(err)
		}
	})
	if runtime.GOOS != "windows" {
		t.Setenv("TMPDIR", parent)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	name := fmt.Sprintf("st5-leaf-%d", os.Getpid())
	if address, err := serveEndpoint(name); address != "" || !errors.Is(err, bootstrap.ErrNoTrustedInstallation) {
		t.Fatalf("default name escaped test refusal: %q %v", address, err)
	}
	address := filepath.Join(parent, name+".sock")
	if runtime.GOOS == "windows" {
		address = `\\.\pipe\oa-test-` + name
	}

	if actual, err := serveEndpoint(address); err != nil || actual != address {
		t.Fatalf("explicit address changed: %q %v", actual, err)
	}
	for _, explicit := range []string{`/owned/path with spaces/name.sock`, `C:\owned\path with spaces\name.sock`, `\\.\pipe\owned.Name_test`} {
		if actual, err := serveEndpoint(explicit); err != nil || actual != explicit {
			t.Fatalf("explicit %q changed to %q: %v", explicit, actual, err)
		}
	}
	listener, err := listen.Listen(address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopListener := context.AfterFunc(ctx, func() { listener.Close() })
	defer stopListener()
	answered := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			answered <- err
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		var request [1]byte
		if _, err = io.ReadFull(conn, request[:]); err == nil {
			if request[0] != 'q' {
				err = fmt.Errorf("unexpected request %q", request[0])
			} else {
				_, err = conn.Write([]byte{'r'})
			}
		}
		answered <- err
	}()
	conn, err := listen.Dial(address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stopConn := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopConn()
	if _, err = conn.Write([]byte{'q'}); err != nil {
		t.Fatal(err)
	}
	var reply [1]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil || reply[0] != 'r' {
		t.Fatalf("reply %q: %v", reply, err)
	}
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
}
func TestLeafEndpointRefusesBeforeProviderWork(t *testing.T) {
	for _, name := range []string{"model.v1", "model_v1", "Model", "model name"} {
		var output, diagnostics bytes.Buffer
		code := run([]string{"serve", "--endpoint", name}, &output, &diagnostics, func(string) string { return "" })
		if code != 2 || !strings.Contains(diagnostics.String(), "[a-z0-9-]+") || !strings.Contains(diagnostics.String(), "explicit absolute endpoint path") || output.Len() != 0 {
			t.Fatalf("%q: code %d, output %q, diagnostics %q", name, code, output.String(), diagnostics.String())
		}
	}
	if runtime.GOOS != "windows" {
		// A short name resolves in the runtime directory below; test binaries
		// need the opt-in to resolve installed endpoints at all.
		t.Setenv("OA_TEST_ALLOW_INSTALLED_RUNTIME", "1")
		dir := t.TempDir()
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", dir)
		var output, diagnostics bytes.Buffer
		code := run([]string{"serve", "--endpoint", "leaf-v1"}, &output, &diagnostics, func(string) string { return "" })
		if code != 2 || !strings.Contains(diagnostics.String(), "private") || output.Len() != 0 {
			t.Fatalf("namespace: code %d, output %q, diagnostics %q", code, output.String(), diagnostics.String())
		}
	}
}
