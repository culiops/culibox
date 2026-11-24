package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

// commit is stamped at build time: go build -ldflags "-X main.commit=<sha>".
var commit string

// version is the release, stamped at build time from `git describe --tags`
// (scripts/version.sh): go build -ldflags "-X main.version=v0.1.0". Empty
// when unstamped.
var version string

// versionLine is what `culibox version` prints: the stamped release (dev when
// unstamped) and the commit. The release smoke tests and the macOS verify
// script parse "culibox <tag> (<sha>)", so keep the shape.
func versionLine() string {
	v := version
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("culibox %s (%s)\n", v, buildCommit())
}

const usage = `usage:
  culibox [serve] [-config <path>]                     run the web control-plane
  culibox doctor  [-config <path>]                     check this machine; prints no secrets
  culibox bug-report [-config <path>]                  redacted diagnostic bundle to paste into a bug report (macOS: | pbcopy)
  culibox init [-config <path>]                        guided first-time setup: dependencies, folders, VM size, Claude token, box, doctor (needs a terminal)
  culibox box setup [-rebuild] [-recreate] [-force] [-no-prune|-prune-local]   build or pull the box image, then remove the image it replaced and old box images a registry can supply again; -no-prune keeps all (macOS: also the box VM)
  culibox box status                                   list boxes (macOS: and the box VM)
  culibox box stop [-force]                            macOS: stop the box VM
  culibox box share list|add <dir> [-ro]|rm <dir>|apply [-force]   macOS: the folders the box VM shares (apply restarts the VM)
  culibox clone fetch [-config <path>] [-base <branch>] [-allow-old-history] <profile> <branch>   bring a clone-mode box's branch home as box/<branch>; safe while culibox serves (a daemon restart aborts it; rerun)
  culibox store init|status|unlock|lock|passwd|list [-config <path>]   the encrypted value store
  culibox store set|rm [-config <path>] <id>           put a value (read from stdin, never argv) or delete one
  culibox service install|uninstall|status [-config <path>]   run serve as a systemd --user service (Linux) or a LaunchAgent (macOS); loopback only, opt-in
  culibox version                                      print the version and commit

Everything culibox writes, its config.json included, lives in one data root:
$CULIBOX_HOME (absolute; the old name AGENT_BOX_HOME still works) or ~/.local/share/culibox.
A ~/.local/share/agent-box from before the rename is moved there by serve, when that is safe.
`

type cliArgs struct {
	Cmd        string // "serve" | "doctor" | "bug-report" | "init" | "box" | "clone" | "store" | "service" | "version"
	Sub        string // box: "setup" | "stop" | "status" | "share"; clone: "fetch"; store: "init" | "status" | ... | "list"; service: "install" | "uninstall" | "status"
	ID         string // store set|rm: the value id
	ConfigPath string
	ConfigSet  bool // -config given explicitly
	Rebuild    bool // box setup
	Recreate   bool // box setup
	NoPrune    bool // box setup: keep the old box images
	PruneLocal bool // box setup: also remove unused local box images (they cannot be pulled again)
	Force      bool // box stop, box setup, box share apply
	ReadOnly   bool // box share add: share the folder read-only
	// ShareAction is box share's action: list | add | rm | apply; SharePath is
	// the <dir> of add and rm.
	ShareAction string
	SharePath   string
	Profile     string // clone fetch: the clone-mode profile
	Branch      string // clone fetch: the branch in the clone to bring home
	Base        string // clone fetch: the branch in clone_of the range starts after (default main)

	AllowOldHistory bool // clone fetch: let a known stale or unverified history through, loudly

	// Service: serve was started by `culibox service` (the unit passes
	// -service): it refuses a non-loopback bind and password mode at every start.
	Service bool
}

// storeSubs are the `agent-box store` subcommands. There is no get: a value
// never comes back out through the CLI.
var storeSubs = []string{"init", "status", "unlock", "lock", "passwd", "set", "rm", "list"}

// serviceSubs are the `culibox service` subcommands.
var serviceSubs = []string{"install", "uninstall", "status"}

const serviceNeedsSub = "service needs a subcommand: install, uninstall or status"

const storeNeedsSub = "store needs a subcommand: init, status, unlock, lock, passwd, set, rm or list"

