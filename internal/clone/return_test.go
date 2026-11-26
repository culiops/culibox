package clone

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
)

func TestListArgsRunsStatus(t *testing.T) {
	r := ReturnRequest{
		Clone:    config.Clone{Of: "/src/of", Dir: "/work/clone"},
		Image:    "culiops/agent-box:2026-10-18.2",
		Instance: "DEV",
	}
	got := ListArgs(r, "n")
	want := []string{
		"run", "--rm", "--init", "--name", "n", "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "1g",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--label", "io.culiops.agent-box.clonelist=DEV",
		"-e", "HOME=/tmp", "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "GIT_OPTIONAL_LOCKS=0",
		"-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory", "-e", "GIT_CONFIG_VALUE_0=/work/clone",
		"--mount", "type=bind,src=/work/clone,dst=/work/clone,readonly",
		"--entrypoint", "sh", "culiops/agent-box:2026-10-18.2",
		"-c", listScript, "sh", "/work/clone",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListArgs:\n got %q\nwant %q", got, want)
	}
	// The clone path is the positional $1, never part of the script text.
	if strings.Contains(listScript, "/work/clone") || !strings.Contains(listScript, `"$1"`) {
		t.Errorf("the script must take the clone dir as $1:\n%s", listScript)
	}
	// The one mount is the clone, read-only; clone_of is not mounted at all, and
	// the container carries no box label.
	mounts := 0
	for i, a := range got {
		if a == "--mount" {
			mounts++
			if !strings.HasSuffix(got[i+1], ",readonly") {
				t.Errorf("mount %q is not read-only", got[i+1])
			}
		}
		if strings.Contains(a, "/src/of") || strings.Contains(a, "io.culiops.agent-box.instance") {
			t.Errorf("argv names clone_of or carries the box label: %q", a)
		}
	}
	if mounts != 1 {
		t.Errorf("%d mounts, want only the clone", mounts)
	}
	// The status is bounded and every untracked file counts; for-each-ref stays fatal.
	for _, w := range []string{"timeout 10 git ", "--untracked-files=all", "for-each-ref"} {
		if !strings.Contains(listScript, w) {
			t.Errorf("listScript lacks %q:\n%s", w, listScript)
		}
	}
	if strings.Contains(listScript, "--untracked-files=normal") {
		t.Errorf("listScript counts untracked directories as one entry:\n%s", listScript)
	}
	// A clone path with shell metacharacters stays one argv element.
	r.Clone.Dir = "/work/a b;$(touch x)"
	got = ListArgs(r, "n")
	if n := len(got); got[n-1] != r.Clone.Dir || got[n-2] != "sh" || got[n-3] != listScript {
		t.Errorf("dir is not the last, separate argv element: %q", got[n-4:])
	}
}

func intp(n int) *int { return &n }

func TestParseListStatus(t *testing.T) {
	sha := strings.Repeat("a", 40)
	refs := sha + " refs/heads/main\n" + sha + " refs/heads/feat\n"
	cases := []struct {
		name    string
		text    string
		entries int
		cut     bool
		unc     *int
		uncCut  bool
	}{
		{"two refs and three files", refs + "#culibox-status\n M a.go\n?? b.go\nA  c.go\n", 2, false, intp(3), false},
		{"clean tree", refs + "#culibox-status\n", 2, false, intp(0), false},
		{"blank lines do not count", refs + "#culibox-status\n\n M a.go\n\n", 2, false, intp(1), false},
		{"no separator", refs, 2, false, nil, false},
		{"torn before the separator", refs + "#culibox-sta", 2, true, nil, false},
		{"status torn at the cap", refs + "#culibox-status\n M a.go\n?? b.g", 2, false, intp(1), true},
		{"more refs than the cap still reach the status", func() string {
			var b strings.Builder
			for i := 0; i < maxListBranches+5; i++ {
				fmt.Fprintf(&b, "%s refs/heads/b%03d\n", sha, i)
			}
			return b.String() + "#culibox-status\n M a.go\n"
		}(), maxListBranches, true, intp(1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var res ReturnResult
			entries, cut := parseList(c.text, &res)
			if len(entries) != c.entries || cut != c.cut {
				t.Errorf("%d entries, cut=%v; want %d, %v", len(entries), cut, c.entries, c.cut)
			}
			if (res.Uncommitted == nil) != (c.unc == nil) || (c.unc != nil && *res.Uncommitted != *c.unc) {
				t.Errorf("Uncommitted = %v, want %v", res.Uncommitted, c.unc)
			}
			if res.UncommittedCut != c.uncCut {
				t.Errorf("UncommittedCut = %v, want %v", res.UncommittedCut, c.uncCut)
			}
		})
	}
}

