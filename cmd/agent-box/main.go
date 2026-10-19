// Command agent-box is a self-hosted web control-plane for managing
// CLI agent sessions. It spawns and owns each agent process on its own PTY
// (no tmux), and serves them in the browser via xterm.js over a WebSocket.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/agent-box/internal/abduco"
	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/analytics"
	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/doctor"
	"github.com/culiops/agent-box/internal/instancelock"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/secrets"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/store"
	"github.com/culiops/agent-box/internal/terminal"
	"github.com/culiops/agent-box/internal/web"
)

// The registry-based transcript locator only runs when the backend can name the
// agent's pid. Assert it at compile time: an interface drift here would silently
// fall back to the cwd heuristic in production.
var _ session.PidResolver = (*abduco.Adapter)(nil)

func main() {
	args, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-box: %v\n%s", err, usage)
		os.Exit(2)
	}
	home, homeErr := serveHome(os.UserHomeDir)
	xdg, abHome := os.Getenv("XDG_CONFIG_HOME"), os.Getenv("AGENT_BOX_HOME")
	cfg, src, cfgErr := config.Resolve(args.ConfigPath, args.ConfigSet, home, xdg, abHome)
	if args.Cmd == "doctor" {
		// home is "" when $HOME is unusable; doctor reports that as a FAIL.
		os.Exit(doctor.Run(os.Stdout, doctor.Input{Config: cfg, Source: src, ConfigErr: cfgErr,
			Home: home, Commit: buildCommit()}))
	}
	if homeErr != nil {
		log.Fatalf("home: %v", homeErr)
	}
	if cfgErr != nil {
		log.Fatalf("config: %v", cfgErr)
	}
	// Resolve only loads; the security invariants (bind, allowed_hosts, socket
	// dir) are enforced here, for every source including built-in defaults.
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if args.Cmd == "box" {
		os.Exit(runBox(args, cfg, home, os.Stdout))
	}
	if args.Cmd == "clone" {
		cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, abHome)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		os.Exit(runClone(args, cfg, home, cfgFiles, os.Stdout))
	}
	log.Printf("config: %s (agent-box %s)", src, buildCommit())
	cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, abHome)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	serve(cfg, home, cfgFiles)
}

// serveHome is the user's home dir, refused when empty or relative: the data
// dir, the profiles file and the config search path are all built from it, and
// would otherwise land relative to wherever serve happened to be started.
func serveHome(userHomeDir func() (string, error)) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("home dir %q is not an absolute path; set $HOME", home)
	}
	return home, nil
}

// tokenVars names the daemon environment variables the static_tokens entries
// read their values from (the `env` reference type), in entry order.
func tokenVars(entries []config.StaticToken) []string {
	var vars []string
	for _, e := range entries {
		if e.Value.Type == "env" && e.Value.Var != "" {
			vars = append(vars, e.Value.Var)
		}
	}
	return vars
}

// profilesPathFor is profiles_path, else profiles.json in the data dir.
func profilesPathFor(cfg config.Config, dataDir string) string {
	if cfg.ProfilesPath != "" {
		return cfg.ProfilesPath
	}
	return filepath.Join(dataDir, "profiles.json")
}

// protectedDirs is every host directory a boxed workspace must not overlap,
// built once for all four gates (spawn, boot check, profile save, picker): the
// data root, and the directory of each config file read on the next start
// (cfgFiles: -config, the ~/.config fallback) and of the profiles file
// (profiles_path may be anywhere). Both files are read through symlinks, so
// every hop's directory and the target's count (box.FileDirs). A box that
// could write any of them could change the binaries agent-box runs. personalDir
// is the default account's config dir (~/.claude): host sessions run its hooks
// and inheriting accounts copy its settings, so a box must not write there.
func protectedDirs(dataDir string, cfgFiles []string, profilesPath, personalDir string) []box.ProtectedDir {
	out := []box.ProtectedDir{box.DataDirProtected(dataDir), box.PersonalConfigDirProtected(personalDir)}
	for _, f := range cfgFiles {
		for _, d := range box.FileDirs(f) {
			out = append(out, box.ConfigDirProtected(d))
		}
	}
	for _, d := range box.FileDirs(profilesPath) {
		out = append(out, box.ProfilesDirProtected(d))
	}
	return box.DedupeProtected(out)
}

