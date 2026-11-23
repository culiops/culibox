package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/handback"
)

func TestParseCloneFetch(t *testing.T) {
	ok := []struct {
		argv []string
		want cliArgs
	}{
		{[]string{"clone", "fetch", "p", "b"}, cliArgs{Cmd: "clone", Sub: "fetch", Profile: "p", Branch: "b", Base: "main"}},
		{[]string{"clone", "fetch", "-base", "dev", "p", "b"}, cliArgs{Cmd: "clone", Sub: "fetch", Profile: "p", Branch: "b", Base: "dev"}},
		{[]string{"clone", "fetch", "-config", "/x.json", "p", "feat/x"},
			cliArgs{Cmd: "clone", Sub: "fetch", ConfigPath: "/x.json", ConfigSet: true, Profile: "p", Branch: "feat/x", Base: "main"}},
		{[]string{"clone", "fetch", "-allow-old-history", "p", "b"},
			cliArgs{Cmd: "clone", Sub: "fetch", Profile: "p", Branch: "b", Base: "main", AllowOldHistory: true}},
	}
	for _, c := range ok {
		got, err := parseArgs(c.argv)
		if err != nil || got != c.want {
			t.Errorf("parseArgs(%q) = %+v,%v want %+v", c.argv, got, err, c.want)
		}
	}
	for _, bad := range [][]string{
		{"clone", "fetch", "p"},                            // missing branch
		{"clone", "fetch", "p", "b", "extra"},              // a third positional
		{"clone", "status"},                                // unknown subcommand
		{"clone"},                                          // no subcommand
		{"clone", "-base", "x"},                            // flag where the subcommand belongs
		{"box", "setup", "-base", "x"},                     // -base is for clone only
		{"serve", "-base", "x"},                            // ditto
		{"clone", "fetch", "p", "b", "-base", "x"},         // flags after positionals
		{"clone", "fetch", "-base", "", "p", "b"},          // an empty base
		{"clone", "fetch", "-rebuild", "p", "b"},           // box flags do not apply
		{"box", "setup", "-allow-old-history"},             // -allow-old-history is for clone fetch only
		{"clone", "fetch", "p", "b", "-allow-old-history"}, // flags after positionals
	} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%q): want error", bad)
		}
	}
}

// cloneEnv is a data root with a store holding one clone profile and a config
// that seeds another, and a stub for clone.Fetch that records its request.
type cloneEnv struct {
	cfg  config.Config
	home string
	goos string // what runClone is told it runs on; linux unless a test sets it
	data string
	req  clone.FetchRequest
	res  clone.FetchResult
	err  error
}

