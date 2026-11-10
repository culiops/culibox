package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/vmshares"
)

func ok() boxFacts {
	return boxFacts{SetUp: true, MacOSMajor: 14, ColimaVer: "0.8.1", ServerArch: "arm64", ProbeRan: true, FirewallRan: true, ImageName: "culiops/agent-box:base",
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
		{"provision drift fails with the firewall reason", func(f *boxFacts) { f.Drifted = true; f.ProvisionDrifted = true }, Fail,
			`differ from what this version and config write (set up by an older version, or broker_port or egress_port changed); run "agent-box box setup -recreate"`},
		{"stale VM firewall fails", func(f *boxFacts) {
			f.FirewallErr = errors.New("the box VM's firewall lets containers on its default network reach the Mac's loopback without the relay; run `agent-box box setup -recreate`")
		}, Fail, "FAIL box VM firewall: the box VM's firewall lets containers"},
		{"unreadable VM firewall warns", func(f *boxFacts) {
			f.FirewallReadErr = errors.New("reading the box VM's firewall rules: exit status 255")
		}, Warn, "WARN box VM firewall: could not read the rules (reading the box VM's firewall rules: exit status 255)"},
		{"drift names the CA setting", func(f *boxFacts) { f.Drifted = true }, Warn, "extra_ca_certs"},
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

// The live firewall check has its own row when it ran, and none when it did
// not (VM stopped).
func TestBoxTierFirewallRow(t *testing.T) {
	if d := detail(boxTierResults("darwin", ok(), tokenOK)); !strings.Contains(d, "PASS box VM firewall: the relay's INPUT rules and the gateway DROP are loaded, and no DOCKER-USER rule passes TCP") {
		t.Fatalf("a verified firewall deserves its own PASS row:\n%s", d)
	}
	f := ok()
	f.Status.Running, f.ProbeRan, f.FirewallRan = false, false, false
	if d := detail(boxTierResults("darwin", f, tokenOK)); strings.Contains(d, "box VM firewall") {
		t.Fatalf("no firewall row for a stopped VM:\n%s", d)
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
	if r := ClaudeBoxToken(c, nil); r.Status != Warn || !strings.Contains(r.Detail, "claude setup-token") {
		t.Fatalf("no secret configured: %+v", r)
	}
	boxedClaude.Secrets = []string{"CLAUDE_CODE_OAUTH_TOKEN"}
	c = config.Config{Agents: []config.AgentConfig{boxedClaude}, SecretSources: []config.SecretSource{{Type: "env"}}}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-test-not-real")
	if r := ClaudeBoxToken(c, nil); r.Status != Pass || strings.Contains(r.Detail, "sk-test") {
		t.Fatalf("configured token must pass and never print: %+v", r)
	}
	os.Unsetenv("CLAUDE_CODE_OAUTH_TOKEN") // t.Setenv above restores it
	if r := ClaudeBoxToken(c, nil); r.Status != Warn {
		t.Fatalf("a secret that does not resolve is not a credential: %+v", r)
	}
	host := config.AgentConfig{Name: "claude", Bin: "claude", Runtime: config.RuntimeHost}
	if r := ClaudeBoxToken(config.Config{Agents: []config.AgentConfig{host}}, nil); r.Status != Pass {
		t.Fatalf("no boxed claude agent: nothing to check: %+v", r)
	}
}

type stubStore struct {
	vals map[string]string
	err  error
}

func (s stubStore) Get(id string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	v, ok := s.vals[id]
	if !ok {
		return nil, secretstore.ErrNoSuchValue
	}
	return []byte(v), nil
}

// A store secret_source must not make doctor fail to start (secrets.New refuses
// the type): with the store it resolves, locked it is a WARN, and with none it
// is a WARN, never a panic.
func TestClaudeBoxTokenStoreSource(t *testing.T) {
	boxed := config.AgentConfig{Name: "claude", Bin: "claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}
	c := config.Config{Agents: []config.AgentConfig{boxed}, SecretSources: []config.SecretSource{{Type: "store"}, {Type: "env"}}}
	os.Unsetenv("CLAUDE_CODE_OAUTH_TOKEN")
	ok := stubStore{vals: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "sk-store-not-real"}}
	if r := ClaudeBoxToken(c, ok); r.Status != Pass || strings.Contains(r.Detail, "sk-store") {
		t.Fatalf("store holds the token: %+v", r)
	}
	if r := ClaudeBoxToken(c, stubStore{err: secretstore.ErrLocked}); r.Status != Warn ||
		!strings.Contains(r.Detail, "is in the store, which is locked") || strings.Contains(r.Detail, "/login") {
		t.Fatalf("a locked store does not resolve and must say so: %+v", r)
	}
	if r := ClaudeBoxToken(c, nil); r.Status != Warn {
		t.Fatalf("a store source with no store: %+v", r)
	}
	// A Keychain failure is a FAIL with the fix: neither "locked" nor "/login".
	ue := &secretstore.UnavailableError{Kind: secretstore.KindAccess, Reason: "denied"}
	if r := ClaudeBoxToken(c, stubStore{err: ue}); r.Status != Fail || !strings.Contains(r.Detail, ue.Fix()) ||
		strings.Contains(r.Detail, "locked") || strings.Contains(r.Detail, "/login") {
		t.Fatalf("an unavailable Keychain must FAIL with the fix: %+v", r)
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

// Review focus 4: whether the user's own docker context is still theirs does not
// depend on the box VM, so the row appears on every darwin early return too.
func TestBoxTierContextRowSurvivesEarlyReturns(t *testing.T) {
	switched := errors.New("your docker CLI's current context is agent-box's box VM")
	for name, mod := range map[string]func(*boxFacts){
		"no colima":       func(f *boxFacts) { f.ColimaErr = errors.New("colima not found") },
		"old macOS":       func(f *boxFacts) { f.MacOSMajor = 12 },
		"socket too long": func(f *boxFacts) { f.SocketErr = errors.New("socket too long") },
		"not set up":      func(f *boxFacts) { f.SetUp = false },
		"status failed":   func(f *boxFacts) { f.StatusErr = errors.New("boom") },
	} {
		f := ok()
		mod(&f)
		f.UserContext = switched
		if d := detail(boxTierResults("darwin", f, tokenOK)); !strings.Contains(d, "FAIL docker context") {
			t.Errorf("%s: the context row was dropped:\n%s", name, d)
		}
		f.UserContext = nil
		if d := detail(boxTierResults("darwin", f, tokenOK)); !strings.Contains(d, "PASS docker context") {
			t.Errorf("%s: the context PASS row was dropped:\n%s", name, d)
		}
	}
}

func TestBoxTierWholeHomeWarns(t *testing.T) {
	f := ok()
	f.WholeHome, f.ShareCount = true, 3
	rs := boxTierResults("darwin", f, tokenOK)
	want := vmshares.WholeHomeLine(3)
	found := false
	for _, r := range rs {
		if r.Check == "box VM folders" && r.Status == Warn && r.Detail == want {
			found = true
		}
	}
	if !found || !strings.Contains(want, "shares your whole home folder") {
		t.Fatalf("want whole-home warn %q:\n%s", want, detail(rs))
	}
}

func TestBoxTierMountsOnlyDriftNoRecreate(t *testing.T) {
	f := ok()
	f.Drifted, f.Drift = true, boxvm.DriftMountsOnly
	rs := boxTierResults("darwin", f, tokenOK)
	if strings.Contains(detail(rs), "-recreate") {
		t.Fatalf("mounts-only drift must not mention -recreate:\n%s", detail(rs))
	}
	if !strings.Contains(detail(rs), "WARN box VM folders: the folder list changed") {
		t.Fatalf("missing folder-list drift warn:\n%s", detail(rs))
	}
	f.Drift = boxvm.DriftOther
	if !strings.Contains(detail(boxTierResults("darwin", f, tokenOK)), "-recreate") {
		t.Fatal("DriftOther keeps the recreate row")
	}
}

func TestBoxTierSharesCorruptWarns(t *testing.T) {
	f := ok()
	f.SharesErr = errors.New("vm-shares.json is damaged")
	f.Drifted, f.Drift = true, boxvm.DriftMountsOnly
	d := detail(boxTierResults("darwin", f, tokenOK))
	if !strings.Contains(d, "box VM folders list: vm-shares.json cannot be read (vm-shares.json is damaged); fix or move it away; until then the folder list cannot change") {
		t.Fatalf("corrupt warn missing:\n%s", d)
	}
	for _, bad := range []string{"apply it", "0 folders", "-recreate", "box VM folders: the box VM sees"} {
		if strings.Contains(d, bad) {
			t.Fatalf("corrupt list must not give %q:\n%s", bad, d)
		}
	}
	f.WholeHome = true
	d = detail(boxTierResults("darwin", f, tokenOK))
	if !strings.Contains(d, "WARN box VM folders: the box VM shares your whole home folder; fix vm-shares.json before applying a folder list\n") || strings.Contains(d, "apply the folder list") {
		t.Fatalf("whole-home + corrupt:\n%s", d)
	}
}

func TestBoxTierFoldersUnreadableWarns(t *testing.T) {
	f := ok()
	f.FoldersErr = errors.New("bad yaml")
	d := detail(boxTierResults("darwin", f, tokenOK))
	if !strings.Contains(d, "WARN box VM folders: cannot read the box VM's folder list (bad yaml); run `agent-box box setup`") || strings.Contains(d, "PASS box VM folders") {
		t.Fatalf("got:\n%s", d)
	}
}

// An applied list matching vm-shares.json is not drift (review: SetShares must
// come before the drift computation).
func TestGatherBoxFactsAppliedListIsNotDrift(t *testing.T) {
	dockerBin, _ := fakeDocker(t)
	dir := t.TempDir()
	colima := filepath.Join(dir, "colima")
	if err := os.WriteFile(colima, []byte("#!/bin/sh\n[ \"$1\" = version ] && echo 'colima version 0.8.1'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.MkdirTemp("/tmp", "abdoc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	home, err := os.MkdirTemp("/tmp", "abhome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := vmshares.Save(data, []vmshares.Share{{Path: proj, Mode: vmshares.RW}}); err != nil {
		t.Fatal(err)
	}
	c := config.Config{DockerBin: dockerBin, ColimaBin: colima}
	// Stage the VM exactly as setup would have: yaml + stamp for this list.
	cfg := boxvm.ConfigFor(c, data, home, colima, dockerBin, 4, 8)
	cfg.Fold = true
	vm := boxvm.NewVM(cfg, boxvm.ExecRunner{})
	vc := vm.Config()
	list, _ := vmshares.Normalize([]vmshares.Share{{Path: proj, Mode: vmshares.RW}}, vmshares.Env{Home: vc.Home, DataRoot: vc.Root, Fold: true})
	vm.SetShares(list)
	y, err := vm.Config().YAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vc.ProfileDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vc.YAMLPath(), y, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(y)
	if err := os.WriteFile(vc.StampPath(), []byte(hex.EncodeToString(sum[:])+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := gatherBoxFacts(c, data, home, "darwin")
	if !f.SetUp || f.Drifted || f.Drift != boxvm.DriftNone || f.WholeHome || f.FoldersErr != nil || f.SharesErr != nil || f.ShareCount != 1 {
		t.Fatalf("facts = %+v", f)
	}
	f.Status, f.StatusErr = boxvm.Status{Running: true, MountType: "virtiofs"}, nil
	d := detail(boxTierResults("darwin", f, tokenOK))
	if strings.Contains(d, "box VM config") || !strings.Contains(d, "PASS box VM folders: the box VM sees 1 folders") {
		t.Fatalf("got:\n%s", d)
	}
}

func TestBoxTierFolderPassAndLinuxNoRow(t *testing.T) {
	f := ok()
	f.ShareCount = 4
	if !strings.Contains(detail(boxTierResults("darwin", f, tokenOK)), "box VM folders: the box VM sees 4 folders and CuliBox data") {
		t.Fatal("missing pass row")
	}
	f.Stopped = []string{"x"}
	if strings.Contains(detail(boxTierResults("linux", f, tokenOK)), "box VM folders") {
		t.Fatal("linux has no folder row")
	}
}
