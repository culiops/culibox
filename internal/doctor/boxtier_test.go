package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/config"
)

func ok() boxFacts {
	return boxFacts{SetUp: true, MacOSMajor: 14, ColimaVer: "0.8.1", ServerArch: "arm64", ProbeRan: true,
		Status: boxvm.Status{Running: true, MountType: "virtiofs", Driver: "macOS Virtualization.Framework"},
		Image:  boximage.ImageState{Present: true, Arch: "arm64", Hash: boximage.Hash()}}
}

func worst(rs []Result) string {
	w := Pass
	for _, r := range rs {
		if r.Status == Fail || (r.Status == Warn && w == Pass) {
			w = r.Status
		}
	}
	return w
}

func detail(rs []Result) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.Status+" "+r.Check+": "+r.Detail)
	}
	return strings.Join(s, "\n")
}

var tokenOK = Result{Pass, "boxed claude token", "ok"}

func TestBoxTierRows(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(*boxFacts)
		want  string
		match string
	}{
		{"all good", func(*boxFacts) {}, Pass, "running"},
		{"no docker", func(f *boxFacts) { f.DockerErr = errors.New("not found") }, Fail, "brew install"},
		{"no colima", func(f *boxFacts) {
			f.ColimaErr = errors.New("colima not found; install it with `brew install colima docker`")
		}, Fail, "brew install"},
		{"not set up", func(f *boxFacts) { f.SetUp = false }, Warn, "box setup"},
		{"old macOS", func(f *boxFacts) { f.MacOSMajor = 12 }, Fail, "13"},
		{"stopped", func(f *boxFacts) { f.Status.Running = false; f.ProbeRan = false }, Pass, "first boxed spawn"},
		{"status failed", func(f *boxFacts) { f.StatusErr = errors.New("colima status: boom") }, Fail, "boom"},
		{"drift", func(f *boxFacts) { f.Drifted = true }, Warn, "-recreate"},
		{"sshfs", func(f *boxFacts) { f.Status.MountType = "sshfs" }, Warn, "virtiofs"},
		{"no image", func(f *boxFacts) { f.Image = boximage.ImageState{} }, Fail, "box setup"},
		{"image inspect failed", func(f *boxFacts) { f.ImageErr = errors.New("docker image inspect: boom") }, Fail, "boom"},
		{"wrong arch", func(f *boxFacts) { f.Image.Arch = "amd64" }, Fail, "amd64"},
		{"stale image", func(f *boxFacts) { f.Image.Hash = "old" }, Warn, "-rebuild"},
		{"probe reached", func(f *boxFacts) {
			f.ProbeErr = errors.New("containment check failed: the box can reach the Mac's loopback services")
		}, Fail, "loopback"},
		{"context switched", func(f *boxFacts) {
			f.UserContext = errors.New("your docker CLI's current context is agent-box's box VM")
		}, Fail, "context"},
		{"context unchecked", func(f *boxFacts) {
			f.UserContext = fmt.Errorf("%w (docker context show: exit status 1)", boxvm.ErrContextUnchecked)
		}, Warn, "could not check"},
		{"socket too long", func(f *boxFacts) { f.SocketErr = errors.New("the box VM socket … Set AGENT_BOX_HOME") }, Fail, "AGENT_BOX_HOME"},
		{"stopped boxes", func(f *boxFacts) { f.Stopped = []string{"ab-box-1"} }, Warn, "ab-box-1"},
		{"old colima", func(f *boxFacts) { f.ColimaVer = "0.6.9" }, Warn, "0.8"},
	}
	for _, c := range cases {
		f := ok()
		c.mod(&f)
		rs := boxTierResults("darwin", f, tokenOK)
		if worst(rs) != c.want || !strings.Contains(detail(rs), c.match) {
			t.Fatalf("%s: want %s containing %q, got\n%s", c.name, c.want, c.match, detail(rs))
		}
	}
}

// An unchecked context must never read as a confident PASS.
func TestBoxTierUncheckedContextIsNotAPass(t *testing.T) {
	f := ok()
	f.UserContext = fmt.Errorf("%w", boxvm.ErrContextUnchecked)
	for _, r := range boxTierResults("darwin", f, tokenOK) {
		if r.Check == "docker context" && r.Status != Warn {
			t.Fatalf("unchecked context must WARN: %+v", r)
		}
	}
}

func TestBoxTierCheckedContextShowsARow(t *testing.T) {
	rs := boxTierResults("darwin", ok(), tokenOK)
	if !strings.Contains(detail(rs), "PASS docker context") {
		t.Fatalf("a verified user context deserves its own PASS row:\n%s", detail(rs))
	}
}

