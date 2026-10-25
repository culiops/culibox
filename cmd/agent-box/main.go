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

	"golang.org/x/term"

	"github.com/culiops/agent-box/internal/abduco"
	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/analytics"
	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxedws"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
	"github.com/culiops/agent-box/internal/doctor"
	"github.com/culiops/agent-box/internal/instancelock"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/secretchain"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/store"
	"github.com/culiops/agent-box/internal/storerefs"
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
	if src.Default && cfgErr == nil {
		// No file yet: let the TLS-interception hint say where to create one.
		if files, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, abHome); err == nil && len(files) > 0 {
			hintConfigPath = files[0]
		}
	}
	// Merge the web-defined credentials before anything validates or reads cfg.
	// Needs a usable home (the data dir hangs off it) and a config that loaded.
	var cred credState
	// baseCfg is config.json alone: the credentials screen needs it to tell a
	// seed from a web entry (the merged cfg would make every entry look like a
	// seed).
	baseCfg := cfg
	if homeErr == nil && cfgErr == nil {
		cfg, cred = mergeCredentials(cfg, home)
	}
	if args.Cmd == "doctor" {
		// home is "" when $HOME is unusable; doctor reports that as a FAIL.
		os.Exit(doctor.Run(os.Stdout, doctor.Input{Config: cfg, Source: src, ConfigErr: cfgErr,
			Home: home, Commit: buildCommit(), CredErrors: cred.Problems()}))
	}
	if homeErr != nil {
		log.Fatalf("home: %v", homeErr)
	}
	if cfgErr != nil {
		log.Fatalf("config: %v", cfgErr)
	}
	// Resolve only loads; the security invariants (bind, allowed_hosts, socket
	// dir) are enforced here, for every source including built-in defaults.
	warnings, err := cred.validateBoot(cfg)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	for _, w := range warnings {
		log.Print("WARNING: " + w)
	}
	if args.Cmd == "box" {
		os.Exit(runBox(args, cfg, home, os.Stdout))
	}
	if args.Cmd == "store" {
		os.Exit(runStore(args, cfg, home, os.Stdin, term.IsTerminal(int(os.Stdin.Fd())), os.Stdout))
	}
	if args.Cmd == "clone" {
		cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, abHome)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		os.Exit(runClone(args, cfg, home, cfgFiles, os.Stdout))
	}
	log.Printf("config: %s (agent-box %s)", src, buildCommit())
	log.Print(cred.summary())
	cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, abHome)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	serve(cfg, baseCfg, cred.File, cred.Bad, home, cfgFiles)
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
	return cfg.ResolvedProfilesPath(dataDir)
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

