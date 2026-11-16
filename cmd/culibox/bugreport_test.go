package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/bugreport"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/doctor"
)

// The report is the product: a FAIL in doctor must not turn into a non-zero
// exit that makes `agent-box bug-report | pbcopy` look broken.
func TestRunBugReportExitsZeroOnDoctorFail(t *testing.T) {
	home := t.TempDir()
	in := doctor.Input{Home: home, Commit: "abc123", ConfigErr: errors.New("config: boom")}
	var out, errw bytes.Buffer
	if code := runBugReport(in, "somehost", "linux", &out, &errw); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "culibox bug report v1") {
		t.Errorf("stdout starts %q", firstLine(out.String()))
	}
	for _, want := range []string{"Issue link: https://", "pbcopy"} {
		if !strings.Contains(errw.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errw.String())
		}
	}
	if strings.Contains(out.String(), "Issue link") {
		t.Error("the issue link belongs on stderr, not in the bundle")
	}
}

// A profiles file that cannot be read still yields a report: seeds only, and
// the bundle says why.
func TestRunBugReportUnreadableProfilesNotesIt(t *testing.T) {
	home := t.TempDir()
	cfg, src, err := config.Resolve("", false, home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.ResolvedDataDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	pp := cfg.ResolvedProfilesPath(cfg.ResolvedDataDir(home))
	if err := os.WriteFile(pp, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := doctor.Input{Config: cfg, Source: src, Home: home, Commit: "abc123"}
	var out, errw bytes.Buffer
	if code := runBugReport(in, "somehost", "linux", &out, &errw); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.Contains(out.String(), "stored profiles could not be read") {
		t.Errorf("bundle does not note the unreadable store:\n%s", out.String())
	}
}

func TestBugReportLogHint(t *testing.T) {
	if got := bugReportLogHint("linux"); !strings.Contains(got, "journalctl --user -u culibox.service") {
		t.Errorf("linux hint = %q", got)
	}
	if got := bugReportLogHint("darwin"); !strings.Contains(got, "culibox serve") {
		t.Errorf("darwin hint = %q", got)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// A home reached through a symlink is hidden in its resolved spelling too, so
// both producers hand the bundle the home as EvalSymlinks spells it.
func TestBugReportInputsCarryResolvedHome(t *testing.T) {
	target := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(target, home); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	cli := bugReportInput(doctor.Input{Home: home, Commit: "abc123"}, "somehost", "linux")
	daemon := daemonBugInput(home, "somehost", config.Config{})
	for name, in := range map[string]bugreport.Input{"cli": cli, "daemon": daemon} {
		if in.Home != home || in.HomeResolved != resolved || in.Host != "somehost" {
			t.Errorf("%s: Home %q, HomeResolved %q, Host %q; want %q, %q, somehost", name, in.Home, in.HomeResolved, in.Host, home, resolved)
		}
	}
}