func newCloneEnv(t *testing.T, stored []config.AgentConfig, seeds []config.AgentConfig) *cloneEnv {
	t.Helper()
	root := t.TempDir()
	e := &cloneEnv{home: filepath.Join(root, "home"), goos: "linux", data: filepath.Join(root, "data")}
	if err := os.MkdirAll(e.data, 0o700); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		b, _ := json.Marshal(stored)
		if err := os.WriteFile(filepath.Join(e.data, "profiles.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg = config.Config{DataDir: e.data, Agents: seeds, BoxImage: "culiops/agent-box:20261018", EnvLabel: "DEV"}
	oldFetch, oldBack := cloneFetch, cloneBackends
	cloneFetch = func(_ context.Context, _ clone.Git, _ clone.Docker, r clone.FetchRequest) (clone.FetchResult, error) {
		e.req = r
		return e.res, e.err
	}
	cloneBackends = func(config.Config, string, string, string, []box.ProtectedDir) (clone.Git, clone.Docker, error) {
		return nil, nil, nil
	}
	t.Cleanup(func() { cloneFetch, cloneBackends = oldFetch, oldBack })
	return e
}

func cloneProfile(name, of, dir string) config.AgentConfig {
	return config.AgentConfig{Name: name, Bin: "claude", CloneOf: of, DefaultCwd: dir}
}

func runCloneArgs(e *cloneEnv, profile, branch, base string) (int, string) {
	var out bytes.Buffer
	rc := runClone(cliArgs{Cmd: "clone", Sub: "fetch", Profile: profile, Branch: branch, Base: base}, e.cfg, e.home, e.goos, nil, &out)
	return rc, out.String()
}

func TestRunCloneUnknownProfile(t *testing.T) {
	e := newCloneEnv(t, nil, nil)
	rc, out := runCloneArgs(e, "nope", "b", "main")
	if rc != 1 || out != "culibox clone: unknown profile \"nope\"\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneNoCloneOf(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{{Name: "plain", Bin: "claude"}})
	rc, out := runCloneArgs(e, "plain", "b", "main")
	if rc != 1 || out != "culibox clone: profile \"plain\" has no clone_of\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if e.req.Branch != "" {
		t.Fatal("Fetch must not run for a profile without clone_of")
	}
}

func TestRunCloneInvalidProfile(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("rel", "relative/path", "/c/d")})
	rc, out := runCloneArgs(e, "rel", "b", "main")
	if rc != 1 || !strings.HasPrefix(out, "culibox clone: ") || !strings.Contains(out, "clone_of") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneStoreOverridesSeed(t *testing.T) {
	stored := []config.AgentConfig{cloneProfile("p", "/src/new", "/clones/new")}
	seeds := []config.AgentConfig{cloneProfile("p", "/src/old", "/clones/old")}
	e := newCloneEnv(t, stored, seeds)
	e.res = clone.FetchResult{Ref: "refs/heads/box/b"}
	rc, out := runCloneArgs(e, "p", "b", "dev")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	r := e.req
	if r.Clone.Of != "/src/new" || r.Clone.Dir != "/clones/new" {
		t.Errorf("clone spec = %+v, want the store's", r.Clone)
	}
	if r.Branch != "b" || r.Base != "dev" {
		t.Errorf("branch/base = %q/%q", r.Branch, r.Base)
	}
	if r.Image != "culiops/agent-box:20261018" {
		t.Errorf("image = %q, want the global box_image", r.Image)
	}
	if r.Instance != "DEV" {
		t.Errorf("instance = %q", r.Instance)
	}
	if r.TempDir != filepath.Join(e.data, "clone-fetch") {
		t.Errorf("TempDir = %q, want under the data dir", r.TempDir)
	}
	if r.Home != e.home {
		t.Errorf("Home = %q", r.Home)
	}
	if len(r.Protected) == 0 {
		t.Error("Protected is empty: the data dir at least must be protected")
	}
}

func TestRunCloneFallsBackToSeed(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("s", "/src/s", "/clones/s")})
	rc, _ := runCloneArgs(e, "s", "b", "main")
	if rc != 0 || e.req.Clone.Of != "/src/s" {
		t.Fatalf("rc=%d req=%+v", rc, e.req)
	}
}

func TestRunCloneOutput(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{
		Ref:      "refs/heads/box/feat/x",
		Commits:  []string{"abc1234 add thing", "def5678 fix thing"},
		Warnings: []string{"1 commit carries an attribution trailer"},
	}
	var buf bytes.Buffer
	rc := runClone(cliArgs{Cmd: "clone", Sub: "fetch", Profile: "p", Branch: "feat/x", Base: "main", AllowOldHistory: true},
		e.cfg, e.home, e.goos, nil, &buf)
	out := buf.String()
	if e.req.DataDir != e.data {
		t.Errorf("DataDir = %q, want the data dir %q (where the base record lives)", e.req.DataDir, e.data)
	}
	if !e.req.AllowOldHistory {
		t.Error("-allow-old-history did not reach the fetch")
	}
	want := "fetched box/feat/x (2 commit(s) not on main):\n" +
		"  abc1234 add thing\n" +
		"  def5678 fix thing\n" +
		"WARN: 1 commit carries an attribution trailer\n" +
		"review: git -C /src/r log -p main..box/feat/x; merge per your review policy\n" +
		"Nothing flagged. This is not a malware scan.\n"
	if rc != 0 || out != want {
		t.Fatalf("rc=%d\ngot:\n%s\nwant:\n%s", rc, out, want)
	}
}

func TestRunCloneFetchError(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.err = errors.New("nothing to fetch")
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || out != "culibox clone: nothing to fetch\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneBackendError(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	cloneBackends = func(config.Config, string, string, string, []box.ProtectedDir) (clone.Git, clone.Docker, error) {
		return nil, nil, errors.New("git not found")
	}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || out != "culibox clone: git not found\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

