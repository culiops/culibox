package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const notFound = "Could not find service \"dev.culiops.culibox\" in domain for user gui: 501\n"

func TestInstallSystemdSequence(t *testing.T) {
	r := &fakeRunner{}
	p := testPlat(t, "linux", r)
	if err := p.Install(context.Background(), baseSpec()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user show-environment",
		"systemctl --user daemon-reload",
		"systemctl --user enable culibox-serve.service",
		"systemctl --user restart culibox-serve.service",
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls %q\nwant  %q", r.calls, want)
	}
	b, _ := os.ReadFile(p.UnitPath())
	unit, _ := RenderSystemd(baseSpec())
	if string(b) != string(unit) {
		t.Fatalf("unit:\n%s", b)
	}
}

func TestInstallUnreachableWritesNothing(t *testing.T) {
	for name, rep := range map[string]reply{
		"no systemd": {"", fmt.Errorf("systemctl show-environment: %w", exec.ErrNotFound)},
		"no bus":     {"Failed to connect to bus: No medium found\n", errExit},
	} {
		r := (&fakeRunner{}).on("systemctl --user show-environment", rep)
		p := testPlat(t, "linux", r)
		err := p.Install(context.Background(), baseSpec())
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if name == "no systemd" && !strings.Contains(err.Error(), "systemd is not available") {
			t.Errorf("%s: %v", name, err)
		}
		if _, serr := os.Lstat(p.UnitPath()); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("%s: unit written", name)
		}
		if len(r.calls) != 1 {
			t.Errorf("%s: calls %v", name, r.calls)
		}
	}
	r := (&fakeRunner{}).on("launchctl print gui/501", reply{"Could not find domain for user gui: 501\n", errExit})
	p := testPlat(t, "darwin", r)
	if err := p.Install(context.Background(), baseSpec()); err == nil || !strings.Contains(err.Error(), "GUI login") {
		t.Errorf("darwin no gui: %v", err)
	}
	if _, serr := os.Lstat(p.UnitPath()); !errors.Is(serr, os.ErrNotExist) {
		t.Error("darwin: plist written")
	}
}

func TestInstallForeignRefused(t *testing.T) {
	r := &fakeRunner{}
	p := testPlat(t, "linux", r)
	os.MkdirAll(strings.TrimSuffix(p.UnitPath(), SystemdUnit), 0o755)
	os.WriteFile(p.UnitPath(), []byte("[Unit]\nmine\n"), 0o644)
	if err := p.Install(context.Background(), baseSpec()); !errors.Is(err, ErrForeign) {
		t.Fatalf("err %v", err)
	}
	if !slices.Equal(r.calls, []string{"systemctl --user show-environment"}) {
		t.Fatalf("calls %v", r.calls)
	}
	if b, _ := os.ReadFile(p.UnitPath()); string(b) != "[Unit]\nmine\n" {
		t.Fatal("foreign unit changed")
	}
}

func TestInstallLaunchdFresh(t *testing.T) {
	r := (&fakeRunner{}).on("launchctl print gui/501/dev.culiops.culibox", reply{notFound, errExit})
	p := testPlat(t, "darwin", r)
	if err := p.Install(context.Background(), baseSpec()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"launchctl print gui/501",
		"launchctl print gui/501/dev.culiops.culibox",
		"launchctl enable gui/501/dev.culiops.culibox",
		"launchctl bootstrap gui/501 " + p.UnitPath(),
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls %q\nwant  %q", r.calls, want)
	}
	if st, _ := Inspect(p.UnitPath()); st != Ours {
		t.Fatal("plist not ours")
	}
}