func serve(cfg, baseCfg config.Config, credFile credfile.File, credBad bool, home string, cfgFiles []string) {
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

	// ONE store for the daemon: the secret_sources chain, the broker's value
	// references and the GitHub App minter all read through it. The data dir
	// exists (the lock created it); the keeper hashes its real path.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	storeAudit := &lazyAudit{path: filepath.Join(dataDir, "audit.db")}
	defer storeAudit.Close()
	vstore := secretstore.Open(dataDir, newKeeper(dataDir), storeAudit)
	resolver, err := secretchain.New(cfg.SecretSources, vstore)
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
	// Every holder of the credential lists (launcher, profiles, broker filter,
	// GitHub minter, the daemon-env scrub list, store-in-use) is fed through one
	// Apply, here for the launcher alone and again once the rest exist. A web
	// credential save calls the same Apply. The launcher's part is first, before
	// anything can spawn: a token entry's env-typed value variable lives in the
	// daemon's env for the broker to read, and like the password it must never
	// reach a session through an inherited base.
	live := &credLive{lnch: lnch, passwordEnv: cfg.PasswordEnv}
	live.Apply(cfg)
	if n := len(cfg.StaticTokens); n > 0 {
		log.Printf("static_tokens: %d entries", n)
	}

	// Agent accounts: accounts.json lives inside the data dir (the store writes it
	// through os.Root). The default account is the agent's own config dir.
	accts, err := account.Open(filepath.Join(dataDir, "accounts.json"), dataDir, bundle.DefaultSource())
	if err != nil {
		log.Fatalf("accounts: %v", err)
	}
	defer accts.Close()
	lnch.SetAccounts(accts)
	var acctList []account.Account
	for _, r := range accts.List() {
		if !r.IsDefault {
			acctList = append(acctList, r.Account)
		}
	}
	ptCopies := storerefs.PlaintextCopies(cfg, acctList, vstore, os.LookupEnv)
	logPlaintextCopies(ptCopies)

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

	// Boxed-workspace record (fs API containment). A box can plant symlinks in
	// its workspace for as long as the tree exists, so the fs API must treat
	// every directory ever spawned boxed as contained. Opened here, after the
	// layer (its Unboxable check filters entries) and before any spawn can happen.
	// Corrupt or unreadable fails closed: serving without it would silently
	// un-contain every workspace.
	unboxable := func(string) (string, bool) { return "", false }
	if layer != nil {
		unboxable = layer.boxes.Unboxable
	}
	boxedReg, boxedSeeded, err := boxedws.Open(dataDir, unboxable)
	if err != nil {
		log.Fatalf("boxed workspace record %s: %v", filepath.Join(dataDir, "boxed-workspaces.json"), err)
	}
	mgr.SetBoxedRecorder(boxedRecorder(boxedReg))
	fsDenied := []string{dataDir}
	if layer != nil && layer.stateRoot != dataDir {
		fsDenied = append(fsDenied, layer.stateRoot)
	}

	// Seccomp gate (N4 spec 7.9). On Linux a box shares the daemon's kernel user
	// keyring, where the unlocked store key lives; Docker's default seccomp
	// profile blocks the keyctl calls that would read it. Measured once here
	// at start for the log; the gate re-probes on every boxed spawn once a store
	// exists, and reads the store's state each time, so `store init` while
	// running takes effect. macOS boxes run in a VM with the key in the host Keychain.
	var seccompProbe func() (bool, error) // also gates store init from the web
	if layer != nil && layer.vm == nil && runtime.GOOS == "linux" {
		cli := layer.cli
		probe := func() (bool, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return cli.SeccompActive(ctx)
		}
		// Start-up line only: the gate probes docker itself on every gated spawn.
		if on, err := probe(); err != nil {
			log.Printf("seccomp: could not read docker's security options (%v); boxed spawns are refused while a store exists, until it can be read", err)
		} else if !on {
			log.Printf("seccomp: Docker's default profile is off; boxed spawns are refused while a store exists")
		}
		lnch.SetBoxGate(launcher.SeccompGate(probe, func() bool { return vstore.Status().Initialized }))
		seccompProbe = probe
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
	h, err := newCloneHooks(runtime.GOOS, layer, home, bundleSrc, dataDir, cfg.BoxInstance(), protected, lnch.Clones)
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
		svc, err := startBrokerWithStore(dataDir, layer.stateRoot, runtime.GOOS, cfg.BrokerPortOrDefault(),
			mgr.Get, web.ManagerService{M: mgr}.Get, cfg.GitHubApps, cfg.StaticTokens, vstore)
		switch {
		case errors.Is(err, errBrokerListen):
			log.Printf("%v (profiles with an AWS binding will refuse to spawn)", err)
		case err != nil:
			log.Fatalf("%v", err)
		default:
			brokerSvc = svc
			live.tokens, live.gh = svc.Tokens, svc.GitHub
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
			boxes.SetEgress(box.Egress{Network: svc.Svc.Network(), Verify: layer.egressVerify(svc.Svc), AddHosts: svc.AddHosts})
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
	// Seed the boxed-workspace record from what exists now, until a seed
	// completes (a docker listing failure is retried next start). AFTER
	// Reconcile, so retained boxed sessions are in the Manager, and BEFORE
	// bootReconcile, which removes orphaned boxes the docker listing must see.
	// The launcher holds only the config seeds here: profiles.json is recorded
	// by recordBoxedProfiles once the profile manager has loaded it.
	if !boxedSeeded {
		var listBoxes func(context.Context) ([]box.BoxInfo, error)
		if layer != nil {
			cli := layer.cli
			listBoxes = func(ctx context.Context) ([]box.BoxInfo, error) {
				return cli.ListBoxes(ctx, box.LabelInstance)
			}
		}
		seedBoxedRegistry(boxedReg, mgr.List(), lnch.Clones(), lnch.BoxedDefaultCwds(), listBoxes)
	}
	// AFTER session reconcile: that pass adopts every surviving box, so what
	// remains unclaimed here is genuinely orphaned.
	if layer != nil {
		// On darwin with the VM stopped this waits for the first boxed spawn: it
		// never starts the VM, and an unreachable docker must not read as "no
		// boxes".
		layer.bootReconcile(context.Background())
		if runtime.GOOS == "linux" { // clone mode is Linux-only
			sctx, scancel := context.WithTimeout(context.Background(), time.Minute)
			if n := sweepCloneContainers(sctx, layer.cli.ListByLabel, layer.cli.Stop, cfg.BoxInstance()); n > 0 {
				log.Printf("clone: removed %d leftover clone container(s)", n)
			}
			scancel()
		}
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
	recordBoxedProfiles(boxedReg, lnch.Clones(), lnch.BoxedDefaultCwds())
	pm.SetCloneCheck(cloneSaveCheck(h, home, protected))
	var vmShared []string // the box VM's shared directories; nil without a VM
	if layer != nil && layer.vm != nil {
		vmShared = layer.vm.Config().Mounts()
	}
	contextCheck := contextSaveCheck(h, home, protected, vmShared)
	pm.SetContextCheck(func(p string) error { _, err := contextCheck(p); return err })
	mgr.SetContextCheck(contextCheck) // the spawn re-checks what the save checked
	// The profile manager, the broker's token set and the minter exist now.
	live.pm = pm
	live.Apply(cfg)
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
		OptionsDropped: lnch.DroppedOptions,
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
	// The credentials screen. baseCfg is config.json before mergeCredentials.
	chk, credAWS := awsServiceFor(brokerSvc, storeAudit)
	creds := web.NewCredentialManager(baseCfg, credFile, dataDir, pm, vstore, storeAudit, chk, live.Apply, web.AccountsOf(accts))
	creds.SetFileBad(credBad)

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
		BoxTrees:   boxTreesFunc(fsDenied, boxedReg, mgr.BoxedCwds, lnch.Clones),

		ShellProfile: shellProfile,
		ShellBin:     config.DetectShell(),

		RateLimitsFile: rateLimitsFile,
		BoxFiles:       mgr.BoxFiles,

		Accounts: accts,
		SecretOK: func(n string) bool { _, ok, err := resolver.Resolve(n); return ok && err == nil },
		// The credentials screen's legacy-secret status: presence only, no value.
		SecretCheck: resolver.Check,

		// vstore is never nil (the daemon always opens one), so no typed-nil
		// interface can reach the web layer.
		Store:                vstore,
		StoreAudit:           storeAudit,
		StoreInUse:           live.InUse,
		StoreUnlockTTL:       cfg.StoreUnlockTTL(),
		StorePlaintextCopies: ptCopies,
		SeccompProbe:         seccompProbe,

		Creds:     creds,
		CredAWS:   credAWS,
		CredAudit: storeAudit,
	}
	mgr.SetStoreLocked(func() bool { return vstore.Status().Locked })
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
			// Everything under boxstate/ is written by a contained agent: its
			// transcript roots are walked through os.Root, never followed out.
			svc.SetBoxState(filepath.Join(dataDir, "boxstate"))
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
