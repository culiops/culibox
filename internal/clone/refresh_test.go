package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

func TestRefreshArgs(t *testing.T) {
	r := RefreshRequest{
		Clone:    config.Clone{Of: "/src/of", Dir: "/work/clone"},
		Image:    "culiops/agent-box:2026-10-18.2",
		Instance: "DEV",
	}
	s1, s2 := strings.Repeat("ab", 20), strings.Repeat("cd", 20)
	got := RefreshArgs(r, "main", []string{s1, s2}, "n")
	want := []string{
		"run", "--rm", "--init", "--name", "n", "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "1g",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--label", "io.culiops.agent-box.clonerefresh=DEV",
		"-e", "HOME=/tmp", "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "GIT_NO_REPLACE_OBJECTS=1",
		"-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory", "-e", "GIT_CONFIG_VALUE_0=/work/clone",
		"--mount", "type=bind,src=/work/clone,dst=/work/clone",
		"--mount", "type=bind,src=/src/of/.git,dst=/src/of/.git,readonly",
		"--entrypoint", "sh", "culiops/agent-box:2026-10-18.2",
		"-c", refreshScript, "agent-box-refresh", "/work/clone", "/src/of", "main", s1, s2,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RefreshArgs:\n got %q\nwant %q", got, want)
	}
	for _, a := range got {
		if strings.Contains(a, box.LabelInstance) {
			t.Errorf("argv carries the box label: %q", a)
		}
	}
}

func TestParseReportDropsJunk(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	in := strings.Join([]string{
		"nonsense=1",
		"head=-x",
		"head=a\x1bb",
		"head_sha=zz",
		"ahead=-1",
		"behind=1234567890",
		"ff=a..b",
		"ff_count=x",
		"stale=$(touch${IFS}pwned)",
		"stale=ok\x07",
		"no equals sign",
		"head=feat/x",
		"head_sha=" + a,
		"ahead=2",
		"behind=3",
		"ff=" + a + ".." + b,
		"ff_count=4",
		"stale=main",
		"stale=feat/x",
		"stale=main",
		"stale=feat/y", // trailing partial line: no newline after it
	}, "\n")
	got := parseReport([]byte(in))
	want := Report{Head: "feat/x", HeadSHA: a, Ahead: 2, Behind: 3, FFFrom: a, FFTo: b, FFCount: 4, Stale: []string{"main", "feat/x"}}
	if !reportEqual(got, want) {
		t.Fatalf("parseReport:\n got %+v\nwant %+v", got, want)
	}

	if r := parseReport([]byte("ff=refused\nff=bogus\n")); !r.FFRefused || r.FFFrom != "" {
		t.Errorf("ff=refused: %+v", r)
	}
	if r := parseReport([]byte("ff=none\nhead=\n")); r.FFRefused || r.FFFrom != "" || r.Head != "" {
		t.Errorf("ff=none: %+v", r)
	}

	var many bytes.Buffer
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&many, "stale=b%d\n", i)
	}
	if r := parseReport(many.Bytes()); len(r.Stale) != 50 || r.Stale[0] != "b0" || r.Stale[49] != "b49" {
		t.Errorf("stale is not capped at the first 50: %d %q", len(r.Stale), r.Stale)
	}
}

func reportEqual(a, b Report) bool {
	return slices.Equal(a.Stale, b.Stale) &&
		a.Head == b.Head && a.HeadSHA == b.HeadSHA && a.Ahead == b.Ahead && a.Behind == b.Behind &&
		a.FFFrom == b.FFFrom && a.FFTo == b.FFTo && a.FFCount == b.FFCount && a.FFRefused == b.FFRefused
}

// scriptDocker stands in for the refresh container: it checks the mounts, then
// runs the real refresh script on the host fixture with the env the argv sets.
type scriptDocker struct {
	t       *testing.T
	dir, of string
	cwd     string // the script's working directory (where a stray `touch` lands)
	home    string
	calls   int
	removed []string
	err     error // when set, returned after the script ran
	hang    bool
	stderr  string
	env     []string // added to the script's env (e.g. GIT_TEST_ASSUME_DIFFERENT_OWNER=1)
}

