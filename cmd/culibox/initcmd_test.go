package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/abduco"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

// fakeInit is an initDeps whose lookPath finds exactly the names in have, and
// whose run records its calls. A recorded run can install a name (install), as
// a real `brew install` would.
type fakeInit struct {
	have    map[string]bool
	runs    [][]string
	runErr  error
	install map[string]string // "brew install colima" -> name that becomes found
	env     map[string]string
}

func (f *fakeInit) deps(goos string) initDeps {
	return initDeps{
		goos:   goos,
		getenv: func(k string) string { return f.env[k] },
		lookPath: func(n string) (string, error) {
			if f.have[n] {
				return "/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		run: func(name string, args ...string) error {
			call := append([]string{name}, args...)
			f.runs = append(f.runs, call)
			if f.runErr != nil {
				return f.runErr
			}
			if n, ok := f.install[strings.Join(call, " ")]; ok {
				f.have[n] = true
			}
			return nil
		},
		macOSMajor: func() int { return 14 },
		osRelease:  func() string { return "" },
		// Step 5: a fresh Keychain-style fake store (no passphrase), and a terminal
		// that answers nothing, so the token is skipped.
		readSecret: func() ([]byte, error) { return nil, errors.New("no terminal") },
		newKeeper:  func(string) secretstore.Keeper { return secretstore.NewFakeKeeper(false) },
		// Steps 7-8: no VM, a box that is not ready yet, a setup and a doctor
		// that pass, and a workspace the VM already sees.
		vmSetUp:    func(config.Config, string) bool { return false },
		boxReady:   func(config.Config, []string, string) bool { return false },
		boxSetup:   func(config.Config, []string, io.Writer) int { return 0 },
		shareAdd:   func(_ config.Config, _ []string, _, path string) (string, error) { return path, nil },
		shareState: func(config.Config, []string, string, string) (string, error) { return "applied", nil },
		shareApply: func(config.Config, []string, string, io.Writer) int { return 0 },
		doctor:     func(config.Config, config.Source, []string, io.Writer) int { return 0 },
	}
}

func allHave(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func newTestUI(in string, out io.Writer) *initUI {
	return &initUI{br: bufio.NewReader(strings.NewReader(in)), out: out, step: 0, total: 8}
}

func TestInitRequiresTTY(t *testing.T) {
	var out bytes.Buffer
	f := &fakeInit{have: map[string]bool{}}
	code := runInit(cliArgs{Cmd: "init"}, t.TempDir(), strings.NewReader(""), false, &out, f.deps("linux"))
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(out.String(), "needs a terminal") {
		t.Fatalf("output lacks 'needs a terminal': %q", out.String())
	}
	if len(f.runs) != 0 {
		t.Fatalf("run called: %v", f.runs)
	}
}

func TestAskEOFIsNo(t *testing.T) {
	u := newTestUI("", io.Discard)
	if u.ask("x?", true) {
		t.Fatal("EOF must be no, even with default yes")
	}
}

func TestAskAnswers(t *testing.T) {
	cases := []struct {
		in   string
		def  bool
		want bool
	}{
		{"\n", true, true}, {"\n", false, false}, {"y\n", false, true}, {"YES\n", false, true},
		{"n\n", true, false}, {"No\n", true, false}, {"maybe\ny\n", false, true}, {"y", false, true},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if got := newTestUI(c.in, &out).ask("q?", c.def); got != c.want {
			t.Errorf("ask(%q, def %v) = %v, want %v (out %q)", c.in, c.def, got, c.want, out.String())
		}
	}
	var out bytes.Buffer
	newTestUI("\n", &out).ask("q?", true)
	if !strings.Contains(out.String(), "[Y/n]") {
		t.Errorf("default yes prompt lacks [Y/n]: %q", out.String())
	}
	out.Reset()
	newTestUI("\n", &out).ask("q?", false)
	if !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("default no prompt lacks [y/N]: %q", out.String())
	}
}

func TestLine(t *testing.T) {
	u := newTestUI("  /work \n\nlast", io.Discard)
	if v, ok := u.line("dir:", "d"); !ok || v != "/work" {
		t.Fatalf("got %q,%v", v, ok)
	}
	if v, ok := u.line("dir:", "d"); !ok || v != "d" {
		t.Fatalf("empty answer must give the default, got %q,%v", v, ok)
	}
	if v, ok := u.line("dir:", ""); !ok || v != "last" {
		t.Fatalf("unterminated last line: %q,%v", v, ok)
	}
	if _, ok := u.line("dir:", "d"); ok {
		t.Fatal("EOF must report ok=false")
	}
}

func TestStepPrinter(t *testing.T) {
	var out bytes.Buffer
	u := &initUI{out: &out, total: 8}
	u.begin("dependencies")
	u.done()
	u.begin("data root")
	u.already("/x")
	u.begin("a")
	u.skipped("no")
	u.begin("b")
	u.stopped("run this\nthen that")
	got := out.String()
	for _, want := range []string{"== step 1/8: dependencies", "done", "== step 2/8: data root", "already done: /x",
		"== step 3/8: a", "skipped: no", "== step 4/8: b", "stopped: run this", "then that"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func depsRun(t *testing.T, f *fakeInit, goos, in string, cfg config.Config) (bool, string) {
	t.Helper()
	var out bytes.Buffer
	u := newTestUI(in, &out)
	ok := initDepsStep(u, cfg, f.deps(goos))
	return ok, out.String()
}

func TestDepsDarwinAllPresent(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "colima", "claude")}
	ok, out := depsRun(t, f, "darwin", "", config.Config{})
	if !ok || !strings.Contains(out, "already done") {
		t.Fatalf("ok=%v out=%q", ok, out)
	}
	if len(f.runs) != 0 {
		t.Fatalf("run called: %v", f.runs)
	}
}

func TestDepsDarwinMissingColimaYes(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "claude"),
		install: map[string]string{"brew install colima": "colima"}}
	ok, out := depsRun(t, f, "darwin", "y\n", config.Config{})
	if !ok {
		t.Fatalf("stopped: %q", out)
	}
	if len(f.runs) != 1 || strings.Join(f.runs[0], " ") != "brew install colima" {
		t.Fatalf("runs = %v", f.runs)
	}
	if !strings.Contains(out, "done") || strings.Contains(out, "stopped") {
		t.Fatalf("out=%q", out)
	}
}

