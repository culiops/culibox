package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/instancelock"
	"github.com/culiops/culibox/internal/rootmove"
	"github.com/culiops/culibox/internal/service"
)

// serviceDeps is the world `culibox service` touches, injected for the tests.
type serviceDeps struct {
	Plat    service.Platform
	Exe     func() (string, error)
	TempDir string
	Getenv  func(string) string
	Held    func(dataDir string) (bool, error)
	Answers func(addr string) bool
}

func realServiceDeps(home, xdg string) serviceDeps {
	return serviceDeps{Plat: service.Real(home, xdg), Exe: os.Executable, TempDir: os.TempDir(), Getenv: os.Getenv,
		Held: instancelock.Held, Answers: func(addr string) bool {
			c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
			if err == nil {
				c.Close()
			}
			return err == nil
		}}
}

// serviceEnvLocale are the locale variables the service keeps: sessions need a
// UTF-8 locale and launchd gives none.
var serviceEnvLocale = []string{"LANG", "LC_ALL", "LC_CTYPE"}

// serviceEnv is the environment the unit records: where programs and the
// config are found, and the locale. Never a secret: password_env and token
// variables are not read here at all. envHome is config.HomeEnv's resolved
// root (absolute by its own rule), "" when unset. The root and config
// variables are always written, empty when unset (both read "" as unset), so
// a value in the supervisor's own environment cannot make the service load
// another config than the one install checked.
func serviceEnv(getenv func(string) string, envHome string) []service.EnvVar {
	var out []service.EnvVar
	if v := getenv("PATH"); v != "" {
		out = append(out, service.EnvVar{Key: "PATH", Value: v})
	}
	xdg := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(xdg) {
		xdg = ""
	}
	out = append(out, service.EnvVar{Key: "CULIBOX_HOME", Value: envHome},
		service.EnvVar{Key: "AGENT_BOX_HOME", Value: ""},
		service.EnvVar{Key: "XDG_CONFIG_HOME", Value: xdg})
	for _, k := range serviceEnvLocale {
		if v := getenv(k); v != "" {
			out = append(out, service.EnvVar{Key: k, Value: v})
		}
	}
	return out
}

// serviceServeRefusal is why serve, started by the service, must not run with
// cfg ("" when it may): the two rules install checks.
func serviceServeRefusal(cfg config.Config) string {
	if !config.LoopbackBind(cfg.BindAddr) {
		return fmt.Sprintf("bind_addr %s is not loopback; a service would serve it unattended. Use 127.0.0.1 (or ::1, localhost), or keep running culibox serve yourself", cfg.BindAddr)
	}
	if cfg.PasswordEnv != "" {
		return fmt.Sprintf("password_env is set (%s), and the service carries no secret, so serve could not read the password. Unset password_env to use the login token, or keep running culibox serve yourself", cfg.PasswordEnv)
	}
	return ""
}

// serviceRefusedExit is serve's exit code when it refuses to run as the
// service. systemd is told not to restart on ExitRefused; launchd has no such
// key and restarts any failure under KeepAlive/SuccessfulExit=false, so on
// darwin the refusal is a clean exit instead of a respawn every 10 s.
func serviceRefusedExit(goos string) int {
	if goos == "darwin" {
		return 0
	}
	return service.ExitRefused
}

// privateStderr keeps the service log 0600 when serve writes it directly (a
// log the user deleted is recreated 0644 by the supervisor; it holds the
// login URL with its token).
func privateStderr(f *os.File) {
	if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o077 != 0 {
		f.Chmod(0o600)
	}
}

// runService is `culibox service install|uninstall|status`.
func runService(a cliArgs, cfg config.Config, home, envHome string, d serviceDeps, w io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(w, "culibox service %s: %s\n", a.Sub, fmt.Sprintf(format, args...))
		return 1
	}
	if !d.Plat.Supported() {
		return fail("not supported on %s (only Linux with systemd and macOS)", d.Plat.GOOS)
	}
	dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
	if err != nil {
		return fail("data dir: %v", err)
	}
	logPath := filepath.Join(dataDir, "service.log")
	ctx := context.Background()
	switch a.Sub {
	case "uninstall":
		removed, err := d.Plat.Uninstall(ctx)
		if err != nil {
			return fail("%v", err)
		}
		if !removed {
			fmt.Fprintf(w, "not installed (%s does not exist)\n", d.Plat.UnitPath())
			return 0
		}
		fmt.Fprintf(w, "stopped the service and removed %s\n", d.Plat.UnitPath())
		fmt.Fprintf(w, "running sessions keep running; culibox serve (or culibox service install) reattaches them\n")
		fmt.Fprintf(w, "the log %s is kept\n", logPath)
		return 0
	case "status":
		return serviceStatus(ctx, d, logPath, w)
	}
	return serviceInstall(ctx, a, cfg, home, envHome, dataDir, logPath, d, w, fail)
}

