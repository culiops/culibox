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
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
)

func TestValidBranch(t *testing.T) {
	for _, ok := range []string{"main", "feat", "feat/x-1", "a.b", "release/1.2.3", "a_b", strings.Repeat("a", 200)} {
		if err := ValidBranch(ok); err != nil {
			t.Errorf("ValidBranch(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a..b", "a b", ".x", "x.lock", "a/./b", "@{-1}", "a//b", "a/", "a.", "a/x.lock/b",
		"a/.b", "a:b", "a~1", "a^", "a\\b", "a\nb", "é", "_x", strings.Repeat("a", 201)} {
		if err := ValidBranch(bad); err == nil {
			t.Errorf("ValidBranch(%q) = nil, want an error", bad)
		}
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("a\x1b]52;c;x\x07b\x7f\u0085  c\x00"); got != "a?]52;c;x?b????c?" {
		t.Errorf("sanitize = %q", got)
	}
	if got := sanitize("a\u202eb\u202ac\u2066d\u2069e\u2065f\u202f"); got != "a?b?c?d?e\u2065f\u202f" {
		t.Errorf("bidi controls: %q", got)
	}
	if got := sanitize("héllo ✓"); got != "héllo ✓" {
		t.Errorf("sanitize mangled printable text: %q", got)
	}
	if got := []rune(sanitize(strings.Repeat("é", 300))); len(got) != 200 {
		t.Errorf("sanitize kept %d runes, want 200", len(got))
	}
}

func TestBundleArgs(t *testing.T) {
	r := FetchRequest{
		Clone:    config.Clone{Of: "/src/of", Dir: "/work/clone"},
		Branch:   "feat/x",
		Base:     "main",
		Image:    "culiops/agent-box:2026-10-18.2",
		Instance: "DEV",
	}
	sha := strings.Repeat("ab", 20)
	got := BundleArgs(r, sha, "n")
	want := []string{
		"run", "--rm", "--init", "--name", "n", "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "1g",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--label", "io.culiops.agent-box.clonefetch=DEV",
		"-e", "HOME=/tmp", "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/src/of/.git/objects",
		"--mount", "type=bind,src=/work/clone,dst=/work/clone,readonly",
		"--mount", "type=bind,src=/src/of/.git,dst=/src/of/.git,readonly",
		"--entrypoint", "git", "culiops/agent-box:2026-10-18.2",
		"-C", "/work/clone", "bundle", "create", "-", "refs/heads/feat/x", "^" + sha,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("BundleArgs:\n got %q\nwant %q", got, want)
	}
	// Every bind is read-only, and the container carries no box label (correction 9).
	for i, a := range got {
		if a == "--mount" && !strings.HasSuffix(got[i+1], ",readonly") {
			t.Errorf("mount %q is not read-only", got[i+1])
		}
		if strings.Contains(a, "io.culiops.agent-box.instance") {
			t.Errorf("argv carries the box label: %q", a)
		}
	}
}

// fakeDocker stands in for the bundle container: it runs the in-container git
// command on the host test fixture, with the alternates variable the argv sets,
// and checks what the real ExecDocker contract promises about stdout.
type fakeDocker struct {
	t       *testing.T
	tempDir string
	calls   int
	stderr  string // when set, fail like the container with this stderr (after the real git ran)
	hang    bool   // when set, block until ctx is done, like a container that never exits
	removed []string
	args    []string // argv of the last RunStream
}

func (f *fakeDocker) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeDocker) RunStream(ctx context.Context, args []string, stdout io.Writer) error {
	f.t.Helper()
	f.calls++
	f.args = slices.Clone(args)
	if f.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	// stdout must be the 0600 temp file (possibly behind the size cap), made under
	// TempDir; the branch listing is the one container that writes to memory.
	isList := slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, LabelCloneList+"=") })
	named, ok := stdout.(interface{ Name() string })
	switch {
	case isList:
	case !ok:
		f.t.Errorf("stdout %T is not backed by a file", stdout)
	default:
		st, err := os.Stat(named.Name())
		if err != nil {
			f.t.Errorf("stdout file: %v", err)
		} else {
			if st.Mode().Perm() != 0o600 {
				f.t.Errorf("stdout file mode %v, want 0600", st.Mode().Perm())
			}
			if filepath.Dir(named.Name()) != f.tempDir {
				f.t.Errorf("stdout file %s is not directly under TempDir %s", named.Name(), f.tempDir)
			}
		}
	}
	var alt string
	for i, a := range args {
		if a == "-e" && strings.HasPrefix(args[i+1], "GIT_ALTERNATE_OBJECT_DIRECTORIES=") {
			alt = strings.TrimPrefix(args[i+1], "GIT_ALTERNATE_OBJECT_DIRECTORIES=")
		}
	}
	i := slices.Index(args, "--entrypoint")
	if i < 0 || args[i+1] != "git" {
		f.t.Fatalf("no git entrypoint in %q", args)
	}
	gitArgs := args[i+3:] // skip --entrypoint git <image>
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = append(CleanEnv(), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+alt)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	if _, err := stdout.Write(out.Bytes()); err != nil { // like the container: the header is out before the exit code
		return err
	}
	if f.stderr != "" {
		return errors.New("docker run: exit status 128: " + f.stderr)
	}
	if runErr != nil {
		return fmt.Errorf("docker run: %w: %s", runErr, strings.TrimSpace(errb.String()))
	}
	return nil
}

// RunStdin records the call and forwards it, so recGit satisfies StdinGit.
func (r *recGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call{dir, slices.Clone(env), slices.Clone(args)})
	r.mu.Unlock()
	return r.g.(StdinGit).RunStdin(ctx, dir, env, stdin, args...)
}

type fetchFx struct {
	t    *testing.T
	of   string
	req  FetchRequest
	rec  *recGit
	dock *fakeDocker
}

func (x *fetchFx) fetch() (FetchResult, error) {
	x.t.Helper()
	return Fetch(context.Background(), x.rec, x.dock, x.req)
}

func (x *fetchFx) inClone(args ...string) { x.t.Helper(); fixtureGit(x.t, x.req.Clone.Dir, args...) }

func (x *fetchFx) commitInClone(file, content, msg string) {
	x.t.Helper()
	if err := os.WriteFile(filepath.Join(x.req.Clone.Dir, file), []byte(content), 0o644); err != nil {
		x.t.Fatal(err)
	}
	x.inClone("add", file)
	x.inClone("commit", "-q", "-m", msg)
}

func (x *fetchFx) ofRev(ref string) (string, error) {
	out, err := x.rec.g.Run(context.Background(), x.of, CleanEnv(), "rev-parse", "--verify", "--end-of-options", ref)
	return strings.TrimSpace(string(out)), err
}

// newFetchFx builds a host repo on "main" and a real clone of it (made by
// EnsureClone) that is on a new branch "feat" with one commit.
func newFetchFx(t *testing.T) *fetchFx {
	t.Helper()
	rec := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	home := t.TempDir()
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "clone")}
	if _, err := EnsureClone(context.Background(), rec, c, home); err != nil {
		t.Fatal(err)
	}
	x := &fetchFx{t: t, of: of, rec: rec}
	temp := filepath.Join(t.TempDir(), "clone-fetch")
	x.dock = &fakeDocker{t: t, tempDir: temp}
	x.req = FetchRequest{Clone: c, Branch: "feat", Base: "main", Image: "img:1", Instance: "T", TempDir: temp, Home: home,
		DataDir: t.TempDir()}
	// The trusted record a clone creation starts: the host main it was made from.
	tip, err := x.ofRev("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := ResetBaseRecord(x.req.DataDir, c.Dir, tip); err != nil {
		t.Fatal(err)
	}
	x.inClone("checkout", "-q", "-b", "feat")
	x.commitInClone("feat.txt", "1", "add feat")
	rec.reset()
	return x
}

func (x *fetchFx) tempEmpty() {
	x.t.Helper()
	ents, err := os.ReadDir(x.req.TempDir)
	if err != nil {
		x.t.Fatal(err)
	}
	if len(ents) != 0 {
		x.t.Errorf("TempDir has %d leftover entries", len(ents))
	}
}

func TestFetchBringsBranchAsBoxRef(t *testing.T) {
	x := newFetchFx(t)
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := x.rec.g.Run(context.Background(), x.req.Clone.Dir, CleanEnv(), "rev-parse", "refs/heads/feat")
	got, err := x.ofRev("refs/heads/box/feat")
	if err != nil || got != strings.TrimSpace(string(want)) {
		t.Fatalf("box/feat = %q, %v; want %q", got, err, strings.TrimSpace(string(want)))
	}
	if len(res.Commits) != 1 || !strings.HasSuffix(res.Commits[0], " add feat") {
		t.Errorf("Commits = %q", res.Commits)
	}
	if res.Ref != "refs/heads/box/feat" || len(res.BaseSHA) != 40 || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	if _, err := x.ofRev("refs/heads/feat"); err == nil {
		t.Error("a plain feat ref appeared in of; only box/feat may")
	}
	x.tempEmpty()
}

