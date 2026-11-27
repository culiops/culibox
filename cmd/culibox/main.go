// Command agent-box is a self-hosted web control-plane for managing
// CLI agent sessions. It spawns and owns each agent process on its own PTY
// (no tmux), and serves them in the browser via xterm.js over a WebSocket.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/culiops/culibox/internal/abduco"
	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/analytics"
	"github.com/culiops/culibox/internal/auditlog"
	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxedws"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/datacompat"
	"github.com/culiops/culibox/internal/doctor"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/instancelock"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/linktrust"
	"github.com/culiops/culibox/internal/logring"
	"github.com/culiops/culibox/internal/secretchain"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/store"
	"github.com/culiops/culibox/internal/storerefs"
	"github.com/culiops/culibox/internal/terminal"
	"github.com/culiops/culibox/internal/web"
	"github.com/culiops/culibox/internal/wsroots"
)

// The registry-based transcript locator only runs when the backend can name the
// agent's pid. Assert it at compile time: an interface drift here would silently
// leave every session unbound in production.
var _ session.PidResolver = (*abduco.Adapter)(nil)

// conversationPollEvery is how often live claude sessions are checked for a
// new conversation (/clear), so history stays whole with no browser open.
const conversationPollEvery = 5 * time.Second

// uidemoVMFolders is set only by the uidemo-tagged file; nil in every normal build.
var uidemoVMFolders func(home string) web.VMFolders

// uidemoScanBases is set only by the uidemo-tagged file: the folders the "Other places" scan
// reads instead of the usual ones. nil in every normal build.
var uidemoScanBases func() []string

func main() {
	args, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "culibox: %v\n%s", err, usage)
		os.Exit(2)
	}
	if args.Cmd == "version" {
		fmt.Print(versionLine())
		return
	}
	// The daemon keeps its last log lines for the bug-report bundle. Installed
	// before anything logs so a startup error is captured; the one-shot
	// subcommands (doctor, bug-report, box, ...) keep plain stderr.
	var ring *logring.Ring
	if args.Cmd == "serve" {
		ring = logring.New(200, 2048)
		log.SetOutput(io.MultiWriter(os.Stderr, ring))
	}
	// The root env var: CULIBOX_HOME, or the deprecated AGENT_BOX_HOME. A refusal
	// is fatal except for doctor and bug-report, which show it as a FAIL row.
	envHome, envNotice, envErr := config.HomeEnv(os.Getenv)
	oneShot := args.Cmd == "doctor" || args.Cmd == "bug-report"
	if envErr != nil && !oneShot {
		log.Fatalf("culibox: %v", envErr)
	}
	if envNotice != "" {
		if args.Cmd == "serve" {
			log.Printf("culibox: %s", envNotice) // into the ring for the bug report
		} else {
			fmt.Fprintf(os.Stderr, "culibox: %s\n", envNotice)
		}
	}
	home, homeErr := serveHome(os.UserHomeDir)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	// The one-time data root move, before the config is resolved so the config,
	// credentials and protected list are all built from the new path.
	var rootNote string
	var facts config.StartFacts
	if homeErr == nil && envErr == nil {
		out, err := maybeMoveDataRoot(args.Cmd, args.ConfigPath, args.ConfigSet, envHome, home, xdg, runtime.GOOS, log.Printf)
		if err != nil {
			log.Fatalf("culibox: %v", err)
		}
		rootNote = out.Reason
		facts = startFacts(out)
	}
	var (
		cfg    config.Config
		src    config.Source
		cfgErr error
	)
	if envErr != nil { // doctor / bug-report: the env row carries the refusal
		cfgErr = envErr
	} else {
		cfg, src, cfgErr = config.Resolve(args.ConfigPath, args.ConfigSet, home, xdg, envHome)
		serveStartWarnings(args.Cmd, src, cfgErr, facts, log.Printf)
	}
	if src.Default && cfgErr == nil {
		// No file yet: let the TLS-interception hint say where to create one.
		if files, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, envHome); err == nil && len(files) > 0 {
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
	if args.Cmd == "doctor" || args.Cmd == "bug-report" {
		// home is "" when $HOME is unusable; doctor reports that as a FAIL. The
		// config files only feed the protected list of the folder row: a
		// failure to name them leaves that list without them.
		cfgFiles, _ := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, envHome)
		in := doctor.Input{Config: cfg, Source: src, ConfigErr: cfgErr,
			Home: home, XDGConfig: xdg, Commit: buildCommit(), CredErrors: cred.Problems(), ConfigFiles: cfgFiles,
			EnvNotice: envNotice, EnvErr: envErr}
		if args.Cmd == "doctor" {
			// Only here may doctor ask the registry; the bug report and the
			// in-app doctor read the cache alone.
			in.LiveImageCheck = true
			os.Exit(doctor.Run(os.Stdout, in))
		}
		host, _ := os.Hostname()
		os.Exit(runBugReport(in, host, runtime.GOOS, os.Stdout, os.Stderr))
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
		cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, envHome)
		if cfgFiles, err = boxConfigFiles(runtime.GOOS, cfgFiles, err); err != nil {
			log.Fatalf("config: %v", err)
		}
		os.Exit(runBox(args, cfg, home, cfgFiles, os.Stdout))
	}
	if args.Cmd == "service" {
		os.Exit(runService(args, cfg, home, envHome, realServiceDeps(home, xdg), os.Stdout))
	}
	if args.Cmd == "init" {
		os.Exit(runInit(args, home, os.Stdin, term.IsTerminal(int(os.Stdin.Fd())), os.Stdout, realInitDeps(home)))
	}
	if args.Cmd == "store" {
		os.Exit(runStore(args, cfg, home, os.Stdin, term.IsTerminal(int(os.Stdin.Fd())), os.Stdout))
	}
	if args.Cmd == "clone" {
		cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, envHome)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		os.Exit(runClone(args, cfg, home, runtime.GOOS, cfgFiles, os.Stdout))
	}
	if args.Service {
		// The unit's promise holds at every start, not only at install: a later
		// edit of bind_addr, or a config the service resolves differently.
		if why := serviceServeRefusal(cfg); why != "" {
			log.Printf("culibox serve -service: %s", why)
			os.Exit(serviceRefusedExit(runtime.GOOS))
		}
		privateStderr(os.Stderr)
	}
	log.Printf("config: %s (culibox %s)", src, buildCommit())
	log.Print(cred.summary())
	cfgFiles, err := config.ConfigFiles(args.ConfigPath, args.ConfigSet, home, xdg, envHome)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	serve(cfg, baseCfg, cred.File, cred.Bad, home, cfgFiles, doctor.Input{Config: cfg, Source: src,
		Home: home, XDGConfig: xdg, Commit: buildCommit(), CredErrors: cred.Problems(), ConfigFiles: cfgFiles,
		EnvNotice: envNotice, RootNote: rootNote, Start: facts}, ring)
}