func serviceInstall(ctx context.Context, a cliArgs, cfg config.Config, home, envHome, dataDir, logPath string,
	d serviceDeps, w io.Writer, fail func(string, ...any) int) int {
	// The next serve start moves the old root, and the unit's log path would
	// then name it through the link the move leaves. Refused only when serve
	// would move it: never a root named by CULIBOX_HOME/AGENT_BOX_HOME
	// (maybeMoveDataRoot), nor one a Mac's box VM keeps in place (rootmove
	// precondition (f)). There it is the root to install for.
	if _, legacy := config.DefaultRoots(home); envHome == "" && dataDir == filepath.Clean(legacy) &&
		!rootmove.VMKeepsRoot(d.Plat.GOOS, dataDir) {
		return fail("the data root is still %s; run culibox serve once (it moves the root to its new place), stop it, then install", dataDir)
	}
	if cfg.DataDir != "" && !filepath.IsAbs(cfg.DataDir) {
		return fail("data_dir %q is relative; the service starts in another folder than this shell. Make it absolute", cfg.DataDir)
	}
	if why := serviceServeRefusal(cfg); why != "" {
		return fail("%s", why)
	}
	exe, err := d.Exe()
	if err != nil {
		return fail("cannot tell which culibox is running: %v", err)
	}
	exe = filepath.Clean(exe)
	if strings.Contains(exe, string(filepath.Separator)+"go-build") || pathUnder(exe, d.TempDir) {
		return fail("%s is a temporary build (go run?); install culibox (setup.sh, a release, or go build) and run that one", exe)
	}
	st, _ := d.Plat.Status(ctx) // unreachable: Install says so before writing anything
	if !(st.File == service.Ours && st.Running) {
		if held, _ := d.Held(dataDir); held {
			return fail("a CuliBox is already serving %s; stop it (sessions survive) and run install again", dataDir)
		}
		if d.Answers(cfg.BindAddr) {
			return fail("something is already serving %s (a culibox serve?); stop it (sessions survive) and run install again", cfg.BindAddr)
		}
	}
	for _, t := range cfg.StaticTokens {
		if t.Value.Type == "env" {
			fmt.Fprintf(w, "warning: token %s reads $%s, which the service will not have; store the value with culibox store set and reference it instead\n", t.Name, t.Value.Var)
		}
	}
	// Before anything is created: a refused install leaves nothing behind.
	if err := d.Plat.Reachable(ctx); err != nil {
		return fail("%v", err)
	}
	if st, err := service.Inspect(d.Plat.UnitPath()); err != nil || st == service.Foreign {
		return fail("%s: %s; CuliBox leaves it alone (remove it yourself if it is a leftover)", d.Plat.UnitPath(), service.ErrForeign)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fail("data dir: %v", err)
	}
	if err := privateLog(logPath); err != nil {
		return fail("log %s: %v", logPath, err)
	}
	args := []string{"serve", "-service"}
	if a.ConfigSet {
		p, err := filepath.Abs(a.ConfigPath)
		if err != nil {
			return fail("config path: %v", err)
		}
		args = append(args, "-config", p)
	}
	spec := service.Spec{Exe: exe, Args: args, Env: serviceEnv(d.Getenv, envHome), LogPath: logPath}
	if err := d.Plat.Install(ctx, spec); err != nil {
		return fail("%v", err)
	}
	// Type=simple and launchd report a start before serve has bound: look once
	// more after a moment, so a serve that exits at once is not called a success.
	d.Plat.Sleep(2 * time.Second)
	if st, err := d.Plat.Status(ctx); err == nil && !st.Running {
		return fail("installed %s, but the service is not running (%s); see %s", d.Plat.UnitPath(), st.Detail, logPath)
	}
	fmt.Fprintf(w, "installed %s and started it\n", d.Plat.UnitPath())
	fmt.Fprintf(w, "log: %s\n", logPath)
	fmt.Fprintf(w, "open http://%s/login; the login token is in %s\n", cfg.BindAddr, filepath.Join(dataDir, "login.token"))
	writeLinger(w, d.Plat)
	fmt.Fprintf(w, "after replacing the culibox binary or changing PATH, run culibox service install again\n")
	return 0
}

// privateLog makes the service log a 0600 file before the supervisor opens
// it: systemd and launchd would create it 0644, and serve logs the login URL
// with its token at every start. Never through a symlink.
func privateLog(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	return f.Chmod(0o600)
}

// pathUnder reports whether p is dir or inside it.
func pathUnder(p, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeLinger(w io.Writer, p service.Platform) {
	on, ok := p.Linger()
	switch {
	case !ok:
	case on:
		fmt.Fprintf(w, "linger: on (the service keeps running after you log out and starts at boot)\n")
	default:
		fmt.Fprintf(w, "linger: off, so when you log out the service stops AND its running agent sessions are killed; it starts again at your next login.\n")
		fmt.Fprintf(w, "  To keep it and its sessions running after logout, and start it at boot: loginctl enable-linger %s (it may ask for an admin password)\n", p.User)
	}
}

// serviceStatus exits 0 when installed and running, 3 when not installed or
// not running, 1 when the supervisor could not be asked.
func serviceStatus(ctx context.Context, d serviceDeps, logPath string, w io.Writer) int {
	st, err := d.Plat.Status(ctx)
	fmt.Fprintf(w, "service file: %s\n", st.Path)
	switch st.File {
	case service.Absent:
		fmt.Fprintf(w, "installed: no (not installed; culibox service install adds it)\n")
	case service.Foreign:
		fmt.Fprintf(w, "installed: no, a file CuliBox did not write is there; CuliBox leaves it alone\n")
	default:
		fmt.Fprintf(w, "installed: yes\n")
	}
	if err != nil {
		fmt.Fprintf(w, "running: could not tell: %v\n", err)
		return 1
	}
	if st.File != service.Ours {
		return 3
	}
	if st.Running {
		fmt.Fprintf(w, "running: yes (pid %s)\n", st.PID)
	} else {
		fmt.Fprintf(w, "running: no (%s); see %s\n", st.Detail, logPath)
	}
	if exe, err := d.Exe(); err == nil && st.Exe != "" && filepath.Clean(exe) != st.Exe {
		fmt.Fprintf(w, "program: %s, not this culibox (%s); run culibox service install again to switch\n", st.Exe, exe)
	} else if st.Exe != "" {
		fmt.Fprintf(w, "program: %s\n", st.Exe)
	}
	writeLinger(w, d.Plat)
	fmt.Fprintf(w, "log: %s\n", logPath)
	if !st.Running {
		return 3
	}
	return 0
}