// warnLeftBehindProfiles: before profiles.json followed the data dir it always
// sat in the default root. A config with a custom data_dir and no profiles_path
// would otherwise start with only the seed profiles and say nothing.
func warnLeftBehindProfiles(cfg config.Config, home, profilesPath string) {
	if cfg.ProfilesPath != "" {
		return
	}
	old, err := config.Root(home, "")
	if err != nil {
		return
	}
	old = filepath.Join(old, "profiles.json")
	if old == profilesPath {
		return
	}
	if _, err := os.Stat(profilesPath); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if _, err := os.Stat(old); err == nil {
		log.Printf("profiles: %s does not exist but %s does (profiles used to live there); move it, or set profiles_path", profilesPath, old)
	}
}

// buildCommit is the stamped commit, else the VCS revision Go embedded, else
// "unknown" (a source tarball carries no .git).
func buildCommit() string {
	if commit != "" {
		return commit
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "unknown"
}

func serve(cfg config.Config, home string, cfgFiles []string) {
	// Absolute and clean: the darwin socket dir is a hash of this string, so two
	// spellings of one dir must not yield two socket dirs.
	dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
	if err != nil {
		log.Fatalf("data dir: %v", err)
	}
	// One instance per data dir, and the port is ours, BEFORE the orphan sweep
	// and reconcile below: a second serve (a forgotten terminal tab) would
	// otherwise kill the live instance's attach clients and adopt its sessions,
	// and only then fail on the busy port.
	release, err := instancelock.Acquire(dataDir)
	if err != nil {
		log.Fatalf("%v (%s)", err, dataDir)
	}
	// The deferred call keeps the lock file reachable for the whole of serve: an
	// unreachable *os.File is closed by its finalizer, which would drop the flock.
	defer release()
	ln, err := net.Listen("tcp", cfg.BindAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.BindAddr, err)
	}
	if w := bindWarning(cfg); w != "" {
		log.Printf("WARNING: %s", w)
	}

	// The login secret, AFTER the lock: in token mode the first start writes
	// login.token, and two concurrent first starts would race on it. Password
	// mode reads the env var named by password_env; token mode generates
	// <dataDir>/login.token. Login is password-only — there is no username.
	login, err := auth.ResolveLogin(cfg, dataDir)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	if login.Mode == auth.ModePassword && auth.WeakPassword(login.Secret) {
		log.Printf("WARNING: the login password (env %s) is shorter than 12 characters", cfg.PasswordEnv)
	}

	resolver, err := secrets.New(cfg.SecretSources)
	if err != nil {
		log.Fatalf("secrets: %v", err)
	}
	// Seed a profile for the user's real $SHELL unless a configured agent already
	// runs that binary, so a quick shell works out of the box. Virtual seed: it is
	// recomputed every boot and never written to the profile store.
	agents, shellProfile := config.WithDetectedShell(cfg.Agents)
	if shellProfile == "" {
		log.Printf("quick shell disabled: every candidate profile name is taken by another binary")
	}

	// Surface each agent's effective base so the clean-by-default posture is
	// visible at startup (an existing config's agents now default to clean).
	for _, a := range agents {
		log.Printf("agent %q: base=%s runtime=%s", a.Name, a.BasePolicy(), cfg.RuntimeFor(a))
	}

	lnch := launcher.New(agents, resolver, cfg.Options)
	lnch.SetDefaultRuntime(cfg.DefaultRuntime)
	// A token entry's env-typed value variable lives in the daemon's env for the
	// broker to read: like the password, it must never reach a session through
	// an inherited base.
	lnch.SetDaemonSecretEnv(append([]string{cfg.PasswordEnv}, tokenVars(cfg.StaticTokens)...)...)
	lnch.SetStaticTokens(cfg.StaticTokens)
	if n := len(cfg.StaticTokens); n > 0 {
		log.Printf("static_tokens: %d entries (read at start; a change needs a restart)", n)
	}

	// Agent accounts: accounts.json lives inside the data dir (the store writes it
	// through os.Root). The default account is the agent's own config dir.
	accts, err := account.Open(filepath.Join(dataDir, "accounts.json"), dataDir, bundle.DefaultSource())
	if err != nil {
		log.Fatalf("accounts: %v", err)
	}
	defer accts.Close()
	lnch.SetAccounts(accts)

	// Default working dir: config value if set, else $HOME.
	defaultCwd := cfg.DefaultCwd
	if defaultCwd == "" {
		defaultCwd = home
	}

	// Cookie-signing secret, persisted under dataDir so a daemon restart does not
	// invalidate live sessions (the session cookie is signed with it; rotating
	// the file is "log out everywhere").
	cookie, err := auth.NewCookieAuth(filepath.Join(dataDir, "cookie.secret"))
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	// Bearer access tokens (named, revocable) for programmatic/native clients.
	tokens, err := auth.OpenTokenStore(filepath.Join(dataDir, "tokens.json"))
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	// Browser session cookie, signed with the persisted secret. 30-day sliding.
	sess := auth.NewSession(cookie.Secret(), 30*24*time.Hour)
	abducoBin := cfg.AbducoBin
	if abducoBin == "" {
		abducoBin = "abduco"
	}
	// Session persistence via abduco: agents run under abduco so they survive a
	// daemon restart. abduco is a hard dependency — fail fast if it is missing.
	if _, err := exec.LookPath(abducoBin); err != nil {
		log.Fatalf("abduco not found on PATH (%q): session persistence requires it; install abduco or set abduco_bin", abducoBin)
	}
	// Linux keeps <dataDir>/abduco; darwin defaults to a short per-data-dir dir
	// in /tmp, because its 103-byte socket path limit cannot afford the data dir.
	socketDir := abduco.SocketDirFor(cfg.AbducoSocketDir, runtime.GOOS, dataDir, os.Getuid())
	checkDir := runtime.GOOS == "darwin"
	if checkDir {
		// /tmp is world-writable: refuse a dir someone else created first. Before
		// abduco.New, whose MkdirAll would accept a squatted dir as is.
		if err := abduco.PrepareSocketDir(socketDir, os.Getuid()); err != nil {
			log.Fatalf("abduco: %v", err)
		}
	}
	adapter, err := abduco.New(abducoBin, socketDir, filepath.Join(dataDir, "sessions", "pids"))
	if err != nil {
		log.Fatalf("abduco: %v", err)
	}
	// Every spawn re-validates the dir it was prepared as, in case it vanished
	// and another user re-created it.
	adapter.CheckDir = checkDir
	// The longest profile name (the quick shell included) must fit in a socket
	// path, or every spawn of it would fail; profiles stored later are checked at
	// spawn. Same user and host values the adapter's own spawn check uses.
	profileNames := make([]string, 0, len(agents))
	for _, a := range agents {
		profileNames = append(profileNames, a.Name)
	}
	if err := abduco.CheckProfileSocketPaths(socketDir, adapter.User(), adapter.Host(), profileNames); err != nil {
		log.Fatalf("abduco: %v", err)
	}
	// Sweep before Reconcile, while every attach client for our socket dir is by
	// definition someone else's leftover. A previous daemon that died by SIGKILL
	// or panic leaves clients whose PTY master is gone; if such a client was not
	// its terminal's foreground process group it never got SIGHUP, and abduco's
	// read loop then spins at 100% CPU indefinitely.
	if n := adapter.SweepOrphanAttachers(); n > 0 {
		log.Printf("swept %d orphaned abduco attach client(s) from a previous daemon", n)
	}

	mgr := session.NewManager(lnch, adapter, 128*1024, 5*time.Minute)
	mgr.SetPaths(home, dataDir)               // where transcripts live: host ~/.claude, boxed <dataDir>/boxstate/<box>
	mgr.SetRepliesEnabled(cfg.RepliesEnabled) // before Reconcile: restore reads it
	if err := mgr.SetSidecarPath(filepath.Join(dataDir, "sessions.json")); err != nil {
		log.Printf("session sidecar unreadable (%v); starting without restart-recovery metadata", err)
	}

	// Containment layer. Installed BEFORE session reconcile so reattached boxed
	// sessions can adopt their boxes; box reconcile runs AFTER, so the orphan
	// reaper never stops a container a live session is running inside.
	boxRuntime := ""
	var layer *boxLayer
	var boxes *box.Manager
	protected := protectedDirs(dataDir, cfgFiles, profilesPathFor(cfg, dataDir), bundle.DefaultSource())
	if l, err := newBoxLayer(cfg, dataDir, home, runtime.GOOS, boxvm.ExecRunner{}, protected); err != nil {
		// Not fatal: without containment the console still runs, on the host
		// tier, and says so rather than pretending to be contained.
		log.Printf("containment unavailable (%v); every session will run on the host tier", err)
	} else {
		layer, boxes = l, l.boxes
		mgr.SetBoxes(boxes)
		boxRuntime = boxes.Runtime()
		if l.vm != nil {
			// The VM is not started here (D3): the first boxed spawn starts it.
			log.Printf("containment ready (macOS box VM in %s, starts on the first boxed spawn): image %s, idle TTL %s",
				l.vm.Config().ColimaHome(), cfg.BoxImageOrDefault(), cfg.BoxIdleTTL())
		} else {
			log.Printf("containment ready: image %s, isolation runtime %s, idle TTL %s",
				cfg.BoxImageOrDefault(), boxRuntime, cfg.BoxIdleTTL())
		}
	}

	// Config bundle: what a boxed agent is allowed to see of the user's config.
	// bundleSrc stays empty with the bundle off or no layer; clone mode reads it
	// below as the memory root.
	bundleSrc := ""
	if layer != nil && cfg.BundleSource != "off" {
		src := bundle.ExpandTilde(cfg.BundleSource)
		if src == "" {
			src = bundle.DefaultSource()
		}
		bundleSrc = src
		// Root is under the layer's state root: on macOS the symlink-resolved
		// data root, the spelling the VM mounts, since the bundle is mounted
		// into boxes.
		bm, err := bundle.New(bundle.Options{
			Source:  src,
			Root:    filepath.Join(layer.stateRoot, "bundle"),
			MCPFile: bundle.DefaultMCPFile(src),
			Prober:  layer.prober(cfg.BoxImageOrDefault()),
			// Probed with the user's MCP commands, so a token entry's server is
			// only offered to a session when the image can run it.
			EntryCommands: entryMCPCommands(cfg.StaticTokens),
		})
		if err != nil {
			log.Printf("config bundle disabled (%v); boxed sessions will run without user config", err)
		} else {
			mgr.SetBundles(bundle.NewProvider(bm))
			log.Printf("config bundle source: %s", src)
		}
	}

	// Clone mode (Linux only). The profile list is read through the launcher on
	// every call, so a profile saved after boot is covered. Before Reconcile,
	// like the box hooks. Hooks are installed on Linux even when clone creation
	// is unavailable (no layer, no git): the host-tier box-owned guard needs
	// neither. The interface must never hold a nil *cloneHooks, so SetClone runs
	// only for a real value.
	h, err := newCloneHooks(runtime.GOOS, layer, home, bundleSrc, dataDir, protected, lnch.Clones)
	if err != nil {
		log.Printf("clone mode unavailable (%v); clone-mode profiles are refused", err)
	}
	if h != nil { // non-nil on Linux even when unavailable: the box-owned guard stays on
		mgr.SetClone(h)
	}

	// Credential broker (M3a): only with containment, because a bound session
	// runs only in a box. Before Reconcile so reattached sessions find the hooks.
	var brokerSvc *brokerService
	if layer != nil {
		svc, err := startBroker(dataDir, layer.stateRoot, runtime.GOOS, cfg.BrokerPortOrDefault(),
			mgr.Get, web.ManagerService{M: mgr}.Get, cfg.GitHubApps, cfg.StaticTokens)
		switch {
		case errors.Is(err, errBrokerListen):
			log.Printf("%v (profiles with an AWS binding will refuse to spawn)", err)
		case err != nil:
			log.Fatalf("%v", err)
		default:
			brokerSvc = svc
			mgr.SetBroker(svc.Hooks)
			log.Printf("credential broker listening on %s", svc.Hooks.EP.BoxURL)
		}
	}

	// Egress control: on Linux every box reaches the network only through the
	// allowlist proxy (bound or not), so it starts without the broker. Not fatal
	// when it cannot start: boxed spawns then refuse, and the rest of the console
	// keeps working. Before Reconcile, and the box hooks before it too, so
	// reattached boxes find them.
	var egressSvc *egressService
	if layer != nil {
		if runtime.GOOS == "linux" {
			// Set whether or not egress starts: without it every boxed spawn is refused.
			mgr.SetEgressAll(true)
		}
		svc, err := startEgress(dataDir, layer, cfg, runtime.GOOS, mgr.Get)
		switch {
		case err != nil && runtime.GOOS == "linux":
			log.Printf("egress control disabled (%v); every boxed spawn will be refused", err)
		case err != nil:
			log.Printf("egress control disabled (%v); profiles with an AWS binding will refuse to spawn", err)
		default:
			egressSvc = svc
			mgr.SetEgress(svc.Svc)
			boxes.SetEgress(box.Egress{Network: svc.Svc.Network(), Verify: layer.egressVerify(svc.Svc)})
			log.Printf("egress control: network %s, port %d", svc.Svc.Network(), cfg.EgressPortOrDefault())
		}
	}

	// Release attach clients on the way out instead of leaving it to SIGHUP (see
	// awaitShutdown). SIGHUP itself is one of the signals: closing the terminal
	// tab that runs `agent-box serve` must take this path too, unless serve was
	// started under nohup. Registered here, not earlier or later: Reconcile is
	// what creates attach clients, so a signal from here on must not take the
	// default disposition and orphan them; everything before it stays
	// interruptible by ^C. The buffered signal is handled once awaitShutdown
	// runs below; a Shutdown before Serve makes Serve return ErrServerClosed.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, shutdownSignalsFor(signal.Ignored)...)
	if signal.Ignored(syscall.SIGHUP) {
		log.Printf("SIGHUP is ignored (nohup): closing the terminal will not stop agent-box")
	}

	if err := mgr.Reconcile(); err != nil {
		log.Printf("session reconcile: %v (continuing; some sessions may not have been recovered)", err)
	}
	// AFTER session reconcile: that pass adopts every surviving box, so what
	// remains unclaimed here is genuinely orphaned.
	if layer != nil {
		// On darwin with the VM stopped this waits for the first boxed spawn: it
		// never starts the VM, and an unreachable docker must not read as "no
		// boxes".
		layer.bootReconcile(context.Background())
		if egressSvc != nil {
			warnBridgeBoxes(context.Background(), layer.cli, cfg.BoxInstance(), egressSvc.Svc.Network())
		}
		go func() {
			t := time.NewTicker(24 * time.Hour)
			defer t.Stop()
			for range t.C {
				layer.dailyReclaim(context.Background())
			}
		}()
		// Reconcile covers a daemon restart only. The sweep covers what happens
		// after it: a docker or VM restart leaves stopped boxes, and a missed
		// release leaves a counter that keeps a box alive. Every 10 minutes is
		// longer than the 5-minute session retention, so an exited session's
		// late release cannot be seen twice as drift.
		go func() {
			t := time.NewTicker(10 * time.Minute)
			defer t.Stop()
			for range t.C {
				sweepBoxes(boxes, mgr)
			}
		}()
	}

	// Runtime profile store (config.Agents are read-only seeds; the store holds
	// UI-created/edited profiles). The manager pushes the merged set to the launcher.
	profilesPath := profilesPathFor(cfg, dataDir)
	warnLeftBehindProfiles(cfg, home, profilesPath)
	if err := os.MkdirAll(filepath.Dir(profilesPath), 0o700); err != nil {
		log.Fatalf("profiles dir: %v", err)
	}
	st, err := store.Open(profilesPath)
	if err != nil {
		// A corrupt/hand-edited store must not brick startup: quarantine it and
		// start with an empty store.
		log.Printf("profiles store %q unreadable (%v); quarantining to %s.corrupt and starting empty",
			profilesPath, err, profilesPath)
		_ = os.Rename(profilesPath, profilesPath+".corrupt")
		st, err = store.Open(profilesPath)
		if err != nil {
			log.Fatalf("profiles store: %v", err)
		}
	}
	pm := web.NewProfileManager(agents, cfg.Options, cfg.DefaultRuntime, protected, st, lnch.SetAgents)
	pm.SetCloneCheck(cloneSaveCheck(h, home, protected))
	pm.SetGitHubApps(cfg.GitHubApps)
	pm.SetStaticTokens(cfg.StaticTokens)
	pm.SetAccounts(accts) // without it a profile naming an account cannot be saved
	// Profiles are now registered with the launcher; backfill Bin for sessions
	// reconciled at startup (before profiles were known) so per-window usage works
	// for profile-based claude sessions that survived a daemon restart.
	mgr.ResolveBins()

	rateLimitsFile := cfg.RateLimitsFile
	if rateLimitsFile == "" {
		rateLimitsFile = filepath.Join(home, ".claude", "abtop-rate-limits.json")
	}
	api := &web.API{Sessions: web.ManagerService{M: mgr}, DefaultCwd: defaultCwd,
		ProfileCwd: func(name string) string {
			p, ok := pm.Get(name)
			if !ok {
				return ""
			}
			return strings.TrimSpace(p.DefaultCwd)
		}}
	var stopBroker, stopEgress func(context.Context)
	if brokerSvc != nil {
		status := brokerSvc.Status // only when running: a nil service must read as "no broker", not a typed nil
		if egressSvc != nil {
			status.Egress = egressSvc.Stats // nil otherwise: no egress object in the reply
		}
		stopBroker = brokerSvc.Stop
		if boxes != nil {
			es := elevationService{brk: brokerSvc.Hooks.B, get: mgr.Get, list: mgr.List, blocker: boxes.GrantBlocker}
			if brokerSvc.SSO != nil { // a nil *Runner must stay a nil interface
				es.runner = brokerSvc.SSO
			}
			brokerSvc.Hooks.B.SetClaimCheck(es.claimCheck)
			api.Elevation = es
			status.Elevation = brokerSvc.Hooks.B.Elevation
			status.SSO = es.ssoStatus
			status.Grant = es.grantContext
		}
		api.AWS = status
	}
	if egressSvc != nil {
		stopEgress = egressSvc.Stop
		api.Egress = web.EgressStatusAdapter{Get: web.ManagerService{M: mgr}.Get, Stats: egressSvc.Stats}
	}
	srv := &web.Server{
		API:        api,
		Session:    sess,
		Tokens:     tokens,
		Password:   login.Secret,
		Profiles:   pm,
		Home:       home,
		Protected:  protected,
		Terminal:   terminal.NewAttachHandler(mgr.Get),
		BoxRuntime: boxRuntime,

		ShellProfile: shellProfile,
		ShellBin:     config.DetectShell(),

		RateLimitsFile: rateLimitsFile,

		Accounts: accts,
		SecretOK: func(n string) bool { _, ok, err := resolver.Resolve(n); return ok && err == nil },
	}
	configureServer(srv, cfg)

	// Analytics: durable token/prompt rollups mined from the Claude Code
	// transcripts. Enabled by default; only an explicit config false turns it off.
	// Roots default to ~/.claude/projects (all host + direct usage — the bulk) plus
	// this instance's boxed-session and agent-account transcript dirs.
	var analyticsSvc *analytics.Service
	if cfg.Analytics.On() {
		dbPath := cfg.Analytics.DBPath
		if dbPath == "" {
			dbPath = filepath.Join(dataDir, "analytics.db")
		}
		roots := cfg.Analytics.Roots
		if len(roots) == 0 {
			// Glob patterns, expanded on every ingest (not at boot) so boxes
			// and agent accounts created while the daemon runs are picked up.
			roots = []string{
				filepath.Join(home, ".claude", "projects"),
				filepath.Join(dataDir, "boxstate", "*", ".claude", "projects"),
				filepath.Join(dataDir, "accounts", "*", "claude", "projects"),
			}
		}
		interval := 24 * time.Hour
		if cfg.Analytics.IngestInterval != "" {
			if d, perr := time.ParseDuration(cfg.Analytics.IngestInterval); perr == nil && d > 0 {
				interval = d
			}
		}
		if svc, aerr := analytics.New(dbPath, roots, interval); aerr != nil {
			log.Printf("analytics disabled: %v", aerr)
		} else {
			analyticsSvc = svc
			srv.Analytics = svc
			log.Printf("analytics: db %s, %d root(s), ingest every %s", dbPath, len(roots), interval)
		}
	}

	// A default workspace that can never be contained must fail at boot, not once
	// per spawn: the spawn-time error is correct but arrives after every click.
	if boxes != nil {
		anyBoxed := false
		for _, a := range agents {
			if cfg.RuntimeFor(a) == config.RuntimeBox {
				anyBoxed = true
			}
		}
		if anyBoxed {
			// The manager's own rule, so on macOS it includes "under the box
			// VM's shared directories".
			if reason, bad := boxes.Unboxable(defaultCwd); bad {
				log.Printf("WARNING: default_cwd %q cannot host a contained session (%s). "+
					"Boxed spawns must name a project directory; set default_cwd to one, "+
					"or set runtime \"host\" on your agents.", defaultCwd, reason)
			}
		}
	}

	// Report what the console will actually offer. WithDetectedShell only sees the
	// config seeds, so a STORED profile shadowing the seed name leaves shellProfile
	// set here while every /api/meta answers "" — without this line the button just
	// silently never appears.
	if effective := srv.QuickShell(); effective != shellProfile {
		log.Printf("quick shell disabled: profile %q no longer runs the detected shell %s "+
			"(a stored profile has taken the name)", shellProfile, config.DetectShell())
	} else if effective != "" {
		log.Printf("quick shell: profile %q → %s", effective, config.DetectShell())
	}

	// Start the analytics ingester (initial pass + daily ticker) in the background;
	// its context is cancelled on shutdown below.
	analyticsCtx, analyticsCancel := context.WithCancel(context.Background())
	defer analyticsCancel()
	if analyticsSvc != nil {
		go analyticsSvc.Start(analyticsCtx)
	}

	// No WriteTimeout, deliberately: on macOS the first boxed spawn holds its
	// POST while the box VM starts (20-30 s, see boxLayer.prepare), and the
	// UI's spawn request has no deadline either. Adding a write timeout here
	// would turn that cold start into a failed spawn.
	httpSrv := &http.Server{Handler: srv.Handler()}

	// Release attach clients on the way out (stop is registered before
	// Reconcile, above); the signal waits in the buffer until this runs.
	go awaitShutdown(stop, shutdownSteps{
		DetachAll: mgr.DetachAll,
		StopAnalytics: func() {
			analyticsCancel()
			if analyticsSvc != nil {
				_ = analyticsSvc.Close()
			}
		},
		StopEgress:   stopEgress,
		StopBroker:   stopBroker,
		ShutdownHTTP: httpSrv.Shutdown,
	}, log.Printf)

	log.Printf("agent-box listening on %s (allowed hosts: %s)", cfg.BindAddr, strings.Join(cfg.HostAllowlist(), ", "))
	if login.Mode == auth.ModeToken {
		// The only place the token is ever printed.
		url, hint := loginURLFor(cfg, login.Secret)
		if hint != "" {
			hint = "\n           (" + hint + ")"
		}
		fmt.Fprintf(os.Stderr, "\nagent-box: open %s%s\n           (token stored in %s)\n\n",
			url, hint, login.TokenPath)
	}
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("agent-box stopped")
}