// boxConfigFiles: `box` uses the config files only for the macOS VM's
// protected list, so failing to name them is fatal on darwin alone.
func boxConfigFiles(goos string, files []string, err error) ([]string, error) {
	if err != nil && goos != "darwin" {
		return nil, nil
	}
	return files, err
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

// boxedFilter is the boxed-workspace registry's filter: the manager's workspace
// rule, share-agnostic. Removing a VM folder never drops a recorded workspace
// (the registry feeds the fs containment trees). No layer = nothing is refused.
func boxedFilter(layer *boxLayer) func(string) (string, bool) {
	if layer == nil {
		return func(string) (string, bool) { return "", false }
	}
	return layer.boxes.UnboxableIgnoringShares
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
//
// bundleSrc is the config bundle's source ("" with the bundle off). The files
// under it have their own CLAUDE.md imports followed, so a custom one that is
// not personalDir gets the same protection: a box that could write it could
// add an "@" line that pulls a host file into the next box.
//
// programs are the files CuliBox runs (boxvm.ProgramPaths): their folders are
// protected too, so a box cannot replace a program the host then starts.
func protectedDirs(dataDir string, cfgFiles []string, profilesPath, personalDir, bundleSrc string, programs ...string) []box.ProtectedDir {
	return box.ProtectedDirsFor(dataDir, cfgFiles, profilesPath, personalDir, bundleSrc, programs...)
}

// protectedDirsOf is protectedDirs for the real process: the default account's
// config dir, the bundle source of cfg and the programs CuliBox runs.
func protectedDirsOf(cfg config.Config, dataDir string, cfgFiles []string) []box.ProtectedDir {
	return protectedDirs(dataDir, cfgFiles, profilesPathFor(cfg, dataDir), bundle.DefaultSource(), bundleSourceOf(cfg), boxvm.ProgramPaths(cfg)...)
}

// bundleSourceOf is the config bundle's source dir: "" with bundle_source
// "off", the configured dir, or bundle.DefaultSource().
func bundleSourceOf(cfg config.Config) string { return bundle.SourceOf(cfg.BundleSource) }

// importDenyDirs is the host directories no CLAUDE.md import may come from: the
// same sources as protectedDirs (data root, each config file's and the profiles
// file's directories, symlink hops included) plus the box state root.
func importDenyDirs(dataDir, stateRoot string, cfgFiles []string, profilesPath string) []string {
	out := []string{dataDir, stateRoot}
	for _, f := range cfgFiles {
		out = append(out, box.FileDirs(f)...)
	}
	return append(out, box.FileDirs(profilesPath)...)
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

func serve(cfg, baseCfg config.Config, credFile credfile.File, credBad bool, home string, cfgFiles []string,
	doctorIn doctor.Input, ring *logring.Ring) {
	started := time.Now()
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
	// Before anything below writes to the data root: on a version change, copy
	// the small files aside, and refuse to start when that cannot be done.
	if err := upgradeBackup(dataDir, cfgFiles, version, commit, time.Now(), exePath(), log.Printf); err != nil {
		log.Fatalf("%v", err)
	}
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
	// An account's own pasted token is read straight from the store even when
	// secret_sources has no store source (account.OwnsStoreToken).
	acctTokenStore, err := secretchain.New([]config.SecretSource{{Type: "store"}}, vstore)
	if err != nil {
		log.Fatalf("secrets: %v", err)
	}
	lnch.SetAccountTokenStore(acctTokenStore)
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
	// Session persistence via abduco: agents run under abduco so they survive a
	// daemon restart. abduco is a hard dependency — fail fast if it is missing.
	// abduco_bin wins; else the abduco next to culibox (a release ships one), then PATH.
	abducoRes, err := abduco.DefaultLookup().Resolve(cfg.AbducoBin)
	if err != nil {
		log.Fatalf("abduco: %v; session persistence requires it; install abduco or set abduco_bin", err)
	}
	abducoBin := abducoRes.Path
	log.Printf("abduco: %s (%s)", abducoBin, abducoRes.Source)
	if abducoRes.Skipped != "" {
		log.Printf("abduco: ignored %s", abducoRes.Skipped)
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
	mgr.SetIDStatePath(filepath.Join(dataDir, "session-ids.json")) // ids are never reissued

	// Containment layer. Installed BEFORE session reconcile so reattached boxed
	// sessions can adopt their boxes; box reconcile runs AFTER, so the orphan
	// reaper never stops a container a live session is running inside.
	boxUnavailable := "" // why there is no box layer, when it failed to build; published in /api/meta
	var layer *boxLayer
	var boxes *box.Manager
	protected := protectedDirsOf(cfg, dataDir, cfgFiles)
	if l, err := newBoxLayer(cfg, dataDir, home, runtime.GOOS, boxvm.ExecRunner{}, protected); err != nil {
		// Not fatal: without containment the console still runs, on the host
		// tier, and says so rather than pretending to be contained.
		log.Printf("containment unavailable (%v); every session will run on the host tier", err)
		boxUnavailable = boxUnavailableReason(err)
	} else {
		layer, boxes = l, l.boxes
		// The keep pass writes box_kept/box_kept_held rows, from the first
		// boot pass on.
		l.setAudit(storeAudit.RecordCred)
		// Docker is installed, but is its daemon usable? The layer stands either way
		// (the daemon may start later); the page and every boxed spawn ask again
		// (boxdaemon.go). Bounded, so a hung daemon cannot hold the boot.
		if why := l.daemonReason(context.Background()); why != "" {
			log.Printf("containment: docker is installed but boxes cannot start: %s (checked again on every boxed spawn)", why)
		}
		if l.vm == nil {
			// An upgraded install's culiops/agent-box:base becomes the new default
			// name before anything uses it. The Mac's VM is not started here (D3):
			// its adoption runs in the first boxed spawn's prepare.
			actx, acancel := context.WithTimeout(context.Background(), 30*time.Second)
			l.adoptImage(actx)
			acancel()
		}
		// Not fatal (D8): without the key the darwin listeners and the relay
		// refuse, so every boxed spawn on this Mac says why instead.
		if err := l.loadRelayKey(); err != nil {
			log.Printf("relay key unavailable: %v; every boxed spawn on this Mac is refused until that is fixed", err)
		}
		mgr.SetBoxes(boxes)
		// The runtime the boot probe read, or "not known yet" when docker could
		// not answer (#334): it is then read before the first box is created, and
		// /api/meta's box_runtime follows it (layerRuntime).
		rtLine := "isolation runtime not known yet (docker did not answer; read again before the first box)"
		if rt, known := boxes.RuntimeKnown(); known {
			rtLine = "isolation runtime " + rt
		}
		if l.vm != nil {
			// The VM is not started here (D3): the first boxed spawn starts it.
			log.Printf("containment ready (macOS box VM in %s, starts on the first boxed spawn): image %s, %s, idle TTL %s",
				l.vm.Config().ColimaHome(), cfg.BoxImageOrDefault(), rtLine, cfg.BoxIdleTTL())
		} else {
			log.Printf("containment ready: image %s, %s, idle TTL %s",
				cfg.BoxImageOrDefault(), rtLine, cfg.BoxIdleTTL())
		}
	}

	// Boxed-workspace record (fs API containment). A box can plant symlinks in
	// its workspace for as long as the tree exists, so the fs API must treat
	// every directory ever spawned boxed as contained. Opened here, after the
	// layer (its Unboxable check filters entries) and before any spawn can happen.
	// Corrupt or unreadable fails closed: serving without it would silently
	// un-contain every workspace.
	unboxable := boxedFilter(layer)
	boxedReg, boxedSeeded, err := boxedws.Open(dataDir, unboxable)
	if err != nil {
		log.Fatalf("boxed workspace record %s: %v", filepath.Join(dataDir, "boxed-workspaces.json"), err)
	}
	mgr.SetBoxedRecorder(boxedRecorder(boxedReg, lnch.Clones))
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
	var bundleImports func() bundle.ImportSet
	var bundles *bundle.Manager
	if src := bundleSourceOf(cfg); layer != nil && src != "" {
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
			// CLAUDE.md imports may not come from agent-box's own data, state
			// or config files.
			ImportDeny: importDenyDirs(dataDir, layer.stateRoot, cfgFiles, profilesPathFor(cfg, dataDir)),
		})
		if err != nil {
			log.Printf("config bundle disabled (%v); boxed sessions will run without user config", err)
		} else {
			mgr.SetBundles(bundle.NewProvider(bm))
			bundleImports = bm.Imports
			bundles = bm
			log.Printf("config bundle source: %s", src)
		}
	}

	// Clone mode (Linux and macOS). The profile list is read through the
	// launcher on every call, so a profile saved after boot is covered. Before
	// Reconcile, like the box hooks. Hooks are installed even when clone
	// creation is unavailable (no layer, no git): the host-tier box-owned guard
	// needs neither. The interface must never hold a nil *cloneHooks, so
	// SetClone runs only for a real value.
	h, err := newCloneHooks(layer, home, bundleSrc, dataDir, cfg.BoxInstance(), protected, lnch.Clones)
	if err != nil {
		log.Printf("clone mode unavailable (%v); clone-mode profiles are refused", err)
	}
	if h != nil { // non-nil even when unavailable: the box-owned guard stays on
		h.audit = storeAudit.RecordCred // every automatic merge is audited before it happens (#370)
		h.boxed = boxedReg              // a folder boxed in place without git is never a clone source (#371)
		mgr.SetClone(h)
	}

	// Handback (#346): what a boxed session leaves in the user's own folder is
	// scanned when the session ends and when its box stops; what a clone's
	// returned branch brings is recorded by the clone hooks. All three write the
	// same store under the data dir.
	hb := newHandbackHooks(dataDir, func(dir string) bool {
		if h == nil {
			return false
		}
		_, owned := h.BoxOwned(dir)
		return owned
	})
	mgr.SetHandback(hb)
	mgr.SetSpawnAudit(spawnAudit{rec: storeAudit})
	if h != nil {
		h.handback = hb
	}
	if layer != nil {
		layer.setOnStop(hb.OnStop)
	}

	// Credential broker (M3a): it starts only with a box layer, so on an
	// instance without one a bound profile is refused on either tier, Host
	// included (pinned by TestHostBoundSpawnRefusedWithoutBroker). Before
	// Reconcile so reattached sessions find the hooks.
	var brokerSvc *brokerService
	if layer != nil {
		svc, err := startBrokerWithStore(dataDir, layer.stateRoot, runtime.GOOS, cfg.BrokerPortOrDefault(),
			mgr.Get, web.ManagerService{M: mgr}.Get, cfg.GitHubApps, cfg.StaticTokens, vstore, layer.addrs, layer.relayKey)
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

	// Egress control: on both OSes every box reaches the network only through
	// the allowlist proxy (bound or not), so it starts without the broker. Not fatal
	// when it cannot start: boxed spawns then refuse, and the rest of the console
	// keeps working. Before Reconcile, and the box hooks before it too, so
	// reattached boxes find them.
	var egressSvc *egressService
	if layer != nil {
		// Set whether or not egress starts: without it every boxed spawn is refused.
		mgr.SetEgressAll(true)
		svc, err := startEgress(dataDir, layer, cfg, runtime.GOOS, mgr.Get)
		switch {
		case err != nil:
			log.Printf("egress control disabled (%v); every boxed spawn will be refused", err)
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
		log.Printf("SIGHUP is ignored (nohup): closing the terminal will not stop culibox")
	}

	// The audit databases are the other record of which ids were issued: floor
	// the counters with them, so a lost session-ids.json cannot make a new
	// session inherit an old one's audit history.
	mgr.SeedIDFloor(auditIDFloor(dataDir))
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
		bootReconcileAndSweep(context.Background(), layer, cfg.BoxInstance())
		if egressSvc != nil {
			warnBridgeBoxes(context.Background(), layer.cli, cfg.BoxInstance(), egressSvc.Svc.Network())
		}
		go func() {
			t := time.NewTicker(reclaimEvery)
			defer t.Stop()
			for range t.C {
				layer.periodicReclaim(context.Background())
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
	// A profiles.json this build cannot parse is left where it is: the store starts
	// empty and refuses writes, never renaming the file away or saving over it.
	st, parseErr, err := store.OpenOrReadOnly(profilesPath)
	if err != nil {
		log.Fatalf("profiles store: %v", err)
	}
	if errors.Is(parseErr, datacompat.ErrNewer) {
		// The message already names the file, the version and the way out.
		log.Printf("profiles store: %v; no stored profile is loaded, profile edits are refused and no session can start until that is fixed and CuliBox restarted", parseErr)
	} else if parseErr != nil {
		log.Printf("profiles store %q has content this CuliBox cannot read (%v); profile edits are refused until it is fixed and CuliBox restarted; %s",
			profilesPath, parseErr, datacompat.Hint)
	}
	// A newer profiles.json may hold overrides of the config seeds; with it
	// unreadable, no spawn can be trusted to match what the user saved (#291).
	lnch.SetProfilesGate(launcher.ProfilesNewerGate(parseErr))
	// applyProfiles also re-reads running sessions' egress policy (#95); the
	// first call, here, follows the restore above.
	pm := web.NewProfileManager(agents, cfg.Options, cfg.DefaultRuntime, protected, st, applyProfiles(lnch, mgr))
	// #330: who changed which fields of which profile, and when, in the same
	// audit as credential edits; a row that cannot be written stops the change.
	pm.SetAudit(storeAudit)
	// What CuliBox added itself is "builtin" in the profile list; config.json's own
	// agents are "config" (#15). Derived every boot, never written to a file.
	pm.SetBuiltin(builtinProfileNames(doctorIn.Source, cfg.Agents, agents))
	recordBoxedProfiles(boxedReg, lnch.Clones(), lnch.BoxedDefaultCwds())
	pm.SetCloneCheck(cloneSaveCheck(h, home, protected))
	// A clone_of whose folder was boxed in place before the record kept a flag
	// needs the user's ack at save (new or changed clone_of only, #371).
	pm.SetInPlaceHistoryCheck(func(of string) bool {
		st, _ := boxedReg.InPlaceStatus(of)
		return st == boxedws.StatusUnknown
	})
	if h != nil {
		pm.SetNotHome(h.NotHome)
	}
	// The hand-back store the hooks write, as the profile rows' flag count and
	// the /api/handback endpoints read it (#346).
	flagged := newFlaggedView(hb.store)
	pm.SetFlagged(flagged.Count)
	// As mgr.SetEgressAll above: the profile list previews each box's network.
	pm.SetEgressAll(layer != nil)
	vmShares := layer.sharesFunc() // the box VM's live folder list; nil without a VM
	pm.SetShares(vmShares)
	vmFolders := bootVMFolders(layer, cfg, pm.All, boxedReg.List, bundleSrc, mgr.List)
	if vmFolders == nil && uidemoVMFolders != nil {
		vmFolders = uidemoVMFolders(home) // nil unless a `-tags uidemo` build asks for it
	}
	contextCheck := contextSaveCheck(h, home, protected, vmShares)
	pm.SetContextCheck(func(p string) error { _, err := contextCheck(p); return err })
	mgr.SetContextCheck(contextCheck) // the spawn re-checks what the save checked
	// The profile manager, the broker's token set and the minter exist now.
	live.pm = pm
	live.Apply(cfg)
	pm.SetAccounts(accts) // without it a profile naming an account cannot be saved
	pm.SetBoxClaudeCredential(secretchain.ProfileBoxCredential(resolver, acctTokenStore, func(n string) (account.Account, error) {
		r, err := accts.Resolve(n)
		return r.Account, err
	}))
	// Profiles are now registered with the launcher; backfill Bin for sessions
	// reconciled at startup (before profiles were known) so per-window usage works
	// for profile-based claude sessions that survived a daemon restart.
	mgr.ResolveBins()

	rateLimitsFile := cfg.RateLimitsFile
	if rateLimitsFile == "" {
		rateLimitsFile = filepath.Join(home, ".claude", "abtop-rate-limits.json")
	}
	api := &web.API{Sessions: web.ManagerService{M: mgr}, DefaultCwd: defaultCwd, Home: home,
		Handback:       flagged,
		OptionsDropped: lnch.DroppedOptions,
		ProfileCwd: func(name string) string {
			p, ok := pm.Get(name)
			if !ok {
				return ""
			}
			return strings.TrimSpace(p.DefaultCwd)
		}}
	var stopBroker, stopEgress, drainEgress func(context.Context)
	if brokerSvc != nil {
		status := brokerSvc.Status // only when running: a nil service must read as "no broker", not a typed nil
		if egressSvc != nil {
			status.Egress = egressSvc.Stats // nil otherwise: no egress object in the reply
		}
		stopBroker = brokerSvc.Stop
		if boxes != nil {
			es := elevationService{brk: brokerSvc.Hooks.B, get: mgr.Get, list: mgr.List, blocker: boxes.GrantBlocker, claim: boxes.ClaimForGrant,
				hosts: newHostFolders()}
			if brokerSvc.SSO != nil { // a nil *Runner must stay a nil interface
				es.runner = brokerSvc.SSO
			}
			brokerSvc.Hooks.B.SetClaimCheck(es.claimCheck)
			// #325: an elevatable session's folder is shared until it holds write
			// credentials. Before the API serves: no boxed spawn can run earlier,
			// and without the hook exclusive boxes refuse every other spawn.
			boxes.SetWriteHold(es.writeHold)
			// #360: a broker-served Host session's window closes its folder
			// too. Host sessions reattached at boot are held as adopted boxes.
			es.seedHostFolders(time.Now())
			boxes.SetHostHolds(es.hostHolds)
			api.Elevation = es
			status.Elevation = brokerSvc.Hooks.B.Elevation
			status.SSO = es.ssoStatus
			status.Grant = es.grantContext
		}
		api.AWS = status
		api.Activity = brokerSvc.Activity
	}
	if egressSvc != nil {
		stopEgress, drainEgress = egressSvc.Stop, egressSvc.Drain
		api.Egress = web.EgressStatusAdapter{Get: web.ManagerService{M: mgr}.Get, Stats: egressSvc.Stats}
	}
	// The credentials screen. baseCfg is config.json before mergeCredentials.
	chk, credAWS := awsServiceFor(brokerSvc, storeAudit)
	creds := web.NewCredentialManager(baseCfg, credFile, dataDir, pm, vstore, storeAudit, chk, live.Apply, web.AccountsOf(accts))
	creds.SetFileBad(credBad)

	// The workspace folders the file API serves besides $HOME. Every add and
	// remove is audited first (storeAudit), so a failed audit saves nothing.
	wsRoots := wsroots.NewService(dataDir, home, web.RootsAudit(storeAudit), time.Now)
	api.Roots = wsRoots // a browser Start with add_root adds its folder here
	if uidemoScanBases != nil {
		wsRoots.SetScanBases(uidemoScanBases) // nil unless a `-tags uidemo` build asks for it
	}

	// The hosts whose agent links open without the confirm dialog, per person.
	// Every add and remove is audited first, like the workspace roots.
	linkTrust := linktrust.NewService(dataDir, web.LinkTrustAudit(storeAudit), time.Now)

	srv := &web.Server{
		Imports:          bundleImports,
		API:              api,
		Session:          sess,
		Tokens:           tokens,
		Password:         login.Secret,
		Profiles:         pm,
		Home:             home,
		DataDir:          dataDir,
		Roots:            wsRoots,
		LinkTrust:        linkTrust,
		Protected:        protected,
		Shares:           vmShares,
		VMFolders:        vmFolders,
		Terminal:         terminal.NewAttachHandler(mgr.Get),
		BoxRuntime:       layerRuntime(layer),
		BoxImage:         boxImageHook(cfg, dataDir, layerImageFacts(layer)),
		CloneUnavailable: cloneUnavailableReason(h),
		CloneEstimate:    cloneEstimateFunc(h),
		BoxTrees:         boxTreesFunc(fsDenied, boxedReg, mgr.BoxedCwds, lnch.Clones),
		ScratchDirs:      web.ScratchCandidates(os.Getenv, os.Getuid(), os.TempDir()),

		// Why BoxRuntime is empty when the containment layer failed to build.
		BoxUnavailableReason: boxUnavailable,
		// Why a layer that exists cannot be used right now (a stopped or
		// not-permitted docker daemon, #154); nil without a layer.
		BoxDaemonReason: layerDaemonReason(layer),

		ShellProfile: shellProfile,
		ShellBin:     config.DetectShell(),

		RateLimitsFile: rateLimitsFile,
		BoxFiles:       mgr.BoxFiles,

		Accounts: accts,
		AccountTokenPresence: func(a account.Account) (secretchain.Presence, error) {
			return secretchain.AccountTokenPresence(resolver, acctTokenStore, a)
		},
		// The credentials screen's legacy-secret status: presence only, no value.
		SecretCheck:         resolver.Check,
		SecretStoredOutside: resolver.StoredOutsideSources,
		SecretSources:       cfg.SecretSources,

		// vstore is never nil (the daemon always opens one), so no typed-nil
		// interface can reach the web layer.
		Store:      vstore,
		StoreAudit: storeAudit,
		StoreInUse: func() bool {
			if live.InUse() {
				return true
			}
			list := accts.List()
			own := make([]account.Account, 0, len(list))
			for _, r := range list {
				own = append(own, r.Account)
			}
			return storerefs.AccountTokensStored(own, vstore)
		},
		StoreUnlockTTL:       cfg.StoreUnlockTTL(),
		StorePlaintextCopies: ptCopies,
		SeccompProbe:         seccompProbe,

		Creds:     creds,
		CredAWS:   credAWS,
		CredAudit: storeAudit,

		Version: version,
		Commit:  buildCommit(),
	}
	// The profile view's kept box folders; none without the box layer.
	if boxes != nil {
		srv.Kept = boxes // never a typed nil
	}
	// A companion shell runs the verified quick-shell profile, read when used.
	api.QuickShell = srv.QuickShell
	// The macOS VM state for the New session dialog; nil without the box layer.
	if layer != nil {
		api.VMState = layer.vmStateAnswer
	}
	// The file tree's git badges and diffs; nil without the box layer.
	srv.Git = gitViewFor(layer, os.Getuid(), os.Getgid())
	sweepGitLeftovers(srv.Git)
	host, _ := os.Hostname()
	srv.BugReport = buildBugReport(bugDeps{
		checks:  func() []doctor.Result { return doctor.Checks(doctorIn) },
		timeout: bugReportTimeout,
		start:   started,
		base:    daemonBugInput(home, host, cfg),
		// The merged set the daemon actually runs: config seeds plus stored profiles.
		profiles: pm.All,
		sessions: mgr.List,
		logs:     ring.Lines,
	})
	mgr.SetStoreLocked(func() bool { return vstore.Status().Locked })
	configureServer(srv, cfg)

	// Audit page: a read-only view of audit.db, opened after the broker and
	// egress audits so their schemas exist. Not fatal: without it the routes are
	// not registered and /api/meta says audit=false.
	var auditReader *auditlog.Reader
	if r, aerr := auditlog.Open(filepath.Join(dataDir, "audit.db")); aerr != nil {
		log.Printf("audit page disabled: %v", aerr)
	} else {
		auditReader = r // a typed nil must never reach srv.Audit
		srv.Audit = r
		// What the retention job will really do: without an egress audit store it
		// never starts, and the footer must not claim a policy that is not run.
		var egressAudit *egress.Audit
		if egressSvc != nil {
			egressAudit = egressSvc.Audit
		}
		srv.AuditRetentionDays = retentionInEffect(egressAudit, cfg.EgressRetentionDays())
	}

	// Analytics: durable token/prompt rollups mined from the Claude Code
	// transcripts. Enabled by default; only an explicit config false turns it off.
	// Roots default to analytics.DefaultRoots: ~/.claude/projects (all host +
	// direct usage — the bulk) plus this instance's boxed-session, conversations
	// folder and agent-account transcript dirs.
	var analyticsSvc *analytics.Service
	if cfg.Analytics.On() {
		dbPath := cfg.Analytics.DBPath
		if dbPath == "" {
			dbPath = filepath.Join(dataDir, "analytics.db")
		}
		roots := cfg.Analytics.Roots
		if len(roots) == 0 {
			// Glob patterns, expanded on every ingest (not at boot) so boxes,
			// conversations folders and agent accounts created while the daemon
			// runs are picked up.
			roots = analytics.DefaultRoots(home, dataDir)
		}
		interval := 2 * time.Minute
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
			// Only a live service: a nil *Service must never reach the manager.
			mgr.SetCostSource(svc)
			if boxes != nil {
				// #330: a box home's conversations moved into its profile's
				// folder keep their read offsets, so they are not counted twice.
				// Before the API serves: no spawn creates a box earlier.
				boxes.SetAdoptHook(svc.CarryOffsets)
			}
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

	// Record /clear conversations for every live claude session even with no
	// browser open; stops with the same context.
	go mgr.PollConversations(analyticsCtx, conversationPollEvery)

	// Prune old egress audit rows (once at start, then daily) while the egress
	// audit store is open; StopRetention waits for it before StopEgress closes it.
	stopRetention := func() {}
	if egressSvc != nil {
		stopRetention = runRetention(egressSvc.Audit, cfg.EgressRetentionDays(), log.Printf)
	}

	// Remove old unmounted config bundles (once at start, then daily) off the
	// spawn path and the boot path; shutdown cancels it.
	stopBundleGC := func() {}
	if layer != nil && bundles != nil {
		stopBundleGC = layer.startBundleGC(func(ctx context.Context) { layer.bundleGCPass(ctx, bundles) }, log.Printf)
	}

	// Look daily, from the registry and GitHub, for the image published for this
	// version. Network only: it never touches docker or a spawn lock. Whether this
	// instance may ask at all is decided per pass from the config.
	stopImageCheck := runImageCheck(imageCheckPass(cfg, dataDir, newImageChecker(), log.Printf), imageCheckFirst, imageCheckTick)

	// No WriteTimeout, deliberately: on macOS the first boxed spawn holds its
	// POST while the box VM starts (20-30 s, see boxLayer.prepare), and the
	// UI's spawn request has no deadline either. Adding a write timeout here
	// would turn that cold start into a failed spawn.
	httpSrv := &http.Server{Handler: srv.Handler()}

	// Release attach clients on the way out (stop is registered before
	// Reconcile, above); the signal waits in the buffer until this runs.
	go awaitShutdown(stop, shutdownSteps{
		DrainEgress: drainEgress,
		DetachAll:   mgr.DetachAll,
		StopAnalytics: func() {
			analyticsCancel()
			if analyticsSvc != nil {
				_ = analyticsSvc.Close()
			}
		},
		StopRetention:  stopRetention,
		StopBundleGC:   stopBundleGC,
		StopImageCheck: stopImageCheck,
		StopEgress:     stopEgress,
		StopBroker:     stopBroker,
		ShutdownHTTP: func(ctx context.Context) error {
			// The reader closes after the drain, so no request reads a closed handle.
			err := httpSrv.Shutdown(ctx)
			if auditReader != nil {
				_ = auditReader.Close()
			}
			return err
		},
	}, log.Printf)

	log.Printf("culibox listening on %s (allowed hosts: %s)", cfg.BindAddr, strings.Join(cfg.HostAllowlist(), ", "))
	if login.Mode == auth.ModeToken {
		// The only place the token is ever printed.
		url, hint := loginURLFor(cfg, login.Secret)
		if hint != "" {
			hint = "\n           (" + hint + ")"
		}
		fmt.Fprintf(os.Stderr, "\nculibox: open %s%s\n           (token stored in %s)\n\n",
			url, hint, login.TokenPath)
	}
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("culibox stopped")
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
