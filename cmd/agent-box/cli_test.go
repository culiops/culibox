package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/web"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		argv []string
		want cliArgs
	}{
		{nil, cliArgs{Cmd: "serve", ConfigPath: ""}},
		{[]string{"-config", "config.prod.json"}, cliArgs{Cmd: "serve", ConfigPath: "config.prod.json", ConfigSet: true}}, // today's systemd/air form
		{[]string{"serve"}, cliArgs{Cmd: "serve", ConfigPath: ""}},
		{[]string{"serve", "--config", "/x.json"}, cliArgs{Cmd: "serve", ConfigPath: "/x.json", ConfigSet: true}},
		{[]string{"doctor"}, cliArgs{Cmd: "doctor", ConfigPath: ""}},
	}
	for _, c := range cases {
		got, err := parseArgs(c.argv)
		if err != nil || got != c.want {
			t.Errorf("parseArgs(%q) = %+v,%v want %+v", c.argv, got, err, c.want)
		}
	}
	for _, bad := range [][]string{{"deploy"}, {"serve", "extra"}, {"-nope"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%q): want error", bad)
		}
	}
}

// Closing the terminal tab that runs `agent-box serve` sends SIGHUP. Without it
// here the daemon dies without DetachAll and its attach clients spin at 100% CPU.
func TestShutdownSignalsIncludeHangup(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		if !containsSig(s) {
			t.Errorf("shutdownSignals missing %v", s)
		}
	}
}

func containsSig(s syscall.Signal) bool {
	for _, x := range shutdownSignals {
		if x == s {
			return true
		}
	}
	return false
}

// A real SIGHUP sent to this process must land on the channel serve listens on.
// Were it missing from shutdownSignals, the default disposition would kill the
// test binary here, which is exactly the daemon's failure mode.
func TestHangupReachesShutdownChannel(t *testing.T) {
	stop := make(chan os.Signal, 1)
	// The not-ignored path (no nohup): the set serve registers then.
	signal.Notify(stop, shutdownSignalsFor(func(os.Signal) bool { return false })...)
	defer signal.Stop(stop)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-stop:
		if sig != syscall.SIGHUP {
			t.Fatalf("got %v, want SIGHUP", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP never reached the shutdown channel")
	}
}

// The handler serve runs on a shutdown signal: attach clients are released
// BEFORE the HTTP server drains, analytics stops, and the log names the signal.
func TestAwaitShutdown_HangupReleasesThenDrains(t *testing.T) {
	var steps []string
	var logged string
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGHUP
	awaitShutdown(stop, shutdownSteps{
		DetachAll:     func() int { steps = append(steps, "detach"); return 2 },
		StopAnalytics: func() { steps = append(steps, "analytics") },
		ShutdownHTTP: func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("HTTP shutdown context has no deadline")
			}
			steps = append(steps, "http")
			return nil
		},
	}, func(f string, a ...any) { logged = fmt.Sprintf(f, a...) })
	if got := strings.Join(steps, ","); got != "detach,analytics,http" {
		t.Errorf("steps = %s, want detach,analytics,http", got)
	}
	if !strings.Contains(logged, "received hangup: releasing 2 attach client(s)") {
		t.Errorf("log = %q", logged)
	}
}

func TestAwaitShutdown_StopsTheBrokerBeforeTheHTTPDrain(t *testing.T) {
	var steps []string
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGTERM
	awaitShutdown(stop, shutdownSteps{
		DetachAll:     func() int { return 0 },
		StopAnalytics: func() {},
		StopBroker: func(ctx context.Context) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("broker shutdown context has no deadline")
			}
			steps = append(steps, "broker")
		},
		ShutdownHTTP: func(context.Context) error { steps = append(steps, "http"); return nil },
	}, func(string, ...any) {})
	if got := strings.Join(steps, ","); got != "broker,http" {
		t.Errorf("steps = %s, want broker,http", got)
	}
}