// clone fetch runs on macOS too, and asks for that OS's backends with the
// same data dir and home it read the profile from.
func TestRunCloneOnDarwin(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.goos = "darwin"
	e.res = clone.FetchResult{Ref: "refs/heads/box/b"}
	var gotOS, gotData, gotHome string
	var gotProt []box.ProtectedDir
	cloneBackends = func(_ config.Config, goos, dataDir, home string, prot []box.ProtectedDir) (clone.Git, clone.Docker, error) {
		gotOS, gotData, gotHome, gotProt = goos, dataDir, home, prot
		return nil, nil, nil
	}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 0 || !strings.HasPrefix(out, "fetched box/b ") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if gotOS != "darwin" || gotData != e.data || gotHome != e.home {
		t.Fatalf("backends for (%q, %q, %q), want (darwin, %q, %q)", gotOS, gotData, gotHome, e.data, e.home)
	}
	// M3: the box layer clone fetch builds knows CuliBox's protected dirs, as serve's does.
	if len(gotProt) == 0 || gotProt[0] != box.DataDirProtected(e.data) {
		t.Fatalf("backends got protected %+v, want the data dir first", gotProt)
	}
}

// lastDockerCall is the last argv the fake docker logged.
func lastDockerCall(d *fakeDocker) string {
	lines := strings.Split(strings.TrimSpace(d.log()), "\n")
	return lines[len(lines)-1]
}

