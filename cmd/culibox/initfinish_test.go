package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/doctor"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/vmshares"
)

func TestLineDiff(t *testing.T) {
	d := lineDiff([]byte("a\nb\nc\n"), []byte("a\nx\nc\n"))
	for _, w := range []string{"-b\n", "+x\n", " a\n", " c\n"} {
		if !strings.Contains(d, w) {
			t.Errorf("diff lacks %q:\n%s", w, d)
		}
	}
	if strings.Contains(d, "-a") || strings.Contains(d, "+a") || strings.Contains(d, "-c") {
		t.Errorf("an unchanged line is marked as changed:\n%s", d)
	}
	if strings.Index(d, "-b") > strings.Index(d, "+x") {
		t.Errorf("the removal must come before the addition:\n%s", d)
	}
}

func TestLineDiffElidesFarContext(t *testing.T) {
	var before, after strings.Builder
	for i := 0; i < 20; i++ {
		line := string(rune('a'+i)) + "\n"
		before.WriteString(line)
		if i == 10 {
			line = "X\n"
		}
		after.WriteString(line)
	}
	d := lineDiff([]byte(before.String()), []byte(after.String()))
	if !strings.Contains(d, "-k\n") || !strings.Contains(d, "+X\n") {
		t.Fatalf("diff lacks the change:\n%s", d)
	}
	if strings.Contains(d, " a\n") || strings.Contains(d, " t\n") {
		t.Fatalf("lines far from the change must be left out:\n%s", d)
	}
	if !strings.Contains(d, " h\n") || !strings.Contains(d, " n\n") {
		t.Fatalf("three lines of context on each side:\n%s", d)
	}
}

// cfgStep runs step 6 against target with the answers in in.
func cfgStep(target, in string, e config.InitEdit) (bool, string) {
	var out bytes.Buffer
	ok := initConfigStep(newTestUI(in, &out), target, e)
	return ok, out.String()
}

func TestConfigStepNewFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "root", "config.json")
	ok, out := cfgStep(target, "\n", config.InitEdit{DefaultCwd: "/w/proj", Account: "claude-box"})
	if !ok {
		t.Fatalf("step stopped: %s", out)
	}
	fi, err := os.Stat(target)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config %v %v", fi, err)
	}
	b, _ := os.ReadFile(target)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks the file line %q:\n%s", line, out)
		}
	}
	for _, w := range []string{"Write " + target + "? [Y/n]", `"default_cwd": "/w/proj"`, `"account": "claude-box"`, "  done"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if _, err := config.Load(target); err != nil {
		t.Fatalf("written config does not load: %v", err)
	}
}

func TestConfigStepNewFileNoStops(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	ok, out := cfgStep(target, "n\n", config.InitEdit{DefaultCwd: "/w/proj"})
	if ok || !strings.Contains(out, "stopped:") || !strings.Contains(out, target) {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a no must write nothing: %v", err)
	}
}

const existingCfg = `{"bind_addr": "127.0.0.1:8899", "zeta": {"keep": [1, 2]},
  "agents": [{"name": "claude", "bin": "claude"}]}
`