func TestAwaitShutdown_StopsEgressBeforeTheBroker(t *testing.T) {
	var steps []string
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGTERM
	awaitShutdown(stop, shutdownSteps{
		DetachAll:     func() int { return 0 },
		StopAnalytics: func() {},
		StopEgress: func(ctx context.Context) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("egress shutdown context has no deadline")
			}
			steps = append(steps, "egress")
		},
		StopBroker:   func(context.Context) { steps = append(steps, "broker") },
		ShutdownHTTP: func(context.Context) error { steps = append(steps, "http"); return nil },
	}, func(string, ...any) {})
	if got := strings.Join(steps, ","); got != "egress,broker,http" {
		t.Errorf("steps = %s, want egress,broker,http", got)
	}
}

// Carry 1: the Host guard refuses everything when AllowedHosts is unset, so the
// serve wiring must fill it from the config.
func TestConfigureServer_HostAllowlistWired(t *testing.T) {
	srv := &web.Server{}
	configureServer(srv, config.Config{BindAddr: "127.0.0.1:8899", EnvLabel: "DEV"})
	if srv.EnvLabel != "DEV" {
		t.Errorf("EnvLabel not wired: %q", srv.EnvLabel)
	}
	h := srv.Handler()
	for host, want := range map[string]int{
		"127.0.0.1:8899":    http.StatusOK,
		"localhost:8899":    http.StatusOK,
		"evil.example:8899": http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("GET /login Host %s = %d, want %d", host, rec.Code, want)
		}
	}
}

// Carry 4: a wildcard bind is not something a browser can open.
func TestLoginURLFor(t *testing.T) {
	const tok = "TOKEN"
	cases := []struct {
		cfg      config.Config
		want     string
		wantHint bool
	}{
		{config.Config{BindAddr: "127.0.0.1:8899"}, "http://127.0.0.1:8899/login#token=TOKEN", false},
		{config.Config{BindAddr: "[::1]:8899"}, "http://[::1]:8899/login#token=TOKEN", false},
		{config.Config{BindAddr: "100.76.118.121:9000"}, "http://100.76.118.121:9000/login#token=TOKEN", false},
		{config.Config{BindAddr: "0.0.0.0:8899", AllowedHosts: []string{"box.example", "mac.lan:8899"}}, "http://mac.lan:8899/login#token=TOKEN", false},
		{config.Config{BindAddr: "0.0.0.0:8899"}, "http://<host>:8899/login#token=TOKEN", true},
		{config.Config{BindAddr: ":8899"}, "http://<host>:8899/login#token=TOKEN", true},
		{config.Config{BindAddr: "[::]:8899"}, "http://<host>:8899/login#token=TOKEN", true},
	}
	for _, c := range cases {
		got, hint := loginURLFor(c.cfg, tok)
		if got != c.want || (hint != "") != c.wantHint {
			t.Errorf("loginURLFor(%q, %v) = %q, hint %q; want %q, hint=%v",
				c.cfg.BindAddr, c.cfg.AllowedHosts, got, hint, c.want, c.wantHint)
		}
	}
}

// Carry 2: a wildcard bind with no allowed_hosts answers every browser with 403.
func TestBindWarning(t *testing.T) {
	for bind, warn := range map[string]bool{
		"127.0.0.1:8899": false, "100.76.118.121:9000": false,
		"0.0.0.0:8899": true, ":8899": true, "[::]:8899": true,
	} {
		got := bindWarning(config.Config{BindAddr: bind})
		if (got != "") != warn {
			t.Errorf("bindWarning(%q) = %q, want warning=%v", bind, got, warn)
		}
	}
	if got := bindWarning(config.Config{BindAddr: "0.0.0.0:8899", AllowedHosts: []string{"mac.lan:8899"}}); got != "" {
		t.Errorf("wildcard with allowed_hosts warned: %q", got)
	}
}

// Carry 2: without an absolute home, the data dir and config search paths
// would silently become relative to wherever serve was started.
func TestServeHome(t *testing.T) {
	for _, c := range []struct {
		home string
		err  error
		ok   bool
	}{
		{"/home/u", nil, true},
		{"", nil, false},
		{"relative/home", nil, false},
		{"", fmt.Errorf("$HOME is not defined"), false},
	} {
		got, err := serveHome(func() (string, error) { return c.home, c.err })
		if (err == nil) != c.ok || (c.ok && got != c.home) {
			t.Errorf("serveHome(%q,%v) = %q,%v want ok=%v", c.home, c.err, got, err, c.ok)
		}
	}
}