func (s *scriptDocker) Remove(_ context.Context, name string) error {
	s.removed = append(s.removed, name)
	return nil
}

func (s *scriptDocker) RunStream(ctx context.Context, args []string, stdout io.Writer) error {
	s.t.Helper()
	s.calls++
	var mounts, env []string
	for i, a := range args {
		switch a {
		case "--mount":
			mounts = append(mounts, args[i+1])
		case "-e":
			env = append(env, args[i+1])
		}
	}
	wantMounts := []string{
		"type=bind,src=" + s.dir + ",dst=" + s.dir,
		"type=bind,src=" + s.of + "/.git,dst=" + s.of + "/.git,readonly",
	}
	if !slices.Equal(mounts, wantMounts) {
		s.t.Errorf("mounts = %q, want %q", mounts, wantMounts)
	}
	if s.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	i := slices.Index(args, "--entrypoint")
	if i < 0 || args[i+1] != "sh" {
		s.t.Fatalf("no sh entrypoint in %q", args)
	}
	cmd := exec.CommandContext(ctx, "sh", args[i+3:]...)
	cmd.Env = append(append(append(CleanEnv(), env...), "HOME="+s.home), s.env...)
	cmd.Dir = s.cwd
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &errb
	runErr := cmd.Run()
	s.stderr = errb.String()
	if s.err != nil {
		return s.err
	}
	if runErr != nil {
		return fmt.Errorf("docker run: %w: %s", runErr, strings.TrimSpace(errb.String()))
	}
	return nil
}

type refreshFx struct {
	t    *testing.T
	of   string
	req  RefreshRequest
	rec  *recGit
	dock *scriptDocker
}

// newRefreshFx builds a host repo on main and a clone of it (on main), with a
// base record started at the host tip, as clone creation leaves it.
func newRefreshFx(t *testing.T) *refreshFx {
	t.Helper()
	rec := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "clone")}
	if _, err := EnsureClone(context.Background(), rec, c, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if err := ResetBaseRecord(data, c.Dir, ofRev(t, of, "main")); err != nil {
		t.Fatal(err)
	}
	x := &refreshFx{t: t, of: of, rec: rec}
	x.dock = &scriptDocker{t: t, dir: c.Dir, of: of, cwd: t.TempDir(), home: t.TempDir()}
	x.req = RefreshRequest{Clone: c, Image: "img:1", Instance: "T", DataDir: data}
	rec.reset()
	return x
}

func (x *refreshFx) refresh() RefreshResult {
	x.t.Helper()
	return Refresh(context.Background(), x.rec, x.dock, x.req)
}

func (x *refreshFx) inClone(args ...string) { x.t.Helper(); fixtureGit(x.t, x.req.Clone.Dir, args...) }

func (x *refreshFx) cloneRev(rev string) string {
	x.t.Helper()
	return ofRev(x.t, x.req.Clone.Dir, rev)
}

func (x *refreshFx) record() BaseRecord {
	x.t.Helper()
	rec, err := ReadBaseRecord(x.req.DataDir, x.req.Clone.Dir)
	if err != nil {
		x.t.Fatal(err)
	}
	return rec
}