func TestDepsDarwinMissingNo(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "claude")}
	ok, out := depsRun(t, f, "darwin", "n\n", config.Config{})
	if ok {
		t.Fatal("a no must stop")
	}
	if !strings.Contains(out, "stopped") || !strings.Contains(out, "brew install colima") {
		t.Fatalf("out=%q", out)
	}
	if len(f.runs) != 0 {
		t.Fatalf("run called: %v", f.runs)
	}
}

func TestDepsDarwinEnterIsNo(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "claude")}
	var out bytes.Buffer
	ok := initDepsStep(newTestUI("\n", &out), config.Config{}, f.deps("darwin"))
	if ok || len(f.runs) != 0 {
		t.Fatalf("Enter must not install: ok=%v runs=%v", ok, f.runs)
	}
	if !strings.Contains(out.String(), "[y/N]") || !strings.Contains(out.String(), "stopped") || !strings.Contains(out.String(), "brew install colima") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestDepsDarwinEOFIsNo(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "docker", "colima", "claude")}
	ok, out := depsRun(t, f, "darwin", "", config.Config{})
	if ok || !strings.Contains(out, "brew install abduco") || len(f.runs) != 0 {
		t.Fatalf("ok=%v runs=%v out=%q", ok, f.runs, out)
	}
}

func TestDepsDarwinNoBrew(t *testing.T) {
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	ok, out := depsRun(t, f, "darwin", "y\n", config.Config{})
	if ok {
		t.Fatal("no brew must stop")
	}
	if !strings.Contains(out, "raw.githubusercontent.com/Homebrew/install/HEAD/install.sh") {
		t.Fatalf("out=%q", out)
	}
	if len(f.runs) != 0 {
		t.Fatalf("run called: %v", f.runs)
	}
}

func TestDepsDarwinClaudeCask(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "colima"),
		install: map[string]string{"brew install --cask claude-code": "claude"}}
	ok, out := depsRun(t, f, "darwin", "y\n", config.Config{})
	if !ok {
		t.Fatalf("stopped: %q", out)
	}
	if len(f.runs) != 1 || strings.Join(f.runs[0], " ") != "brew install --cask claude-code" {
		t.Fatalf("runs = %v", f.runs)
	}
}

