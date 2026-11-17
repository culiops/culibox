package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/service"
)

func TestParseService(t *testing.T) {
	for _, sub := range []string{"install", "uninstall", "status"} {
		got, err := parseArgs([]string{"service", sub})
		if err != nil || got != (cliArgs{Cmd: "service", Sub: sub}) {
			t.Errorf("service %s = %+v, %v", sub, got, err)
		}
	}
	got, err := parseArgs([]string{"service", "install", "-config", "c.json"})
	if err != nil || got != (cliArgs{Cmd: "service", Sub: "install", ConfigPath: "c.json", ConfigSet: true}) {
		t.Errorf("-config: %+v %v", got, err)
	}
	for _, bad := range [][]string{{"service"}, {"service", "-config", "x"}, {"service", "start"}, {"service", "install", "-force"},
		{"service", "install", "extra"}, {"service", "status", "-rebuild"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%q): want error", bad)
		}
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestServiceEnv(t *testing.T) {
	got := serviceEnv(envOf(map[string]string{
		"PATH": "/opt/homebrew/bin:/usr/bin", "XDG_CONFIG_HOME": "rel", "LANG": "en_US.UTF-8",
		"LC_CTYPE": "UTF-8", "AB_PROD_PASS": "secret", "CULIBOX_HOME": "/ignored-the-resolved-one-wins",
	}), "/data/root")
	want := []service.EnvVar{{Key: "PATH", Value: "/opt/homebrew/bin:/usr/bin"}, {Key: "CULIBOX_HOME", Value: "/data/root"},
		{Key: "AGENT_BOX_HOME", Value: ""}, {Key: "XDG_CONFIG_HOME", Value: ""}, {Key: "LANG", Value: "en_US.UTF-8"}, {Key: "LC_CTYPE", Value: "UTF-8"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	got = serviceEnv(envOf(map[string]string{"PATH": "/usr/bin", "XDG_CONFIG_HOME": "/x/cfg", "LC_ALL": "C.UTF-8"}), "")
	want = []service.EnvVar{{Key: "PATH", Value: "/usr/bin"}, {Key: "CULIBOX_HOME", Value: ""}, {Key: "AGENT_BOX_HOME", Value: ""},
		{Key: "XDG_CONFIG_HOME", Value: "/x/cfg"}, {Key: "LC_ALL", Value: "C.UTF-8"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// unitRunner is a scripted boxvm.Runner for the command tests.
type unitRunner struct {
	calls    []string
	active   bool
	stayDown bool              // restart does not make it active (serve exits at once)
	fail     map[string]string // call prefix → output, returned with an error
}

func (r *unitRunner) Run(_ context.Context, _ []string, bin string, args ...string) (string, error) {
	call := strings.Join(append([]string{bin}, args...), " ")
	r.calls = append(r.calls, call)
	for k, out := range r.fail {
		if strings.HasPrefix(call, k) {
			return out, errors.New("exit status 1")
		}
	}
	if strings.Contains(call, " restart ") && !r.stayDown {
		r.active = true
	}
	if strings.Contains(call, "is-active") {
		if r.active {
			return "active\n", nil
		}
		return "inactive\n", errors.New("exit status 3")
	}
	if call == "systemctl --version" {
		return "systemd 255 (255.4-1ubuntu8)\n", nil
	}
	if strings.Contains(call, "MainPID") {
		return "99\n", nil
	}
	return "", nil
}

type unitFixture struct {
	cfg     config.Config
	home    string
	deps    serviceDeps
	runner  *unitRunner
	held    bool
	answers bool
}

func newUnitFixture(t *testing.T) *unitFixture {
	t.Helper()
	home := t.TempDir()
	f := &unitFixture{home: home, runner: &unitRunner{}}
	f.cfg = config.Config{BindAddr: "127.0.0.1:8899", DataDir: filepath.Join(home, ".local", "share", "culibox")}
	f.deps = serviceDeps{
		Plat: service.Platform{GOOS: "linux", UID: 1000, User: "u", Home: home, LingerDir: t.TempDir(),
			Run: f.runner, Sleep: func(time.Duration) {}},
		Exe:     func() (string, error) { return "/home/u/.local/bin/culibox", nil },
		TempDir: "/tmp",
		Getenv:  envOf(map[string]string{"PATH": "/usr/bin:/bin"}),
		Held:    func(string) (bool, error) { return f.held, nil },
		Answers: func(string) bool { return f.answers },
	}
	return f
}

func (f *unitFixture) run(t *testing.T, sub string, extra ...func(*cliArgs)) (int, string) {
	t.Helper()
	a := cliArgs{Cmd: "service", Sub: sub}
	for _, x := range extra {
		x(&a)
	}
	var out strings.Builder
	code := runService(a, f.cfg, f.home, "", f.deps, &out)
	return code, out.String()
}

func TestServiceInstall(t *testing.T) {
	f := newUnitFixture(t)
	code, out := f.run(t, "install")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	unit := f.deps.Plat.UnitPath()
	b, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`ExecStart="/home/u/.local/bin/culibox" "serve" "-service"` + "\n", `Environment="PATH=/usr/bin:/bin"`,
		"append:" + filepath.Join(f.cfg.DataDir, "service.log")} {
		if !strings.Contains(string(b), want) {
			t.Errorf("unit lacks %q:\n%s", want, b)
		}
	}
	for _, want := range []string{unit, filepath.Join(f.cfg.DataDir, "service.log"), "http://127.0.0.1:8899/login",
		filepath.Join(f.cfg.DataDir, "login.token"), "loginctl enable-linger u"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "#token=") {
		t.Errorf("output carries a token URL:\n%s", out)
	}
	if fi, err := os.Stat(f.cfg.DataDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir: %v %v", fi, err)
	}
}

func TestServiceInstallRefusals(t *testing.T) {
	cases := []struct {
		name string
		set  func(*unitFixture)
		want string
	}{
		{"tailnet bind", func(f *unitFixture) { f.cfg.BindAddr = "100.64.0.2:9000" }, "not loopback"},
		{"public bind", func(f *unitFixture) { f.cfg.BindAddr = "0.0.0.0:8899"; f.cfg.AllowPublic = true }, "not loopback"},
		{"password_env", func(f *unitFixture) { f.cfg.PasswordEnv = "AB_PASS" }, "password_env"},
		{"temp exe", func(f *unitFixture) {
			f.deps.Exe = func() (string, error) { return "/tmp/culibox", nil }
		}, "temporary"},
		{"go run", func(f *unitFixture) {
			f.deps.Exe = func() (string, error) { return "/home/u/.cache/go-build123/b001/exe/culibox", nil }
		}, "temporary"},
		{"lock held", func(f *unitFixture) { f.held = true }, "already serving"},
		{"port answers", func(f *unitFixture) { f.answers = true }, "already serving"},
		{"unsupported", func(f *unitFixture) { f.deps.Plat.GOOS = "freebsd" }, "not supported"},
		{"legacy root", func(f *unitFixture) { f.cfg.DataDir = filepath.Join(f.home, ".local", "share", "agent-box") }, "culibox serve once"},
		{"relative data_dir", func(f *unitFixture) { f.cfg.DataDir = "data" }, "relative"},
		{"no user bus", func(f *unitFixture) {
			f.runner.fail = map[string]string{"systemctl --user show-environment": "Failed to connect to bus: No medium found\n"}
		}, "not reachable"},
	}
	for _, c := range cases {
		f := newUnitFixture(t)
		c.set(f)
		code, out := f.run(t, "install")
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, output %q (want %q)", c.name, code, out, c.want)
		}
		if _, err := os.Lstat(f.deps.Plat.UnitPath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: unit written", c.name)
		}
		if _, err := os.Lstat(f.cfg.DataDir); c.name != "legacy root" && c.name != "relative data_dir" && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: data dir created", c.name)
		}
		for _, call := range f.runner.calls {
			if !strings.Contains(call, "is-active") && !strings.Contains(call, "show-environment") {
				t.Errorf("%s: supervisor changed: %q", c.name, call)
			}
		}
	}
}

func TestServiceInstallRefreshWhileRunning(t *testing.T) {
	f := newUnitFixture(t)
	if code, out := f.run(t, "install"); code != 0 {
		t.Fatal(out)
	}
	f.runner.active, f.answers, f.held = true, true, true
	f.runner.calls = nil
	code, out := f.run(t, "install")
	if code != 0 {
		t.Fatalf("refresh refused: %s", out)
	}
	if !slices.Contains(f.runner.calls, "systemctl --user restart culibox-serve.service") {
		t.Fatalf("no restart: %q", f.runner.calls)
	}
}

func TestServiceInstallConfigPinned(t *testing.T) {
	f := newUnitFixture(t)
	t.Chdir(f.home)
	code, out := f.run(t, "install", func(a *cliArgs) { a.ConfigPath, a.ConfigSet = "rel.json", true })
	if code != 0 {
		t.Fatal(out)
	}
	b, _ := os.ReadFile(f.deps.Plat.UnitPath())
	if !strings.Contains(string(b), `"serve" "-service" "-config" "`+filepath.Join(f.home, "rel.json")+`"`) {
		t.Fatalf("unit:\n%s", b)
	}
}

func TestServiceInstallWarnsEnvToken(t *testing.T) {
	f := newUnitFixture(t)
	f.cfg.StaticTokens = []config.StaticToken{{Name: "gh", Value: config.KeyRef{Type: "env", Var: "GH_TOKEN"}}}
	code, out := f.run(t, "install")
	if code != 0 || !strings.Contains(out, "GH_TOKEN") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	b, _ := os.ReadFile(f.deps.Plat.UnitPath())
	if strings.Contains(string(b), "GH_TOKEN") {
		t.Fatal("the unit names the token variable")
	}
}

func TestServiceStatusExitCodes(t *testing.T) {
	f := newUnitFixture(t)
	if code, out := f.run(t, "status"); code != 3 || !strings.Contains(out, "not installed") {
		t.Errorf("absent: %d %s", code, out)
	}
	f.run(t, "install")
	f.runner.active = false
	if code, out := f.run(t, "status"); code != 3 || !strings.Contains(out, "inactive") {
		t.Errorf("inactive: %d %s", code, out)
	}
	f.runner.active = true
	code, out := f.run(t, "status")
	if code != 0 || !strings.Contains(out, "pid 99") || !strings.Contains(out, "linger: off") {
		t.Errorf("active: %d %s", code, out)
	}
	f.deps.Exe = func() (string, error) { return "/usr/local/bin/culibox", nil }
	if _, out := f.run(t, "status"); !strings.Contains(out, "/home/u/.local/bin/culibox") || !strings.Contains(out, "install again") {
		t.Errorf("exe mismatch not shown: %s", out)
	}
	f.runner.fail = map[string]string{"systemctl --user is-active": "Failed to connect to bus\n"}
	if code, _ := f.run(t, "status"); code != 1 {
		t.Errorf("unreachable: %d", code)
	}
}

func TestServiceUninstall(t *testing.T) {
	f := newUnitFixture(t)
	if code, out := f.run(t, "uninstall"); code != 0 || !strings.Contains(out, "not installed") {
		t.Errorf("absent: %d %s", code, out)
	}
	unit := f.deps.Plat.UnitPath()
	os.MkdirAll(filepath.Dir(unit), 0o755)
	os.WriteFile(unit, []byte("[Unit]\n"), 0o644)
	if code, out := f.run(t, "uninstall"); code != 1 || !strings.Contains(out, "not written by culibox") {
		t.Errorf("foreign: %d %s", code, out)
	}
	os.Remove(unit)
	f.run(t, "install")
	code, out := f.run(t, "uninstall")
	if code != 0 || !strings.Contains(out, "culibox serve") {
		t.Errorf("ours: %d %s", code, out)
	}
	if _, err := os.Lstat(unit); !errors.Is(err, os.ErrNotExist) {
		t.Error("unit left behind")
	}
}

func TestParseServeService(t *testing.T) {
	got, err := parseArgs([]string{"serve", "-service", "-config", "/c.json"})
	if err != nil || !got.Service || got.Cmd != "serve" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := parseArgs([]string{"doctor", "-service"}); err == nil {
		t.Error("doctor -service accepted")
	}
}

func TestServiceServeRefusal(t *testing.T) {
	ok := config.Config{BindAddr: "127.0.0.1:8899"}
	if why := serviceServeRefusal(ok); why != "" {
		t.Errorf("loopback refused: %s", why)
	}
	for _, c := range []config.Config{{BindAddr: "100.64.0.2:9000"}, {BindAddr: "0.0.0.0:8899", AllowPublic: true},
		{BindAddr: "127.0.0.1:8899", PasswordEnv: "P"}} {
		if serviceServeRefusal(c) == "" {
			t.Errorf("%+v allowed", c)
		}
	}
}

func TestServiceInstallLogIsPrivate(t *testing.T) {
	f := newUnitFixture(t)
	os.MkdirAll(f.cfg.DataDir, 0o755) // an older root that is not 0700
	logPath := filepath.Join(f.cfg.DataDir, "service.log")
	os.WriteFile(logPath, []byte("old\n"), 0o644)
	if code, out := f.run(t, "install"); code != 0 {
		t.Fatal(out)
	}
	fi, err := os.Stat(logPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v %v", fi, err)
	}
	if b, _ := os.ReadFile(logPath); string(b) != "old\n" {
		t.Fatalf("log truncated: %q", b)
	}
}

func TestServiceInstallRefusesLogSymlink(t *testing.T) {
	f := newUnitFixture(t)
	os.MkdirAll(f.cfg.DataDir, 0o700)
	target := filepath.Join(f.home, "elsewhere")
	os.WriteFile(target, nil, 0o644)
	os.Symlink(target, filepath.Join(f.cfg.DataDir, "service.log"))
	if code, out := f.run(t, "install"); code != 1 || !strings.Contains(out, "service.log") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
		t.Fatal("symlink target chmodded")
	}
	if _, err := os.Lstat(f.deps.Plat.UnitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unit written")
	}
}

func TestServiceInstallServeExitsAtOnce(t *testing.T) {
	f := newUnitFixture(t)
	f.runner.stayDown = true
	code, out := f.run(t, "install")
	if code != 1 || !strings.Contains(out, "not running") || !strings.Contains(out, "service.log") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
