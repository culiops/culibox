package service

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func testPlat(t *testing.T, goos string, r *fakeRunner) Platform {
	t.Helper()
	return Platform{GOOS: goos, UID: 501, User: "u", Home: t.TempDir(), LingerDir: t.TempDir(),
		Run: r, Sleep: func(time.Duration) {}}
}

func TestUnitPath(t *testing.T) {
	p := Platform{GOOS: "linux", Home: "/home/u"}
	if got := p.UnitPath(); got != "/home/u/.config/systemd/user/culibox-serve.service" {
		t.Errorf("linux: %s", got)
	}
	p.XDGConfig = "/x/cfg"
	if got := p.UnitPath(); got != "/x/cfg/systemd/user/culibox-serve.service" {
		t.Errorf("linux xdg: %s", got)
	}
	p.XDGConfig = "rel/cfg"
	if got := p.UnitPath(); got != "/home/u/.config/systemd/user/culibox-serve.service" {
		t.Errorf("linux relative xdg: %s", got)
	}
	p = Platform{GOOS: "darwin", Home: "/Users/u", XDGConfig: "/x/cfg"}
	if got := p.UnitPath(); got != "/Users/u/Library/LaunchAgents/dev.culiops.culibox.plist" {
		t.Errorf("darwin: %s", got)
	}
	if (Platform{GOOS: "freebsd"}).Supported() || !(Platform{GOOS: "darwin"}).Supported() || !(Platform{GOOS: "linux"}).Supported() {
		t.Error("Supported")
	}
}

func TestLinger(t *testing.T) {
	p := testPlat(t, "linux", &fakeRunner{})
	if on, ok := p.Linger(); on || !ok {
		t.Errorf("absent: %v %v", on, ok)
	}
	os.WriteFile(filepath.Join(p.LingerDir, "u"), nil, 0o644)
	if on, ok := p.Linger(); !on || !ok {
		t.Errorf("present: %v %v", on, ok)
	}
	p.GOOS = "darwin"
	if _, ok := p.Linger(); ok {
		t.Error("darwin linger known")
	}
}

func writeOurs(t *testing.T, p Platform) {
	t.Helper()
	render := RenderSystemd
	if p.GOOS == "darwin" {
		render = RenderLaunchd
	}
	b, err := render(baseSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteUnit(p.UnitPath(), b); err != nil {
		t.Fatal(err)
	}
}

func TestStatusSystemd(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{}
	p := testPlat(t, "linux", r)
	st, err := p.Status(ctx)
	if err != nil || st.File != Absent || len(r.calls) != 0 {
		t.Fatalf("absent: %+v %v calls %v", st, err, r.calls)
	}
	writeOurs(t, p)
	r.on("systemctl --user is-active", reply{"active\n", nil}).on("systemctl --user show -p MainPID", reply{"4242\n", nil})
	st, err = p.Status(ctx)
	if err != nil || st.File != Ours || !st.Running || st.PID != "4242" || st.Exe != baseSpec().Exe || st.Detail != "active" {
		t.Fatalf("active: %+v %v", st, err)
	}
	if !st.LingerKnown || st.Linger {
		t.Errorf("linger: %+v", st)
	}
	for _, state := range []string{"inactive", "failed", "activating"} {
		r.on("systemctl --user is-active", reply{state + "\n", errExit})
		st, err = p.Status(ctx)
		if err != nil || st.Running || st.Detail != state {
			t.Errorf("%s: %+v %v", state, st, err)
		}
	}
	r.on("systemctl --user is-active", reply{"Failed to connect to bus: No medium found\n", errExit})
	if _, err := p.Status(ctx); err == nil {
		t.Error("bus failure: no error")
	}
}

func TestStatusLaunchd(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{}
	p := testPlat(t, "darwin", r)
	writeOurs(t, p)
	r.on("launchctl print gui/501/dev.culiops.culibox", reply{"gui/501/dev.culiops.culibox = {\n\tstate = running\n\tpid = 777\n}\n", nil})
	st, err := p.Status(ctx)
	if err != nil || !st.Running || st.PID != "777" || st.Exe != baseSpec().Exe || st.LingerKnown {
		t.Fatalf("running: %+v %v", st, err)
	}
	if !slices.Contains(r.calls, "launchctl print gui/501/dev.culiops.culibox") {
		t.Errorf("calls %v", r.calls)
	}
	r.on("launchctl print gui/501/dev.culiops.culibox", reply{"gui/501/dev.culiops.culibox = {\n\tstate = not running\n}\n", nil})
	if st, err = p.Status(ctx); err != nil || st.Running || st.Detail != "not running" {
		t.Errorf("loaded idle: %+v %v", st, err)
	}
	r.on("launchctl print gui/501/dev.culiops.culibox", reply{"Bad request.\nCould not find service \"dev.culiops.culibox\" in domain for user gui: 501\n", errExit})
	if st, err = p.Status(ctx); err != nil || st.Running || st.Detail != "not loaded" {
		t.Errorf("not loaded: %+v %v", st, err)
	}
	r.on("launchctl print gui/501/dev.culiops.culibox", reply{"Operation not permitted\n", errExit})
	if _, err = p.Status(ctx); err == nil {
		t.Error("other failure: no error")
	}
}

func TestStatusForeignSkipsSupervisor(t *testing.T) {
	r := &fakeRunner{}
	p := testPlat(t, "linux", r)
	os.MkdirAll(filepath.Dir(p.UnitPath()), 0o755)
	os.WriteFile(p.UnitPath(), []byte("[Unit]\n"), 0o644)
	st, err := p.Status(context.Background())
	if err != nil || st.File != Foreign || len(r.calls) != 0 {
		t.Fatalf("%+v %v %v", st, err, r.calls)
	}
}

func TestManagerXDG(t *testing.T) {
	r := (&fakeRunner{}).on("systemctl --user show-environment", reply{"HOME=/home/u\nXDG_CONFIG_HOME=/mgr/cfg\nPATH=/usr/bin\n", nil})
	p := testPlat(t, "linux", r)
	if got := p.managerXDG("/shell/cfg"); got != "/mgr/cfg" {
		t.Errorf("manager value: %q", got)
	}
	r.on("systemctl --user show-environment", reply{"HOME=/home/u\n", nil})
	if got := p.managerXDG("/shell/cfg"); got != "" {
		t.Errorf("manager has none (the shell's must not win): %q", got)
	}
	r.on("systemctl --user show-environment", reply{"Failed to connect to bus\n", errExit})
	if got := p.managerXDG("/shell/cfg"); got != "/shell/cfg" {
		t.Errorf("unreachable: %q", got)
	}
	r.on("systemctl --user show-environment", reply{"XDG_CONFIG_HOME=$'/a\\nb'\n", nil})
	if got := p.managerXDG("/shell/cfg"); got != "" {
		t.Errorf("quoted: %q", got)
	}
	p.GOOS = "darwin"
	if got := p.managerXDG("/shell/cfg"); got != "/shell/cfg" {
		t.Errorf("darwin: %q", got)
	}
}