func TestDepsDarwinAsksPerPackageInOrder(t *testing.T) {
	f := &fakeInit{have: allHave("brew"),
		install: map[string]string{"brew install abduco": "abduco", "brew install colima": "colima",
			"brew install --cask claude-code": "claude"}}
	// abduco yes, docker no, colima yes, claude yes
	ok, out := depsRun(t, f, "darwin", "y\nn\ny\ny\n", config.Config{})
	if ok {
		t.Fatal("docker was declined: must stop")
	}
	var got []string
	for _, r := range f.runs {
		got = append(got, strings.Join(r, " "))
	}
	want := "brew install abduco|brew install colima|brew install --cask claude-code"
	if strings.Join(got, "|") != want {
		t.Fatalf("runs = %v", got)
	}
	if !strings.Contains(out, "brew install docker") {
		t.Fatalf("stop must name what is still missing: %q", out)
	}
}

func TestDepsDarwinBrewFails(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "claude"), runErr: errors.New("exit 1")}
	ok, out := depsRun(t, f, "darwin", "y\n", config.Config{})
	if ok || !strings.Contains(out, "stopped") {
		t.Fatalf("ok=%v out=%q", ok, out)
	}
}

func TestDepsDarwinInstalledButStillMissing(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "claude")} // run succeeds, install map empty
	ok, out := depsRun(t, f, "darwin", "y\n", config.Config{})
	if ok || !strings.Contains(out, "stopped") || !strings.Contains(out, "colima") {
		t.Fatalf("ok=%v out=%q", ok, out)
	}
}

func TestDepsDarwinColimaHomebrewPrefix(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "claude", "/opt/homebrew/bin/colima")}
	ok, out := depsRun(t, f, "darwin", "", config.Config{})
	if !ok || !strings.Contains(out, "already done") {
		t.Fatalf("ok=%v out=%q", ok, out)
	}
}

func TestDepsUsesConfiguredBins(t *testing.T) {
	f := &fakeInit{have: allHave("/opt/ab", "/opt/dk", "/opt/cl")}
	cfg := config.Config{AbducoBin: "/opt/ab", DockerBin: "/opt/dk", Agents: []config.AgentConfig{{Name: "claude", Bin: "/opt/cl"}}}
	ok, out := depsRun(t, f, "linux", "", cfg)
	if !ok || !strings.Contains(out, "already done") {
		t.Fatalf("ok=%v out=%q", ok, out)
	}
}

func TestDepsPicksTheClaudeAgent(t *testing.T) {
	f := &fakeInit{have: allHave("abduco", "docker", "bash", "/opt/cl")}
	cfg := config.Config{Agents: []config.AgentConfig{{Name: "shell", Bin: "bash"}, {Name: "claude", Bin: "/opt/cl"}}}
	ok, out := depsRun(t, f, "linux", "", cfg)
	if !ok || !strings.Contains(out, "already done") {
		t.Fatalf("ok=%v out=%q", ok, out)
	}
	// Not found by name: the binary's base name counts, and with none it is plain claude.
	f = &fakeInit{have: allHave("abduco", "docker", "bash", "/opt/other/claude")}
	cfg = config.Config{Agents: []config.AgentConfig{{Name: "shell", Bin: "bash"}, {Name: "c2", Bin: "/opt/other/claude"}}}
	if ok, out := depsRun(t, f, "linux", "", cfg); !ok {
		t.Fatalf("base-name match: %q", out)
	}
	f = &fakeInit{have: allHave("abduco", "docker", "bash")}
	cfg = config.Config{Agents: []config.AgentConfig{{Name: "shell", Bin: "bash"}}}
	if ok, out := depsRun(t, f, "linux", "", cfg); ok || !strings.Contains(out, "missing: claude") {
		t.Fatalf("no claude agent must check plain claude: ok=%v %q", ok, out)
	}
}

func TestDepsLinuxPrintsNeverRuns(t *testing.T) {
	f := &fakeInit{have: allHave("docker", "claude")} // colima is absent and must not matter
	d := f.deps("linux")
	d.osRelease = func() string { return "ubuntu" }
	var out bytes.Buffer
	ok := initDepsStep(newTestUI("y\n", &out), config.Config{}, d)
	if ok {
		t.Fatal("missing abduco must stop")
	}
	if !strings.Contains(out.String(), "sudo apt install abduco") || strings.Contains(out.String(), "colima") {
		t.Fatalf("out=%q", out.String())
	}
	if len(f.runs) != 0 {
		t.Fatalf("run called: %v", f.runs)
	}
}

