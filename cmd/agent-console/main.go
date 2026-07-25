// Command agent-console is a self-hosted web control-plane for managing
// CLI agent sessions. It spawns and owns each agent process on its own PTY
// (no tmux), and serves them in the browser via xterm.js over a WebSocket.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/culiops/culiops-agent-console/internal/abduco"
	"github.com/culiops/culiops-agent-console/internal/auth"
	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/secrets"
	"github.com/culiops/culiops-agent-console/internal/session"
	"github.com/culiops/culiops-agent-console/internal/store"
	"github.com/culiops/culiops-agent-console/internal/terminal"
	"github.com/culiops/culiops-agent-console/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	user, pass, err := auth.ResolveCred(cfg)
	if err != nil {
		log.Fatalf("auth: %v", err)
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
		log.Printf("agent %q: base=%s", a.Name, a.BasePolicy())
	}

	lnch := launcher.New(agents, resolver, cfg.Options)

	// Default working dir: config value if set, else $HOME.
	home, _ := os.UserHomeDir()
	defaultCwd := cfg.DefaultCwd
	if defaultCwd == "" {
		defaultCwd = home
	}

	// Session persistence via abduco: agents run under abduco so they survive a
	// daemon restart. abduco is a hard dependency — fail fast if it is missing.
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share", "agent-console")
	}
	// WS auth cookie secret, persisted under dataDir so a daemon restart does not
	// invalidate live browsers' cookies (the /ws route is cookie-only gated; a
	// per-process secret would 401 the client's auto-reconnect forever).
	cookie, err := auth.NewCookieAuth(filepath.Join(dataDir, "cookie.secret"))
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	abducoBin := cfg.AbducoBin
	if abducoBin == "" {
		abducoBin = "abduco"
	}
	if _, err := exec.LookPath(abducoBin); err != nil {
		log.Fatalf("abduco not found on PATH (%q): session persistence requires it; install abduco or set abduco_bin", abducoBin)
	}
	socketDir := cfg.AbducoSocketDir
	if socketDir == "" {
		socketDir = filepath.Join(dataDir, "abduco")
	}
	adapter, err := abduco.New(abducoBin, socketDir, filepath.Join(dataDir, "sessions", "pids"))
	if err != nil {
		log.Fatalf("abduco: %v", err)
	}
	mgr := session.NewManager(lnch, adapter, 128*1024, 5*time.Minute)
	if err := mgr.SetSidecarPath(filepath.Join(dataDir, "sessions.json")); err != nil {
		log.Printf("session sidecar unreadable (%v); starting without restart-recovery metadata", err)
	}
	if err := mgr.Reconcile(); err != nil {
		log.Printf("session reconcile: %v (continuing; some sessions may not have been recovered)", err)
	}

	// Runtime profile store (config.Agents are read-only seeds; the store holds
	// UI-created/edited profiles). The manager pushes the merged set to the launcher.
	profilesPath := cfg.ProfilesPath
	if profilesPath == "" {
		profilesPath = filepath.Join(home, ".local", "share", "agent-console", "profiles.json")
	}
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
	pm := web.NewProfileManager(agents, cfg.Options, st, lnch.SetAgents)

	srv := &web.Server{
		API:      &web.API{Sessions: web.ManagerService{M: mgr}, DefaultCwd: defaultCwd},
		Cookie:   cookie,
		Profiles: pm,
		Home:     home,
		Terminal: terminal.NewAttachHandler(mgr.Get),
		EnvLabel: cfg.EnvLabel,
		EnvColor: cfg.EnvColor,

		ShellProfile: shellProfile,
		ShellBin:     config.DetectShell(),
	}

	log.Printf("agent-console listening on %s", cfg.BindAddr)
	if err := http.ListenAndServe(cfg.BindAddr, srv.Handler(user, pass)); err != nil {
		log.Fatal(err)
	}
}