// configureServer sets the web server fields that come straight from config.
// The Host allowlist above all: left unset, the guard refuses every request.
func configureServer(srv *web.Server, cfg config.Config) {
	archiveBytes, archiveFiles := cfg.ArchiveLimits()
	srv.AllowedHosts = cfg.HostAllowlist()
	srv.EnvLabel = cfg.EnvLabel
	srv.RepliesEnabled = cfg.RepliesEnabled
	srv.EnvColor = cfg.EnvColor
	srv.MaxUploadBytes = cfg.UploadLimit()
	srv.MaxArchiveBytes = archiveBytes
	srv.MaxArchiveFiles = archiveFiles
}

// wildcardBind reports a bind_addr that listens on every interface (only
// possible with allow_public).
func wildcardBind(bind string) (port string, ok bool) {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return "", false
	}
	switch host {
	case "", "0.0.0.0", "::":
		return port, true
	}
	return port, false
}

// bindWarning is a startup warning for a config that will refuse every browser:
// a wildcard bind is allowlisted only as its literal "0.0.0.0:P", which no
// browser sends as a Host header.
func bindWarning(cfg config.Config) string {
	if _, ok := wildcardBind(cfg.BindAddr); ok && len(cfg.AllowedHosts) == 0 {
		return fmt.Sprintf("bind_addr %s listens on every interface but allowed_hosts is empty: "+
			"every browser request will be refused (403) until allowed_hosts lists the host:port you open", cfg.BindAddr)
	}
	return ""
}