func TestRefreshBehindOnMainFastForwards(t *testing.T) {
	x := newRefreshFx(t)
	old := ofRev(t, x.of, "main")
	commitIn(t, x.of, "c")
	tip := ofRev(t, x.of, "main")

	res := x.refresh()
	if !res.Refreshed || res.Error != "" {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	r := res.Report
	if r.FFFrom != old || r.FFTo != tip || r.FFCount != 1 || r.FFRefused {
		t.Errorf("ff = %+v, want %s..%s (+1)", r, old, tip)
	}
	if r.Ahead != 0 || r.Behind != 0 || r.Head != "main" || r.HeadSHA != tip {
		t.Errorf("report = %+v", r)
	}
	if res.Base != "main" || res.Branch != "main" || res.Rewritten || res.Unverified {
		t.Errorf("res = %+v", res)
	}
	if res.HeadInHost == nil || !*res.HeadInHost {
		t.Errorf("HeadInHost = %v, want true", res.HeadInHost)
	}
	if _, err := os.Stat(filepath.Join(x.req.Clone.Dir, "c")); err != nil {
		t.Errorf("the fast-forward did not reach the work tree: %v", err)
	}
	if got := x.record().SHAs; !slices.Equal(got, []string{old, tip}) {
		t.Errorf("record = %q, want the new tip appended", got)
	}
}

func TestRefreshFeatureBranchMovesMainRef(t *testing.T) {
	x := newRefreshFx(t)
	x.inClone("checkout", "-q", "-b", "feat")
	commitIn(t, x.of, "c")
	tip := ofRev(t, x.of, "main")

	res := x.refresh()
	if !res.Refreshed {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if got := x.cloneRev("refs/heads/main"); got != tip {
		t.Errorf("clone main = %s, want host tip %s", got, tip)
	}
	if res.Report.Head != "feat" || res.Branch != "feat" || res.Report.Behind != 1 || res.Report.Ahead != 0 {
		t.Errorf("res = %+v", res)
	}
	if res.Report.FFTo != tip || res.Report.FFCount != 1 {
		t.Errorf("ff = %+v", res.Report)
	}
	if _, err := os.Stat(filepath.Join(x.req.Clone.Dir, "c")); err == nil {
		t.Error("the checked-out feat work tree changed")
	}
}

func TestRefreshDivergedMainRefused(t *testing.T) {
	x := newRefreshFx(t)
	commitIn(t, x.req.Clone.Dir, "local")
	local := x.cloneRev("main")
	commitIn(t, x.of, "c")

	res := x.refresh()
	if !res.Refreshed || !res.Report.FFRefused || res.Report.FFFrom != "" {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if got := x.cloneRev("main"); got != local {
		t.Errorf("local main moved: %s, want %s", got, local)
	}
	if res.Report.Ahead != 1 || res.Report.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 1/1", res.Report.Ahead, res.Report.Behind)
	}
	if res.HeadInHost == nil || *res.HeadInHost {
		t.Errorf("HeadInHost = %v, want false (the local commit was never sent)", res.HeadInHost)
	}
}

func TestRefreshDirtyTreeKept(t *testing.T) {
	x := newRefreshFx(t)
	dirty := filepath.Join(x.req.Clone.Dir, "a")
	if err := os.WriteFile(dirty, []byte("work in progress"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitIn(t, x.of, "c")
	tip := ofRev(t, x.of, "main")

	res := x.refresh()
	if !res.Refreshed || res.Report.FFTo != tip {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if got, _ := os.ReadFile(dirty); string(got) != "work in progress" {
		t.Errorf("uncommitted change lost: %q", got)
	}
}

// rewriteHost resets host main to its root commit and adds a new one.
func rewriteHost(t *testing.T, of string) {
	t.Helper()
	fixtureGit(t, of, "reset", "-q", "--hard", ofRev(t, of, "main~1"))
	commitIn(t, of, "rewritten")
}

func TestRefreshRewrittenReportsStale(t *testing.T) {
	x := newRefreshFx(t)
	old := ofRev(t, x.of, "main")
	x.inClone("checkout", "-q", "-b", "feat")
	commitIn(t, x.req.Clone.Dir, "f")
	rewriteHost(t, x.of)

	res := x.refresh()
	if !res.Rewritten || res.Error != "" {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	slices.Sort(res.Report.Stale)
	if !slices.Equal(res.Report.Stale, []string{"feat", "main"}) {
		t.Errorf("stale = %q, want [feat main]", res.Report.Stale)
	}
	if !res.Report.FFRefused || x.cloneRev("main") != old {
		t.Errorf("local main must not follow a rewrite: %+v", res.Report)
	}
}

func TestRefreshUnverifiedWithoutRecord(t *testing.T) {
	x := newRefreshFx(t)
	x.req.DataDir = t.TempDir() // no record here
	res := x.refresh()
	if !res.Unverified || !res.Refreshed {
		t.Fatalf("res = %+v", res)
	}
	p, err := baseRecordPath(x.req.DataDir, x.req.Clone.Dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "unverified\n" + ofRev(t, x.of, "main") + "\n"; string(raw) != want {
		t.Errorf("record = %q, want %q", raw, want)
	}
}

func TestRefreshSkipRunsNoContainer(t *testing.T) {
	for _, skip := range []string{SkipInUse, SkipCreated, SkipOtherInstance} {
		t.Run(skip, func(t *testing.T) {
			x := newRefreshFx(t)
			old := ofRev(t, x.of, "main")
			commitIn(t, x.of, "c")
			x.req.Skip = skip
			res := x.refresh()
			if x.dock.calls != 0 {
				t.Errorf("docker ran %d times", x.dock.calls)
			}
			if res.Skipped != skip || res.Refreshed || res.Base != "main" {
				t.Errorf("res = %+v", res)
			}
			if res.HeadInHost == nil || !*res.HeadInHost || res.Branch != "main" {
				t.Errorf("host checks did not run: %+v", res)
			}
			if got := x.record().SHAs; !slices.Equal(got, []string{old}) {
				t.Errorf("record = %q, want it untouched", got)
			}
		})
	}
}

func TestRefreshSkipStillReportsRewrite(t *testing.T) {
	x := newRefreshFx(t)
	rewriteHost(t, x.of)
	x.req.Skip = SkipInUse
	if res := x.refresh(); !res.Rewritten || x.dock.calls != 0 {
		t.Errorf("res = %+v, calls %d", res, x.dock.calls)
	}
}

func TestRefreshNoBase(t *testing.T) {
	x := newRefreshFx(t)
	fixtureGit(t, x.of, "branch", "-M", "trunk")
	res := x.refresh()
	if res.Skipped != SkipNoBase || x.dock.calls != 0 || res.Base != "" || res.Unverified || res.Rewritten {
		t.Errorf("res = %+v, calls %d", res, x.dock.calls)
	}
}

func TestRefreshFailureIsReported(t *testing.T) {
	x := newRefreshFx(t)
	x.dock.err = errors.New("docker run: exit status 128: \x1b[31mfatal: boom")
	res := x.refresh()
	if res.Refreshed || strings.ContainsRune(res.Error, 0x1b) ||
		!strings.HasPrefix(res.Error, "fetching host main in the box failed: ") || !strings.Contains(res.Error, "fatal: boom") {
		t.Errorf("res = %+v", res)
	}
	if len(x.dock.removed) != 1 {
		t.Errorf("removed = %q, want the container removed", x.dock.removed)
	}
	if res.HeadInHost == nil {
		t.Error("host checks did not run after a failure")
	}
}

func TestRefreshTimeoutIsReported(t *testing.T) {
	x := newRefreshFx(t)
	x.dock.hang = true
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := Refresh(ctx, x.rec, x.dock, x.req)
	if res.Refreshed || res.Error != "timed out" {
		t.Errorf("res = %+v", res)
	}
	if len(x.dock.removed) != 1 || !strings.HasPrefix(x.dock.removed[0], "agent-box-clonerefresh-T-") {
		t.Errorf("removed = %q", x.dock.removed)
	}
	// The host check after the container must not run on the expired context.
	if res.HeadInHost == nil || !*res.HeadInHost {
		t.Errorf("HeadInHost = %v after a timeout, want it computed (true)", res.HeadInHost)
	}
}

func TestRefreshCancelledIsNotTimedOut(t *testing.T) {
	x := newRefreshFx(t)
	x.dock.hang = true
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := Refresh(ctx, x.rec, x.dock, x.req)
	if res.Refreshed || res.Error != "cancelled" {
		t.Errorf("res = %+v, want Error cancelled", res)
	}
	if res.HeadInHost == nil {
		t.Error("host checks did not run after a cancel")
	}
}

func TestRefreshFetchFailureExitsNonZero(t *testing.T) {
	x := newRefreshFx(t)
	// A clone git cannot open: the in-box fetch fails, and so must the script.
	if err := os.WriteFile(filepath.Join(x.req.Clone.Dir, ".git", "HEAD"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := x.refresh()
	if res.Refreshed || !strings.HasPrefix(res.Error, "fetching host main in the box failed: ") || res.Report.HeadSHA != "" {
		t.Errorf("res = %+v", res)
	}
	if res.HeadInHost != nil || res.Branch != "" {
		t.Errorf("an unreadable HEAD must be unknown: %+v", res)
	}
}

func TestRefreshNeverRunsHostGitInTheClone(t *testing.T) {
	x := newRefreshFx(t)
	x.inClone("checkout", "-q", "-b", "feat")
	rewriteHost(t, x.of)
	if res := x.refresh(); !res.Refreshed || !res.Rewritten || res.HeadInHost == nil {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if len(x.rec.calls) == 0 {
		t.Fatal("no host git calls recorded")
	}
	for _, c := range x.rec.calls {
		if c.dir != x.of {
			t.Errorf("git ran in %q, want only %q: %q", c.dir, x.of, c.args)
		}
		if !slices.Equal(c.env, CleanEnv()) {
			t.Errorf("env = %q, want CleanEnv", c.env)
		}
		if !slices.Equal(c.args[:len(hardenFlags)], hardenFlags) {
			t.Errorf("args %q do not start with the hardening flags", c.args)
		}
		for _, a := range c.args {
			if strings.Contains(a, x.req.Clone.Dir) {
				t.Errorf("arg %q names the clone", a)
			}
		}
	}
}

// Branch names are box-written: the script must quote them, and the host must
// drop the ones ValidBranch refuses.
func TestRefreshHostileBranchNames(t *testing.T) {
	x := newRefreshFx(t)
	hostile := []string{"$(touch${IFS}pwned)", "`touch${IFS}pwned2`", "a;touch${IFS}pwned3", "-dash"}
	for _, n := range hostile {
		x.inClone("update-ref", "refs/heads/"+n, "HEAD")
	}
	x.inClone("symbolic-ref", "HEAD", "refs/heads/"+hostile[0])
	rewriteHost(t, x.of)

	res := x.refresh()
	if !res.Refreshed || !res.Rewritten {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if !slices.Equal(res.Report.Stale, []string{"main"}) {
		t.Errorf("stale = %q, want only [main]", res.Report.Stale)
	}
	if res.Report.Head != "" || res.Branch != "" || res.Report.HeadSHA == "" {
		t.Errorf("hostile HEAD branch was not dropped: %+v", res)
	}
	for _, d := range []string{x.dock.cwd, x.dock.home, x.req.Clone.Dir, x.of} {
		for _, f := range []string{"pwned", "pwned2", "pwned3"} {
			if _, err := os.Stat(filepath.Join(d, f)); err == nil {
				t.Errorf("%s/%s exists: a branch name was executed", d, f)
			}
		}
	}
}

// A box-written remote or hook must not change what the refresh fetches or run
// anything: the host repo is named by path and hooks are off.
func TestRefreshIgnoresBoxRemoteAndHooks(t *testing.T) {
	x := newRefreshFx(t)
	marker := filepath.Join(t.TempDir(), "ran")
	x.inClone("config", "remote.origin.url", filepath.Join(t.TempDir(), "elsewhere"))
	x.inClone("config", "remote.origin.uploadpack", "touch "+marker+"; git-upload-pack")
	hooks := filepath.Join(x.req.Clone.Dir, ".git", "hooks")
	mustMkdir(t, hooks)
	x.inClone("config", "core.hooksPath", hooks) // the clone is made with /dev/null; a box can change it
	for _, h := range []string{"reference-transaction", "post-merge", "post-checkout"} {
		if err := os.WriteFile(filepath.Join(hooks, h), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commitIn(t, x.of, "c")
	tip := ofRev(t, x.of, "main")

	res := x.refresh()
	if !res.Refreshed || res.Report.FFTo != tip {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a box-written hook or uploadpack ran")
	}
}

// The record must hold every base the clone can get, so a base that cannot be
// recorded is not handed to the clone.
func TestRefreshUnrecordableBaseRunsNoContainer(t *testing.T) {
	x := newRefreshFx(t)
	p, err := baseRecordPath(x.req.DataDir, x.req.Clone.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not a sha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := x.refresh()
	if x.dock.calls != 0 || res.Refreshed || !strings.HasPrefix(res.Error, "could not record host main: ") || !res.Unverified {
		t.Errorf("res = %+v, calls %d", res, x.dock.calls)
	}
}
