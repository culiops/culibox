package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"
)

// commit is stamped at build time: go build -ldflags "-X main.commit=<sha>".
var commit string

const usage = `usage:
  agent-box [serve] [-config <path>]                   run the web control-plane
  agent-box doctor  [-config <path>]                   check this machine; prints no secrets
  agent-box bug-report [-config <path>]                redacted diagnostic bundle to paste into a bug report (macOS: | pbcopy)
  agent-box box setup [-rebuild] [-recreate [-force]]  build the box image (macOS: also the box VM)
  agent-box box status                                 list boxes (macOS: and the box VM)
  agent-box box stop [-force]                          macOS: stop the box VM
  agent-box clone fetch [-config <path>] [-base <branch>] <profile> <branch>   bring a clone-mode box's branch home as box/<branch>
  agent-box store init|status|unlock|lock|passwd|list [-config <path>]   the encrypted value store
  agent-box store set|rm [-config <path>] <id>         put a value (read from stdin, never argv) or delete one

Everything agent-box writes, its config.json included, lives in one data root:
$AGENT_BOX_HOME (absolute) or ~/.local/share/agent-box.
`

type cliArgs struct {
	Cmd        string // "serve" | "doctor" | "bug-report" | "box" | "clone" | "store"
	Sub        string // box: "setup" | "stop" | "status"; clone: "fetch"; store: "init" | "status" | ... | "list"
	ID         string // store set|rm: the value id
	ConfigPath string
	ConfigSet  bool   // -config given explicitly
	Rebuild    bool   // box setup
	Recreate   bool   // box setup
	Force      bool   // box stop, box setup -recreate
	Profile    string // clone fetch: the clone-mode profile
	Branch     string // clone fetch: the branch in the clone to bring home
	Base       string // clone fetch: the branch in clone_of the range starts after (default main)
}

// storeSubs are the `agent-box store` subcommands. There is no get: a value
// never comes back out through the CLI.
var storeSubs = []string{"init", "status", "unlock", "lock", "passwd", "set", "rm", "list"}

const storeNeedsSub = "store needs a subcommand: init, status, unlock, lock, passwd, set, rm or list"

// parseArgs reads argv without the program name. No subcommand means serve, so
// the existing `agent-box -config x.json` invocations (air, the PROD unit) are
// unchanged.
func parseArgs(argv []string) (cliArgs, error) {
	cmd := "serve"
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		cmd, argv = argv[0], argv[1:]
	}
	sub := ""
	switch cmd {
	case "serve", "doctor", "bug-report":
	case "box":
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return cliArgs{}, errors.New("box needs a subcommand: setup, stop or status")
		}
		sub, argv = argv[0], argv[1:]
		if sub != "setup" && sub != "stop" && sub != "status" {
			return cliArgs{}, fmt.Errorf("unknown box subcommand %q", sub)
		}
	case "clone":
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return cliArgs{}, errors.New("clone needs a subcommand: fetch")
		}
		sub, argv = argv[0], argv[1:]
		if sub != "fetch" {
			return cliArgs{}, fmt.Errorf("unknown clone subcommand %q", sub)
		}
	case "store":
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return cliArgs{}, errors.New(storeNeedsSub)
		}
		sub, argv = argv[0], argv[1:]
		if !slices.Contains(storeSubs, sub) {
			return cliArgs{}, fmt.Errorf("unknown store subcommand %q", sub)
		}
	default:
		return cliArgs{}, fmt.Errorf("unknown command %q", cmd)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "path to config file (default: <data root>/config.json, the root being $AGENT_BOX_HOME or ~/.local/share/agent-box; then $XDG_CONFIG_HOME or ~/.config/agent-box/config.json unless AGENT_BOX_HOME is set; else built-in defaults; never ./config.json)")
	rebuild := fs.Bool("rebuild", false, "box setup: rebuild the image even if it is current")
	recreate := fs.Bool("recreate", false, "box setup: delete and recreate the box VM (macOS)")
	force := fs.Bool("force", false, "box stop, box setup -recreate: go ahead even with boxes running or the VM state unknown")
	base := fs.String("base", "", "clone fetch: the branch in clone_of the fetched range starts after (default main)")
	if err := fs.Parse(argv); err != nil {
		return cliArgs{}, err
	}
	var profile, branch, id string
	if cmd == "store" && (sub == "set" || sub == "rm") {
		// Flags come before the id, as with clone.
		if fs.NArg() != 1 {
			return cliArgs{}, fmt.Errorf("store %s needs exactly one <id> after its flags", sub)
		}
		id = fs.Arg(0)
	} else if cmd == "clone" {
		// Flags come before the two positionals; the flag package stops at the
		// first positional, so a late flag lands here as a third argument.
		if fs.NArg() != 2 {
			return cliArgs{}, errors.New("clone fetch needs exactly <profile> <branch> after its flags")
		}
		profile, branch = fs.Arg(0), fs.Arg(1)
	} else if fs.NArg() > 0 {
		return cliArgs{}, errors.New("unexpected arguments: " + strings.Join(fs.Args(), " "))
	}
	if (*rebuild || *recreate) && sub != "setup" {
		return cliArgs{}, errors.New("-rebuild and -recreate only apply to box setup")
	}
	if *force && sub != "stop" && !(sub == "setup" && *recreate) {
		return cliArgs{}, errors.New("-force only applies to box stop and box setup -recreate")
	}
	set, baseSet := false, false
	fs.Visit(func(f *flag.Flag) {
		set = set || f.Name == "config"
		baseSet = baseSet || f.Name == "base"
	})
	if baseSet && cmd != "clone" {
		return cliArgs{}, errors.New("-base only applies to clone fetch")
	}
	if cmd == "clone" {
		if baseSet && *base == "" {
			return cliArgs{}, errors.New("-base needs a branch name")
		}
		if !baseSet {
			*base = "main"
		}
	}
	return cliArgs{Cmd: cmd, Sub: sub, ConfigPath: *path, ConfigSet: set,
		Rebuild: *rebuild, Recreate: *recreate, Force: *force,
		Profile: profile, Branch: branch, Base: *base, ID: id}, nil
}