// On macOS the bundle container of clone fetch runs on the box VM's docker,
// where the clone is shared.
func TestCloneDockerOnDarwinIsTheVMs(t *testing.T) {
	root := resolved(t, shortRoot(t))
	d := newFakeDocker(t)
	fc := &fakeColima{running: true}
	dk, err := cloneDocker(context.Background(), config.Config{DockerBin: d.bin, ColimaBin: "/bin/sh"}, "darwin", root, root, fc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dk.Remove(context.Background(), "agent-box-clonefetch-x"); err != nil {
		t.Fatal(err)
	}
	want := "--host unix://" + filepath.Join(root, "vm", "agent-box", "docker.sock") + " --config " + filepath.Join(root, "docker") + " rm -f agent-box-clonefetch-x"
	if got := lastDockerCall(d); got != want {
		t.Fatalf("docker call = %q, want %q", got, want)
	}
}

// clone fetch never starts the VM: with it stopped there is no docker to run
// the bundle container on, and the refusal says how to start it.
func TestCloneDockerRefusesAStoppedVM(t *testing.T) {
	root := resolved(t, shortRoot(t))
	d := newFakeDocker(t)
	fc := &fakeColima{}
	_, err := cloneDocker(context.Background(), config.Config{DockerBin: d.bin, ColimaBin: "/bin/sh"}, "darwin", root, root, fc, nil)
	if err == nil || !strings.Contains(err.Error(), "box VM is not running") || !strings.Contains(err.Error(), "culibox box setup") {
		t.Fatalf("err = %v; want the stopped-VM refusal", err)
	}
	if fc.called("start") {
		t.Fatal("clone fetch started the VM")
	}
}

// On Linux the bundle container runs on the host's docker, with no global flags.
func TestCloneDockerOnLinuxIsTheHosts(t *testing.T) {
	root := t.TempDir()
	d := newFakeDocker(t)
	dk, err := cloneDocker(context.Background(), config.Config{DockerBin: d.bin}, "linux", root, root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dk.Remove(context.Background(), "agent-box-clonefetch-x"); err != nil {
		t.Fatal(err)
	}
	if got := lastDockerCall(d); got != "rm -f agent-box-clonefetch-x" {
		t.Fatalf("docker call = %q", got)
	}
}

// hasLinePrefix reports a line of log that starts with prefix.
func hasLinePrefix(log, prefix string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// I2: clone fetch runs the box image, so an upgraded install whose image still
// carries only the pre-rename default name adopts it here too (box_image
// unset); a pinned box_image is never tagged.
func TestCloneDockerAdoptsTheLegacyDefault(t *testing.T) {
	root := t.TempDir()
	d := adoptDocker(t, config.LegacyDefaultBoxImage)
	if _, err := cloneDocker(context.Background(), config.Config{DockerBin: d.bin}, "linux", root, root, nil, nil); err != nil {
		t.Fatal(err)
	}
	if want := "tag " + config.LegacyDefaultBoxImage + " " + config.DefaultBoxImage; !hasLinePrefix(d.log(), want) {
		t.Fatalf("clone fetch did not adopt (%q):\n%s", want, d.log())
	}

	d = adoptDocker(t, config.LegacyDefaultBoxImage)
	if _, err := cloneDocker(context.Background(), config.Config{DockerBin: d.bin, BoxImage: "me/img:1"}, "linux", root, root, nil, nil); err != nil {
		t.Fatal(err)
	}
	if hasLinePrefix(d.log(), "tag ") || hasLinePrefix(d.log(), "image ") {
		t.Fatalf("a pinned box_image must never inspect or tag:\n%s", d.log())
	}
}

// A stopped VM is refused before any adoption: no image is inspected or tagged.
func TestCloneDockerStoppedVMDoesNotAdopt(t *testing.T) {
	root := resolved(t, shortRoot(t))
	d := adoptDocker(t, config.LegacyDefaultBoxImage)
	if _, err := cloneDocker(context.Background(), config.Config{DockerBin: d.bin, ColimaBin: "/bin/sh"}, "darwin", root, root, &fakeColima{}, nil); err == nil {
		t.Fatal("a stopped VM was not refused")
	}
	if log := d.log(); strings.Contains(log, "image inspect") || strings.Contains(log, " tag "+config.LegacyDefaultBoxImage) {
		t.Fatalf("image inspected or tagged with the VM stopped:\n%s", log)
	}
}

func TestRunCloneUnreadableStore(t *testing.T) {
	e := newCloneEnv(t, nil, nil)
	if err := os.WriteFile(filepath.Join(e.data, "profiles.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || !strings.HasPrefix(out, "culibox clone: ") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	// Read-only: a corrupt store is reported, never quarantined or rewritten.
	if b, _ := os.ReadFile(filepath.Join(e.data, "profiles.json")); string(b) != "{not json" {
		t.Fatalf("profiles.json was modified: %q", b)
	}
}

func TestRunCloneOutputQuotesTheRepoPath(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/my repos/it's", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/b", Commits: []string{"abc1234 x"}}
	rc, out := runCloneArgs(e, "p", "b", "main")
	want := "review: git -C '/my repos/it'\\''s' log -p main..box/b; merge per your review policy\n"
	if rc != 0 || !strings.Contains(out, want) {
		t.Fatalf("rc=%d out=%q want it to contain %q", rc, out, want)
	}
}

func flaggedReport() handback.Report {
	return handback.Report{
		Inspected: 7,
		Flags: []handback.Flag{
			{Path: "docs/report.pdf", Classes: []handback.Class{handback.ClassMismatch}, Detail: "ELF executable named .pdf", SHA256: strings.Repeat("ab", 32)},
			{Path: "big.bin", Classes: []handback.Class{handback.ClassExecutable}, Detail: "ELF executable", HashNote: "not hashed: larger than 64 MiB"},
		},
	}
}

func TestCloneFetchPrintsFlags(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/b", Commits: []string{"abc1234 x"}, Handback: flaggedReport()}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	_, after, ok := strings.Cut(out, "review: git -C /src/r log -p main..box/b; merge per your review policy\n")
	if !ok {
		t.Fatalf("no review line in %q", out)
	}
	for _, want := range []string{
		"Flagged for your review",
		"docs/report.pdf  ELF executable named .pdf  sha256 " + strings.Repeat("ab", 32),
		"big.bin  ELF executable  not hashed: larger than 64 MiB",
		"This is not a malware scan.",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("output after the review line lacks %q:\n%s", want, after)
		}
	}
	if strings.Contains(after, "Nothing flagged") {
		t.Errorf("a flagged branch must not say nothing was flagged:\n%s", after)
	}
	// The same record the daemon's return writes, so the panel shows it.
	recs, err := handback.Store{Dir: filepath.Join(e.data, "handback")}.Pending()
	if err != nil || len(recs) != 1 {
		t.Fatalf("Pending = %+v, %v; want the branch's record", recs, err)
	}
	if r := recs[0]; r.Kind != handback.KindClone || r.CloneOf != "/src/r" || r.Branch != "box/b" || len(r.Flags) != 2 || r.ID != handback.RecordID(handback.CloneKey("/src/r", "b")) {
		t.Fatalf("record = %+v", r)
	}
}

func TestCloneFetchPrintsTruncationInsteadOfNothingFlagged(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/b", Handback: handback.Report{Truncated: "walk cap of 500000 entries reached"}}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 0 || !strings.Contains(out, "Not everything was checked: walk cap of 500000 entries reached\n") || strings.Contains(out, "Nothing flagged") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

// The ref moved and a later step failed: the branch is home, so its flags are
// still shown, before the failure.
func TestCloneFetchPrintsFlagsWhenALaterStepFailed(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/b", Handback: flaggedReport()}
	e.err = errors.New("fetched refs/heads/box/b but cannot list it")
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || !strings.Contains(out, "Flagged for your review") || !strings.HasSuffix(out, "culibox clone: fetched refs/heads/box/b but cannot list it\n") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

// A record that cannot be written is said, and the flags are still printed.
func TestCloneFetchSaysWhenTheRecordCannotBeWritten(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/b", Handback: flaggedReport()}
	dir := filepath.Join(e.data, "handback")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, handback.RecordID(handback.CloneKey("/src/r", "b"))+".json")); err != nil {
		t.Fatal(err)
	}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 0 || !strings.Contains(out, "WARN: could not record what was flagged for review") || !strings.Contains(out, "docs/report.pdf") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if b, _ := os.ReadFile(other); string(b) != "x" {
		t.Fatalf("the symlink target was written: %q", b)
	}
}

// A profile whose clone_of is another profile's clone (or a recorded clone dir)
// would let a box plant git hooks that the host's fetch then runs.
func TestRunCloneRefusesBoxOwnedCloneOf(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{
		cloneProfile("victim", "/src/real", "/clones/victim"),
		cloneProfile("evil", "/clones/victim", "/clones/evil"),
		cloneProfile("nested", "/clones/victim/sub/repo", "/clones/nested"),
		cloneProfile("recorded", "/clones/gone/repo", "/clones/rec"),
	})
	if err := os.WriteFile(filepath.Join(e.data, cloneDirsFile), []byte("/clones/gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"evil", "nested", "recorded"} {
		rc, out := runCloneArgs(e, p, "b", "main")
		if rc != 1 || !strings.Contains(out, "is box-owned") || !strings.Contains(out, "a clone must be made from a host-owned repo") {
			t.Errorf("%s: rc=%d out=%q", p, rc, out)
		}
	}
	if e.req.Branch != "" {
		t.Fatal("Fetch ran for a box-owned clone_of")
	}
	// The victim itself (host-owned clone_of) still works.
	if rc, out := runCloneArgs(e, "victim", "b", "main"); rc != 0 {
		t.Fatalf("victim: rc=%d out=%q", rc, out)
	}
}

func TestRunCloneCorruptRecordFailsClosed(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	if err := os.WriteFile(filepath.Join(e.data, cloneDirsFile), []byte("not/absolute\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || !strings.Contains(out, "is box-owned") || e.req.Branch != "" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

// Ctrl-C must cancel the fetch's context, not kill the process before the
// bundle temp file is cleaned up.
func TestRunCloneCancelsOnInterrupt(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	// Swallow the signal in the test process either way, so the unfixed code
	// fails this test instead of killing the test binary.
	swallow := make(chan os.Signal, 1)
	signal.Notify(swallow, os.Interrupt)
	defer signal.Stop(swallow)
	cancelled := false
	cloneFetch = func(ctx context.Context, _ clone.Git, _ clone.Docker, _ clone.FetchRequest) (clone.FetchResult, error) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			cancelled = true
		case <-time.After(3 * time.Second):
		}
		return clone.FetchResult{}, ctx.Err()
	}
	rc, _ := runCloneArgs(e, "p", "b", "main")
	if !cancelled || rc != 1 {
		t.Fatalf("cancelled=%v rc=%d; SIGINT must cancel the fetch context", cancelled, rc)
	}
}

// A legacy profile still carrying the retired box.image loads with it ignored
// everywhere else; the clone CLI reads the raw store/config, so it must too.
func TestRunCloneIgnoresLegacyBoxImage(t *testing.T) {
	p := cloneProfile("old", "/src/r", "/clones/r")
	p.Box = config.BoxConfig{Image: "x:1"}
	e := newCloneEnv(t, []config.AgentConfig{p}, nil)
	rc, out := runCloneArgs(e, "old", "b", "main")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q, want a legacy box.image ignored", rc, out)
	}
}

// A profile stored before the CA env refusal existed still clones: the save
// path refuses, the stored one keeps working (its value is dropped at spawn).
func TestRunCloneIgnoresStoredCAEnv(t *testing.T) {
	p := cloneProfile("old", "/src/r", "/clones/r")
	p.Env = map[string]string{"NODE_EXTRA_CA_CERTS": "/x", "KEEP": "1"}
	e := newCloneEnv(t, []config.AgentConfig{p}, nil)
	rc, out := runCloneArgs(e, "old", "b", "main")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q, want a stored CA env name ignored", rc, out)
	}
	if p.Env["NODE_EXTRA_CA_CERTS"] != "/x" {
		t.Fatal("the shared env map was mutated")
	}
}

func TestWithoutCAEnvDropsTrustDirNamesToo(t *testing.T) {
	in := map[string]string{"SSL_CERT_DIR": "/x", "GIT_SSL_CAPATH": "/x", "SSL_CERT_FILE": "/x", "FOO": "1"}
	got := withoutCAEnv(in)
	if len(got) != 1 || got["FOO"] != "1" {
		t.Fatalf("withoutCAEnv = %v, want only FOO", got)
	}
	if len(in) != 4 {
		t.Fatal("the caller's map must not be mutated")
	}
}

func TestCloneFetchForgetsNotHome(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/feat"}
	st := clone.ReturnState{At: time.Now(), Base: "main", NotHome: []clone.NotHome{
		{Branch: "feat", Reason: clone.ReasonNotFF},
		{Branch: "x", Reason: clone.ReasonCheckedOut},
	}}
	if err := clone.WriteReturnState(e.data, "/clones/r", st); err != nil {
		t.Fatal(err)
	}
	rc, out := runCloneArgs(e, "p", "feat", "main")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	got, ok, err := clone.ReadReturnState(e.data, "/clones/r")
	if err != nil || !ok || len(got.NotHome) != 1 || got.NotHome[0].Branch != "x" {
		t.Fatalf("state after the fetch = %+v ok=%v err=%v; want only x", got, ok, err)
	}
	if strings.Contains(out, "WARN:") {
		t.Fatalf("a clean forget printed a warning: %q", out)
	}

	// A failed fetch leaves the entry alone.
	e.err = errors.New("nothing to fetch")
	if rc, _ := runCloneArgs(e, "p", "x", "main"); rc != 1 {
		t.Fatal("want the fetch to fail")
	}
	got, _, _ = clone.ReadReturnState(e.data, "/clones/r")
	if len(got.NotHome) != 1 {
		t.Fatalf("a failed fetch changed the state: %+v", got)
	}
}

// A state file that cannot be rewritten is a warning, never a failed fetch.
func TestCloneFetchForgetFailureIsOnlyAWarning(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/feat"}
	if err := clone.WriteReturnState(e.data, "/clones/r", clone.ReturnState{NotHome: []clone.NotHome{{Branch: "feat", Reason: clone.ReasonNotFF}}}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(e.data, "clone-return"))
	for _, en := range ents {
		if strings.HasSuffix(en.Name(), ".json") {
			os.WriteFile(filepath.Join(e.data, "clone-return", en.Name()), []byte("{broken"), 0o600)
		}
	}
	rc, out := runCloneArgs(e, "p", "feat", "main")
	if rc != 0 || !strings.Contains(out, "WARN: could not clear") {
		t.Fatalf("rc=%d out=%q; want success with a WARN line", rc, out)
	}
}