func TestDepsLinuxDistros(t *testing.T) {
	for id, want := range map[string]string{
		"debian": "sudo apt install abduco", "fedora": "sudo dnf install abduco",
		"arch": "sudo pacman -S abduco", "gentoo": "install abduco with your package manager",
		"": "install abduco with your package manager",
	} {
		f := &fakeInit{have: allHave("docker", "claude")}
		d := f.deps("linux")
		d.osRelease = func() string { return id }
		var out bytes.Buffer
		if initDepsStep(newTestUI("", &out), config.Config{}, d) || !strings.Contains(out.String(), want) {
			t.Errorf("distro %q: want %q in %q", id, want, out.String())
		}
	}
}

func TestDepsLinuxDockerAndClaudeLinks(t *testing.T) {
	f := &fakeInit{have: allHave("abduco")}
	var out bytes.Buffer
	if initDepsStep(newTestUI("", &out), config.Config{}, f.deps("linux")) {
		t.Fatal("must stop")
	}
	for _, w := range []string{"https://docs.docker.com/engine/install/", "https://docs.anthropic.com/en/docs/claude-code/setup"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("missing %q in %q", w, out.String())
		}
	}
}

func TestDepsOldMacOS(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "colima", "claude")}
	d := f.deps("darwin")
	d.macOSMajor = func() int { return 12 }
	var out bytes.Buffer
	if initDepsStep(newTestUI("", &out), config.Config{}, d) {
		t.Fatal("macOS 12 must stop")
	}
	if !strings.Contains(out.String(), "macOS 13") || !strings.Contains(out.String(), "stopped") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestDepsUnknownMacOSContinues(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "abduco", "docker", "colima", "claude")}
	d := f.deps("darwin")
	d.macOSMajor = func() int { return 0 }
	if !initDepsStep(newTestUI("", io.Discard), config.Config{}, d) {
		t.Fatal("an unreadable macOS version must not stop init")
	}
}

func initHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h) // the box workspace rule reads os.UserHomeDir
	return h
}

// wsAnswer answers step 3 with a folder under home that init creates.
const wsAnswer = "~/work\ny\n"

// initRest answers the rest of a fresh run after wsAnswer: no `claude
// setup-token` (the token is then skipped) and yes to writing the new config.
const initRest = "n\n\n"

func TestInitDataRootCreates(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	var out bytes.Buffer
	code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, f.deps("linux"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	root := filepath.Join(home, ".local", "share", "culibox")
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("root %s: %v %v", root, fi, err)
	}
	s := out.String()
	for _, w := range []string{"== step 1/8: dependencies", "== step 2/8: data root", root} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func TestInitDataRootExisting(t *testing.T) {
	home := initHome(t)
	root := filepath.Join(home, ".local", "share", "culibox")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, f.deps("linux")); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "already done: "+root) {
		t.Fatalf("out=%s", out.String())
	}
}

func TestInitDataRootEnv(t *testing.T) {
	home := initHome(t)
	root := filepath.Join(t.TempDir(), "relocated")
	f := &fakeInit{have: allHave("abduco", "docker", "claude"), env: map[string]string{"CULIBOX_HOME": root}}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, f.deps("linux")); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "CULIBOX_HOME") {
		t.Fatalf("the reason must name the env var: %s", out.String())
	}
}

func TestInitDataRootLegacy(t *testing.T) {
	home := initHome(t)
	legacy := filepath.Join(home, ".local", "share", "agent-box")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, f.deps("linux")); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "already done: "+legacy) {
		t.Fatalf("out=%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "culibox")); err == nil {
		t.Fatal("must not create the new root next to the legacy one")
	}
}

