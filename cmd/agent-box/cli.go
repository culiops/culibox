package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// commit is stamped at build time: go build -ldflags "-X main.commit=<sha>".
var commit string

const usage = `usage:
  agent-box [serve] [-config <path>]   run the web control-plane
  agent-box doctor  [-config <path>]   check this machine; prints no secrets
`

type cliArgs struct {
	Cmd        string // "serve" | "doctor"
	ConfigPath string
	ConfigSet  bool // -config given explicitly
}

// parseArgs reads argv without the program name. No subcommand means serve, so
// the existing `agent-box -config x.json` invocations (air, the PROD unit) are
// unchanged.
func parseArgs(argv []string) (cliArgs, error) {
	cmd := "serve"
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		cmd, argv = argv[0], argv[1:]
	}
	switch cmd {
	case "serve", "doctor":
	default:
		return cliArgs{}, fmt.Errorf("unknown command %q", cmd)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "path to config file (default: $XDG_CONFIG_HOME or ~/.config /agent-box/config.json, else built-in defaults; never ./config.json)")
	if err := fs.Parse(argv); err != nil {
		return cliArgs{}, err
	}
	if fs.NArg() > 0 {
		return cliArgs{}, errors.New("unexpected arguments: " + strings.Join(fs.Args(), " "))
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "config" })
	return cliArgs{Cmd: cmd, ConfigPath: *path, ConfigSet: set}, nil
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
	ShutdownHTTP  func(context.Context) error
}

// awaitShutdown blocks until a signal arrives on stop, then releases attach
// clients instead of leaving it to SIGHUP. SIGHUP on a closing PTY master
// reaches only that terminal's foreground process group, so a client that is
// not it survives the daemon and spins at 100% CPU forever on a hung-up fd. The
// agents themselves are untouched — they live under abduco and are meant to
// survive a restart. The HTTP server drains last, so Serve returns only after
// the attach clients are gone.
func awaitShutdown(stop <-chan os.Signal, s shutdownSteps, logf func(string, ...any)) {
	sig := <-stop
	logf("received %v: releasing %d attach client(s), agents stay running", sig, s.DetachAll())
	s.StopAnalytics()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.ShutdownHTTP(ctx)
}