// returnFx is a fetchFx whose clone has, besides main (equal to the host's),
// the branches feat and main both ahead of it, and old, which is host main.
func newReturnFx(t *testing.T) *fetchFx {
	t.Helper()
	x := newFetchFx(t) // clone on feat, one commit
	x.inClone("branch", "old", "main")
	x.inClone("checkout", "-q", "main")
	x.commitInClone("main.txt", "m", "add main")
	x.rec.reset()
	return x
}

func (x *fetchFx) retReq(mode ReturnMode) ReturnRequest {
	r := x.req
	return ReturnRequest{Clone: r.Clone, Mode: mode, Image: r.Image, Instance: r.Instance, TempDir: r.TempDir,
		Home: r.Home, DataDir: r.DataDir}
}

func (x *fetchFx) ret(mode ReturnMode) ReturnResult {
	x.t.Helper()
	return Return(context.Background(), x.rec, x.dock, x.retReq(mode))
}

func branchesOf(nh []NotHome) []string {
	var b []string
	for _, n := range nh {
		b = append(b, n.Branch+":"+n.Reason)
	}
	return b
}

func (x *fetchFx) boxRefs() string {
	x.t.Helper()
	return gitIn(x.t, x.of, "", "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/box/")
}

func TestReturnBringsEveryUnhomedBranchIncludingBase(t *testing.T) {
	x := newReturnFx(t)
	res := x.ret(ReturnAuto)
	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	if want := []string{"box/feat", "box/main"}; !slices.Equal(res.Home, want) {
		t.Fatalf("Home = %q, want %q", res.Home, want)
	}
	if len(res.NotHome) != 0 || res.Base != "main" || res.At.IsZero() {
		t.Errorf("result = %+v", res)
	}
	for _, b := range []string{"feat", "main"} {
		want := strings.TrimSpace(gitIn(t, x.req.Clone.Dir, "", "rev-parse", "refs/heads/"+b))
		if got, err := x.ofRev("refs/heads/box/" + b); err != nil || got != want {
			t.Errorf("box/%s = %q, %v; want the clone's %s", b, got, err, want)
		}
	}
	// old equals host main: already home, so neither brought nor reported.
	if _, err := x.ofRev("refs/heads/box/old"); err == nil {
		t.Error("box/old exists; old is host main and was already home")
	}
	// The host's own main is untouched by the base branch coming home.
	if got, _ := x.ofRev("refs/heads/main"); got == strings.TrimSpace(gitIn(t, x.req.Clone.Dir, "", "rev-parse", "refs/heads/main")) {
		t.Error("host main moved to the clone's main; only box/main may")
	}
	if x.dock.calls != 3 { // the list, then one bundle per branch
		t.Errorf("%d containers, want 3", x.dock.calls)
	}
	if st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir); err != nil || !ok || len(st.NotHome) != 0 || st.Error != "" || !st.At.Equal(res.At) {
		t.Errorf("state = %+v, %v, %v; want an empty one at %v", st, ok, err, res.At)
	}
	x.tempEmpty()
	if q := x.quarantineRefs(); q != "" {
		t.Errorf("quarantine refs left: %q", q)
	}
}

func TestReturnSkipsBranchesAlreadyHome(t *testing.T) {
	x := newReturnFx(t)
	if res := x.ret(ReturnAuto); len(res.Home) != 2 {
		t.Fatalf("first run: %+v", res)
	}
	before := x.boxRefs()
	x.dock.calls = 0
	res := x.ret(ReturnAuto)
	if len(res.Home) != 0 || len(res.NotHome) != 0 || res.Error != "" {
		t.Errorf("second run = %+v, want nothing to do", res)
	}
	if x.dock.calls != 1 {
		t.Errorf("%d containers, want only the list", x.dock.calls)
	}
	if x.boxRefs() != before {
		t.Error("a box ref moved on the second run")
	}
}