// loginURLFor is the token-mode login URL to print. A wildcard bind is not an
// address a browser can open (and its literal is no Host the guard accepts), so
// it uses the first allowlisted host on the bind's port, else a placeholder and
// a hint saying what to put there.
func loginURLFor(cfg config.Config, token string) (url, hint string) {
	port, ok := wildcardBind(cfg.BindAddr)
	if !ok {
		return auth.LoginURL(cfg.BindAddr, token), ""
	}
	for _, h := range cfg.HostAllowlist() {
		if host, p, err := net.SplitHostPort(h); err == nil && p == port {
			if _, wild := wildcardBind(h); !wild && host != "" {
				return auth.LoginURL(h, token), ""
			}
		}
	}
	return auth.LoginURL("<host>:"+port, token),
		"replace <host> with this machine's address and add it as <host>:" + port + " to allowed_hosts"
}

// sweepBoxes runs one box sweep and logs every action with its reason. A sweep
// skipped because the daemon is unreachable logs nothing: that is the expected
// state whenever the VM is stopped, and it must not fill the log.
//
// The pass is bounded: it holds the box manager's operation lock, so a hung
// daemon must not be able to hold it (and every spawn) indefinitely.
func sweepBoxes(boxes *box.Manager, mgr *session.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rep, err := boxes.Sweep(ctx, mgr.LiveBoxCounts())
	for _, a := range rep.Removed {
		log.Printf("box sweep: removed %s: %s", a.Box, a.Reason)
	}
	for _, a := range rep.Corrected {
		log.Printf("box sweep: corrected %s: %s", a.Box, a.Reason)
	}
	if err != nil {
		log.Printf("box sweep: %v", err)
	}
}

// reclaimBoxState removes the host homes of boxes that are gone and unused, and
// reports what it freed. internal/box returns results rather than logging so its
// decisions stay testable without a daemon; the printing belongs here.
func reclaimBoxState(ctx context.Context, boxes *box.Manager) {
	done, err := boxes.ReclaimState(ctx, box.DefaultStateTTL)
	for _, r := range done {
		log.Printf("reclaimed box state %s: unused for %d days, %d MiB freed",
			r.Box, int(r.Age.Hours()/24), r.Bytes/(1024*1024))
	}
	if err != nil {
		log.Printf("box state reclaim: %v. The home stays; usually a directory a box "+
			"left unwritable (chmod it back), otherwise an old root-owned stub that has "+
			"to be removed as its owner. New boxes create neither.", err)
	}
}

// entryMCPCommands lists what the token entries' MCP servers run in the box:
// agentbox-env, which launches them, and each entry's own command. Empty when
// no entry declares a server, so such a daemon probes nothing extra.
func entryMCPCommands(ts []config.StaticToken) []string {
	var cmds []string
	for _, t := range ts {
		if t.MCP != nil {
			cmds = append(cmds, t.MCP.Command)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return append([]string{"agentbox-env"}, cmds...)
}