// parseArgs reads argv without the program name. No subcommand means serve, so
// the existing `agent-box -config x.json` invocations (air, the PROD unit) are
// unchanged.
func parseArgs(argv []string) (cliArgs, error) {
	// --version is the alias of the version subcommand; it takes nothing either.
	if len(argv) > 0 && (argv[0] == "--version" || argv[0] == "-version") {
		argv = append([]string{"version"}, argv[1:]...)
	}
	cmd := "serve"
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		cmd, argv = argv[0], argv[1:]
	}
	sub := ""
	switch cmd {
	case "serve", "doctor", "bug-report", "init":
	case "version":
		if len(argv) > 0 {
			return cliArgs{}, errors.New("version takes no flags or arguments")
		}
		return cliArgs{Cmd: "version"}, nil
	case "box":
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return cliArgs{}, errors.New("box needs a subcommand: setup, stop, status or share")
		}
		sub, argv = argv[0], argv[1:]
		if sub != "setup" && sub != "stop" && sub != "status" && sub != "share" {
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
	case "service":
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return cliArgs{}, errors.New(serviceNeedsSub)
		}
		sub, argv = argv[0], argv[1:]
		if !slices.Contains(serviceSubs, sub) {
			return cliArgs{}, fmt.Errorf("unknown service subcommand %q", sub)
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
	shareAction := ""
	var sharePos []string
	if cmd == "box" && sub == "share" {
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return cliArgs{}, errors.New(shareNeedsAction)
		}
		shareAction, argv = argv[0], argv[1:]
		if !slices.Contains(shareActions, shareAction) {
			return cliArgs{}, fmt.Errorf("unknown box share action %q", shareAction)
		}
		argv, sharePos = splitShareArgs(argv)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "path to config file (default: <data root>/config.json, the root being $CULIBOX_HOME or ~/.local/share/culibox (an older ~/.local/share/agent-box is used until serve moves it); then $XDG_CONFIG_HOME or ~/.config/culibox/config.json (an older agent-box one is read too) unless CULIBOX_HOME is set; else built-in defaults; never ./config.json)")
	rebuild := fs.Bool("rebuild", false, "box setup: build the image here even if it is current (never pulls the published image)")
	recreate := fs.Bool("recreate", false, "box setup: delete and recreate the box VM (macOS)")
	noPrune := fs.Bool("no-prune", false, "box setup: keep old box images instead of removing the unused ones")
	pruneLocal := fs.Bool("prune-local", false, "box setup: also remove ALL unused box images built here; nothing can pull them again, and another CuliBox on this Docker may pin one")
	force := fs.Bool("force", false, "box stop, box setup, box share apply: go ahead even with boxes running or the VM state unknown")
	ro := fs.Bool("ro", false, "box share add: share the folder read-only")
	base := fs.String("base", "", "clone fetch: the branch in clone_of the fetched range starts after (default main)")
	asService := fs.Bool("service", false, "serve: started by culibox service; refuse a non-loopback bind_addr and password_env")
	allowOld := fs.Bool("allow-old-history", false, "clone fetch: fetch a branch whose history CuliBox cannot verify, or that carries history host main rewrote away (printed as a warning)")
	if err := fs.Parse(argv); err != nil {
		return cliArgs{}, err
	}
	var profile, branch, id, sharePath string
	if cmd == "box" && sub == "share" {
		switch want := shareArgCount[shareAction]; {
		case want == 0 && len(sharePos) > 0:
			return cliArgs{}, fmt.Errorf("box share %s takes no folder", shareAction)
		case want == 1 && len(sharePos) != 1:
			return cliArgs{}, fmt.Errorf("box share %s needs one <dir>", shareAction)
		case want == 1:
			sharePath = sharePos[0]
		}
	} else if cmd == "store" && (sub == "set" || sub == "rm") {
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
	if *noPrune && !(cmd == "box" && sub == "setup") {
		return cliArgs{}, errors.New("-no-prune only applies to box setup")
	}
	if *pruneLocal && !(cmd == "box" && sub == "setup") {
		return cliArgs{}, errors.New("-prune-local only applies to box setup")
	}
	if *noPrune && *pruneLocal {
		return cliArgs{}, errors.New("-no-prune and -prune-local contradict each other")
	}
	if *force && sub != "stop" && sub != "setup" && !(sub == "share" && shareAction == "apply") {
		return cliArgs{}, errors.New("-force only applies to box stop, box setup and box share apply")
	}
	if *ro && !(sub == "share" && shareAction == "add") {
		return cliArgs{}, errors.New("-ro only applies to box share add")
	}
	set, baseSet := false, false
	fs.Visit(func(f *flag.Flag) {
		set = set || f.Name == "config"
		baseSet = baseSet || f.Name == "base"
	})
	if baseSet && cmd != "clone" {
		return cliArgs{}, errors.New("-base only applies to clone fetch")
	}
	if *asService && cmd != "serve" {
		return cliArgs{}, errors.New("-service only applies to serve")
	}
	if *allowOld && cmd != "clone" {
		return cliArgs{}, errors.New("-allow-old-history only applies to clone fetch")
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
		Rebuild: *rebuild, Recreate: *recreate, NoPrune: *noPrune, PruneLocal: *pruneLocal, Force: *force,
		Profile: profile, Branch: branch, Base: *base, ID: id, AllowOldHistory: *allowOld,
		ReadOnly: *ro, ShareAction: shareAction, SharePath: sharePath, Service: *asService}, nil
}

