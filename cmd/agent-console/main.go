// Command agent-console is a self-hosted web control-plane for managing
// CLI agent sessions. It spawns and owns each agent process on its own PTY
// (no tmux), and serves them in the browser via xterm.js over a WebSocket.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

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
	cookie, err := auth.NewCookieAuth()
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	resolver, err := secrets.New(cfg.SecretSources)
	if err != nil {
		log.Fatalf("secrets: %v", err)
	}
	// Surface each agent's effective base so the clean-by-default posture is
	// visible at startup (an existing config's agents now default to clean).
	for _, a := range cfg.Agents {
		log.Printf("agent %q: base=%s", a.Name, a.BasePolicy())
	}

	lnch := launcher.New(cfg.Agents, resolver, cfg.Options)
	mgr := session.NewManager(lnch, 128*1024, 5*time.Minute)

	// Default working dir: config value if set, else $HOME.
	home, _ := os.UserHomeDir()
	defaultCwd := cfg.DefaultCwd
	if defaultCwd == "" {
		defaultCwd = home
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
		log.Fatalf("profiles store: %v", err)
	}
	pm := web.NewProfileManager(cfg.Agents, cfg.Options, st, lnch.SetAgents)

	srv := &web.Server{
		API:      &web.API{Sessions: web.ManagerService{M: mgr}, DefaultCwd: defaultCwd},
		Cookie:   cookie,
		Profiles: pm,
		Home:     home,
		Terminal: terminal.NewAttachHandler(mgr.Get),
	}

	log.Printf("agent-console listening on %s", cfg.BindAddr)
	if err := http.ListenAndServe(cfg.BindAddr, srv.Handler(user, pass)); err != nil {
		log.Fatal(err)
	}
}