// `nohup agent-box serve &` ignores SIGHUP on purpose. signal.Notify would
// re-enable it, so an ignored SIGHUP must stay out of the registered set.
func TestShutdownSignalsFor(t *testing.T) {
	notIgnored := shutdownSignalsFor(func(os.Signal) bool { return false })
	if len(notIgnored) != len(shutdownSignals) {
		t.Errorf("not ignored: got %v, want %v", notIgnored, shutdownSignals)
	}
	hupIgnored := shutdownSignalsFor(func(s os.Signal) bool { return s == syscall.SIGHUP })
	want := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if fmt.Sprint(hupIgnored) != fmt.Sprint(want) {
		t.Errorf("SIGHUP ignored (nohup): got %v, want %v", hupIgnored, want)
	}
	// Only SIGHUP follows the inherited disposition: INT and TERM are how the
	// operator and systemd stop the daemon, and must always take the clean path.
	allIgnored := shutdownSignalsFor(func(os.Signal) bool { return true })
	if fmt.Sprint(allIgnored) != fmt.Sprint(want) {
		t.Errorf("all ignored: got %v, want %v", allIgnored, want)
	}
}

// A signal registered before Reconcile can be handled before Serve starts:
// Shutdown first, then Serve, must still be the clean ErrServerClosed exit.
func TestServeAfterShutdownIsCleanExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.NotFoundHandler()}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve after Shutdown = %v, want ErrServerClosed", err)
	}
}

// profiles.json follows the data dir, so moving data_dir moves the profiles too.
func TestProfilesPathFor(t *testing.T) {
	if got := profilesPathFor(config.Config{}, "/srv/ab"); got != "/srv/ab/profiles.json" {
		t.Errorf("default: got %q", got)
	}
	if got := profilesPathFor(config.Config{ProfilesPath: "/x/p.json"}, "/srv/ab"); got != "/x/p.json" {
		t.Errorf("explicit profiles_path: got %q", got)
	}
}

func TestWarnLeftBehindProfiles(t *testing.T) {
	home := t.TempDir()
	oldRoot := filepath.Join(home, ".local", "share", "agent-box")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "profiles.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	moved := filepath.Join(t.TempDir(), "profiles.json")
	warnLeftBehindProfiles(config.Config{}, home, moved)
	if !strings.Contains(buf.String(), "profiles used to live there") {
		t.Fatalf("no warning for profiles left in the old root: %q", buf.String())
	}
	buf.Reset()
	warnLeftBehindProfiles(config.Config{}, home, filepath.Join(oldRoot, "profiles.json"))
	warnLeftBehindProfiles(config.Config{ProfilesPath: moved}, home, moved)
	if buf.Len() != 0 {
		t.Fatalf("warned without cause: %q", buf.String())
	}
}

func TestParseBoxSubcommands(t *testing.T) {
	a, err := parseArgs([]string{"box", "setup", "-rebuild"})
	if err != nil || a.Cmd != "box" || a.Sub != "setup" || !a.Rebuild {
		t.Fatalf("got %+v %v", a, err)
	}
	a, err = parseArgs([]string{"box", "stop", "-force"})
	if err != nil || a.Sub != "stop" || !a.Force {
		t.Fatalf("got %+v %v", a, err)
	}
	a, err = parseArgs([]string{"box", "setup", "-recreate", "-force"})
	if err != nil || !a.Recreate || !a.Force {
		t.Fatalf("got %+v %v", a, err)
	}
	for _, bad := range [][]string{{"box"}, {"box", "nuke"}, {"box", "status", "-force"}, {"box", "stop", "-rebuild"},
		{"box", "setup", "-force"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

// The kill-switch must reach the Server (it feeds /api/meta, which the client
// reads to fail closed), and stay off when the config is silent.
func TestConfigureServer_RepliesEnabledWired(t *testing.T) {
	on := &web.Server{}
	configureServer(on, config.Config{BindAddr: "127.0.0.1:8899", RepliesEnabled: true})
	if !on.RepliesEnabled {
		t.Error("RepliesEnabled not wired")
	}
	off := &web.Server{}
	configureServer(off, config.Config{BindAddr: "127.0.0.1:8899"})
	if off.RepliesEnabled {
		t.Error("RepliesEnabled on without config")
	}
}