// shareActions are the `agent-box box share` actions, with how many <dir>s each takes.
var shareActions = []string{"list", "add", "rm", "apply"}
var shareArgCount = map[string]int{"list": 0, "add": 1, "rm": 1, "apply": 0}

const shareNeedsAction = "box share needs list, add, rm or apply"

// splitShareArgs separates flags from <dir>s so a flag may come before or after
// the folder (the flag package stops at the first positional). -config takes the
// next word as its value; "--" ends the flags.
func splitShareArgs(argv []string) (flags, pos []string) {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			return flags, append(pos, argv[i+1:]...)
		case a == "-config" || a == "--config":
			flags = append(flags, a)
			if i+1 < len(argv) {
				i++
				flags = append(flags, argv[i])
			}
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	return flags, pos
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

// egressDrainBound is how long a shutdown lets open egress tunnels finish
// (#362). The systemd unit gives the daemon 90 s to stop (TimeoutStopSec in
// internal/service). On darwin it is shorter: launchd sends SIGKILL about 20 s
// after SIGTERM unless the plist sets ExitTimeOut (CuliBox's does not), and the
// steps after the drain take up to about 7 s more before Close audits the
// tunnels it cuts.
func egressDrainBound(goos string) time.Duration {
	if goos == "darwin" {
		return 10 * time.Second
	}
	return 60 * time.Second
}

// shutdownSteps are what serve undoes on a shutdown signal, as functions so the
// signal path is testable without a daemon.
type shutdownSteps struct {
	// DrainEgress lets the boxes' open egress tunnels finish (#362): a restart
	// otherwise cuts the API response an agent is streaming. It runs first, while
	// everything else still works, with a context bounded by DrainBound that a
	// second signal cancels. Nil when egress control is not running.
	DrainEgress   func(context.Context)
	DrainBound    time.Duration // 0 → egressDrainBound(runtime.GOOS)
	DetachAll     func() int    // release attach clients; returns how many
	StopAnalytics func()
	// StopRetention cancels the egress audit pruning job and waits for it, so
	// StopEgress does not close the audit store under a delete. Nil when none runs.
	StopRetention func()
	// StopBundleGC cancels the config-bundle GC and waits for it. Nil when none runs.
	StopBundleGC func()
	// StopImageCheck cancels the daily box image check and waits for it. It makes
	// network calls only, so its order among the others does not matter.
	StopImageCheck func()
	// StopEgress stops the egress proxy (waiting for every tunnel to be audited)
	// and closes its audit store. Nil when egress control is not running. It runs
	// before the broker: a tunnel in flight is not worth keeping once we drain.
	StopEgress func(context.Context)
	// StopBroker stops the credential broker and closes its audit store. Nil when
	// the broker is not running (no containment layer, or it could not listen).
	StopBroker   func(context.Context)
	ShutdownHTTP func(context.Context) error
}

// awaitShutdown blocks until a signal arrives on stop, lets open egress
// tunnels finish (DrainEgress, bounded; a second signal cuts it), then releases
// attach clients instead of leaving it to SIGHUP. SIGHUP on a closing PTY master
// reaches only that terminal's foreground process group, so a client that is
// not it survives the daemon and spins at 100% CPU forever on a hung-up fd. The
// agents themselves are untouched — they live under abduco and are meant to
// survive a restart. The HTTP server drains last, so Serve returns only after
// the attach clients are gone. The broker stops before it: no new credential
// request is served once the console is draining.
func awaitShutdown(stop <-chan os.Signal, s shutdownSteps, logf func(string, ...any)) {
	sig := <-stop
	if s.DrainEgress != nil {
		bound := s.DrainBound
		if bound <= 0 {
			bound = egressDrainBound(runtime.GOOS)
		}
		logf("received %v: letting open egress tunnels finish (up to %s; signal again to cut them now)", sig, bound)
		ctx, cancel := context.WithTimeout(context.Background(), bound)
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		s.DrainEgress(ctx)
		cancel()
	}
	logf("received %v: releasing %d attach client(s), agents stay running", sig, s.DetachAll())
	s.StopAnalytics()
	if s.StopRetention != nil {
		s.StopRetention()
	}
	if s.StopBundleGC != nil {
		s.StopBundleGC()
	}
	if s.StopImageCheck != nil {
		s.StopImageCheck()
	}
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
