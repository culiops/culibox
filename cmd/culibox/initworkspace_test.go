package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

const gib = uint64(1) << 30

// wsHome is a fresh home that os.UserHomeDir (which the box rule reads) agrees
// with, with a data dir inside it.
func wsHome(t *testing.T) (home, dataDir string) {
	t.Helper()
	home = initHome(t)
	dataDir = filepath.Join(home, ".local", "share", "culibox")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, dataDir
}

func TestNormalizeWorkspace(t *testing.T) {
	home := t.TempDir()
	cwd, _ := os.Getwd()
	cases := []struct{ in, want string }{
		{"~/w", filepath.Join(home, "w")},
		{"~", home},
		{"w/", filepath.Join(cwd, "w")},
		{home + "/a/../b//", filepath.Join(home, "b")},
	}
	for _, c := range cases {
		got, err := normalizeWorkspace(c.in, home)
		if err != nil || got != c.want {
			t.Errorf("normalizeWorkspace(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := normalizeWorkspace("  ", home); err == nil {
		t.Error("an empty answer must be an error")
	}
}

func TestNormalizeWorkspaceResolvesSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got, err := normalizeWorkspace("~/link", home); err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestWorkspaceStepOffersExistingDefault(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, ok := initWorkspaceStep(newTestUI("\n", &out), config.Config{DefaultCwd: proj}, dataDir, home, nil, initDeps{})
	if !ok || got != proj {
		t.Fatalf("got %q,%v: %s", got, ok, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "== step 1/8: workspace folder") || !strings.Contains(s, "["+proj+"]") || !strings.Contains(s, "already done") {
		t.Fatalf("out=%s", s)
	}
}

func TestWorkspaceStepNoDefaultAndEOFStops(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if _, ok := initWorkspaceStep(newTestUI("", &out), config.Config{}, dataDir, home, nil, initDeps{}); ok {
		t.Fatal("EOF must stop")
	}
	if !strings.Contains(out.String(), "stopped:") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestWorkspaceStepRefusesHomeThenAccepts(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	got, ok := initWorkspaceStep(newTestUI("~\n~/work\ny\n", &out), config.Config{}, dataDir, home, nil, initDeps{})
	want := filepath.Join(home, "work")
	if !ok || got != want {
		t.Fatalf("got %q,%v: %s", got, ok, out.String())
	}
	fi, err := os.Stat(want)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("created dir: %v %v", fi, err)
	}
	if n := strings.Count(out.String(), "your home directory"); n != 1 {
		t.Fatalf("refusal printed %d times: %s", n, out.String())
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "done") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestWorkspaceStepRefusesSymlinkToHome(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	outside := t.TempDir()
	if err := os.Symlink(home, filepath.Join(outside, "h")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, ok := initWorkspaceStep(newTestUI(filepath.Join(outside, "h")+"\n", &out), config.Config{}, dataDir, home, nil, initDeps{}); ok {
		t.Fatal("a link to home must be refused")
	}
	if !strings.Contains(out.String(), "your home directory") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestWorkspaceStepRefusesDataAndConfigDirs(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "culibox")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dataDir, filepath.Join(dataDir, "boxstate"), cfgDir, filepath.Dir(dataDir)} {
		var out bytes.Buffer
		if _, ok := initWorkspaceStep(newTestUI(p+"\n", &out), config.Config{}, dataDir, home, []string{cfgFile}, initDeps{}); ok {
			t.Errorf("%s must be refused: %s", p, out.String())
		}
		if !strings.Contains(out.String(), "cannot be the workspace") {
			t.Errorf("%s: no refusal text: %s", p, out.String())
		}
	}
}

func TestWorkspaceStepDeclinedCreateCountsAsATry(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	in := "~/a\nn\n~/b\nn\n~/c\nn\n~/d\ny\n"
	if _, ok := initWorkspaceStep(newTestUI(in, &out), config.Config{}, dataDir, home, nil, initDeps{}); ok {
		t.Fatalf("three declined creates must stop: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "d")); err == nil {
		t.Fatal("a fourth answer must not be read")
	}
}

func TestWorkspaceStepCreateEOFIsNo(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if _, ok := initWorkspaceStep(newTestUI("~/a\n", &out), config.Config{}, dataDir, home, nil, initDeps{}); ok {
		t.Fatalf("EOF at the create prompt must not create: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "a")); err == nil {
		t.Fatal("created on EOF")
	}
}

func TestWorkspaceStepAFileIsAskedAgain(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	f := filepath.Join(home, "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, ok := initWorkspaceStep(newTestUI(f+"\n"+proj+"\n", &out), config.Config{}, dataDir, home, nil, initDeps{})
	if !ok || got != proj || !strings.Contains(out.String(), "is not a folder") {
		t.Fatalf("got %q,%v: %s", got, ok, out.String())
	}
}

func TestWorkspaceStepThreeRefusalsStops(t *testing.T) {
	home, dataDir := wsHome(t)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if _, ok := initWorkspaceStep(newTestUI("~\n~\n"+home+"\n~/never\ny\n", &out), config.Config{}, dataDir, home, nil, initDeps{}); ok {
		t.Fatalf("three refusals must stop: %s", out.String())
	}
	if !strings.Contains(out.String(), "stopped:") {
		t.Fatalf("out=%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "never")); err == nil {
		t.Fatal("a fourth answer must not be read")
	}
}

func TestDefaultVMSize(t *testing.T) {
	cases := []struct {
		cpus   int
		mem    uint64
		wc, wg int
	}{
		{8, 8 * gib, 4, 4}, {10, 16 * gib, 5, 4}, {12, 32 * gib, 6, 8}, {16, 64 * gib, 8, 8}, {2, 0, 2, 4},
		{1, 4 * gib, 2, 4}, {8, 8*gib - 1, 4, 4},
	}
	for _, c := range cases {
		if gc, gg := defaultVMSize(c.cpus, c.mem); gc != c.wc || gg != c.wg {
			t.Errorf("defaultVMSize(%d, %d GiB) = %d,%d; want %d,%d", c.cpus, c.mem>>30, gc, gg, c.wc, c.wg)
		}
	}
}

func vmDeps(goos string, cpus int, mem uint64, set bool) initDeps {
	d := (&fakeInit{}).deps(goos)
	d.hostSize = func() (int, uint64) { return cpus, mem }
	d.vmSetUp = func(config.Config, string) bool { return set }
	return d
}

func TestVMSizeStepAcceptDefault(t *testing.T) {
	var out bytes.Buffer
	c, g, ok := initVMSizeStep(newTestUI("\n", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, false))
	if !ok || c != 5 || g != 4 {
		t.Fatalf("got %d,%d,%v: %s", c, g, ok, out.String())
	}
	s := out.String()
	for _, w := range []string{"== step 1/8: VM size", `VM size: 5 CPUs, 4 GiB RAM. Enter to accept, or type e.g. "6 8" (CPUs GiB):`, "inferred"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in %s", w, s)
		}
	}
	if strings.Contains(s, "8 GB Macs") {
		t.Errorf("16 GiB host must not get the 8 GB warning: %s", s)
	}
}

func TestVMSizeStep8GBWarning(t *testing.T) {
	var out bytes.Buffer
	c, g, ok := initVMSizeStep(newTestUI("\n", &out), config.Config{}, "/d", vmDeps("darwin", 8, 8*gib, false))
	if !ok || c != 4 || g != 4 {
		t.Fatalf("got %d,%d,%v", c, g, ok)
	}
	if !strings.Contains(out.String(), `8 GB Macs will feel a 4 GiB VM; close it with "culibox box stop" when idle.`) {
		t.Fatalf("out=%s", out.String())
	}
}

func TestVMSizeStepOverride(t *testing.T) {
	var out bytes.Buffer
	c, g, ok := initVMSizeStep(newTestUI("6 8\n", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, false))
	if !ok || c != 6 || g != 8 {
		t.Fatalf("got %d,%d,%v", c, g, ok)
	}
}

func TestVMSizeStepInvalidIsReasked(t *testing.T) {
	for _, bad := range []string{"99 8", "6 99", "0 4", "4 0", "4 1", "-1 4", "6", "a b", "6 8 9", "4.5 8"} {
		var out bytes.Buffer
		c, g, ok := initVMSizeStep(newTestUI(bad+"\n6 8\n", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, false))
		if !ok || c != 6 || g != 8 {
			t.Errorf("%q: got %d,%d,%v", bad, c, g, ok)
		}
		if n := strings.Count(out.String(), "VM size: 5 CPUs"); n != 2 {
			t.Errorf("%q: asked %d times, want 2: %s", bad, n, out.String())
		}
	}
}

func TestVMSizeStepLowMemoryExplainsTheFloor(t *testing.T) {
	var out bytes.Buffer
	c, g, ok := initVMSizeStep(newTestUI("4 1\n4 2\n", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, false))
	if !ok || c != 4 || g != 2 {
		t.Fatalf("got %d,%d,%v: %s", c, g, ok, out.String())
	}
	if !strings.Contains(out.String(), "GiB must be between 2 and 16") || !strings.Contains(out.String(), "too small") {
		t.Fatalf("the floor and its reason must be said: %s", out.String())
	}
}

func TestVMSizeStepEOFStops(t *testing.T) {
	var out bytes.Buffer
	_, _, ok := initVMSizeStep(newTestUI("", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, false))
	if ok || !strings.Contains(out.String(), "stopped:") {
		t.Fatalf("ok=%v out=%s", ok, out.String())
	}
	out.Reset()
	if _, _, ok := initVMSizeStep(newTestUI("99 8\n", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, false)); ok {
		t.Fatal("EOF after an invalid answer must stop")
	}
}

func TestVMSizeStepUnknownHostMemoryHasNoUpperBound(t *testing.T) {
	var out bytes.Buffer
	c, g, ok := initVMSizeStep(newTestUI("2 64\n", &out), config.Config{}, "/d", vmDeps("darwin", 4, 0, false))
	if !ok || c != 2 || g != 64 {
		t.Fatalf("got %d,%d,%v: %s", c, g, ok, out.String())
	}
}

func TestVMSizeStepProposesConfigSize(t *testing.T) {
	var out bytes.Buffer
	cfg := config.Config{BoxVMCPUs: 3, BoxVMMemoryGiB: 6}
	c, g, ok := initVMSizeStep(newTestUI("\n", &out), cfg, "/d", vmDeps("darwin", 10, 16*gib, false))
	if !ok || c != 3 || g != 6 || !strings.Contains(out.String(), "VM size: 3 CPUs, 6 GiB RAM.") {
		t.Fatalf("got %d,%d,%v: %s", c, g, ok, out.String())
	}
}

func TestVMSizeStepExistingVM(t *testing.T) {
	var out bytes.Buffer
	c, g, ok := initVMSizeStep(newTestUI("", &out), config.Config{}, "/d", vmDeps("darwin", 10, 16*gib, true))
	if !ok || c != 0 || g != 0 {
		t.Fatalf("got %d,%d,%v", c, g, ok)
	}
	s := out.String()
	for _, w := range []string{"already done", "colima's current size", "box setup -recreate", "loses images and cache"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in %s", w, s)
		}
	}
	if strings.Contains(s, "VM size:") {
		t.Errorf("must not ask: %s", s)
	}
	out.Reset()
	initVMSizeStep(newTestUI("", &out), config.Config{BoxVMCPUs: 3, BoxVMMemoryGiB: 6}, "/d", vmDeps("darwin", 10, 16*gib, true))
	if !strings.Contains(out.String(), "3 CPUs, 6 GiB") {
		t.Errorf("must show the configured size: %s", out.String())
	}
}

func TestVMSizeStepLinuxSkips(t *testing.T) {
	var out bytes.Buffer
	d := (&fakeInit{}).deps("linux") // no hostSize or vmSetUp: they must not be called
	c, g, ok := initVMSizeStep(newTestUI("", &out), config.Config{}, "/d", d)
	if !ok || c != 0 || g != 0 || !strings.Contains(out.String(), "skipped") {
		t.Fatalf("got %d,%d,%v: %s", c, g, ok, out.String())
	}
}
