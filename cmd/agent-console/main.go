// Command agent-console is a self-hosted web control-plane for managing
// Claude/agent CLI sessions running in tmux, viewed in the browser via
// in-process PTY -> WebSocket -> xterm.js terminals.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/culiops/culiops-agent-console/internal/auth"
	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/session"
	"github.com/culiops/culiops-agent-console/internal/terminal"
	"github.com/culiops/culiops-agent-console/internal/tmux"
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

	mgr := &session.Manager{
		Tmux:     tmux.NewCLIClient(),
		Launcher: launcher.New(cfg.Agents),
		NameFn:   session.DefaultNameFn,
	}

	srv := &web.Server{
		API:      &web.API{Sessions: mgr},
		Cookie:   cookie,
		Agents:   web.AgentNames(cfg),
		Terminal: terminal.NewWSHandler(terminal.TmuxAttachFactory),
	}

	log.Printf("agent-console listening on %s", cfg.BindAddr)
	if err := http.ListenAndServe(cfg.BindAddr, srv.Handler(user, pass)); err != nil {
		log.Fatal(err)
	}
}