// shutdownSignals end the daemon through DetachAll. SIGHUP is the one a laptop
// sends: closing the terminal tab that runs `agent-box serve`.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// shutdownSignalsFor is shutdownSignals minus a SIGHUP the process started
// with ignored: `nohup agent-box serve &` asks to outlive the terminal, and
// signal.Notify would re-enable the SIGHUP that nohup ignored. INT and TERM are
// always registered. ignored is signal.Ignored, injectable for the test.
func shutdownSignalsFor(ignored func(os.Signal) bool) []os.Signal {
	out := make([]os.Signal, 0, len(shutdownSignals))
	for _, s := range shutdownSignals {
		if s == syscall.SIGHUP && ignored(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// shutdownSteps are what serve undoes on a shutdown signal, as functions so the
// signal path is testable without a daemon.
type shutdownSteps struct {
	DetachAll     func() int // release attach clients; returns how many
	StopAnalytics func()
	// StopEgress stops the egress proxy (waiting for every tunnel to be audited)
	// and closes its audit store. Nil when egress control is not running. It runs
	// before the broker: a tunnel in flight is not worth keeping once we drain.
	StopEgress func(context.Context)
	// StopBroker stops the credential broker and closes its audit store. Nil when
	// the broker is not running (no containment layer, or it could not listen).
	StopBroker   func(context.Context)
	ShutdownHTTP func(context.Context) error
}

// awaitShutdown blocks until a signal arrives on stop, then releases attach
// clients instead of leaving it to SIGHUP. SIGHUP on a closing PTY master
// reaches only that terminal's foreground process group, so a client that is
// not it survives the daemon and spins at 100% CPU forever on a hung-up fd. The
// agents themselves are untouched — they live under abduco and are meant to
// survive a restart. The HTTP server drains last, so Serve returns only after
// the attach clients are gone. The broker stops before it: no new credential
// request is served once the console is draining.
func awaitShutdown(stop <-chan os.Signal, s shutdownSteps, logf func(string, ...any)) {
	sig := <-stop
	logf("received %v: releasing %d attach client(s), agents stay running", sig, s.DetachAll())
	s.StopAnalytics()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if s.StopEgress != nil {
		s.StopEgress(ctx)
	}
	if s.StopBroker != nil {
		s.StopBroker(ctx)
	}
	_ = s.ShutdownHTTP(ctx)
}
