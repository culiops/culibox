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
	// stdout must be the 0600 temp file (possibly behind the size cap), made under TempDir.
	named, ok := stdout.(interface{ Name() string })
	if !ok {
		f.t.Errorf("stdout %T is not backed by a file", stdout)
	} else {
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
	x.req = FetchRequest{Clone: c, Branch: "feat", Base: "main", Image: "img:1", Instance: "T", TempDir: temp, Home: home}
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
		if !slices.Equal(c.env, clean) {
			t.Errorf("git %v: env is not CleanEnv()", c.args)
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
		got, err := packOffset(write(tc.in))
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