func TestBoxTierLinux(t *testing.T) {
	rs := boxTierResults("linux", boxFacts{DockerErr: errors.New("not found")}, tokenOK)
	if worst(rs) != Warn {
		t.Fatalf("linux without docker stays a WARN (host tier only): %s", detail(rs))
	}
	rs = boxTierResults("linux", boxFacts{Stopped: []string{"ab-box-2"}}, tokenOK)
	if worst(rs) != Warn || !strings.Contains(detail(rs), "ab-box-2") {
		t.Fatalf("linux stopped boxes must WARN: %s", detail(rs))
	}
}

func TestClaudeBoxToken(t *testing.T) {
	boxedClaude := config.AgentConfig{Name: "claude", Bin: "claude"}
	c := config.Config{Agents: []config.AgentConfig{boxedClaude}}
	if r := ClaudeBoxToken(c); r.Status != Warn || !strings.Contains(r.Detail, "claude setup-token") {
		t.Fatalf("no secret configured: %+v", r)
	}
	boxedClaude.Secrets = []string{"CLAUDE_CODE_OAUTH_TOKEN"}
	c = config.Config{Agents: []config.AgentConfig{boxedClaude}, SecretSources: []config.SecretSource{{Type: "env"}}}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-test-not-real")
	if r := ClaudeBoxToken(c); r.Status != Pass || strings.Contains(r.Detail, "sk-test") {
		t.Fatalf("configured token must pass and never print: %+v", r)
	}
	os.Unsetenv("CLAUDE_CODE_OAUTH_TOKEN") // t.Setenv above restores it
	if r := ClaudeBoxToken(c); r.Status != Warn {
		t.Fatalf("a secret that does not resolve is not a credential: %+v", r)
	}
	host := config.AgentConfig{Name: "claude", Bin: "claude", Runtime: config.RuntimeHost}
	if r := ClaudeBoxToken(config.Config{Agents: []config.AgentConfig{host}}); r.Status != Pass {
		t.Fatalf("no boxed claude agent: nothing to check: %+v", r)
	}
}

// fakeDocker is a docker that records its argv and answers `ps` with one
// stopped and one running box.
func fakeDocker(t *testing.T) (bin, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + argvLog + "\n" +
		"printf 'ab-box-1\\texited\\t/w\\t2026-10-15 10:00:00 +0000 UTC\\nab-box-2\\trunning\\t/w\\t2026-10-15 10:00:00 +0000 UTC\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvLog
}

func TestGatherBoxFactsLinuxListsOnlyThisInstancesStoppedBoxes(t *testing.T) {
	bin, argvLog := fakeDocker(t)
	f := gatherBoxFacts(config.Config{DockerBin: bin, EnvLabel: "PROD"}, t.TempDir(), t.TempDir(), "linux")
	if f.DockerErr != nil || len(f.Stopped) != 1 || f.Stopped[0] != "ab-box-1" {
		t.Fatalf("facts = %+v", f)
	}
	b, _ := os.ReadFile(argvLog)
	if !strings.Contains(string(b), "label=io.culiops.agent-box.instance=PROD") {
		t.Fatalf("must filter by this instance's label, argv: %s", b)
	}
}

func TestGatherBoxFactsLinuxMissingDocker(t *testing.T) {
	f := gatherBoxFacts(config.Config{DockerBin: "docker-not-here-xyz"}, t.TempDir(), t.TempDir(), "linux")
	if f.DockerErr == nil {
		t.Fatal("a missing docker must be reported")
	}
}

// Doctor must never start the VM: with a colima that records its calls, a
// gather on a not-yet-set-up machine may only ask for its version.
func TestGatherBoxFactsDarwinNeverStartsTheVM(t *testing.T) {
	dockerBin, _ := fakeDocker(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	colima := filepath.Join(dir, "colima")
	script := "#!/bin/sh\necho \"$1\" >> " + calls + "\n[ \"$1\" = version ] && echo 'colima version 0.8.1'\nexit 0\n"
	if err := os.WriteFile(colima, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.MkdirTemp("/tmp", "abdoc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	f := gatherBoxFacts(config.Config{DockerBin: dockerBin, ColimaBin: colima}, data, t.TempDir(), "darwin")
	if f.SetUp || f.ColimaVer != "0.8.1" || f.ColimaErr != nil || f.DockerErr != nil {
		t.Fatalf("facts = %+v", f)
	}
	b, _ := os.ReadFile(calls)
	if strings.TrimSpace(string(b)) != "version" {
		t.Fatalf("doctor may only ask colima for its version, got calls:\n%s", b)
	}
}