func TestConfigStepExistingNoChangesNothing(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(target, []byte(existingCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, out := cfgStep(target, "\n", config.InitEdit{DefaultCwd: "/w/proj"})
	if ok || !strings.Contains(out, "stopped:") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if b, _ := os.ReadFile(target); string(b) != existingCfg {
		t.Fatalf("file changed:\n%s", b)
	}
	for _, w := range []string{"Apply? [y/N]", `+  "default_cwd": "/w/proj",`, "sorted"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestConfigStepExistingYesWrites(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(target, []byte(existingCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, out := cfgStep(target, "y\n", config.InitEdit{DefaultCwd: "/w/proj"})
	if !ok || !strings.Contains(out, "  done") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	c, err := config.Load(target)
	if err != nil || c.DefaultCwd != "/w/proj" || c.DefaultRuntime != config.RuntimeBox {
		t.Fatalf("config %+v %v", c, err)
	}
	b, _ := os.ReadFile(target)
	if !strings.Contains(string(b), `"keep": [`) {
		t.Fatalf("an unknown key was lost:\n%s", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestConfigStepIdentical(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	e := config.InitEdit{DefaultCwd: "/w/proj", Account: "claude-box"}
	plan, _, err := config.PlanInitWrite(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different layout: normalized it is identical.
	var unsorted bytes.Buffer
	unsorted.WriteString(strings.ReplaceAll(string(plan), "\n  ", "\n    "))
	if err := os.WriteFile(target, unsorted.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(target, past, past); err != nil {
		t.Fatal(err)
	}
	ok, out := cfgStep(target, "", e)
	if !ok || !strings.Contains(out, "already done:") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if strings.Contains(out, "?") {
		t.Fatalf("nothing to change must ask nothing: %s", out)
	}
	fi, _ := os.Stat(target)
	if !fi.ModTime().Equal(past) {
		t.Fatalf("file was rewritten: mtime %v", fi.ModTime())
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, unsorted.Bytes()) {
		t.Fatal("file changed")
	}
}

func TestConfigStepReportsAgentOnAnotherAccount(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"bind_addr": "127.0.0.1:8899", "agents": [{"name": "work", "bin": "claude", "account": "other"}]}`
	if err := os.WriteFile(target, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out := cfgStep(target, "n\n", config.InitEdit{DefaultCwd: "/w/proj", Account: "claude-box"})
	if !strings.Contains(out, "work") || !strings.Contains(out, "another account") {
		t.Fatalf("the skipped agent must be named: %s", out)
	}
}

func TestConfigStepRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte(existingCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "config.json")
	if err := os.Symlink(real, target); err != nil {
		t.Fatal(err)
	}
	ok, out := cfgStep(target, "y\n", config.InitEdit{DefaultCwd: "/w/proj"})
	if ok || !strings.Contains(out, "stopped:") || !strings.Contains(out, "symlink") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if b, _ := os.ReadFile(real); string(b) != existingCfg {
		t.Fatal("the link target was changed")
	}
	if fi, _ := os.Lstat(target); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
}

func TestConfigStepBrokenFileStops(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(target, []byte("[1]"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, out := cfgStep(target, "y\n", config.InitEdit{DefaultCwd: "/w/proj"})
	if ok || !strings.Contains(out, "stopped:") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
}

// boxFake records the order of box setup, share and apply calls, and models
// the VM the way the real code behaves: box setup boots a new VM with the
// folder list as it is then, starts a stopped one, and (like boxSetupVM) cannot
// apply a changed list to a VM that is not running; `box share apply` refuses
// while the VM is stopped or a box runs. A folder added after the VM booted
// stays pending until an apply succeeds.
type boxFake struct {
	ready        bool
	vmUp         bool // a VM that was set up before this run
	stopped      bool // ...and is not running now (a reboot, `box stop`)
	boxesRunning bool
	setupRC      int
	covered      string // the folder that already covers the workspace
	addErr       error
	stateErr     error
	whole        bool // state reads "whole_home"
	calls        []string

	listed  bool // the workspace is in the saved list
	applied bool // the VM's colima.yaml has it
	running bool
}

func (f *boxFake) apply(d *initDeps) {
	f.listed = f.covered != ""
	f.applied = f.listed && f.vmUp // a covered folder was applied by the earlier setup
	f.running = f.vmUp && !f.stopped
	d.boxReady = func(config.Config, []string, string) bool { return f.ready }
	d.vmSetUp = func(config.Config, string) bool { return f.vmUp }
	d.boxSetup = func(_ config.Config, _ []string, w io.Writer) int {
		f.calls = append(f.calls, "setup")
		io.WriteString(w, "PASS image: fake\n")
		if f.setupRC != 0 {
			return f.setupRC
		}
		switch {
		case !f.vmUp:
			f.applied = f.listed // the first boot has the list as it is now
		case f.listed != f.applied && !f.running:
			io.WriteString(w, "refusing: the box VM reads as not running. Use -force.\n")
			return 1
		case f.listed != f.applied:
			f.applied = f.listed // the drift path: a stop and start
		}
		f.vmUp, f.running = true, true
		return 0
	}
	d.shareAdd = func(_ config.Config, _ []string, _, path string) (string, error) {
		f.calls = append(f.calls, "share "+path)
		if f.addErr == nil && f.covered == "" {
			f.listed = true
		}
		return f.covered, f.addErr
	}
	d.shareApply = func(_ config.Config, _ []string, _ string, w io.Writer) int {
		f.calls = append(f.calls, "apply")
		if !f.running || f.boxesRunning {
			io.WriteString(w, "refusing: the box VM is not running or a box runs. Use -force.\n")
			return 1
		}
		io.WriteString(w, "stop… done (3 s)\nstart… done (20 s)\n")
		f.applied = f.listed
		return 0
	}
	d.shareState = func(config.Config, []string, string, string) (string, error) {
		f.calls = append(f.calls, "state")
		switch {
		case f.stateErr != nil:
			return "", f.stateErr
		case !f.vmUp:
			return "not_set_up", nil
		case f.listed && !f.applied:
			return "pending", nil
		}
		return "applied", nil
	}
}

func boxStep(goos, in string, f *boxFake) (bool, string) {
	d := (&fakeInit{}).deps(goos)
	f.apply(&d)
	var out bytes.Buffer
	ok := initBoxStep(newTestUI(in, &out), config.Config{}, nil, "/data", "/w/proj", d)
	return ok, out.String()
}

func calls(f *boxFake) string { return strings.Join(f.calls, ",") }

// #312: with no VM yet, the workspace goes into the folder list BEFORE box
// setup starts the VM, so the first boot has it and `box share list` shows
// nothing pending.
func TestBoxStepSharesBeforeSetupAndEndsApplied(t *testing.T) {
	f := &boxFake{}
	ok, out := boxStep("darwin", "", f)
	if !ok || calls(f) != "share /w/proj,setup,state" {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
	for _, w := range []string{"== step 1/8: box setup", "added /w/proj to the box VM's folders; box setup below starts the VM with it", "PASS image: fake", "its folder list is applied", "  done"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "Restart the box VM") || strings.Contains(out, "takes effect after") {
		t.Errorf("nothing is left to apply by hand:\n%s", out)
	}
	if strings.Index(out, "added /w/proj") > strings.Index(out, "PASS image: fake") {
		t.Errorf("the folder line must come before setup's output:\n%s", out)
	}
}

// The review's loop: a VM that is set up but stopped (a reboot, `box stop`) and
// a workspace new to the list. Adding first would make box setup try to apply a
// changed list to a VM that is not running, which it refuses. So setup runs
// first (it starts the VM), then the folder is added, then init applies it.
func TestBoxStepStoppedVMSetsUpThenAddsThenApplies(t *testing.T) {
	f := &boxFake{vmUp: true, stopped: true}
	ok, out := boxStep("darwin", "\n", f)
	if !ok || calls(f) != "setup,share /w/proj,state,apply,state" {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
	for _, w := range []string{"Restart the box VM now to apply them? [Y/n]", "start… done", "its folder list is applied", "  done"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "refusing") || strings.Contains(out, "stopped:") {
		t.Errorf("no refusal expected:\n%s", out)
	}
}

// Same VM, folder already covered: setup starts it and nothing is applied.
func TestBoxStepStoppedVMWithCoveredFolderAsksNothing(t *testing.T) {
	f := &boxFake{vmUp: true, stopped: true, covered: "/w"}
	ok, out := boxStep("darwin", "", f)
	if !ok || calls(f) != "setup,share /w/proj,state" || strings.Contains(out, "Restart the box VM") || !strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

// A ready box whose user declines "Run box setup again?" and whose workspace is
// new: the folder is added, and init offers the restart instead of ending on
// "done" with the first boxed spawn refused.
func TestBoxStepReadyDeclinedNewFolderOffersApply(t *testing.T) {
	f := &boxFake{ready: true, vmUp: true}
	ok, out := boxStep("darwin", "\n\n", f)
	if !ok || calls(f) != "share /w/proj,state,apply,state" || !strings.Contains(out, "its folder list is applied") || !strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

// Declining the restart is not "done": init stops with a command that works.
func TestBoxStepDecliningTheRestartStops(t *testing.T) {
	f := &boxFake{ready: true, vmUp: true}
	ok, out := boxStep("darwin", "\nn\n", f)
	if ok || calls(f) != "share /w/proj,state" || !strings.Contains(out, "stopped:") || !strings.Contains(out, "culibox box share apply") || strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

// A box that runs makes apply refuse: init stops and names -force.
func TestBoxStepApplyRefusedWhileBoxesRun(t *testing.T) {
	f := &boxFake{ready: true, vmUp: true, boxesRunning: true}
	ok, out := boxStep("darwin", "\n\n", f)
	if ok || !strings.Contains(out, "refusing:") || !strings.Contains(out, "stopped:") || !strings.Contains(out, "box share apply -force") || strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

// Re-running after a declined restart finds the folder listed (covered) and
// still pending: it offers the restart again rather than saying "done".
func TestBoxStepRerunWithListedButPendingFolderOffersApply(t *testing.T) {
	f := &boxFake{ready: true, vmUp: true, covered: "/w/proj"}
	d := (&fakeInit{}).deps("darwin")
	f.apply(&d)
	f.applied = false // listed, VM does not have it
	var out bytes.Buffer
	ok := initBoxStep(newTestUI("\n\n", &out), config.Config{}, nil, "/data", "/w/proj", d)
	if !ok || calls(f) != "share /w/proj,state,apply,state" || !strings.Contains(out.String(), "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out.String())
	}
}

// Setup finishing with the workspace still pending is not "done": init stops
// and says how to finish.
func TestBoxStepFreshVMStopsWhenShareStaysPending(t *testing.T) {
	f := &boxFake{}
	d := (&fakeInit{}).deps("darwin")
	f.apply(&d)
	d.shareState = func(config.Config, []string, string, string) (string, error) { return "pending", nil }
	var out bytes.Buffer
	ok := initBoxStep(newTestUI("", &out), config.Config{}, nil, "/data", "/w/proj", d)
	s := out.String()
	if ok || !strings.Contains(s, "stopped:") || !strings.Contains(s, "culibox box share apply") || strings.Contains(s, "  done") {
		t.Fatalf("ok=%v out=%s", ok, s)
	}
}

func TestBoxStepStopsWhenShareStateUnreadable(t *testing.T) {
	f := &boxFake{stateErr: errors.New("colima.yaml unreadable")}
	ok, out := boxStep("darwin", "", f)
	if ok || !strings.Contains(out, "stopped:") || !strings.Contains(out, "colima.yaml unreadable") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
}

// A VM that shares the whole home has a workspace under home; one outside it
// (an external volume) is pending unless a folder the VM applied covers it.
func TestWholeHomeShareState(t *testing.T) {
	rw := func(p string) vmshares.Share { return vmshares.Share{Path: p, Mode: vmshares.RW} }
	ro := func(p string) vmshares.Share { return vmshares.Share{Path: p, Mode: vmshares.RO} }
	for _, c := range []struct {
		name, ws string
		applied  []vmshares.Share
		want     string
	}{
		{"under home", "/Users/me/work/proj", nil, "applied"},
		{"home itself", "/Users/me", nil, "applied"},
		{"case only (APFS)", "/users/ME/work", nil, "applied"},
		{"outside home", "/Volumes/x/proj", nil, "pending"},
		{"outside home, applied rw", "/Volumes/x/proj", []vmshares.Share{rw("/Volumes/x")}, "applied"},
		{"outside home, applied ro", "/Volumes/x/proj", []vmshares.Share{ro("/Volumes/x")}, "pending"},
	} {
		if got := wholeHomeShareState(c.ws, "/Users/me", c.applied, true); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// A case-only difference (APFS) or a symlinked spelling is the same folder, so
// init does not print a "sees ... through ..." line for it.
func TestSameFolder(t *testing.T) {
	if !sameFolder("/Users/Me/Work", "/users/me/work") || !sameFolder("/w/proj", "/w/proj") {
		t.Error("spellings of one folder must match")
	}
	if sameFolder("/w", "/w/proj") {
		t.Error("a parent is not the same folder")
	}
}

// Re-running on a machine whose list already covers the workspace adds
// nothing and prints no "added" line.
func TestBoxStepSetupWithShareAlreadyCovered(t *testing.T) {
	f := &boxFake{covered: "/w"}
	ok, out := boxStep("darwin", "", f)
	if !ok || calls(f) != "share /w/proj,setup,state" || !strings.Contains(out, "the box VM sees /w/proj through /w") || strings.Contains(out, "added /w/proj") || !strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

func TestBoxStepCoveredSpellingDifferingByCaseIsQuiet(t *testing.T) {
	f := &boxFake{covered: "/W/Proj"}
	_, out := boxStep("darwin", "", f)
	if strings.Contains(out, "sees") {
		t.Fatalf("a case-only difference is the same folder:\n%s", out)
	}
}

func TestBoxStepReadySkips(t *testing.T) {
	f := &boxFake{ready: true, vmUp: true, covered: "/w/proj"}
	ok, out := boxStep("darwin", "\n", f)
	if !ok || !strings.Contains(out, "Run box setup again? [y/N]") || !strings.Contains(out, "already done:") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	for _, c := range f.calls {
		if c == "setup" || c == "apply" {
			t.Fatalf("%s ran: %v", c, f.calls)
		}
	}
}

func TestBoxStepReadyYesRunsSetup(t *testing.T) {
	f := &boxFake{ready: true, vmUp: true, covered: "/w/proj"}
	ok, out := boxStep("darwin", "y\n", f)
	if !ok || calls(f) != "setup,share /w/proj,state" || !strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

func TestBoxStepSetupFails(t *testing.T) {
	f := &boxFake{setupRC: 1}
	ok, out := boxStep("darwin", "", f)
	if ok || !strings.Contains(out, "stopped:") || !strings.Contains(out, "culibox box setup") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if calls(f) != "share /w/proj,setup" {
		t.Fatalf("the folder state is not read after a failed setup: %v", f.calls)
	}
}

func TestBoxStepShareFails(t *testing.T) {
	f := &boxFake{addErr: errors.New("overlaps the data root")}
	ok, out := boxStep("darwin", "", f)
	if ok || !strings.Contains(out, "stopped:") || !strings.Contains(out, "culibox box share add") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if calls(f) != "share /w/proj" {
		t.Fatalf("the VM must not start without its folder: %v", f.calls)
	}
}

func TestBoxStepLinuxSetupOnly(t *testing.T) {
	f := &boxFake{}
	ok, out := boxStep("linux", "", f)
	if !ok || calls(f) != "setup" || !strings.Contains(out, "macOS") || !strings.Contains(out, "  done") {
		t.Fatalf("ok=%v calls=%v out=%s", ok, f.calls, out)
	}
}

func finishStep(t *testing.T, cfg config.Config, rc int) (int, string) {
	t.Helper()
	d := (&fakeInit{}).deps("linux")
	d.doctor = func(_ config.Config, _ config.Source, _ []string, w io.Writer) int {
		io.WriteString(w, "PASS config: fake\n")
		return rc
	}
	var out bytes.Buffer
	code := initFinishStep(newTestUI("", &out), cfg, config.Source{}, nil, t.TempDir(), d)
	return code, out.String()
}

func TestFinishPrintsLoginURL(t *testing.T) {
	code, out := finishStep(t, config.Config{BindAddr: "127.0.0.1:8899"}, 0)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, w := range []string{"PASS config: fake", "Start CuliBox: culibox serve", "Then open: http://127.0.0.1:8899/login#token=", "  done"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}

	t.Setenv("CB_INIT_TEST_PW", "a-long-enough-password")
	code, out = finishStep(t, config.Config{BindAddr: "127.0.0.1:8899", PasswordEnv: "CB_INIT_TEST_PW"}, 0)
	if code != 0 || !strings.Contains(out, "password") || strings.Contains(out, "#token=") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if strings.Contains(out, "a-long-enough-password") {
		t.Fatalf("the password was printed: %s", out)
	}
}

func TestFinishDoctorFailExits1(t *testing.T) {
	code, out := finishStep(t, config.Config{BindAddr: "127.0.0.1:8899"}, 1)
	if code != 1 || !strings.Contains(out, "stopped:") || !strings.Contains(out, "culibox serve") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestInitBoxSetupFailureExits1(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	d := f.deps("linux")
	d.boxSetup = func(config.Config, []string, io.Writer) int { return 1 }
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, d); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "culibox box setup") || strings.Contains(out.String(), "step 8/8") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestInitWritesConfigThenUsesIt(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	d := f.deps("linux")
	var setupCwd, doctorCwd string
	d.boxSetup = func(c config.Config, _ []string, _ io.Writer) int { setupCwd = c.DefaultCwd; return 0 }
	d.doctor = func(c config.Config, _ config.Source, _ []string, _ io.Writer) int {
		doctorCwd = c.DefaultCwd
		return 0
	}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, d); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	ws := filepath.Join(home, "work")
	if setupCwd != ws || doctorCwd != ws {
		t.Fatalf("box setup and doctor must see the written config: %q %q", setupCwd, doctorCwd)
	}
	target := filepath.Join(home, ".local", "share", "culibox", "config.json")
	c, err := config.Load(target)
	if err != nil || c.DefaultCwd != ws || c.DefaultRuntime != config.RuntimeBox {
		t.Fatalf("config %+v %v", c, err)
	}
	for _, w := range []string{"== step 6/8: config", "== step 7/8: box setup", "== step 8/8: doctor and login URL"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}

// snapshot is every regular file under dir with its bytes.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		m[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInitRerunConfiguredMachine(t *testing.T) {
	home := initHome(t)
	dataDir := filepath.Join(home, ".local", "share", "culibox")
	ws := filepath.Join(home, "work")
	for _, dir := range []string{dataDir, ws} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The config exactly as init writes it.
	plan, _, err := config.PlanInitWrite(nil, config.InitEdit{DefaultCwd: ws, VMCPUs: 4, VMMemoryGiB: 8, Account: initAccountName})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFileAtomic(filepath.Join(dataDir, "config.json"), plan); err != nil {
		t.Fatal(err)
	}
	// The token in the store and the account pointing at it.
	k := secretstore.NewFakeKeeper(false)
	st := secretstore.Open(dataDir, k, nil)
	if err := st.Init(nil, "t"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(account.TokenID(initAccountName), []byte(tokTest), "t"); err != nil {
		t.Fatal(err)
	}
	as, err := account.Open(filepath.Join(dataDir, "accounts.json"), dataDir, filepath.Join(home, "claude-default"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.Create(account.Account{Name: initAccountName, Provider: account.ProviderClaude, TokenSecret: account.TokenID(initAccountName)}); err != nil {
		t.Fatal(err)
	}
	as.Close()
	// A serve already ran: the login token exists.
	if _, err := auth.ResolveLogin(config.Config{}, dataDir); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dataDir)

	f := &fakeInit{have: allHave("abduco", "docker", "colima", "claude")}
	d := f.deps("darwin")
	d.newKeeper = func(string) secretstore.Keeper { return k }
	d.readSecret = func() ([]byte, error) { t.Fatal("a configured machine must read no secret"); return nil, nil }
	d.vmSetUp = func(config.Config, string) bool { return true }
	bf := &boxFake{ready: true, vmUp: true, covered: ws}
	bf.apply(&d)
	doctorRan := false
	d.doctor = func(_ config.Config, _ config.Source, _ []string, w io.Writer) int {
		doctorRan = true
		io.WriteString(w, "PASS boxed claude token: fake\n")
		return 0
	}
	var out bytes.Buffer
	code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader("\n\n\n"), true, &out, d)
	s := out.String()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, s)
	}
	noToken(t, s)
	if n := strings.Count(s, "already done: "); n != 7 {
		t.Errorf("want 7 already-done lines, got %d:\n%s", n, s)
	}
	if strings.Count(s, "\n  done\n") != 1 || !strings.HasSuffix(s, "  done\n") {
		t.Errorf("only step 8 ends in done, last:\n%s", s)
	}
	for _, bad := range []string{"skipped:", "stopped:", "Write ", "Apply?"} {
		if strings.Contains(s, bad) {
			t.Errorf("output has %q:\n%s", bad, s)
		}
	}
	if !doctorRan || !strings.Contains(s, "PASS boxed claude token: fake") || !strings.Contains(s, "/login#token=") {
		t.Errorf("step 8 must run doctor and print the URL:\n%s", s)
	}
	for _, c := range bf.calls {
		if c == "setup" || len(f.runs) != 0 {
			t.Fatalf("nothing may run: calls=%v runs=%v", bf.calls, f.runs)
		}
	}
	after := snapshot(t, dataDir)
	if len(after) != len(before) {
		t.Errorf("files appeared or vanished: before %d, after %d", len(before), len(after))
	}
	for p, b := range before {
		if after[p] != b {
			t.Errorf("%s changed", p)
		}
	}
}

func TestRealInitDepsNeverReachABoxWithoutDocker(t *testing.T) {
	d := realInitDeps(t.TempDir())
	cfg := config.Config{DockerBin: "culibox-no-such-docker", ColimaBin: "culibox-no-such-colima"}
	if d.vmSetUp(cfg, t.TempDir()) {
		t.Fatal("no colima: the VM is not set up")
	}
	if d.boxReady(cfg, nil, t.TempDir()) {
		t.Fatal("no docker: the box is not ready")
	}
}

var (
	tierPass   = doctor.Result{Status: doctor.Pass, Check: "boxed tier", Detail: "docker found"}
	imagePass  = doctor.Result{Status: doctor.Pass, Check: "box image", Detail: "current"}
	tokenPass  = doctor.Result{Status: doctor.Pass, Check: "boxed claude token", Detail: "1 agent"}
	tokenLock  = doctor.Result{Status: doctor.Warn, Check: "boxed claude token", Detail: "locked"}
	stoppedBox = doctor.Result{Status: doctor.Warn, Check: "stopped boxes", Detail: "x"}
)

func TestInitBoxReadyFrom(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	cases := []struct {
		name         string
		goos         string
		rep          doctor.BoxTierReport
		stored, imgs func() bool
		want         bool
	}{
		{"linux all pass", "linux", doctor.BoxTierReport{Rows: []doctor.Result{tierPass}, Token: tokenPass}, no, yes, true},
		{"linux image missing", "linux", doctor.BoxTierReport{Rows: []doctor.Result{tierPass}, Token: tokenPass}, no, no, false},
		{"linux locked, token stored", "linux", doctor.BoxTierReport{Rows: []doctor.Result{tierPass}, Token: tokenLock, TokenLockedOnly: true}, yes, yes, true},
		{"linux locked, token not stored", "linux", doctor.BoxTierReport{Rows: []doctor.Result{tierPass}, Token: tokenLock, TokenLockedOnly: true}, no, yes, false},
		{"linux token WARN not only locked", "linux", doctor.BoxTierReport{Rows: []doctor.Result{tierPass}, Token: tokenLock}, yes, yes, false},
		{"linux another WARN", "linux", doctor.BoxTierReport{Rows: []doctor.Result{tierPass, stoppedBox}, Token: tokenPass}, yes, yes, false},
		{"darwin all pass", "darwin", doctor.BoxTierReport{Rows: []doctor.Result{tierPass, imagePass, tokenPass}, Token: tokenPass}, no, no, true},
		{"darwin no image row (VM stopped)", "darwin", doctor.BoxTierReport{Rows: []doctor.Result{tierPass, tokenPass}, Token: tokenPass}, no, yes, false},
		{"darwin locked, token stored", "darwin", doctor.BoxTierReport{Rows: []doctor.Result{tierPass, imagePass, tokenLock}, Token: tokenLock, TokenLockedOnly: true}, yes, no, true},
		{"darwin locked, token not stored", "darwin", doctor.BoxTierReport{Rows: []doctor.Result{tierPass, imagePass, tokenLock}, Token: tokenLock, TokenLockedOnly: true}, no, no, false},
	}
	for _, c := range cases {
		if got := initBoxReadyFrom(c.goos, c.rep, c.stored, c.imgs); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// On Linux the store is a passphrase store whose CLI unlock expires: a locked
// store holding the claude-box token is still a ready box, so a re-run asks
// before running box setup again.
func TestBoxStepLinuxLockedStoreWithTokenIsReady(t *testing.T) {
	f := newTokFix(t, true)
	if err := f.store().Init([]byte(tokPass), "t"); err != nil {
		t.Fatal(err)
	}
	if err := f.store().Set(account.TokenID(initAccountName), []byte(tokTest), "t"); err != nil {
		t.Fatal(err)
	}
	if !f.store().Status().Locked {
		t.Fatal("setup: the store must be locked")
	}
	rep := doctor.BoxTierReport{Rows: []doctor.Result{tierPass}, Token: tokenLock, TokenLockedOnly: true}
	d := (&fakeInit{}).deps("linux")
	d.boxReady = func(config.Config, []string, string) bool {
		return initBoxReadyFrom("linux", rep, func() bool { return initTokenStored(f.dataDir, f.k) }, func() bool { return true })
	}
	setupRan := false
	d.boxSetup = func(config.Config, []string, io.Writer) int { setupRan = true; return 0 }
	var out bytes.Buffer
	ok := initBoxStep(newTestUI("\n", &out), config.Config{}, nil, f.dataDir, "/w/proj", d)
	s := out.String()
	if !ok || setupRan || !strings.Contains(s, "Run box setup again? [y/N]") || !strings.Contains(s, "already done:") {
		t.Fatalf("ok=%v setupRan=%v out=%s", ok, setupRan, s)
	}
	noToken(t, s)
}

func TestInitTokenStoredNeedsTheTokenID(t *testing.T) {
	f := newTokFix(t, true)
	if initTokenStored(f.dataDir, f.k) {
		t.Fatal("no store: not stored")
	}
	if err := f.store().Init([]byte(tokPass), "t"); err != nil {
		t.Fatal(err)
	}
	if err := f.store().Set("OTHER_SECRET", []byte("x"), "t"); err != nil {
		t.Fatal(err)
	}
	if initTokenStored(f.dataDir, f.k) {
		t.Fatal("another id is not the claude-box token")
	}
}