func TestInstallLaunchdRefreshWaitsForBootout(t *testing.T) {
	r := (&fakeRunner{}).on("launchctl print gui/501/dev.culiops.culibox",
		reply{"state = running\n", nil}, reply{"state = running\n", nil}, reply{"state = running\n", nil}, reply{notFound, errExit})
	p := testPlat(t, "darwin", r)
	writeOurs(t, p)
	if err := p.Install(context.Background(), baseSpec()); err != nil {
		t.Fatal(err)
	}
	boot := slices.Index(r.calls, "launchctl bootout gui/501/dev.culiops.culibox")
	strap := slices.Index(r.calls, "launchctl bootstrap gui/501 "+p.UnitPath())
	if boot < 0 || strap < boot {
		t.Fatalf("calls %q", r.calls)
	}
	prints := 0
	for _, c := range r.calls[boot:strap] {
		if c == "launchctl print gui/501/dev.culiops.culibox" {
			prints++
		}
	}
	if prints != 3 {
		t.Fatalf("polled %d times between bootout and bootstrap: %q", prints, r.calls)
	}
}

func TestInstallLaunchdBootoutNeverFinishes(t *testing.T) {
	r := (&fakeRunner{}).on("launchctl print gui/501/dev.culiops.culibox", reply{"state = running\n", nil})
	p := testPlat(t, "darwin", r)
	writeOurs(t, p)
	if err := p.Install(context.Background(), baseSpec()); err == nil {
		t.Fatal("no error")
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "launchctl bootstrap") {
			t.Fatalf("bootstrapped anyway: %q", r.calls)
		}
	}
}

func TestUninstallSystemd(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{}
	p := testPlat(t, "linux", r)
	if removed, err := p.Uninstall(ctx); removed || err != nil || len(r.calls) != 0 {
		t.Fatalf("absent: %v %v %v", removed, err, r.calls)
	}
	os.MkdirAll(strings.TrimSuffix(p.UnitPath(), SystemdUnit), 0o755)
	os.WriteFile(p.UnitPath(), []byte("mine\n"), 0o644)
	if _, err := p.Uninstall(ctx); !errors.Is(err, ErrForeign) || len(r.calls) != 0 {
		t.Fatalf("foreign: %v %v", err, r.calls)
	}
	os.Remove(p.UnitPath())
	writeOurs(t, p)
	r.on("systemctl --user disable", reply{"Failed to connect to bus\n", errExit})
	if _, err := p.Uninstall(ctx); err == nil {
		t.Fatal("disable failure: no error")
	}
	if st, _ := Inspect(p.UnitPath()); st != Ours {
		t.Fatal("unit removed although the service could not be stopped")
	}
	r.calls = nil
	r.on("systemctl --user disable", reply{"", nil})
	if removed, err := p.Uninstall(ctx); !removed || err != nil {
		t.Fatalf("ours: %v %v", removed, err)
	}
	want := []string{"systemctl --user disable --now culibox-serve.service", "systemctl --user daemon-reload"}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls %q", r.calls)
	}
	if st, _ := Inspect(p.UnitPath()); st != Absent {
		t.Fatal("unit still there")
	}
}

func TestUninstallLaunchd(t *testing.T) {
	r := (&fakeRunner{}).on("launchctl print gui/501/dev.culiops.culibox", reply{"state = running\n", nil}, reply{notFound, errExit})
	p := testPlat(t, "darwin", r)
	writeOurs(t, p)
	if removed, err := p.Uninstall(context.Background()); !removed || err != nil {
		t.Fatalf("%v %v", removed, err)
	}
	want := []string{
		"launchctl print gui/501/dev.culiops.culibox",
		"launchctl bootout gui/501/dev.culiops.culibox",
		"launchctl print gui/501/dev.culiops.culibox",
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls %q", r.calls)
	}
	if st, _ := Inspect(p.UnitPath()); st != Absent {
		t.Fatal("plist still there")
	}
	// Not loaded: no bootout, file removed.
	r = (&fakeRunner{}).on("launchctl print gui/501/dev.culiops.culibox", reply{notFound, errExit})
	p.Run = r
	writeOurs(t, p)
	if removed, err := p.Uninstall(context.Background()); !removed || err != nil || len(r.calls) != 1 {
		t.Fatalf("not loaded: %v %v %q", removed, err, r.calls)
	}
}
