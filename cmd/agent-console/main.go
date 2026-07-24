// Command agent-console is a self-hosted web control-plane for managing
// CLI agent sessions. It spawns and owns each agent process on its own PTY
// (no tmux), and serves them in the browser via xterm.js over a WebSocket.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/culiops/culiops-agent-console/internal/auth"
	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/session"
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

	mgr := session.NewManager(launcher.New(cfg.Agents), 128*1024, 5*time.Minute)

	srv := &web.Server{
		API:      &web.API{Sessions: web.ManagerService{M: mgr}},
		Cookie:   cookie,
		Agents:   web.AgentNames(cfg),
		Terminal: terminal.NewAttachHandler(mgr.Get),
	}

	log.Printf("agent-console listening on %s", cfg.BindAddr)
	if err := http.ListenAndServe(cfg.BindAddr, srv.Handler(user, pass)); err != nil {
		log.Fatal(err)
	}
}