func TestReturnDryRecordsOffWithoutFetching(t *testing.T) {
	x := newReturnFx(t)
	res := x.ret(ReturnDry)
	if want := []string{"feat:off", "main:off"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	if len(res.Home) != 0 || res.Error != "" {
		t.Errorf("result = %+v", res)
	}
	if x.dock.calls != 1 {
		t.Errorf("%d containers, want only the list", x.dock.calls)
	}
	if got := x.boxRefs(); got != "" {
		t.Errorf("box refs exist after a dry run: %q", got)
	}
	st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || !ok || !slices.Equal(branchesOf(st.NotHome), branchesOf(res.NotHome)) {
		t.Errorf("state = %+v, %v, %v", st, ok, err)
	}
}

func TestReturnRecordsRefusalsAndWritesState(t *testing.T) {
	x := newReturnFx(t)
	// The host's box/feat has a commit the clone's feat lacks.
	racer := strings.TrimSpace(gitIn(t, x.of, "", "commit-tree", "-m", "racer", "main^{tree}"))
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/feat", racer)

	res := x.ret(ReturnAuto)
	if want := []string{"box/main"}; !slices.Equal(res.Home, want) {
		t.Errorf("Home = %q, want %q", res.Home, want)
	}
	if want := []string{"feat:not-ff"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Fatalf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	if d := res.NotHome[0].Detail; !strings.Contains(d, "box/feat on the host has commits feat lacks") {
		t.Errorf("Detail = %q", d)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != racer {
		t.Errorf("box/feat = %s, want it unchanged at %s", got, racer)
	}
	st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || !ok || !slices.Equal(st.NotHome, res.NotHome) || st.Base != "main" {
		t.Errorf("state = %+v, %v, %v; want NotHome %+v", st, ok, err, res.NotHome)
	}

	// A branch checked out in a worktree of clone_of is recorded as such.
	fixtureGit(t, x.of, "checkout", "-q", "box/main")
	x.inClone("commit", "-q", "--allow-empty", "-m", "more")
	res = x.ret(ReturnAuto)
	if want := []string{"feat:not-ff", "main:checked-out"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
}

func TestReturnForcedModeOverwritesADivergedBoxRef(t *testing.T) {
	x := newReturnFx(t)
	racer := strings.TrimSpace(gitIn(t, x.of, "", "commit-tree", "-m", "racer", "main^{tree}"))
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/feat", racer)
	res := x.ret(ReturnForced)
	if want := []string{"box/feat", "box/main"}; !slices.Equal(res.Home, want) || len(res.NotHome) != 0 {
		t.Errorf("result = %+v", res)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got == racer {
		t.Error("box/feat still the host's; the forced mode should take the clone's")
	}
}

func TestReturnForcedModeRefusesACheckedOutBoxRef(t *testing.T) {
	x := newReturnFx(t)
	x.ret(ReturnAuto) // box/main and box/feat exist
	fixtureGit(t, x.of, "checkout", "-q", "box/main")
	before, _ := x.ofRev("refs/heads/box/main")
	x.inClone("commit", "-q", "--allow-empty", "-m", "more")
	res := x.ret(ReturnForced)
	if len(res.Home) != 0 { // box/feat has nothing new
		t.Errorf("Home = %q, want none", res.Home)
	}
	if want := []string{"main:checked-out"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	if got, _ := x.ofRev("refs/heads/box/main"); got != before {
		t.Errorf("box/main = %s, want it unchanged at %s", got, before)
	}
}

func refsOutsideBox(t *testing.T, of string) string {
	t.Helper()
	var keep []string
	for _, l := range strings.Split(gitIn(t, of, "", "for-each-ref", "--format=%(refname) %(objectname)", "refs/"), "\n") {
		if !strings.HasPrefix(l, "refs/heads/box/") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n") + "\n" + gitIn(t, of, "", "rev-parse", "HEAD") + "\n" + gitIn(t, of, "", "status", "--porcelain")
}

func TestReturnNeverWritesOutsideBoxRefs(t *testing.T) {
	x := newReturnFx(t)
	x.inClone("branch", "-m", "feat", "feat/x")
	x.inClone("checkout", "-q", "-b", "box/y", "main")
	x.commitInClone("y.txt", "y", "add y")
	before := refsOutsideBox(t, x.of)
	res := x.ret(ReturnAuto)
	if want := []string{"box/box/y", "box/feat/x", "box/main"}; !slices.Equal(res.Home, want) {
		t.Fatalf("Home = %q (%+v), want %q", res.Home, res, want)
	}
	if after := refsOutsideBox(t, x.of); after != before {
		t.Errorf("refs outside refs/heads/box/ changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// listDocker answers the listing container with fixed text (or an error) and
// hands every other container to the real fake.
type listDocker struct {
	*fakeDocker
	out   string
	lists int
}

func (l *listDocker) RunStream(ctx context.Context, args []string, stdout io.Writer) error {
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, LabelCloneList+"=") }) {
		l.lists++
		_, err := io.WriteString(stdout, l.out)
		return err
	}
	return l.fakeDocker.RunStream(ctx, args, stdout)
}

func TestReturnRejectsBadNamesFromTheListing(t *testing.T) {
	x := newReturnFx(t)
	sha := strings.Repeat("a", 40)
	long := sha + " refs/heads/" + strings.Repeat("x", 300-len(sha)-len(" refs/heads/"))
	if len(long) != 300 {
		t.Fatalf("long line is %d bytes", len(long))
	}
	ld := &listDocker{fakeDocker: x.dock, out: strings.Join([]string{
		sha + " refs/heads/-x",
		sha + " refs/heads/a..b\x1b]0;t\x07",
		"junk",
		long,
		sha + " refs/tags/v1",
	}, "\n") + "\n"}
	res := Return(context.Background(), x.rec, ld, x.retReq(ReturnAuto))
	if want := []string{"-x:name", "a..b?]0;t?:name"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	if x.dock.calls != 0 || ld.lists != 1 || len(res.Home) != 0 {
		t.Errorf("%d bundle containers, %d lists, Home %q; want no fetch for a bad name", x.dock.calls, ld.lists, res.Home)
	}
	if got := x.boxRefs(); got != "" {
		t.Errorf("box refs: %q", got)
	}
}

func TestReturnReadsOnlyCompleteLinesAndCapsTheBranchCount(t *testing.T) {
	x := newReturnFx(t)
	var b strings.Builder
	for i := 0; i < maxListBranches+5; i++ {
		fmt.Fprintf(&b, "%s refs/heads/-b%03d\n", strings.Repeat("a", 40), i)
	}
	b.WriteString(strings.Repeat("a", 40) + " refs/heads/-torn") // no newline: cut off
	ld := &listDocker{fakeDocker: x.dock, out: b.String()}
	res := Return(context.Background(), x.rec, ld, x.retReq(ReturnAuto))
	if len(res.NotHome) != maxListBranches {
		t.Errorf("%d branches recorded, want the first %d", len(res.NotHome), maxListBranches)
	}
	for _, n := range res.NotHome {
		if n.Branch == "-torn" {
			t.Error("a line with no newline was read")
		}
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "not looked at") {
		t.Errorf("Warnings = %q, want one saying some branches were not looked at", res.Warnings)
	}
}

// manyBranches deletes feat and gives the clone n branches b01.. each one
// commit ahead of main (which is host main, so main itself is home).
func manyBranches(x *fetchFx, n int) {
	x.inClone("checkout", "-q", "main")
	x.inClone("branch", "-D", "feat")
	for i := 1; i <= n; i++ {
		b := fmt.Sprintf("b%02d", i)
		x.inClone("checkout", "-q", "-b", b, "main")
		x.commitInClone(b+".txt", b, "add "+b)
	}
	x.inClone("checkout", "-q", "main")
	x.rec.reset()
}

func TestReturnCapsAttempts(t *testing.T) {
	x := newFetchFx(t)
	manyBranches(x, 25)
	// b01..b03 were refused before; they go last, so they are the ones deferred.
	err := WriteReturnState(x.req.DataDir, x.req.Clone.Dir, ReturnState{At: time.Now(), NotHome: []NotHome{
		{Branch: "b01", Reason: ReasonNotFF}, {Branch: "b02", Reason: ReasonRefused}, {Branch: "b03", Reason: ReasonCheckedOut},
		{Branch: "b04", Reason: ReasonFailed},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res := x.ret(ReturnAuto)
	if x.dock.calls != 1+maxReturnAttempts {
		t.Errorf("%d containers, want the list and %d fetches", x.dock.calls, maxReturnAttempts)
	}
	if len(res.Home) != maxReturnAttempts {
		t.Errorf("%d branches home, want %d", len(res.Home), maxReturnAttempts)
	}
	want := []string{"b01:deferred", "b02:deferred", "b03:deferred", "b24:deferred", "b25:deferred"}
	if !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	for _, b := range []string{"box/b04", "box/b23"} {
		if !slices.Contains(res.Home, b) {
			t.Errorf("%s was not brought (a failed branch is retried before the refused ones)", b)
		}
	}
	// The next run brings the five.
	x.dock.calls = 0
	res = x.ret(ReturnAuto)
	if len(res.Home) != 5 || len(res.NotHome) != 0 {
		t.Errorf("second run = %+v, want the 5 deferred brought", res)
	}
}

// cancelGit cancels the run's context right after a host git call that
// matches, like a deadline that falls between two branches.
type cancelGit struct {
	*recGit
	match  func(args []string) bool
	cancel context.CancelFunc
}

func (c *cancelGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	out, err := c.recGit.Run(ctx, dir, env, args...)
	if c.match(args) {
		c.cancel()
	}
	return out, err
}

func (c *cancelGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	return c.recGit.RunStdin(ctx, dir, env, stdin, args...)
}

func TestReturnDeadlineDefersTheRest(t *testing.T) {
	x := newReturnFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The commit listing is Fetch's last call for a branch without shares.
	g := &cancelGit{recGit: x.rec, cancel: cancel, match: func(a []string) bool { return slices.Contains(a, "--no-show-signature") }}
	res := Return(ctx, g, x.dock, x.retReq(ReturnAuto))
	if want := []string{"box/feat"}; !slices.Equal(res.Home, want) {
		t.Errorf("Home = %q, want %q", res.Home, want)
	}
	if want := []string{"main:deferred"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	if x.dock.calls != 2 {
		t.Errorf("%d containers, want the list and one fetch", x.dock.calls)
	}
	st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || !ok || !slices.Equal(st.NotHome, res.NotHome) {
		t.Errorf("state = %+v, %v, %v; it must be written after the deadline", st, ok, err)
	}
	x.tempEmpty()
}

func TestReturnListFailureKeepsPreviousState(t *testing.T) {
	x := newReturnFx(t)
	prev := []NotHome{{Branch: "old-work", Reason: ReasonNotFF, Detail: "kept"}}
	if err := WriteReturnState(x.req.DataDir, x.req.Clone.Dir, ReturnState{Base: "main", NotHome: prev}); err != nil {
		t.Fatal(err)
	}
	x.dock.stderr = "fatal: boom\x1b[2J"
	res := x.ret(ReturnAuto)
	if res.Error == "" || strings.Contains(res.Error, "\x1b") || !strings.Contains(res.Error, "boom") {
		t.Errorf("Error = %q, want the sanitized failure", res.Error)
	}
	if !slices.Equal(res.NotHome, prev) || len(res.Home) != 0 {
		t.Errorf("result = %+v, want the previous NotHome and nothing home", res)
	}
	if x.dock.calls != 1 || len(x.dock.removed) != 1 {
		t.Errorf("%d containers, %d removed; want the failed list container removed", x.dock.calls, len(x.dock.removed))
	}
	st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || !ok || st.Error == "" || !slices.Equal(st.NotHome, prev) || !st.At.Equal(res.At) {
		t.Errorf("state = %+v, %v, %v; want Error set and NotHome kept", st, ok, err)
	}
	// A later good run clears the error.
	x.dock.stderr = ""
	if res = x.ret(ReturnAuto); res.Error != "" || len(res.NotHome) != 0 {
		t.Fatalf("good run = %+v", res)
	}
	if st, _, _ := ReadReturnState(x.req.DataDir, x.req.Clone.Dir); st.Error != "" || len(st.NotHome) != 0 {
		t.Errorf("state after a good run = %+v", st)
	}
}

func TestReturnRefusesUnusablePaths(t *testing.T) {
	x := newReturnFx(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(x.req.Clone.Dir, link); err != nil {
		t.Fatal(err)
	}
	r := x.retReq(ReturnAuto)
	r.Clone.Dir = link
	res := Return(context.Background(), x.rec, x.dock, r)
	if res.Error == "" || !strings.Contains(res.Error, "symlink") || x.dock.calls != 0 {
		t.Errorf("Error = %q, %d containers; want a refusal before any container", res.Error, x.dock.calls)
	}
	r = x.retReq(ReturnAuto)
	r.Image = "-v"
	if res = Return(context.Background(), x.rec, x.dock, r); res.Error == "" || x.dock.calls != 0 {
		t.Errorf("Error = %q, %d containers; want a bad image refused", res.Error, x.dock.calls)
	}
}

func TestReturnWithoutADataDirRecordsRefusals(t *testing.T) {
	x := newReturnFx(t)
	r := x.retReq(ReturnAuto)
	r.DataDir = ""
	res := Return(context.Background(), x.rec, x.dock, r)
	// Fetch's history check cannot be done without a base record: refused, and recorded.
	if want := []string{"feat:refused", "main:refused"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Errorf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
}

func TestReturnNeverRunsHostGitInTheClone(t *testing.T) {
	x := newReturnFx(t)
	if res := x.ret(ReturnAuto); len(res.Home) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if len(x.rec.calls) == 0 {
		t.Fatal("no host git call was recorded")
	}
	clean := CleanEnv()
	for _, c := range x.rec.calls {
		if c.dir != x.of {
			t.Errorf("git %v ran in %q, want only %q", c.args, c.dir, x.of)
		}
		if !slices.Equal(c.env, clean) && !slices.Equal(c.env, append(slices.Clone(clean), "GIT_NO_REPLACE_OBJECTS=1")) {
			t.Errorf("git %v: env is neither CleanEnv() nor CleanEnv()+GIT_NO_REPLACE_OBJECTS=1", c.args)
		}
		if !slices.Contains(c.args, "core.hooksPath=/dev/null") || !slices.Contains(c.args, "core.fsmonitor=false") {
			t.Errorf("git %v is not hardened", c.args)
		}
		for _, a := range c.args {
			if a == x.req.Clone.Dir || strings.HasPrefix(a, x.req.Clone.Dir+"/") {
				t.Errorf("git %v names the clone (%s)", c.args, a)
			}
		}
	}
}

func TestReturnNoBase(t *testing.T) {
	x := newReturnFx(t)
	fixtureGit(t, x.of, "branch", "-m", "main", "trunk")
	res := x.ret(ReturnAuto)
	if want := []string{"feat:refused", "main:refused"}; !slices.Equal(branchesOf(res.NotHome), want) {
		t.Fatalf("NotHome = %q, want %q", branchesOf(res.NotHome), want)
	}
	for _, n := range res.NotHome {
		if n.Detail != "host repo has neither main nor master" {
			t.Errorf("Detail = %q", n.Detail)
		}
	}
	if x.dock.calls != 1 || len(res.Home) != 0 {
		t.Errorf("%d containers, Home %q; want only the list", x.dock.calls, res.Home)
	}
	if got := x.boxRefs(); got != "" {
		t.Errorf("box refs: %q", got)
	}
}

func TestReturnCancelledReportsTheCancel(t *testing.T) {
	x := newReturnFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	x.dock.hang = true
	res := Return(ctx, x.rec, x.dock, x.retReq(ReturnAuto))
	if res.Error != "cancelled" {
		t.Errorf("Error = %q, want cancelled", res.Error)
	}
}

func TestReturnCarriesHandbackPerBranch(t *testing.T) {
	x := newReturnFx(t)
	x.inClone("checkout", "-q", "feat")
	x.dockerCommitELF("tool.pdf")
	res := x.ret(ReturnAuto)
	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	rep, ok := res.Handback["box/feat"]
	if !ok {
		t.Fatalf("Handback = %+v, want an entry for box/feat", res.Handback)
	}
	if f, ok := flagFor(rep, "tool.pdf"); !ok || len(f.Classes) == 0 {
		t.Errorf("box/feat report = %+v", rep)
	}
	if _, ok := res.Handback["box/old"]; ok {
		t.Error("a branch that did not come home has a report")
	}
}

func TestReturnCountsUncommittedFromTheListing(t *testing.T) {
	x := newReturnFx(t)
	ld := &listDocker{fakeDocker: x.dock, out: strings.Repeat("a", 40) + " refs/heads/old\n#culibox-status\n M a\n?? b\n"}
	res := Return(context.Background(), x.rec, ld, x.retReq(ReturnAuto))
	if res.Uncommitted == nil || *res.Uncommitted != 2 || res.UncommittedCut {
		t.Fatalf("Uncommitted = %v cut=%v, want 2", res.Uncommitted, res.UncommittedCut)
	}
	st, _, _ := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if st.Uncommitted == nil || *st.Uncommitted != 2 || st.UncommittedCut {
		t.Errorf("state = %+v, want Uncommitted 2", st)
	}
	// The cap cutting the status part is the status's cut, not a branch-list warning.
	ld.out = strings.Repeat("a", 40) + " refs/heads/old\n#culibox-status\n M a\n?? b"
	res = Return(context.Background(), x.rec, ld, x.retReq(ReturnAuto))
	if res.Uncommitted == nil || *res.Uncommitted != 1 || !res.UncommittedCut || len(res.Warnings) != 0 {
		t.Errorf("Uncommitted = %v cut=%v warnings=%q", res.Uncommitted, res.UncommittedCut, res.Warnings)
	}
	// An old image (no separator) knows nothing about the tree.
	ld.out = strings.Repeat("a", 40) + " refs/heads/old\n"
	if res = Return(context.Background(), x.rec, ld, x.retReq(ReturnAuto)); res.Uncommitted != nil || res.UncommittedCut {
		t.Errorf("no separator: Uncommitted = %v cut=%v, want unknown", res.Uncommitted, res.UncommittedCut)
	}
}

// needTimeout skips a test that runs the real listScript on a host without
// coreutils timeout (the container image has it).
func needTimeout(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout(1) on this host")
	}
}

func TestReturnCountsUncommittedForReal(t *testing.T) {
	needTimeout(t)
	x := newReturnFx(t)
	if res := x.ret(ReturnAuto); res.Uncommitted == nil || *res.Uncommitted != 0 {
		t.Fatalf("clean clone: Uncommitted = %v, want 0", res.Uncommitted)
	}
	dir := x.req.Clone.Dir
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := x.ret(ReturnAuto); res.Uncommitted == nil || *res.Uncommitted != 2 || res.UncommittedCut {
		t.Fatalf("Uncommitted = %v cut=%v, want 2", res.Uncommitted, res.UncommittedCut)
	}
	// Every file of an untracked directory counts, not the directory once.
	for _, n := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(dir, "newdir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "newdir", n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if res := x.ret(ReturnAuto); res.Uncommitted == nil || *res.Uncommitted != 5 {
		t.Fatalf("Uncommitted = %v, want 5 (2 + the 3 files of newdir)", res.Uncommitted)
	}
}

// A status that fails (here a corrupt index) must not stop the branches coming
// home: only the count is unknown.
func TestReturnBranchesComeHomeWhenStatusFails(t *testing.T) {
	needTimeout(t)
	x := newReturnFx(t)
	if err := os.WriteFile(filepath.Join(x.req.Clone.Dir, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := x.ret(ReturnAuto)
	if res.Error != "" {
		t.Fatalf("Error = %q; a failed status is not a failed listing", res.Error)
	}
	if want := []string{"box/feat", "box/main"}; !slices.Equal(res.Home, want) {
		t.Errorf("Home = %q, want %q", res.Home, want)
	}
	if res.Uncommitted != nil || res.UncommittedCut {
		t.Errorf("Uncommitted = %v cut=%v, want unknown", res.Uncommitted, res.UncommittedCut)
	}
	if st, _, _ := ReadReturnState(x.req.DataDir, x.req.Clone.Dir); st.Uncommitted != nil || st.Error != "" {
		t.Errorf("state = %+v, want no count and no error", st)
	}
}

func TestReturnKeepsUncommittedOnError(t *testing.T) {
	x := newReturnFx(t)
	if err := WriteReturnState(x.req.DataDir, x.req.Clone.Dir, ReturnState{Base: "main", Uncommitted: intp(4), UncommittedCut: true}); err != nil {
		t.Fatal(err)
	}
	x.dock.stderr = "fatal: boom"
	res := x.ret(ReturnAuto)
	if res.Error == "" || res.Uncommitted == nil || *res.Uncommitted != 4 || !res.UncommittedCut {
		t.Errorf("result = %+v, want the previous 4 (cut) kept", res)
	}
	st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || !ok || st.Uncommitted == nil || *st.Uncommitted != 4 || !st.UncommittedCut {
		t.Errorf("state = %+v, %v, %v; want Uncommitted 4 (cut) kept", st, ok, err)
	}
}

// mergeReq is retReq with the profile opted in to merging home and an audit
// recorder.
func (x *fetchFx) mergeReq(mode ReturnMode, a *auditRec) ReturnRequest {
	r := x.retReq(mode)
	r.Clone.MergeHome = true
	r.MergeAudit = a.fn
	return r
}

func (x *fetchFx) hostTip(branch string) string {
	x.t.Helper()
	got, err := x.ofRev("refs/heads/" + branch)
	if err != nil {
		x.t.Fatal(err)
	}
	return got
}

func TestReturnMergesWhenOptedIn(t *testing.T) {
	x := newReturnFx(t)
	a := &auditRec{}
	before := x.hostTip("main")
	res := Return(context.Background(), x.rec, x.dock, x.mergeReq(ReturnAuto, a))
	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	if res.Merge == nil || !res.Merge.Merged || res.Merge.Branch != "main" {
		t.Fatalf("Merge = %+v, want main merged", res.Merge)
	}
	want := strings.TrimSpace(gitIn(t, x.req.Clone.Dir, "", "rev-parse", "refs/heads/main"))
	if got := x.hostTip("main"); got != want || got == before {
		t.Errorf("host main = %s, want the clone's main %s", got, want)
	}
	if !slices.Equal(a.actions(), []string{"clone_merge_home"}) {
		t.Errorf("audit = %q", a.calls)
	}
	st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || !ok || st.Merge == nil || !st.Merge.Merged || st.Merge.To != want {
		t.Errorf("state = %+v, %v, %v; want the merge recorded", st, ok, err)
	}
}

func TestReturnNoMergeForForcedDryOrError(t *testing.T) {
	cases := []struct {
		name string
		mode ReturnMode
		edit func(r *ReturnRequest)
		err  bool
	}{
		{"forced", ReturnForced, func(*ReturnRequest) {}, false},
		{"dry", ReturnDry, func(*ReturnRequest) {}, false},
		{"listing failure", ReturnAuto, func(r *ReturnRequest) { r.Image = "" }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newReturnFx(t)
			a := &auditRec{}
			req := x.mergeReq(c.mode, a)
			c.edit(&req)
			before := x.hostTip("main")
			res := Return(context.Background(), x.rec, x.dock, req)
			if (res.Error != "") != c.err {
				t.Fatalf("Error = %q, want error=%v", res.Error, c.err)
			}
			if res.Merge != nil {
				t.Errorf("Merge = %+v, want nil", res.Merge)
			}
			if x.hostTip("main") != before || len(a.calls) != 0 {
				t.Errorf("host main moved or audit written: %q", a.calls)
			}
			if st, ok, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir); err != nil || !ok || st.Merge != nil {
				t.Errorf("state = %+v, %v, %v; want no merge", st, ok, err)
			}
		})
	}
}

func TestReturnMergesWhenNothingMoved(t *testing.T) {
	x := newReturnFx(t)
	a := &auditRec{}
	// A tracked file of the host is edited: the first pass is refused.
	if err := os.WriteFile(filepath.Join(x.of, "a"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := Return(context.Background(), x.rec, x.dock, x.mergeReq(ReturnAuto, a))
	if res.Merge == nil || res.Merge.Reason != MergeDirty {
		t.Fatalf("first Merge = %+v, want dirty", res.Merge)
	}
	if st, _, _ := ReadReturnState(x.req.DataDir, x.req.Clone.Dir); st.Merge == nil || st.Merge.Reason != MergeDirty {
		t.Errorf("state Merge = %+v, want dirty", st.Merge)
	}
	fixtureGit(t, x.of, "checkout", "-q", "--", "a")
	// Second pass: no new box commit, nothing comes home, the merge is still tried.
	res = Return(context.Background(), x.rec, x.dock, x.mergeReq(ReturnAuto, a))
	if len(res.Home) != 0 {
		t.Fatalf("Home = %q, want nothing moved", res.Home)
	}
	if res.Merge == nil || !res.Merge.Merged {
		t.Fatalf("second Merge = %+v, want merged", res.Merge)
	}
}

// box/X in clone_of is shared by every clone of the repo: a box/main another
// clone wrote (here one the clone's main cannot fast-forward, so this return
// leaves it) is not this profile's work and is never merged.
func TestReturnDoesNotMergeAnotherClonesBox(t *testing.T) {
	x := newReturnFx(t)
	env := append(CleanEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	out, err := x.rec.g.Run(context.Background(), x.of, env, "commit-tree", "-m", "another clone", "-p", "refs/heads/main", "refs/heads/main^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	other := strings.TrimSpace(string(out))
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", other)
	before := x.hostTip("main")
	a := &auditRec{}
	res := Return(context.Background(), x.rec, x.dock, x.mergeReq(ReturnAuto, a))
	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	if got, _ := x.ofRev("refs/heads/box/main"); got != other {
		t.Fatalf("box/main = %s, want the other clone's %s left in place", got, other)
	}
	if res.Merge != nil {
		t.Fatalf("Merge = %+v, want nil: box/main is not this clone's", res.Merge)
	}
	if x.hostTip("main") != before || len(a.calls) != 0 {
		t.Fatalf("host main moved or audit written: %q", a.calls)
	}
}

func TestReturnMergeOffIsUnchanged(t *testing.T) {
	x := newReturnFx(t)
	res := x.ret(ReturnAuto)
	if res.Merge != nil {
		t.Fatalf("Merge = %+v with the flag off", res.Merge)
	}
	for _, c := range x.rec.calls {
		if c.dir != x.of {
			continue
		}
		for _, a := range c.args {
			if a == "symbolic-ref" || a == "merge" || a == "check-attr" {
				t.Errorf("clone_of git call %q with the flag off", c.args)
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(x.req.DataDir, returnStateDir, mustStateName(t, x.req.Clone.Dir)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"merge"`) {
		t.Errorf("state has a merge key: %s", raw)
	}
}

func mustStateName(t *testing.T, dir string) string {
	t.Helper()
	n, err := returnStateName(dir)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReturnStateMergeCarry(t *testing.T) {
	// A nil result after a merge keeps the merge; after a refusal it clears it.
	x := newReturnFx(t)
	a := &auditRec{}
	res := Return(context.Background(), x.rec, x.dock, x.mergeReq(ReturnAuto, a))
	if res.Merge == nil || !res.Merge.Merged {
		t.Fatalf("Merge = %+v, want merged", res.Merge)
	}
	merged := *res.Merge
	res = Return(context.Background(), x.rec, x.dock, x.mergeReq(ReturnAuto, a)) // X == box/X: nothing to do
	if res.Merge != nil {
		t.Fatalf("second Merge = %+v, want nil", res.Merge)
	}
	st, _, err := ReadReturnState(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || st.Merge == nil || !st.Merge.Merged || st.Merge.To != merged.To || !st.Merge.At.Equal(merged.At) {
		t.Fatalf("state Merge = %+v, %v; want the previous merge kept", st.Merge, err)
	}

	y := newReturnFx(t)
	if err := os.WriteFile(filepath.Join(y.of, "a"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = Return(context.Background(), y.rec, y.dock, y.mergeReq(ReturnAuto, a))
	if res.Merge == nil || res.Merge.Reason != MergeDirty {
		t.Fatalf("Merge = %+v, want dirty", res.Merge)
	}
	// The host moves ahead of box/main: nothing to bring, so no result.
	fixtureGit(t, y.of, "checkout", "-q", "--", "a")
	fixtureGit(t, y.of, "merge", "-q", "--ff-only", "box/main")
	y.hostCommit("h.txt", "h", "host ahead")
	res = Return(context.Background(), y.rec, y.dock, y.mergeReq(ReturnAuto, a))
	if res.Merge != nil {
		t.Fatalf("Merge = %+v, want nil", res.Merge)
	}
	if st, _, err := ReadReturnState(y.req.DataDir, y.req.Clone.Dir); err != nil || st.Merge != nil {
		t.Errorf("state Merge = %+v, %v; want it dropped after a refusal", st.Merge, err)
	}
}
