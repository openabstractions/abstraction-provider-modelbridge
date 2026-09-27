package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	if err != nil || len(programs) != 1 || programs[0] != installed {
		t.Fatalf("installed programs = %v, %v", programs, err)
	}
	if runtime.GOOS == "windows" {
		windowless := filepath.Join(dir, "openabstractionsw.exe")
		if err := os.WriteFile(windowless, []byte("runtime"), 0o600); err != nil {
			t.Fatal(err)
		}
		programs, err = runtimeProgramsAt(filepath.Join(dir, "modelbridged.exe"), "")
		if err != nil || len(programs) != 2 || programs[1] != windowless {
			t.Fatalf("windowless runtime = %v, %v", programs, err)
		}
	}
	explicit := filepath.Join(t.TempDir(), "custom-runtime")
	if err := os.WriteFile(explicit, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	programs, err = runtimeProgramsAt(filepath.Join(dir, "modelbridged"), explicit)
	if err != nil || len(programs) != 1 || programs[0] != explicit {
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
