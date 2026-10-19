// Command modelbridged lends a model file one store on this machine holds to
// an engine installed here, by link, and keeps the ledger that says which
// lends are in force. It serves abstraction.storage/lend@1 to the runtime,
// which decides each application's right and forwards. It never moves,
// renames or deletes an original. Of a model's bytes it reads only the first
// four of a file whose name states no model format, to tell GGUF from a
// pickle (MODELBRIDGE-L16, MODELBRIDGE-L21).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-provider-modelbridge/go/service"
)

const usage = `modelbridged lends a model file one store on this machine holds to an engine
installed here. A lend is a symbolic link, a hard link where the operating
system or the volume refuses one, and a copy only when it is asked for. The
original is never moved, renamed or deleted. Every lend is recorded in a
ledger, and the ledger, not the engine's own list, says whether a lend is in
force.

Usage:
  modelbridged serve --endpoint NAME --state DIR [--runtime-program PATH]
                     [--config FILE] [--home DIR]
                     [--lemonade-base URL]
  modelbridged lends --state DIR
  modelbridged help

Commands:
  serve      answer abstraction.storage/lend@1 (Lend, Unlend, Lends) on the
             identity-bound local endpoint NAME, for the designated runtime
             program, until interrupted
  lends      print the ledger, once, as JSON on stdout: a provider diagnostic,
             never an application interface
  help       print this help (also -h, --help, or no arguments)

Options:
  --endpoint NAME       local endpoint name; the platform endpoint is
                        \\.\pipe\openabstractions-NAME on Windows and a socket
                        in a per-account directory elsewhere
  --runtime-program P   absolute path of the runtime executable permitted to
                        call; default is the installed runtime beside this
                        provider (both forms on Windows)
  --state DIR           this provider's own state directory; the ledger and the
                        directory of links lent to Lemonade live in it
  --config FILE         the store configuration abstraction-storage-over-local-stores
                        reads, JSON; default $INVENTORYD_CONFIG
  --home DIR            read this directory as the user's home instead of the real one
  --lemonade-base URL   where Lemonade answers, such as http://localhost:8000.
                        Lending to Lemonade writes extra_models_dir through
                        that server's own POST /internal/set. Without it, and
                        whenever that server does not answer, a lend to
                        lemonade is refused unsupported_engine: this provider
                        never edits another program's configuration file.

Engines: lmstudio (its own models folder, every kind under the one root),
lemonade (an OA-managed directory registered as its extra_models_dir, every
kind under the one root), comfyui (its own models folder, one subfolder per
classified kind: checkpoints, loras, vae, clip, unet, diffusion_models,
controlnet, embeddings, upscale_models, clip_vision, text_encoders; an object
this provider cannot classify into one of those is refused unsupported_engine).

Rights: the runtime decides abstraction.storage/lend on resource engine:<name>
for the calling program before it forwards a call here. This command derives
its runtime designation from the installation or --runtime-program.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		if _, err := io.WriteString(stdout, usage); err != nil {
			return 1
		}
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		if _, err := io.WriteString(stdout, usage); err != nil {
			return 1
		}
		return 0
	case "serve":
		return runServe(args[1:], stdout, stderr, getenv)
	case "lends":
		return runLends(args[1:], stdout, stderr)
	}
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	fmt.Fprintf(stderr, "modelbridged: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// compose builds the bridge one command uses.
func compose(state, config, home, lemonadeBase string, getenv func(string) string) (*bridge.Bridge, error) {
	cfg, err := source.LoadConfig(config)
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	env := source.Env{GOOS: runtime.GOOS, Getenv: getenv, Config: cfg, Home: home}
	if env.Home == "" {
		env.Home = userHome(getenv)
	}
	if env.Home == "" {
		return nil, fmt.Errorf("no home directory; pass --home")
	}
	if state == "" {
		return nil, fmt.Errorf("--state is required")
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	ledger, err := bridge.OpenLedger(filepath.Join(state, "lends.json"))
	if err != nil {
		return nil, err
	}
	engines := []bridge.Engine{
		bridge.LMStudio{Env: env},
		bridge.Lemonade{Directory: filepath.Join(state, "lent", "lemonade"), Base: lemonadeBase},
		bridge.ComfyUI{Env: env},
	}
	return bridge.New(bridge.NewLocalStores(env), ledger, bridge.OSLinker(), engines...), nil
}

func runServe(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if wantsHelp(args) {
		if _, err := io.WriteString(stdout, usage); err != nil {
			return 1
		}
		return 0
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	//unchecked: usage text on a help/error path; a failed write to stderr has nothing further to report
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	endpoint := fs.String("endpoint", "", "")
	runtimeProgram := fs.String("runtime-program", "", "")
	state := fs.String("state", getenv("MODELBRIDGE_STATE"), "")
	config := fs.String("config", getenv("INVENTORYD_CONFIG"), "")
	home := fs.String("home", "", "")
	lemonade := fs.String("lemonade-base", getenv("LEMONADE_BASE"), "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *endpoint == "" {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stderr, "modelbridged serve: --endpoint is required\n\n"+usage)
		return 2
	}
	programs, err := trustedRuntimePrograms(*runtimeProgram)
	if err != nil {
		//unchecked: failed stderr is only a lost diagnostic; exit status 2 still refuses to start the provider
		fmt.Fprintf(stderr, "modelbridged serve: %v\n", err)
		return 2
	}
	b, err := compose(*state, *config, *home, *lemonade, getenv)
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "modelbridged serve: %v\n", err)
		return 2
	}
	host, err := service.Listen(listen.Endpoint(*endpoint), b, programs...)
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "modelbridged serve: %v\n", err)
		return 1
	}
	host.OnError = func(err error) {
		//unchecked: callback has no return path; a failed diagnostic cannot turn a failed call into a success
		fmt.Fprintf(stderr, "modelbridged serve: call: %v\n", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	fmt.Fprintf(stdout, "modelbridged serving abstraction.storage/lend@1 on %s\n", listen.Endpoint(*endpoint))
	if err := host.Serve(ctx); err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "modelbridged serve: %v\n", err)
		return 1
	}
	return 0
}

// trustedRuntimePrograms is fixed by this provider's installation, never by
// the process connecting to its endpoint. An explicit path supports an
// isolated runtime installation and test fixtures.
func trustedRuntimePrograms(configured string) ([]string, error) {
	if configured != "" {
		return runtimeProgramsAt("", configured)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return runtimeProgramsAt(self, "")
}

func runtimeProgramsAt(self, configured string) ([]string, error) {
	var names []string
	if configured != "" {
		names = []string{configured}
	} else {
		dir := filepath.Dir(self)
		name := "openabstractions"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		names = append(names, filepath.Join(dir, name))
		if runtime.GOOS == "windows" {
			names = append(names, filepath.Join(dir, "openabstractionsw.exe"))
		}
	}
	var programs []string
	for _, name := range names {
		if !identity.ValidSubjectProgram(name) || !filepath.IsAbs(name) {
			return nil, fmt.Errorf("--runtime-program must be a clean absolute executable path")
		}
		info, err := os.Stat(name)
		if err != nil {
			if configured == "" && os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("runtime program %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("runtime program %s is not a regular file", name)
		}
		programs = append(programs, identity.CanonicalProgramPath(name))
	}
	if len(programs) == 0 {
		return nil, errors.New("no installed runtime program beside modelbridged")
	}
	return programs, nil
}

func runLends(args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		if _, err := io.WriteString(stdout, usage); err != nil {
			return 1
		}
		return 0
	}
	fs := flag.NewFlagSet("lends", flag.ContinueOnError)
	fs.SetOutput(stderr)
	//unchecked: usage text on a help/error path; a failed write to stderr has nothing further to report
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	state := fs.String("state", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *state == "" {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stderr, "modelbridged lends: --state is required\n\n"+usage)
		return 2
	}
	ledger, err := bridge.OpenLedger(filepath.Join(*state, "lends.json"))
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "modelbridged lends: %v\n", err)
		return 1
	}
	raw, err := marshal(ledger.All())
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "modelbridged lends: %v\n", err)
		return 1
	}
	n, err := stdout.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		//unchecked: diagnostic only; exit status 1 already reports that the ledger export failed
		fmt.Fprintf(stderr, "modelbridged lends: output: %v\n", err)
		return 1
	}
	return 0
}

func userHome(getenv func(string) string) string {
	if runtime.GOOS == "windows" {
		if h := getenv("USERPROFILE"); h != "" {
			return h
		}
	} else if h := getenv("HOME"); h != "" {
		return h
	}
	//unchecked: a failure just leaves h empty, which the caller (compose) already checks and reports as "no home directory"
	h, _ := os.UserHomeDir()
	return h
}