func TestInitDataRootNotADir(t *testing.T) {
	// A root that is a file is refused when the config resolves, before step 1.
	home := initHome(t)
	root := filepath.Join(home, "file")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{have: allHave("abduco", "docker", "claude"), env: map[string]string{"CULIBOX_HOME": root}}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(""), true, &out, f.deps("linux")); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "not a directory") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestInitDataDirFromConfigIsAFile(t *testing.T) {
	// data_dir naming a file gets past Resolve: step 2 stops on it.
	home := initHome(t)
	root := filepath.Join(home, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"data_dir": "`+file+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{have: allHave("abduco", "docker", "claude"), env: map[string]string{"CULIBOX_HOME": root}}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(""), true, &out, f.deps("linux")); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "== step 2/8: data root") || !strings.Contains(out.String(), "stopped: "+file+" is not a folder") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestInitStopsAtDepsWithExit1(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("docker", "claude")}
	d := f.deps("linux")
	d.osRelease = func() string { return "ubuntu" }
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(""), true, &out, d); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), "step 2/8") {
		t.Fatalf("a stopped step must end the chain: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); err == nil {
		t.Fatal("nothing may be created before the dependencies are in place")
	}
}

func TestRealInitDepsAreWired(t *testing.T) {
	d := realInitDeps(t.TempDir())
	if d.vmSetUp == nil || d.boxReady == nil || d.boxSetup == nil || d.shareAdd == nil || d.shareState == nil || d.shareApply == nil || d.doctor == nil || d.newKeeper == nil {
		t.Fatal("every seam must be wired")
	}
	if d.goos == "" || d.getenv == nil || d.lookPath == nil || d.run == nil || d.readSecret == nil || d.hostSize == nil || d.macOSMajor == nil || d.osRelease == nil {
		t.Fatal("real seams missing")
	}
}

func TestInitRunsWorkspaceAndVMSizeSteps(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, f.deps("linux")); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	for _, w := range []string{"== step 3/8: workspace folder", "== step 4/8: VM size", "skipped: the box VM exists only on macOS"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("missing %q in:\n%s", w, out.String())
		}
	}
}

func TestInitStopsWhenNoWorkspaceIsGiven(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(""), true, &out, f.deps("linux")); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), "step 4/8") {
		t.Fatalf("a stopped step must end the chain: %s", out.String())
	}
}

func TestInitRunsTheTokenStepAfterVMSize(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	d := f.deps("linux")
	k := secretstore.NewFakeKeeper(true)
	d.newKeeper = func(string) secretstore.Keeper { return k }
	reads := []string{tokPass, tokPass, tokTest}
	d.readSecret = func() ([]byte, error) {
		if len(reads) == 0 {
			return nil, errors.New("no terminal")
		}
		r := reads[0]
		reads = reads[1:]
		return []byte(r), nil
	}
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+initRest), true, &out, d); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "== step 5/8: Claude token") {
		t.Fatalf("out=%s", out.String())
	}
	noToken(t, out.String())
	if len(reads) != 0 {
		t.Fatalf("token step did not read its answers: %v", reads)
	}
}

func TestInitStopsWhenTheTokenStepStops(t *testing.T) {
	home := initHome(t)
	f := &fakeInit{have: allHave("abduco", "docker", "claude")}
	d := f.deps("linux")
	k := secretstore.NewFakeKeeper(true) // readSecret fails: the passphrase cannot be read
	d.newKeeper = func(string) secretstore.Keeper { return k }
	var out bytes.Buffer
	if code := runInit(cliArgs{Cmd: "init"}, home, strings.NewReader(wsAnswer+"n\n"), true, &out, d); code != 1 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "stopped:") {
		t.Fatalf("out=%s", out.String())
	}
}

// An abduco next to culibox (a release tarball's) counts: init asks nothing for it.
func TestInitHaveAbducoBundled(t *testing.T) {
	f := &fakeInit{have: allHave("brew", "docker", "colima", "claude")}
	d := f.deps("darwin")
	var asked []string
	d.resolveAbduco = func(configured string) (abduco.Resolved, error) {
		asked = append(asked, configured)
		return abduco.Resolved{Path: "/opt/culibox/abduco", Source: abduco.SourceBundled}, nil
	}
	var out bytes.Buffer
	if ok := initDepsStep(newTestUI("", &out), config.Config{}, d); !ok || !strings.Contains(out.String(), "already done") {
		t.Fatalf("ok=%v out=%q", ok, out.String())
	}
	if len(f.runs) != 0 {
		t.Fatalf("run called: %v", f.runs)
	}
	// abduco_bin unset: the resolver gets "" so the next-to-culibox lookup runs.
	if len(asked) == 0 || asked[0] != "" {
		t.Fatalf("resolver got %q, want \"\"", asked)
	}
}