func TestFetchWhenCloneLacksBase(t *testing.T) {
	x := newFetchFx(t)
	// main moves in of after the clone was made: the clone lacks the base commit.
	if err := os.WriteFile(filepath.Join(x.of, "later"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "add", "later")
	fixtureGit(t, x.of, "commit", "-q", "-m", "later")
	newMain, err := x.ofRev("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.rec.g.Run(context.Background(), x.req.Clone.Dir, CleanEnv(), "cat-file", "-e", newMain+"^{commit}"); err == nil {
		t.Fatal("the clone already has the new main; the test would not exercise the alternates path")
	}
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 1 {
		t.Errorf("Commits = %q, want just the box commit", res.Commits)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err != nil {
		t.Error(err)
	}
}

func TestFetchRefusesBadBranch(t *testing.T) {
	for _, bad := range []string{"-x", "a..b", "a b", ".x", "x.lock", "a/./b", "@{-1}", "", strings.Repeat("a", 201)} {
		x := newFetchFx(t)
		x.req.Branch = bad
		if _, err := x.fetch(); err == nil {
			t.Errorf("branch %q: no error", bad)
		}
		x.req.Branch, x.req.Base = "feat", bad
		if _, err := x.fetch(); err == nil {
			t.Errorf("base %q: no error", bad)
		}
		if len(x.rec.calls) != 0 || x.dock.calls != 0 {
			t.Errorf("%q: %d git and %d docker calls before the refusal", bad, len(x.rec.calls), x.dock.calls)
		}
	}
}

func TestFetchForceUpdatesBoxRef(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	first, _ := x.ofRev("refs/heads/box/feat")
	// Rewrite history in the clone: drop the commit, make a different one.
	x.inClone("reset", "-q", "--hard", "main")
	x.commitInClone("other.txt", "2", "rewritten")
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	want, _ := x.rec.g.Run(context.Background(), x.req.Clone.Dir, CleanEnv(), "rev-parse", "refs/heads/feat")
	got, _ := x.ofRev("refs/heads/box/feat")
	if got != strings.TrimSpace(string(want)) || got == first {
		t.Errorf("box/feat = %s, want the rewritten tip %s (was %s)", got, strings.TrimSpace(string(want)), first)
	}
}

func TestFetchNeverRunsHostGitInTheClone(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if len(x.rec.calls) == 0 {
		t.Fatal("no host git call was recorded")
	}
	clean := CleanEnv()
	for _, c := range x.rec.calls {
		if c.dir != x.of {
			t.Errorf("git %v ran in %q, want only %q", c.args, c.dir, x.of)
		}
		// The history checks add GIT_NO_REPLACE_OBJECTS=1 and nothing else.
		if !slices.Equal(c.env, clean) && !slices.Equal(c.env, append(slices.Clone(clean), "GIT_NO_REPLACE_OBJECTS=1")) {
			t.Errorf("git %v: env is neither CleanEnv() nor CleanEnv()+GIT_NO_REPLACE_OBJECTS=1", c.args)
		}
		for _, a := range c.args {
			if a == x.req.Clone.Dir || strings.HasPrefix(a, x.req.Clone.Dir+"/") {
				t.Errorf("git %v names the clone (%s)", c.args, a)
			}
		}
	}
}

func TestFetchSanitizesSubjects(t *testing.T) {
	x := newFetchFx(t)
	x.commitInClone("v.txt", "v", "a\x1b]52;c;x\x07b")
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 2 || !strings.HasSuffix(res.Commits[0], " a?]52;c;x?b") {
		t.Errorf("Commits = %q", res.Commits)
	}
	for _, c := range res.Commits {
		if strings.ContainsAny(c, "\x1b\x07") {
			t.Errorf("control character survived in %q", c)
		}
	}
}

func TestFetchWarnings(t *testing.T) {
	x := newFetchFx(t)
	x.req.Clone.Shares = []string{"CLAUDE.md", "docs/superpowers"}
	x.commitInClone("one.txt", "1", "trailer one\n\nCo-Authored-By: Someone <s@x>")
	x.commitInClone("two.txt", "2", "made\n\n🤖 Generated with Some Tool")
	x.commitInClone("CLAUDE.md", "local", "add shared file")
	if err := os.MkdirAll(filepath.Join(x.req.Clone.Dir, "docs/superpowers"), 0o755); err != nil {
		t.Fatal(err)
	}
	x.commitInClone("docs/superpowers/plan.md", "p", "add under share")
	x.commitInClone("docs/other.md", "o", "unrelated docs")
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "2 commit(s) carry an AI attribution trailer (Co-Authored-By / Generated with); strip them before any push") {
		t.Errorf("no attribution warning:\n%s", joined)
	}
	if !strings.Contains(joined, "box/feat changes shared (local-only) paths: ") ||
		!strings.Contains(joined, "CLAUDE.md") || !strings.Contains(joined, "docs/superpowers/plan.md") {
		t.Errorf("no shared-paths warning:\n%s", joined)
	}
	if strings.Contains(joined, "docs/other.md") || strings.Contains(joined, "feat.txt") {
		t.Errorf("warning names a path outside the shares:\n%s", joined)
	}
}

func TestFetchWarnsOnShareFileAddedThenRemoved(t *testing.T) {
	x := newFetchFx(t)
	x.req.Clone.Shares = []string{"CLAUDE.md"}
	x.commitInClone("CLAUDE.md", "local", "add shared file")
	x.inClone("rm", "-q", "CLAUDE.md")
	x.inClone("commit", "-q", "-m", "remove it again")
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "box/feat changes shared (local-only) paths: CLAUDE.md") {
		t.Errorf("a share file in the branch history was not reported: %q", res.Warnings)
	}
	// Listed once even though two commits touch it.
	if w := strings.Join(res.Warnings, "\n"); strings.Contains(w, "CLAUDE.md, CLAUDE.md") {
		t.Errorf("path listed twice: %s", w)
	}
}

