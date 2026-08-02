// Command agent-box is a self-hosted web control-plane for managing
// CLI agent sessions. It spawns and owns each agent process on its own PTY
// (no tmux), and serves them in the browser via xterm.js over a WebSocket.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/culiops/agent-box/internal/abduco"
	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/secrets"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/store"
	"github.com/culiops/agent-box/internal/terminal"
	"github.com/culiops/agent-box/internal/web"
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
		log.Printf("agent %q: base=%s runtime=%s", a.Name, a.BasePolicy(), cfg.RuntimeFor(a))
	}

	lnch := launcher.New(agents, resolver, cfg.Options)
	lnch.SetDefaultRuntime(cfg.DefaultRuntime)

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
		dataDir = filepath.Join(home, ".local", "share", "agent-box")
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
	// Sweep before Reconcile, while every attach client for our socket dir is by
	// definition someone else's leftover. A previous daemon that died by SIGKILL
	// or panic leaves clients whose PTY master is gone; if such a client was not
	// its terminal's foreground process group it never got SIGHUP, and abduco's
	// read loop then spins at 100% CPU indefinitely.
	if n := adapter.SweepOrphanAttachers(); n > 0 {
		log.Printf("swept %d orphaned abduco attach client(s) from a previous daemon", n)
	}

	mgr := session.NewManager(lnch, adapter, 128*1024, 5*time.Minute)
	mgr.SetPaths(home, dataDir) // where transcripts live: host ~/.claude, boxed <dataDir>/boxstate/<box>
	if err := mgr.SetSidecarPath(filepath.Join(dataDir, "sessions.json")); err != nil {
		log.Printf("session sidecar unreadable (%v); starting without restart-recovery metadata", err)
	}

	// Containment layer. Installed BEFORE session reconcile so reattached boxed
	// sessions can adopt their boxes; box reconcile runs AFTER, so the orphan
	// reaper never stops a container a live session is running inside.
	boxRuntime := ""
	boxDocker, _ := box.LookDocker(cfg.DockerBin)
	boxes, err := newBoxes(cfg, dataDir)
	switch {
	case err != nil:
		// Not fatal: without containment the console still runs, on the host
		// tier, and says so rather than pretending to be contained.
		log.Printf("containment unavailable (%v); every session will run on the host tier", err)
	default:
		mgr.SetBoxes(boxes)
		boxRuntime = boxes.Runtime()
		log.Printf("containment ready: image %s, isolation runtime %s, idle TTL %s",
			boxImage(cfg), boxRuntime, cfg.BoxIdleTTL())
	}

	// Config bundle: what a boxed agent is allowed to see of the user's config.
	if boxes != nil && cfg.BundleSource != "off" {
		src := bundle.ExpandTilde(cfg.BundleSource)
		if src == "" {
			src = bundle.DefaultSource()
		}
		bm, err := bundle.New(bundle.Options{
			Source:  src,
			Root:    filepath.Join(dataDir, "bundle"),
			MCPFile: bundle.DefaultMCPFile(src),
			Prober:  bundle.ImageProber{Docker: boxDocker, Image: boxImage(cfg)},
		})
		if err != nil {
			log.Printf("config bundle disabled (%v); boxed sessions will run without user config", err)
		} else {
			mgr.SetBundles(bundle.NewProvider(bm))
			log.Printf("config bundle source: %s", src)
		}
	}

	if err := mgr.Reconcile(); err != nil {
		log.Printf("session reconcile: %v (continuing; some sessions may not have been recovered)", err)
	}
	// AFTER session reconcile: that pass adopts every surviving box, so what
	// remains unclaimed here is genuinely orphaned.
	if boxes != nil {
		if err := boxes.Reconcile(context.Background()); err != nil {
			log.Printf("box reconcile: %v (continuing; an orphaned box may be left running)", err)
		}
		// After reconcile: that pass removes the containers, so what is left on
		// disk belongs to boxes that are genuinely gone.
		reclaimBoxState(boxes)
		go func() {
			t := time.NewTicker(24 * time.Hour)
			defer t.Stop()
			for range t.C {
				reclaimBoxState(boxes)
			}
		}()
	}

	// Runtime profile store (config.Agents are read-only seeds; the store holds
	// UI-created/edited profiles). The manager pushes the merged set to the launcher.
	profilesPath := cfg.ProfilesPath
	if profilesPath == "" {
		profilesPath = filepath.Join(home, ".local", "share", "agent-box", "profiles.json")
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
	pm := web.NewProfileManager(agents, cfg.Options, cfg.DefaultRuntime, st, lnch.SetAgents)

	archiveBytes, archiveFiles := cfg.ArchiveLimits()
	srv := &web.Server{
		API:        &web.API{Sessions: web.ManagerService{M: mgr}, DefaultCwd: defaultCwd},
		Cookie:     cookie,
		Profiles:   pm,
		Home:       home,
		Terminal:   terminal.NewAttachHandler(mgr.Get),
		EnvLabel:   cfg.EnvLabel,
		EnvColor:   cfg.EnvColor,
		BoxRuntime: boxRuntime,

		ShellProfile: shellProfile,
		ShellBin:     config.DetectShell(),

		MaxUploadBytes:  cfg.UploadLimit(),
		MaxArchiveBytes: archiveBytes,
		MaxArchiveFiles: archiveFiles,
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
			if reason, bad := web.UnboxableDefaultCwd(defaultCwd, home); bad {
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

	httpSrv := &http.Server{Addr: cfg.BindAddr, Handler: srv.Handler(user, pass)}

	// Release attach clients on the way out instead of leaving it to SIGHUP.
	// SIGHUP on a closing PTY master reaches only that terminal's foreground
	// process group, so a client that is not it survives the daemon and spins at
	// 100% CPU forever on a hung-up fd. The agents themselves are untouched —
	// they live under abduco and are meant to survive a restart.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		log.Printf("received %v: releasing %d attach client(s), agents stay running",
			sig, mgr.DetachAll())
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	log.Printf("agent-box listening on %s", cfg.BindAddr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("agent-box stopped")
}

// reclaimBoxState removes the host homes of boxes that are gone and unused, and
// reports what it freed. internal/box returns results rather than logging so its
// decisions stay testable without a daemon; the printing belongs here.
func reclaimBoxState(boxes *box.Manager) {
	done, err := boxes.ReclaimState(context.Background(), box.DefaultStateTTL)
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

// boxImage picks the configured box image, falling back to the project default.
func boxImage(cfg config.Config) string {
	if cfg.BoxImage != "" {
		return cfg.BoxImage
	}
	return "culiops/agent-box:base"
}

// newBoxes builds the containment layer, or reports why it is unavailable. A
// missing docker or a missing image must degrade to the host tier loudly, never
// silently pretend to contain.
func newBoxes(cfg config.Config, dataDir string) (*box.Manager, error) {
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		return nil, err
	}
	instance := cfg.EnvLabel
	if instance == "" {
		instance = "default"
	}
	return box.New(box.Options{
		DockerBin: bin,
		Image:     boxImage(cfg),
		Instance:  instance,
		IdleTTL:   cfg.BoxIdleTTL(),
		StateRoot: dataDir,
		Client:    box.CLIClient{Bin: bin},
	})
}