// gitIn runs host git in dir with stdin, for building crafted objects in tests.
func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = CleanEnv()
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// breakTip replaces the clone's feat tip with a commit whose author has no
// email: an object `index-pack --strict` refuses (fsck missingEmail) but a
// plain bundle fetch accepts.
func breakTip(t *testing.T, x *fetchFx) {
	t.Helper()
	dir := x.req.Clone.Dir
	tree := gitIn(t, dir, "", "rev-parse", "HEAD^{tree}")
	parent := gitIn(t, dir, "", "rev-parse", "HEAD")
	commit := "tree " + tree + "\nparent " + parent + "\nauthor No Email 1700000000 +0000\ncommitter F <f@x> 1700000000 +0000\n\nbad\n"
	sha := gitIn(t, dir, commit, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	gitIn(t, dir, "", "update-ref", "refs/heads/feat", sha)
	chk := exec.Command("git", "-C", dir, "fsck", "--strict", "--no-dangling")
	chk.Env = CleanEnv()
	if out, err := chk.CombinedOutput(); err == nil || !strings.Contains(string(out), "missingEmail") {
		t.Fatalf("the fixture commit is not the broken object this test needs: %v: %s", err, out)
	}
}

// git does not fsck the objects of a bundle fetch (measured on git 2.43), so a
// commit whose author has no email must be stopped by the index-pack step.
func TestFetchRefusesObjectsThatFailStrictChecks(t *testing.T) {
	x := newFetchFx(t)
	breakTip(t, x)
	_, err := x.fetch()
	if err == nil || !strings.Contains(err.Error(), "strict checks") {
		t.Fatalf("err = %v, want a strict-check refusal", err)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err == nil {
		t.Error("box/feat exists after a refused fetch")
	}
	x.tempEmpty()
}

// packFiles lists clone_of's objects/pack.
func packFiles(t *testing.T, of string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(of, ".git", "objects", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// The strict check must not leave its pack in clone_of: a refused bundle would
// otherwise stay there as tmp_pack_* (box-chosen bytes, up to the size cap), and
// an accepted one as a full pack the real fetch then duplicates.
func TestFetchLeavesNothingInOfsPackDir(t *testing.T) {
	// A refused bundle changes nothing in of's pack dir. An accepted one adds
	// exactly the one pack the real fetch keeps (git keeps a bundle fetch's pack),
	// not a second one from the check.
	countPacks := func(names []string) (n int) {
		for _, f := range names {
			if strings.HasSuffix(f, ".pack") {
				n++
			}
		}
		return
	}
	for _, refused := range []bool{true, false} {
		x := newFetchFx(t)
		if refused {
			breakTip(t, x)
		}
		before := packFiles(t, x.of)
		_, err := x.fetch()
		if refused != (err != nil) {
			t.Fatalf("refused=%v err=%v", refused, err)
		}
		after := packFiles(t, x.of)
		for _, n := range after {
			if strings.HasPrefix(n, "tmp_pack_") {
				t.Errorf("refused=%v: %s left in clone_of", refused, n)
			}
		}
		want := 0
		if !refused {
			want = 1
		}
		if got := countPacks(after) - countPacks(before); got != want {
			t.Errorf("refused=%v: %d new pack(s) in clone_of, want %d: %v -> %v", refused, got, want, before, after)
		}
		x.tempEmpty()
	}
}

func TestPackOffset(t *testing.T) {
	write := func(content string) *os.File {
		f, err := os.Create(filepath.Join(t.TempDir(), "b"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		f.WriteString(content)
		f.Seek(0, io.SeekStart)
		return f
	}
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, in string
		want     int64
		bad      string
	}{
		{"v2", "# v2 git bundle\n-" + sha + " c\n" + sha + " refs/heads/x\n\nPACK", int64(len("# v2 git bundle\n-" + sha + " c\n" + sha + " refs/heads/x\n\n")), ""},
		{"v2 empty header", "# v2 git bundle\n\nPACK", int64(len("# v2 git bundle\n\n")), ""},
		{"v3", "# v3 git bundle\n@object-format=sha1\n" + sha + " refs/heads/x\n\nPACK", int64(len("# v3 git bundle\n@object-format=sha1\n" + sha + " refs/heads/x\n\n")), ""},
		{"v3 sha256", "# v3 git bundle\n@object-format=sha256\n\nPACK", int64(len("# v3 git bundle\n@object-format=sha256\n\n")), ""},
		{"v3 filter after ref", "# v3 git bundle\n@object-format=sha1\n-" + sha + " c\n" + sha + " refs/heads/x\n@filter=blob:none\n\nPACK", 0, "not supported"},
		{"v3 filter after prerequisite", "# v3 git bundle\n-" + sha + " c\n@filter=blob:none\n" + sha + " refs/heads/x\n\nPACK", 0, "not supported"},
		{"v3 unknown capability", "# v3 git bundle\n" + sha + " refs/heads/x\n@whatever=1\n\nPACK", 0, "not supported"},
		{"v3 object-format other", "# v3 git bundle\n@object-format=md5\n\nPACK", 0, "not supported"},
		{"v2 with capability", "# v2 git bundle\n@object-format=sha1\n\nPACK", 0, "not supported"},
		{"long prerequisite subject", "# v2 git bundle\n-" + sha + " " + strings.Repeat("s", 5<<10) + "\n" + sha + " refs/heads/x\n\nPACK", int64(len("# v2 git bundle\n-" + sha + " " + strings.Repeat("s", 5<<10) + "\n" + sha + " refs/heads/x\n\n")), ""},
		{"v3 filter", "# v3 git bundle\n@filter=blob:none\n\nPACK", 0, "not supported"},
		{"v1", "# v1 git bundle\n\nPACK", 0, "not a git bundle"},
		{"garbage", "PACK\x00\x00", 0, "bundle"},
		{"no blank line", "# v2 git bundle\n" + sha + " refs/heads/x\n", 0, "truncated"},
		{"long line", "# v2 git bundle\n" + strings.Repeat("x", 70<<10) + "\n\n", 0, "too long"},
	} {
		got, _, err := packOffset(write(tc.in))
		switch {
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.bad)
		case tc.bad == "" && (err != nil || got != tc.want):
			t.Errorf("%s: off = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

func TestFetchCapsBundleSize(t *testing.T) {
	x := newFetchFx(t)
	old := maxBundleBytes
	maxBundleBytes = 16
	t.Cleanup(func() { maxBundleBytes = old })
	_, err := x.fetch()
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want a size refusal", err)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err == nil {
		t.Error("box/feat exists after a refused fetch")
	}
	x.tempEmpty()
}

func TestFetchEmptyRange(t *testing.T) {
	x := newFetchFx(t)
	x.dock.stderr = "fatal: Refusing to create empty bundle."
	_, err := x.fetch()
	if err == nil || !strings.Contains(err.Error(), "nothing to fetch: feat has no commits that main lacks") {
		t.Fatalf("err = %v", err)
	}
	x.tempEmpty()
	// The real thing: the branch has nothing main lacks. git exits 128 after the 17-byte header.
	x = newFetchFx(t)
	x.inClone("checkout", "-q", "-b", "same", "main")
	x.req.Branch = "same"
	_, err = x.fetch()
	if err == nil || !strings.Contains(err.Error(), "nothing to fetch") {
		t.Fatalf("real empty range: err = %v", err)
	}
}

func TestFetchSanitizesContainerStderr(t *testing.T) {
	x := newFetchFx(t)
	x.dock.stderr = "fatal: \x1b]52;c;evil\x07 broke"
	_, err := x.fetch()
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") || !strings.Contains(err.Error(), "?]52;c;evil? broke") {
		t.Fatalf("err = %q", err)
	}
}

func TestFetchRefusesUnsafeInputs(t *testing.T) {
	x := newFetchFx(t)
	x.req.Image = "-v"
	if _, err := x.fetch(); err == nil {
		t.Error("an image that looks like an option was accepted")
	}
	x = newFetchFx(t)
	x.req.Clone.Dir = x.req.Clone.Dir + ",ro"
	if _, err := x.fetch(); err == nil {
		t.Error("a comma in the clone dir was accepted")
	}
	x = newFetchFx(t)
	link := x.req.Clone.Dir + "-link"
	if err := os.Symlink(x.req.Clone.Dir, link); err != nil {
		t.Fatal(err)
	}
	x.req.Clone.Dir = link
	if _, err := x.fetch(); err == nil {
		t.Error("a symlinked clone dir was accepted")
	}
	x = newFetchFx(t)
	x.req.Home = ""
	if _, err := x.fetch(); err == nil {
		t.Error("an empty home was accepted")
	}
	if x.dock.calls != 0 {
		t.Error("docker ran for a refused request")
	}
}

func TestFetchTempFileRemovedOnFailure(t *testing.T) {
	x := newFetchFx(t)
	x.req.Branch = "nosuch" // the container's git fails: bad ref
	_, err := x.fetch()
	if err == nil {
		t.Fatal("want an error")
	}
	x.tempEmpty()
}

// A script standing in for the docker binary.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecDockerStreamsStdoutAndKeepsStderrTail(t *testing.T) {
	// 20000 bytes of noise, then an escape sequence and the real fatal line: the
	// kept tail must hold the escape (so sanitizing is exercised) and the last line.
	bin := writeScript(t, `printf 'DATA'; i=0; while [ $i -lt 2000 ]; do printf 'xxxxxxxxxx' >&2; i=$((i+1)); done; printf '\033]52;c;evil\007\nfatal: the final line\n' >&2; exit 3`)
	var out bytes.Buffer
	err := ExecDocker{Bin: bin}.RunStream(context.Background(), []string{"run"}, &out)
	if out.String() != "DATA" {
		t.Errorf("stdout = %q", out.String())
	}
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") || len(err.Error()) > 600 {
		t.Fatalf("err = %q", err)
	}
	if !strings.Contains(err.Error(), "?]52;c;evil?") || !strings.Contains(err.Error(), "fatal: the final line") {
		t.Errorf("err = %q, want the sanitized escape and the final line", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("sink full") }

func TestExecDockerStopsWhenTheSinkFails(t *testing.T) {
	bin := writeScript(t, `while :; do printf 'xxxxxxxxxxxxxxxx'; done`)
	done := make(chan error, 1)
	go func() { done <- ExecDocker{Bin: bin}.RunStream(context.Background(), nil, failWriter{}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "sink full") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunStream kept going after the writer failed")
	}
}

func TestExecDockerCancelLeavesNoGoroutine(t *testing.T) {
	bin := writeScript(t, `exec sleep 60`)
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ExecDocker{Bin: bin}.RunStream(ctx, nil, io.Discard) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want an error after cancel")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunStream did not return after cancel")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines: %d before, %d after", before, n)
	}
}

// hardenedPrefix is what every host git call in clone_of must start with.
var hardenedPrefix = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

// The host repo is user-owned, but a hook planted in it (through a box-writable
// clone_of, or any other way) must still never run during a fetch: git 2.43
// runs reference-transaction on the ref update of `fetch`.
func TestFetchDoesNotRunRepoHooks(t *testing.T) {
	x := newFetchFx(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(x.of, ".git", "hooks", "reference-transaction")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// core.hooksPath in the repo's config is the other planting route.
	fixtureGit(t, x.of, "config", "core.hooksPath", filepath.Dir(hook))
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repo hook ran on the host during clone fetch")
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err != nil {
		t.Fatalf("the fetch itself must still succeed: %v", err)
	}
}

func TestFetchHostGitCallsAreHardened(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if len(x.rec.calls) == 0 {
		t.Fatal("no host git call was recorded")
	}
	for _, c := range x.rec.calls {
		if !slices.Equal(c.args[:min(len(c.args), len(hardenedPrefix))], hardenedPrefix) {
			t.Errorf("git %v does not start with %v", c.args, hardenedPrefix)
		}
	}
}

// Every ancestor of the clone dir is re-checked at fetch time, as at spawn: a
// box that owns an ancestor could swap it for a link and make the bundle
// container read a different repo.
func TestFetchRefusesASymlinkedParent(t *testing.T) {
	x := newFetchFx(t)
	real := filepath.Dir(x.req.Clone.Dir)
	link := filepath.Join(t.TempDir(), "link")
	mustSymlink(t, real, link)
	x.req.Clone.Dir = filepath.Join(link, filepath.Base(x.req.Clone.Dir))
	_, err := x.fetch()
	if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("want a refusal naming the symlinked parent, got %v", err)
	}
	if x.dock.calls != 0 {
		t.Fatal("docker ran with a symlinked parent above the clone")
	}
}

func TestPackOffsetCountsPrereqs(t *testing.T) {
	sha, sha2 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, in string
		want     int
	}{
		{"v2 two", "# v2 git bundle\n-" + sha + " one\n-" + sha2 + " two\n" + sha + " refs/heads/x\n\nPACK", 2},
		{"v3 one", "# v3 git bundle\n@object-format=sha1\n-" + sha + "\n" + sha + " refs/heads/x\n\nPACK", 1},
		{"none", "# v2 git bundle\n" + sha + " refs/heads/x\n\nPACK", 0},
	} {
		p := filepath.Join(t.TempDir(), "b")
		if err := os.WriteFile(p, []byte(tc.in), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		off, n, err := packOffset(f)
		f.Close()
		if err != nil || n != tc.want || off != int64(len(tc.in)-len("PACK")) {
			t.Errorf("%s: packOffset = %d, %d, %v; want prereqs %d", tc.name, off, n, err, tc.want)
		}
		if n, err := bundlePrereqs(p); err != nil || n != tc.want {
			t.Errorf("%s: bundlePrereqs = %d, %v; want %d", tc.name, n, err, tc.want)
		}
	}
	if _, err := bundlePrereqs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("bundlePrereqs of a missing file: no error")
	}
}

// quarantineRefs lists refs/agent-box/ in clone_of: a fetch, refused or not,
// must leave none behind.
func (x *fetchFx) quarantineRefs() string {
	x.t.Helper()
	return gitIn(x.t, x.of, "", "for-each-ref", "--format=%(refname)", "refs/agent-box/")
}

// noBoxRef fails when box/<branch> or any quarantine ref exists in clone_of.
func (x *fetchFx) noBoxRef(branch string) {
	x.t.Helper()
	if _, err := x.ofRev("refs/heads/box/" + branch); err == nil {
		x.t.Errorf("box/%s exists after a refused fetch", branch)
	}
	if q := x.quarantineRefs(); q != "" {
		x.t.Errorf("quarantine refs left in clone_of: %q", q)
	}
}

// rewriteHostMain drops host main's tip, the base recorded when the clone was
// made, and commits something else on its parent: the shape of a scrubbed
// secret. feat in the clone is still built on the dropped tip, which it returns.
func (x *fetchFx) rewriteHostMain() string {
	x.t.Helper()
	old, err := x.ofRev("refs/heads/main")
	if err != nil {
		x.t.Fatal(err)
	}
	fixtureGit(x.t, x.of, "reset", "-q", "--hard", "HEAD~1")
	if err := os.WriteFile(filepath.Join(x.of, "scrubbed"), []byte("s"), 0o644); err != nil {
		x.t.Fatal(err)
	}
	fixtureGit(x.t, x.of, "add", "scrubbed")
	fixtureGit(x.t, x.of, "commit", "-q", "-m", "scrubbed")
	return old
}

func TestFetchRefusesUnrelatedHistory(t *testing.T) {
	for _, allow := range []bool{false, true} {
		x := newFetchFx(t)
		x.inClone("checkout", "-q", "--orphan", "orphan")
		x.inClone("commit", "-q", "-m", "unrelated root")
		x.req.Branch, x.req.AllowOldHistory = "orphan", allow
		_, err := x.fetch()
		if err == nil || !strings.Contains(err.Error(), "refusing orphan: it shares no history with main in "+x.of) ||
			!strings.Contains(err.Error(), "git rebase origin/main") {
			t.Fatalf("allow=%v: err = %v, want a shares-no-history refusal", allow, err)
		}
		x.noBoxRef("orphan")
		x.tempEmpty()
	}
}

func TestFetchRefusesStaleHistory(t *testing.T) {
	x := newFetchFx(t)
	old := x.rewriteHostMain()
	_, err := x.fetch()
	if err == nil || !strings.Contains(err.Error(), "refusing feat: it carries history host main no longer has (recorded tip "+old[:12]+
		" is an ancestor of feat)") || !strings.Contains(err.Error(), "git rebase origin/main") ||
		!strings.Contains(err.Error(), "do not point a ref at them") {
		t.Fatalf("err = %v, want a stale-history refusal naming %s", err, old[:12])
	}
	x.noBoxRef("feat")
	x.tempEmpty()

	// A rewrite from the root (filter-repo) leaves no shared history at all:
	// the zero-prerequisite check refuses it first.
	x = newFetchFx(t)
	fixtureGit(t, x.of, "checkout", "-q", "--orphan", "new")
	fixtureGit(t, x.of, "commit", "-q", "-m", "rewritten root")
	fixtureGit(t, x.of, "branch", "-f", "main", "new")
	fixtureGit(t, x.of, "checkout", "-q", "main")
	if _, err := x.fetch(); err == nil || !strings.Contains(err.Error(), "shares no history") {
		t.Fatalf("root rewrite: err = %v", err)
	}
	x.noBoxRef("feat")
}

func TestFetchAllowOldHistoryOverridesStale(t *testing.T) {
	x := newFetchFx(t)
	old := x.rewriteHostMain()
	x.req.AllowOldHistory = true
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err != nil {
		t.Fatalf("box/feat missing after an overridden fetch: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.HasPrefix(res.Warnings[0], "-allow-old-history: the history check was overridden (") ||
		!strings.Contains(res.Warnings[0], old[:12]) || !strings.HasSuffix(res.Warnings[0], "; review box/feat before any merge or push") {
		t.Fatalf("Warnings = %q", res.Warnings)
	}
	if q := x.quarantineRefs(); q != "" {
		t.Errorf("quarantine refs left: %q", q)
	}
	// The override is for a known stale ancestor only: no stale SHA, no warning.
	x = newFetchFx(t)
	x.req.AllowOldHistory = true
	if res, err := x.fetch(); err != nil || len(res.Warnings) != 0 {
		t.Fatalf("clean fetch with the flag: %v, %q", err, res.Warnings)
	}
}

func TestFetchRefusesUnverifiedRecord(t *testing.T) {
	recPath := func(x *fetchFx) string {
		p, err := baseRecordPath(x.req.DataDir, x.req.Clone.Dir)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name  string
		setup func(x *fetchFx)
	}{
		{"no record", func(x *fetchFx) { os.Remove(recPath(x)) }},
		{"unverified", func(x *fetchFx) {
			tip, _ := x.ofRev("refs/heads/main")
			os.WriteFile(recPath(x), []byte("unverified\n"+tip+"\n"), 0o600)
		}},
		{"corrupt", func(x *fetchFx) { os.WriteFile(recPath(x), []byte("not a sha\n"), 0o600) }},
		{"no data dir", func(x *fetchFx) { x.req.DataDir = "" }},
	} {
		x := newFetchFx(t)
		tc.setup(x)
		_, err := x.fetch()
		if err == nil || !strings.Contains(err.Error(), "clone fetch: this clone's history is unverified (created before refresh tracking); "+
			"recreate the clone, or pass -allow-old-history once you have checked feat") {
			t.Fatalf("%s: err = %v, want the unverified refusal", tc.name, err)
		}
		if x.dock.calls != 0 {
			t.Errorf("%s: docker ran %d time(s) for an unverified clone", tc.name, x.dock.calls)
		}
		x.noBoxRef("feat")

		// The flag lets it through, loudly.
		x.req.AllowOldHistory = true
		res, err := x.fetch()
		if err != nil {
			t.Fatalf("%s with -allow-old-history: %v", tc.name, err)
		}
		if len(res.Warnings) == 0 || !strings.HasPrefix(res.Warnings[0], "-allow-old-history: the history check was overridden (") {
			t.Errorf("%s: Warnings = %q", tc.name, res.Warnings)
		}
	}
}

func TestFetchRecordsHostBaseTip(t *testing.T) {
	x := newFetchFx(t)
	if err := os.WriteFile(filepath.Join(x.of, "later"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "add", "later")
	fixtureGit(t, x.of, "commit", "-q", "-m", "later")
	tip, err := x.ofRev("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadBaseRecord(x.req.DataDir, x.req.Clone.Dir)
	if err != nil || rec.Unverified || len(rec.SHAs) != 2 || rec.SHAs[1] != tip {
		t.Fatalf("record = %+v, %v; want the trusted record ending in %s", rec, err, tip)
	}
}

// A recorded base that host main rewrote away and git gc then deleted is
// stale but absent from clone_of: it cannot be an ancestor of a fetched branch,
// whose history git checked is all present, so it must not refuse.
func TestFetchIgnoresStaleSHAGoneFromHost(t *testing.T) {
	x := newFetchFx(t)
	tip, _ := x.ofRev("refs/heads/main")
	gone := strings.Repeat("ab", 20)
	if err := ResetBaseRecord(x.req.DataDir, x.req.Clone.Dir, gone); err != nil {
		t.Fatal(err)
	}
	if err := AppendBase(x.req.DataDir, x.req.Clone.Dir, tip); err != nil {
		t.Fatal(err)
	}
	if res, err := x.fetch(); err != nil || len(res.Warnings) != 0 {
		t.Fatalf("err = %v, Warnings = %q", err, res.Warnings)
	}
}

// hookGit is a StdinGit that lets a test replace one host git call with an
// error, or act right after one ran.
type hookGit struct {
	g     StdinGit
	fail  func(sub string) error
	after func(sub string)
}

// gitSub is the subcommand of a hardened argv: the first word after the -c pairs.
func gitSub(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" {
			return args[i]
		}
		i++
	}
	return ""
}

func (h *hookGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return h.RunStdin(ctx, dir, env, nil, args...)
}

func (h *hookGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	sub := gitSub(args)
	if h.fail != nil {
		if err := h.fail(sub); err != nil {
			return nil, err
		}
	}
	out, err := h.g.RunStdin(ctx, dir, env, stdin, args...)
	if h.after != nil {
		h.after(sub)
	}
	return out, err
}

// exitErr is a real *exec.ExitError with the given status.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if c, ok := exitCode(err); !ok || c != code {
		t.Fatalf("exit %d gave %v", code, err)
	}
	return fmt.Errorf("git: %w", err)
}

func TestFetchLeavesNoQuarantineRef(t *testing.T) {
	check := func(name string, x *fetchFx, g Git, ctx context.Context, wantErr string) {
		t.Helper()
		_, err := Fetch(ctx, g, x.dock, x.req)
		switch {
		case wantErr == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)):
			t.Errorf("%s: err = %v, want %q", name, err, wantErr)
		}
		if q := x.quarantineRefs(); q != "" {
			t.Errorf("%s: quarantine refs left in clone_of: %q", name, q)
		}
		if wantErr != "" {
			if _, err := x.ofRev("refs/heads/box/" + x.req.Branch); err == nil {
				t.Errorf("%s: box/%s exists after a refused fetch", name, x.req.Branch)
			}
		}
		x.tempEmpty()
	}
	bg := context.Background()

	x := newFetchFx(t)
	check("success", x, x.rec, bg, "")

	x = newFetchFx(t)
	x.rewriteHostMain()
	check("stale", x, x.rec, bg, "carries history host main no longer has")

	x = newFetchFx(t)
	breakTip(t, x)
	check("strict checks", x, x.rec, bg, "strict checks")

	x = newFetchFx(t)
	x.inClone("checkout", "-q", "--orphan", "orphan")
	x.inClone("commit", "-q", "-m", "root")
	x.req.Branch = "orphan"
	check("unrelated", x, x.rec, bg, "shares no history")

	// A StaleSHAs failure (rev-list output overflowing after a huge rewrite) is
	// "cannot verify", which the flag does not cover.
	x = newFetchFx(t)
	x.rewriteHostMain()
	x.req.AllowOldHistory = true
	h := &hookGit{g: x.rec, fail: func(sub string) error {
		if sub == "rev-list" {
			return errors.New("git rev-list: output larger than 16777216 bytes")
		}
		return nil
	}}
	check("stale check error", x, h, bg, "clone fetch: cannot check feat against host history: git rev-list: output larger")

	// merge-base exits 128 on a real error: refuse, never pass, flag or not.
	x = newFetchFx(t)
	x.rewriteHostMain()
	x.req.AllowOldHistory = true
	h = &hookGit{g: x.rec, fail: func(sub string) error {
		if sub == "merge-base" {
			return exitErr(t, 128)
		}
		return nil
	}}
	check("ancestor check error", x, h, bg, "clone fetch: cannot check feat against host history")

	// The parent context ends right after the import: the cleanup still runs.
	x = newFetchFx(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	h = &hookGit{g: x.rec, after: func(sub string) {
		if sub == "fetch" {
			if q, want := x.quarantineRefs(), quarantinePrefix+x.req.Clone.Hash()+"/feat"; q != want {
				t.Errorf("after the import the quarantine ref is %q, want %s", q, want)
			}
			cancel()
		}
	}}
	check("cancelled", x, h, ctx, "context canceled")
}

// Two profiles may share clone_of. Another fetch that overwrites the
// quarantine ref between this one's import and its rev-parse must not get its
// tip checked against this clone's record and published as box/<branch>.
func TestFetchRefusesATipChangedDuringTheCheck(t *testing.T) {
	x := newFetchFx(t)
	other, err := x.ofRev("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	q := quarantinePrefix + x.req.Clone.Hash() + "/feat"
	h := &hookGit{g: x.rec, after: func(sub string) {
		if sub == "fetch" {
			gitIn(t, x.of, "", "update-ref", q, other)
		}
	}}
	_, err = Fetch(context.Background(), h, x.dock, x.req)
	if err == nil || !strings.Contains(err.Error(),
		"clone fetch: refusing feat: the imported tip changed while it was checked (a concurrent fetch?); retry") {
		t.Fatalf("err = %v, want the changed-tip refusal", err)
	}
	x.noBoxRef("feat")
	x.tempEmpty()
}

// The rewritten-away guard (clone-refresh follow-up 11): a branch must not bring
// back a host commit the host rewrote away after the box fetched it.

// hostCommit commits file on the host's checked-out main and returns the commit.
func (x *fetchFx) hostCommit(file, content, msg string) string {
	x.t.Helper()
	if err := os.WriteFile(filepath.Join(x.of, file), []byte(content), 0o644); err != nil {
		x.t.Fatal(err)
	}
	fixtureGit(x.t, x.of, "add", file)
	fixtureGit(x.t, x.of, "commit", "-q", "-m", msg)
	sha, err := x.ofRev("HEAD")
	if err != nil {
		x.t.Fatal(err)
	}
	return sha
}

// boxFetchesHost has the box `git fetch origin` and start feat on origin/main.
func (x *fetchFx) boxFetchesHost() {
	x.t.Helper()
	x.inClone("fetch", "-q", "origin")
	x.inClone("checkout", "-q", "-B", "feat", "origin/main")
}

// hostRewrittenAway is the scenario: host main gets Y, the box fetches it and
// commits feat on it, then the host resets main to where it was (no gc, so Y is
// still in clone_of's object store). It returns Y.
func (x *fetchFx) hostRewrittenAway() string {
	x.t.Helper()
	b0, err := x.ofRev("refs/heads/main")
	if err != nil {
		x.t.Fatal(err)
	}
	y := x.hostCommit("secret", "s", "Y")
	x.boxFetchesHost()
	x.commitInClone("feat.txt", "1", "feat on Y")
	fixtureGit(x.t, x.of, "reset", "-q", "--hard", b0)
	return y
}

func (x *fetchFx) imported() map[string]bool {
	x.t.Helper()
	m, _, err := ReadImportRecord(x.req.DataDir, x.of)
	if err != nil {
		x.t.Fatal(err)
	}
	return m
}

// bundleDocker stands in for a box that hands over a bundle it prepared.
type bundleDocker struct{ path string }

func (bundleDocker) Remove(context.Context, string) error { return nil }

func (b bundleDocker) RunStream(_ context.Context, _ []string, stdout io.Writer) error {
	f, err := os.Open(b.path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(stdout, f)
	return err
}

// argsGit fails any host git call whose argv the predicate picks.
type argsGit struct {
	g    StdinGit
	fail func(args []string) error
}

func (a *argsGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return a.RunStdin(ctx, dir, env, nil, args...)
}

func (a *argsGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	if err := a.fail(args); err != nil {
		return nil, err
	}
	return a.g.RunStdin(ctx, dir, env, stdin, args...)
}

func wantRewrittenRefusal(t *testing.T, err error, y, of string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "clone fetch: refusing feat: it carries commit "+y[:12]+" that "+of+
		" had but no branch there reaches any more (history rewritten away, or a branch you deleted). "+
		"In the box: git rebase origin/main. After review: -allow-old-history") {
		t.Fatalf("err = %v, want a rewritten-away refusal naming %s", err, y[:12])
	}
}

func TestFetchRefusesRewrittenAwayHostCommit(t *testing.T) {
	x := newFetchFx(t)
	y := x.hostRewrittenAway()
	if _, err := x.ofRev(y + "^{commit}"); err != nil {
		t.Fatalf("Y is not still in clone_of: %v", err)
	}
	_, err := x.fetch()
	wantRewrittenRefusal(t, err, y, x.of)
	x.noBoxRef("feat")
	x.tempEmpty()
	// Y is inside the pack here, present in clone_of, and the record must not take
	// it for the box's: only commits clone_of lacked are recorded.
	if x.imported()[y] {
		t.Error("the refused host commit was recorded as box-made")
	}
}

// A bundle `feat ^Y` names the unreachable Y as a prerequisite and does not carry
// it, so the pack alone shows nothing: the tip's history is what carries Y.
func TestFetchRefusesUnreachablePrerequisite(t *testing.T) {
	x := newFetchFx(t)
	y := x.hostRewrittenAway()
	bundle := filepath.Join(t.TempDir(), "crafted.bundle")
	x.inClone("bundle", "create", bundle, "feat", "^"+y)
	_, err := Fetch(context.Background(), x.rec, bundleDocker{bundle}, x.req)
	wantRewrittenRefusal(t, err, y, x.of)
	x.noBoxRef("feat")
	x.tempEmpty()
}

func TestFetchAllowsOrdinaryNewCommits(t *testing.T) {
	x := newFetchFx(t)
	x.hostCommit("later", "l", "later on host")
	x.boxFetchesHost()
	x.commitInClone("feat.txt", "1", "box one")
	x.commitInClone("feat.txt", "2", "box two")
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	tip, err := x.ofRev("refs/heads/box/feat")
	if err != nil {
		t.Fatal(err)
	}
	prev, _ := x.ofRev("refs/heads/box/feat~1")
	rec := x.imported()
	if !rec[tip] || !rec[prev] {
		t.Errorf("import record %v lacks the box's commits %s, %s", rec, tip, prev)
	}
	if hostTip, _ := x.ofRev("refs/heads/main"); rec[hostTip] {
		t.Error("a commit the host already had was recorded as box-made")
	}
	for s := range rec {
		if out := gitIn(t, x.of, s+"\n", "cat-file", "--batch-check"); !strings.Contains(out, " commit ") {
			t.Errorf("record holds %s, which is not a commit: %q", s, out)
		}
	}
}

func TestFetchAllowsHostCommitsStillReachable(t *testing.T) {
	x := newFetchFx(t)
	fixtureGit(t, x.of, "checkout", "-q", "-b", "hostfeat")
	x.hostCommit("hostfeat.txt", "h", "on hostfeat")
	fixtureGit(t, x.of, "checkout", "-q", "main")
	x.hostCommit("m1.txt", "m", "on main")
	x.boxFetchesHost()
	x.inClone("merge", "-q", "--no-edit", "origin/hostfeat")
	x.commitInClone("feat.txt", "1", "box work")
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchAllowsGrowingBranch(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	b1, _ := x.ofRev("refs/heads/box/feat")
	x.commitInClone("feat2.txt", "2", "b2")
	if _, err := x.fetch(); err != nil {
		t.Fatalf("the second return of a growing branch: %v", err)
	}
	if b2, _ := x.ofRev("refs/heads/box/feat"); b2 == b1 {
		t.Fatal("box/feat did not move")
	}
}

func TestFetchAllowsAfterBoxRefDeleted(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "update-ref", "-d", "refs/heads/box/feat")
	x.commitInClone("feat2.txt", "2", "b2")
	if _, err := x.fetch(); err != nil {
		t.Fatalf("a fetch after the user deleted box/feat: %v", err)
	}

	// It is the record that lets it through: b1 is then in clone_of and in no ref.
	x = newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	b1, _ := x.ofRev("refs/heads/box/feat")
	fixtureGit(t, x.of, "update-ref", "-d", "refs/heads/box/feat")
	if err := os.Remove(importPath(t, x.req.DataDir, x.of)); err != nil {
		t.Fatal(err)
	}
	x.commitInClone("feat2.txt", "2", "b2")
	_, err := x.fetch()
	wantRewrittenRefusal(t, err, b1, x.of)
}

func TestFetchSeedsImportRecordFromExistingBoxRefs(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	b1, _ := x.ofRev("refs/heads/box/feat")
	// box/feat dates from before this guard: no record.
	if err := os.Remove(importPath(t, x.req.DataDir, x.of)); err != nil {
		t.Fatal(err)
	}
	x.commitInClone("feat2.txt", "2", "b2")
	if _, err := x.fetch(); err != nil {
		t.Fatalf("a fetch of a branch fetched before the guard: %v", err)
	}
	b2, _ := x.ofRev("refs/heads/box/feat")
	rec := x.imported()
	if !rec[b1] || !rec[b2] {
		t.Errorf("record %v lacks box/feat's box-only commits %s (seeded) and %s (imported)", rec, b1, b2)
	}
	if hostTip, _ := x.ofRev("refs/heads/main"); rec[hostTip] {
		t.Error("the seed recorded a commit reachable from host main")
	}
}

func TestFetchRefusesFilterRepoRewrittenCommit(t *testing.T) {
	if _, err := exec.LookPath("git-filter-repo"); err != nil {
		t.Skip("git-filter-repo is not installed")
	}
	x := newFetchFx(t)
	y := x.hostCommit("secret", "s", "Y")
	x.boxFetchesHost()
	x.commitInClone("feat.txt", "1", "feat on Y")
	cmd := exec.Command("git", "-C", x.of, "filter-repo", "--invert-paths", "--path", "secret", "--force")
	cmd.Env = append(CleanEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filter-repo: %v: %s", err, out)
	}
	if _, err := x.ofRev(y + "^{commit}"); err == nil {
		t.Fatal("filter-repo left Y in clone_of; the scenario needs it pruned")
	}
	_, err := x.fetch()
	wantRewrittenRefusal(t, err, y, x.of)
	x.noBoxRef("feat")
	x.tempEmpty()
}

// A recorded base tip the host rewrote away and then pruned is caught by the
// base record (7b), which runs after the import has put the tip back.
func TestFetchPrunedRecordedTipStillCaught(t *testing.T) {
	x := newFetchFx(t)
	tip := x.rewriteHostMain()
	fixtureGit(t, x.of, "reflog", "expire", "--expire=now", "--all")
	fixtureGit(t, x.of, "gc", "-q", "--prune=now")
	if _, err := x.ofRev(tip + "^{commit}"); err == nil {
		t.Fatal("the recorded tip survived gc; the scenario needs it pruned")
	}
	_, err := x.fetch()
	if err == nil || !strings.Contains(err.Error(), "refusing feat: it carries history host main no longer has (recorded tip "+tip[:12]+
		" is an ancestor of feat)") {
		t.Fatalf("err = %v, want the base-record refusal naming %s", err, tip[:12])
	}
	x.noBoxRef("feat")
	x.tempEmpty()
}

func TestFetchAllowOldHistoryOverridesRewrittenAway(t *testing.T) {
	x := newFetchFx(t)
	y := x.hostRewrittenAway()
	x.req.AllowOldHistory = true
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err != nil {
		t.Fatalf("box/feat missing after an overridden fetch: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.HasPrefix(res.Warnings[0], "-allow-old-history: the history check was overridden (") ||
		!strings.Contains(res.Warnings[0], y[:12]) || !strings.HasSuffix(res.Warnings[0], "; review box/feat before any merge or push") {
		t.Fatalf("Warnings = %q", res.Warnings)
	}
	if q := x.quarantineRefs(); q != "" {
		t.Errorf("quarantine refs left: %q", q)
	}
}

// A check that cannot run refuses, flag or not: it is the same rule as 7b.
func TestFetchRewrittenAwayCheckFailureRefusesEvenWithFlag(t *testing.T) {
	x := newFetchFx(t)
	x.hostRewrittenAway()
	x.req.AllowOldHistory = true
	g := &argsGit{g: x.rec, fail: func(args []string) error {
		if slices.Contains(args, "rev-list") && slices.Contains(args, "--exclude=box/*") {
			return errors.New("git rev-list: output larger than 16777216 bytes")
		}
		return nil
	}}
	_, err := Fetch(context.Background(), g, x.dock, x.req)
	if err == nil || !strings.Contains(err.Error(), "clone fetch: cannot check feat against host history: git rev-list: output larger") {
		t.Fatalf("err = %v, want a cannot-check refusal", err)
	}
	x.noBoxRef("feat")
	x.tempEmpty()
}

// postGit runs a host git call for real and then fails it when the predicate
// says so: the call's effect is there, its result is lost (a cut-short import).
type postGit struct {
	g    StdinGit
	fail func(args []string) error
}

func (p *postGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return p.RunStdin(ctx, dir, env, nil, args...)
}

func (p *postGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	out, err := p.g.RunStdin(ctx, dir, env, stdin, args...)
	if err == nil {
		err = p.fail(args)
	}
	return out, err
}

// The import puts the objects into clone_of before anything else runs. If it is
// cut short, the retry finds them present and in no ref: they must already be
// recorded as the box's, or the guard would name them as host history for good.
func TestFetchRetriesAfterInterruptedImport(t *testing.T) {
	x := newFetchFx(t)
	cut := true
	g := &postGit{g: x.rec, fail: func(args []string) error {
		if cut && slices.Contains(args, "fetch") {
			return errors.New("git fetch: killed")
		}
		return nil
	}}
	if _, err := Fetch(context.Background(), g, x.dock, x.req); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("err = %v, want the cut-short import", err)
	}
	tip := gitIn(t, x.req.Clone.Dir, "", "rev-parse", "refs/heads/feat")
	if _, err := x.ofRev(tip + "^{commit}"); err != nil {
		t.Fatalf("the box's commit did not land in clone_of; the scenario is not the interrupted one: %v", err)
	}
	x.noBoxRef("feat")
	cut = false
	if _, err := x.fetch(); err != nil {
		t.Fatalf("the retry after an interrupted import: %v", err)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != tip {
		t.Fatalf("box/feat = %s, want %s", got, tip)
	}
}

// Only host branches and tags count as a host holding a commit. HEAD,
// refs/original (filter-branch), refs/stash and remote-tracking refs do not, so
// a commit that only they reach is still "rewritten away".
func TestFetchWhichHostRefsCountAsReachable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(x *fetchFx, y string)
		pass  bool
	}{
		{"a non-box branch counts", func(x *fetchFx, y string) { fixtureGit(t, x.of, "update-ref", "refs/heads/hostfeat", y) }, true},
		{"a tag counts", func(x *fetchFx, y string) { fixtureGit(t, x.of, "update-ref", "refs/tags/keep", y) }, true},
		{"refs/original does not", func(x *fetchFx, y string) {
			fixtureGit(t, x.of, "update-ref", "refs/original/refs/heads/main", y)
		}, false},
		{"refs/stash does not", func(x *fetchFx, y string) { fixtureGit(t, x.of, "update-ref", "refs/stash", y) }, false},
		{"a remote-tracking ref does not", func(x *fetchFx, y string) {
			fixtureGit(t, x.of, "update-ref", "refs/remotes/origin/main", y)
		}, false},
		{"HEAD on box/feat does not", func(x *fetchFx, y string) {
			fixtureGit(t, x.of, "update-ref", "refs/heads/box/feat", y)
			fixtureGit(t, x.of, "symbolic-ref", "HEAD", "refs/heads/box/feat")
		}, false},
	} {
		x := newFetchFx(t)
		y := x.hostRewrittenAway()
		// A record that exists, so the seed (which would take Y from box/feat) is not what is tested.
		if err := AppendImported(x.req.DataDir, x.of, []string{shaN(1)}); err != nil {
			t.Fatal(err)
		}
		tc.setup(x, y)
		_, err := x.fetch()
		if tc.pass {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "it carries commit "+y[:12]) {
			t.Errorf("%s: err = %v, want a refusal naming %s", tc.name, err, y[:12])
		}
	}
}

// The seed counts branches and tags only: with HEAD on box/feat a query over
// --all comes back empty, and that empty record would then count as existing.
func TestFetchSeedsWithHeadOnABoxBranch(t *testing.T) {
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	b1, _ := x.ofRev("refs/heads/box/feat")
	if err := os.Remove(importPath(t, x.req.DataDir, x.of)); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "symbolic-ref", "HEAD", "refs/heads/box/feat")
	x.commitInClone("feat2.txt", "2", "b2")
	if _, err := x.fetch(); err != nil {
		t.Fatalf("a fetch of a branch fetched before the guard, with HEAD on box/feat: %v", err)
	}
	if !x.imported()[b1] {
		t.Error("the seed did not record box/feat's box-only commit")
	}
}

// Accepted residual, documented, not a goal: a host commit that was pruned
// (gc --prune=now, BFG...) and that no commit-map names is indistinguishable from
// a box-made commit once the box sends it again. The README says the same.
func TestFetchAcceptsPrunedRewrittenCommitWithoutCommitMap_AcceptedResidual(t *testing.T) {
	x := newFetchFx(t)
	y := x.hostRewrittenAway()
	fixtureGit(t, x.of, "reflog", "expire", "--expire=now", "--all")
	fixtureGit(t, x.of, "gc", "-q", "--prune=now")
	if _, err := x.ofRev(y + "^{commit}"); err == nil {
		t.Fatal("Y survived gc; the scenario needs it pruned")
	}
	if _, err := x.fetch(); err != nil {
		t.Fatalf("the accepted residual is that this passes; got %v", err)
	}
}

// wantReason fails unless err is a *Refusal with the reason, whose text is the
// wrapped error's, unchanged.
func wantReason(t *testing.T, err error, reason string) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v (%T), want a *Refusal", err, err)
	}
	if r.Reason != reason {
		t.Fatalf("Reason = %q, want %q (err: %v)", r.Reason, reason, err)
	}
	if r.Err == nil || r.Error() != r.Err.Error() || errors.Unwrap(r) != r.Err {
		t.Fatalf("Refusal text %q is not the wrapped error's %v", r.Error(), r.Err)
	}
	return r
}

// ffOnlyFx has a box/feat on the host (one commit), then one more commit in the
// box, with the fetch set to fast-forward only. It returns box/feat's first value.
func ffOnlyFx(t *testing.T) (*fetchFx, string) {
	t.Helper()
	x := newFetchFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	first, err := x.ofRev("refs/heads/box/feat")
	if err != nil {
		t.Fatal(err)
	}
	x.commitInClone("feat2.txt", "2", "b2")
	x.req.FastForwardOnly = true
	return x, first
}

func (x *fetchFx) cloneTip() string {
	x.t.Helper()
	return strings.TrimSpace(gitIn(x.t, x.req.Clone.Dir, "", "rev-parse", "refs/heads/feat"))
}

func TestFetchFastForwardOnlyCreatesNewRef(t *testing.T) {
	x := newFetchFx(t)
	x.req.FastForwardOnly = true
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := x.ofRev("refs/heads/box/feat")
	if got != x.cloneTip() || res.Ref != "refs/heads/box/feat" {
		t.Errorf("box/feat = %s (res %q), want the tip %s", got, res.Ref, x.cloneTip())
	}
	if q := x.quarantineRefs(); q != "" {
		t.Errorf("quarantine refs left: %q", q)
	}
}

func TestFetchFastForwardOnlyAdvances(t *testing.T) {
	x, first := ffOnlyFx(t)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	got, _ := x.ofRev("refs/heads/box/feat")
	if got != x.cloneTip() || got == first {
		t.Errorf("box/feat = %s, want the new tip %s (was %s)", got, x.cloneTip(), first)
	}
}

func TestFetchFastForwardOnlyRefusesDiverged(t *testing.T) {
	x, first := ffOnlyFx(t)
	// The box drops its first commit and commits something else: the host's
	// box/feat has a commit the new tip lacks.
	x.inClone("reset", "-q", "--hard", "main")
	x.commitInClone("other.txt", "x", "other")
	_, err := x.fetch()
	r := wantReason(t, err, ReasonNotFF)
	if want := "box/feat on the host has commits feat lacks; not overwritten (agent-box clone fetch overwrites it)"; !strings.Contains(r.Error(), want) {
		t.Errorf("err = %q, want it to say %q", r.Error(), want)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != first {
		t.Errorf("box/feat = %s, want it unchanged at %s", got, first)
	}
	if q := x.quarantineRefs(); q != "" {
		t.Errorf("quarantine refs left: %q", q)
	}
	x.tempEmpty()

	// The CLI's forced update still takes it.
	x.req.FastForwardOnly = false
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != x.cloneTip() {
		t.Errorf("forced: box/feat = %s, want %s", got, x.cloneTip())
	}
}

func TestFetchFastForwardOnlyRefusesCheckedOutRef(t *testing.T) {
	// In a linked worktree of clone_of.
	x, first := ffOnlyFx(t)
	wt := filepath.Join(t.TempDir(), "wt")
	fixtureGit(t, x.of, "worktree", "add", "-q", wt, "box/feat")
	_, err := x.fetch()
	r := wantReason(t, err, ReasonCheckedOut)
	if want := "box/feat is checked out in " + wt + "; not moved"; !strings.Contains(r.Error(), want) {
		t.Errorf("err = %q, want it to say %q", r.Error(), want)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != first {
		t.Errorf("box/feat = %s, want it unchanged at %s", got, first)
	}
	x.tempEmpty()

	// In clone_of itself.
	x, first = ffOnlyFx(t)
	fixtureGit(t, x.of, "checkout", "-q", "box/feat")
	_, err = x.fetch()
	r = wantReason(t, err, ReasonCheckedOut)
	if want := "box/feat is checked out in " + x.of + "; not moved"; !strings.Contains(r.Error(), want) {
		t.Errorf("err = %q, want it to say %q", r.Error(), want)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != first {
		t.Errorf("box/feat = %s, want it unchanged at %s", got, first)
	}
}

func TestFetchForcedRefuseCheckedOutRef(t *testing.T) {
	// Forced overwrite (not fast-forward only) that still never moves a branch a
	// host worktree has checked out: "Bring work home now" sets the field, the CLI does not.
	x, first := ffOnlyFx(t)
	x.req.FastForwardOnly = false
	x.req.RefuseCheckedOut = true
	wt := filepath.Join(t.TempDir(), "wt")
	fixtureGit(t, x.of, "worktree", "add", "-q", wt, "box/feat")
	// Diverge the box so a forced update would overwrite, not fast-forward.
	x.inClone("reset", "-q", "--hard", "main")
	x.commitInClone("other.txt", "x", "other")
	_, err := x.fetch()
	r := wantReason(t, err, ReasonCheckedOut)
	if want := "box/feat is checked out in " + wt + "; not moved"; !strings.Contains(r.Error(), want) {
		t.Errorf("err = %q, want it to say %q", r.Error(), want)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != first {
		t.Errorf("box/feat = %s, want it unchanged at %s", got, first)
	}
	if head := strings.TrimSpace(gitIn(t, wt, "", "rev-parse", "HEAD")); head != first {
		t.Errorf("worktree HEAD = %s, want it unchanged at %s", head, first)
	}
	if q := x.quarantineRefs(); q != "" {
		t.Errorf("quarantine refs left: %q", q)
	}

	// Not checked out: the forced update still overwrites.
	fixtureGit(t, x.of, "worktree", "remove", "--force", wt)
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != x.cloneTip() {
		t.Errorf("box/feat = %s, want the clone's %s", got, x.cloneTip())
	}
}

func TestFetchCLIForcedStillMovesACheckedOutRef(t *testing.T) {
	// The CLI leaves RefuseCheckedOut false and keeps today's behaviour.
	x, _ := ffOnlyFx(t)
	x.req.FastForwardOnly = false
	wt := filepath.Join(t.TempDir(), "wt")
	fixtureGit(t, x.of, "worktree", "add", "-q", wt, "box/feat")
	if _, err := x.fetch(); err != nil {
		t.Fatal(err)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != x.cloneTip() {
		t.Errorf("box/feat = %s, want %s", got, x.cloneTip())
	}
}

func TestFetchFastForwardOnlyLosesARaceSafely(t *testing.T) {
	x, _ := ffOnlyFx(t)
	racer := strings.TrimSpace(gitIn(t, x.of, "", "commit-tree", "-m", "racer", "HEAD^{tree}"))
	moved := false
	g := &hookGit{g: x.rec, fail: func(sub string) error {
		if sub == "update-ref" && !moved {
			moved = true
			fixtureGit(t, x.of, "update-ref", "refs/heads/box/feat", racer)
		}
		return nil
	}}
	_, err := Fetch(context.Background(), g, x.dock, x.req)
	wantReason(t, err, ReasonNotFF)
	if !moved {
		t.Fatal("the race never happened")
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != racer {
		t.Errorf("box/feat = %s, want the racer's %s", got, racer)
	}
}

func TestFetchRefusalReasons(t *testing.T) {
	// unrelated history: no prerequisite commits
	x := newFetchFx(t)
	x.inClone("checkout", "-q", "--orphan", "orphan")
	x.inClone("commit", "-q", "-m", "unrelated root")
	x.req.Branch = "orphan"
	_, err := x.fetch()
	wantReason(t, err, ReasonRefused)
	if !strings.Contains(err.Error(), "refusing orphan: it shares no history with main in "+x.of) {
		t.Errorf("unrelated: err = %v", err)
	}

	// stale history
	x = newFetchFx(t)
	old := x.rewriteHostMain()
	_, err = x.fetch()
	wantReason(t, err, ReasonRefused)
	if !strings.Contains(err.Error(), "refusing feat: it carries history host main no longer has (recorded tip "+old[:12]+" is an ancestor of feat)") {
		t.Errorf("stale: err = %v", err)
	}

	// unverified record
	x = newFetchFx(t)
	p, perr := baseRecordPath(x.req.DataDir, x.req.Clone.Dir)
	if perr != nil {
		t.Fatal(perr)
	}
	os.Remove(p)
	_, err = x.fetch()
	wantReason(t, err, ReasonRefused)
	if want := "clone fetch: this clone's history is unverified (created before refresh tracking); " +
		"recreate the clone, or pass -allow-old-history once you have checked feat"; err.Error() != want {
		t.Errorf("unverified: err = %q, want %q", err, want)
	}

	// no main or master on the host
	x = newFetchFx(t)
	fixtureGit(t, x.of, "branch", "-m", "main", "trunk")
	x.req.Base = "trunk"
	_, err = x.fetch()
	wantReason(t, err, ReasonRefused)
	if want := "clone fetch: cannot check feat against host history: " + x.of + " has no main or master branch"; err.Error() != want {
		t.Errorf("no base: err = %q, want %q", err, want)
	}

	// rewritten away on the host
	x = newFetchFx(t)
	y := x.hostRewrittenAway()
	_, err = x.fetch()
	wantReason(t, err, ReasonRefused)
	wantRewrittenRefusal(t, err, y, x.of)

	// too large
	x = newFetchFx(t)
	oldMax := maxBundleBytes
	maxBundleBytes = 16
	t.Cleanup(func() { maxBundleBytes = oldMax })
	_, err = x.fetch()
	wantReason(t, err, ReasonTooLarge)
	if want := "clone fetch: the bundle is larger than 16 bytes; refusing it"; err.Error() != want {
		t.Errorf("too large: err = %q, want %q", err, want)
	}
	maxBundleBytes = oldMax

	// empty: the branch is already home
	x = newFetchFx(t)
	x.dock.stderr = "fatal: Refusing to create empty bundle."
	_, err = x.fetch()
	wantReason(t, err, reasonEmpty)
	if want := "nothing to fetch: feat has no commits that main lacks"; err.Error() != want {
		t.Errorf("empty: err = %q, want %q", err, want)
	}
}

func TestFetchAllowsRetryAfterRefusal(t *testing.T) {
	x, first := ffOnlyFx(t)
	x.inClone("reset", "-q", "--hard", "main")
	x.commitInClone("other.txt", "x", "other")
	_, err := x.fetch()
	wantReason(t, err, ReasonNotFF)
	if got, _ := x.ofRev("refs/heads/box/feat"); got != first {
		t.Fatalf("box/feat = %s, want %s", got, first)
	}
	// The host drops its diverged box/feat. The commits the refused run brought
	// are the box's, so they must not read as host history now.
	fixtureGit(t, x.of, "update-ref", "-d", "refs/heads/box/feat")
	if _, err := x.fetch(); err != nil {
		t.Fatalf("retry after the host deleted its diverged box/feat: %v", err)
	}
	if got, _ := x.ofRev("refs/heads/box/feat"); got != x.cloneTip() {
		t.Errorf("box/feat = %s, want %s", got, x.cloneTip())
	}
}
