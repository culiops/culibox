package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

func TestEstimateEnoughRule(t *testing.T) {
	const mib = 1 << 20
	cases := []struct {
		name            string
		git, tree, free int64
		want            bool
	}{
		{"empty needs the spare", 0, 0, 512 * mib, true},
		{"one byte under the spare", 0, 0, 512*mib - 1, false},
		{"ten percent on top", 500 * mib, 500 * mib, 1000*mib*11/10 + 512*mib, true},
		{"one byte short of the ten percent", 500 * mib, 500 * mib, 1000*mib*11/10 + 512*mib - 1, false},
		{"nothing free", 1, 1, 0, false},
		{"big repo, big disk", 50 << 30, 50 << 30, 1 << 40, true},
	}
	for _, c := range cases {
		if got := enoughFor(c.git, c.tree, c.free); got != c.want {
			t.Errorf("%s: enoughFor(%d, %d, %d) = %v, want %v", c.name, c.git, c.tree, c.free, got, c.want)
		}
	}
}

func TestEstimateCopyOfARealRepo(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, false) // two commits, files "a" and "b" of one byte each
	dir := filepath.Join(t.TempDir(), "box-clones", "work", "deeper")
	e, err := EstimateCopy(context.Background(), g, of, dir)
	if err != nil {
		t.Fatal(err)
	}
	if e.TreeBytes != 2 {
		t.Errorf("TreeBytes = %d, want 2", e.TreeBytes)
	}
	if e.GitBytes <= 0 || e.FreeBytes <= 0 {
		t.Errorf("GitBytes = %d, FreeBytes = %d, want both > 0", e.GitBytes, e.FreeBytes)
	}
	if !e.Enough || e.Partial || e.LFS {
		t.Errorf("Enough=%v Partial=%v LFS=%v, want true false false", e.Enough, e.Partial, e.LFS)
	}
	// Every host git call is hardened and clean-env, run in the user's repo.
	for _, c := range g.calls {
		if c.dir != of || strings.Join(c.env, " ") != strings.Join(CleanEnv(), " ") {
			t.Errorf("git call %v ran in %q with env %v", c.args, c.dir, c.env)
		}
		if len(c.args) < len(hardenFlags) || strings.Join(c.args[:len(hardenFlags)], " ") != strings.Join(hardenFlags, " ") {
			t.Errorf("git call %v does not start with hardenFlags", c.args)
		}
	}
}

func TestEstimateCopyEmptyRepoHasNoTree(t *testing.T) {
	g := newRec(t)
	of := filepath.Join(t.TempDir(), "of")
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, of, "init", "-q")
	e, err := EstimateCopy(context.Background(), g, of, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e.TreeBytes != 0 || !e.Enough {
		t.Errorf("TreeBytes = %d, Enough = %v; want 0, true", e.TreeBytes, e.Enough)
	}
}

func TestEstimateCopyLFSHint(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, false)
	if err := os.WriteFile(filepath.Join(of, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := EstimateCopy(context.Background(), g, of, t.TempDir())
	if err != nil || !e.LFS {
		t.Fatalf("LFS = %v, %v; want true", e.LFS, err)
	}
	// A symlink at .gitattributes is not read.
	os.Remove(filepath.Join(of, ".gitattributes"))
	target := filepath.Join(t.TempDir(), "attrs")
	os.WriteFile(target, []byte("* filter=lfs\n"), 0o644)
	if err := os.Symlink(target, filepath.Join(of, ".gitattributes")); err != nil {
		t.Fatal(err)
	}
	if e, err := EstimateCopy(context.Background(), g, of, t.TempDir()); err != nil || e.LFS {
		t.Fatalf("LFS = %v, %v; a symlinked .gitattributes must not count", e.LFS, err)
	}
}

func TestEstimateCopyGitFailureIsAnError(t *testing.T) {
	g := newRec(t)
	if _, err := EstimateCopy(context.Background(), g, filepath.Join(t.TempDir(), "missing"), t.TempDir()); err == nil {
		t.Fatal("want an error for a path that is not a repo")
	}
}

func TestWalkGitBytesRegularFilesOnlyNoSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "big"), make([]byte, 5000), 0o644)
	os.MkdirAll(filepath.Join(root, "objects", "ab"), 0o755)
	os.WriteFile(filepath.Join(root, "HEAD"), make([]byte, 10), 0o644)
	os.WriteFile(filepath.Join(root, "objects", "ab", "cd"), make([]byte, 100), 0o644)
	if err := os.Symlink(filepath.Join(outside, "big"), filepath.Join(root, "linkfile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	sum, partial := walkGitBytes(context.Background(), root, 1000, time.Minute)
	if sum != 110 || partial {
		t.Errorf("sum = %d, partial = %v; want 110, false (links must not be followed or counted)", sum, partial)
	}
	// The root itself a symlink is not followed either.
	link := filepath.Join(t.TempDir(), "gitlink")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if sum, _ := walkGitBytes(context.Background(), link, 1000, time.Minute); sum != 0 {
		t.Errorf("a symlinked root counted %d bytes, want 0", sum)
	}
	// A missing root is a partial zero, not a panic.
	if sum, partial := walkGitBytes(context.Background(), filepath.Join(root, "nope"), 10, time.Minute); sum != 0 || !partial {
		t.Errorf("missing root: sum = %d, partial = %v; want 0, true", sum, partial)
	}
}

func TestWalkGitBytesBounds(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 600; i++ {
		os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), []byte("x"), 0o644)
	}
	sum, partial := walkGitBytes(context.Background(), root, 100, time.Minute)
	if !partial || sum <= 0 || sum > 100 {
		t.Errorf("entry cap: sum = %d, partial = %v; want a partial sum of at most 100", sum, partial)
	}
	sum, partial = walkGitBytes(context.Background(), root, 1_000_000, -time.Second)
	if !partial || sum >= 600 {
		t.Errorf("time budget: sum = %d, partial = %v; want a partial sum below 600", sum, partial)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, partial = walkGitBytes(ctx, root, 1_000_000, time.Minute); !partial {
		t.Error("a cancelled context must stop the walk as partial")
	}
	if sum, partial = walkGitBytes(context.Background(), root, 1_000_000, time.Minute); partial || sum != 600 {
		t.Errorf("unbounded: sum = %d, partial = %v; want 600, false", sum, partial)
	}
}

func TestLsTreeSumAcrossChunks(t *testing.T) {
	in := "100644 blob aaaa      12\ta b\n160000 commit bbbb       -\tsub\n100755 blob cccc     100\tx\ty\n100644 blob dddd       7\tlast"
	for _, chunk := range []int{1, 3, 7, len(in)} {
		var s lsTreeSum
		for i := 0; i < len(in); i += chunk {
			s.Write([]byte(in[i:min(i+chunk, len(in))]))
		}
		s.flush()
		if s.total != 119 {
			t.Errorf("chunk %d: total = %d, want 119", chunk, s.total)
		}
	}
}

// A size git could not read (a missing object prints "BAD", exit 0) makes the
// sum a lower bound; a submodule's "-" does not.
func TestLsTreeSumBadSizeIsPartial(t *testing.T) {
	var s lsTreeSum
	s.Write([]byte("100644 blob aaaa      12\ta\n160000 commit bbbb       -\tsub\n"))
	s.flush()
	if s.total != 12 || s.bad {
		t.Errorf("total = %d, bad = %v; want 12, false", s.total, s.bad)
	}
	s.Write([]byte("100644 blob cccc     BAD\tgone\n"))
	s.flush()
	if s.total != 12 || !s.bad {
		t.Errorf("total = %d, bad = %v; want 12, true", s.total, s.bad)
	}
}

// Host git is not run when the caller says so (g nil), nor in a repo whose
// config may declare a promisor remote: the tree size is then unknown and the
// estimate is partial (#371 S1).
func TestEstimateCopyRunsNoGitWhenToldOrForAPromisor(t *testing.T) {
	of := makeOf(t, false)
	e, err := EstimateCopy(context.Background(), nil, of, t.TempDir())
	if err != nil || !e.Partial || e.TreeBytes != 0 || e.GitBytes <= 0 {
		t.Fatalf("no git: %+v, %v; want partial, tree 0, git > 0", e, err)
	}
	g := newRec(t)
	fixtureGit(t, of, "config", "remote.origin.promisor", "true")
	e, err = EstimateCopy(context.Background(), g, of, t.TempDir())
	if err != nil || !e.Partial || e.TreeBytes != 0 {
		t.Fatalf("promisor: %+v, %v; want partial, tree 0", e, err)
	}
	if len(g.calls) != 0 {
		t.Errorf("git ran in a promisor repo: %v", g.calls)
	}
}

func TestDeclaresPromisor(t *testing.T) {
	cfg := func(t *testing.T, body string) string {
		of := t.TempDir()
		if err := os.MkdirAll(filepath.Join(of, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(of, ".git", "config"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return of
	}
	for _, c := range []struct {
		name, body string
		want       bool
	}{
		{"plain", "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = /x\n", false},
		{"partialClone", "[extensions]\n\tpartialClone = origin\n", true},
		{"promisor any case", "[remote \"o\"]\n\tProMisor\n", true},
	} {
		if got := declaresPromisor(cfg(t, c.body)); got != c.want {
			t.Errorf("%s: declaresPromisor = %v, want %v", c.name, got, c.want)
		}
	}
	// A config that is not a plain file is a yes: git would read what it points at.
	of := t.TempDir()
	os.MkdirAll(filepath.Join(of, ".git"), 0o755)
	target := filepath.Join(t.TempDir(), "cfg")
	os.WriteFile(target, []byte("[core]\n"), 0o644)
	if err := os.Symlink(target, filepath.Join(of, ".git", "config")); err != nil {
		t.Fatal(err)
	}
	if !declaresPromisor(of) {
		t.Error("a symlinked config must count as a promisor")
	}
	// No repo at all is a no: git then reports it.
	if declaresPromisor(filepath.Join(t.TempDir(), "missing")) {
		t.Error("a missing repo must not count as a promisor")
	}
}

func TestFreeBytesUsesNearestExistingAncestor(t *testing.T) {
	base := t.TempDir()
	a, err := freeBytes(base)
	if err != nil || a <= 0 {
		t.Fatalf("freeBytes(existing) = %d, %v", a, err)
	}
	b, err := freeBytes(filepath.Join(base, "x", "y", "z"))
	if err != nil || b <= 0 {
		t.Fatalf("freeBytes(missing below existing) = %d, %v", b, err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB", 2 << 40: "2.0 TiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// noGit fails the test if the clone path runs git at all.
type noGit struct{ t *testing.T }

func (n noGit) Run(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
	n.t.Errorf("git ran: %v", args)
	return nil, errors.New("no git expected")
}

func TestCreateRefusesNotEnough(t *testing.T) {
	of := makeOf(t, false)
	parent := t.TempDir()
	c := config.Clone{Of: of, Dir: filepath.Join(parent, "box-clones", "work")}
	est := func(_ context.Context, gotOf, gotDir string) (Estimate, error) {
		if gotOf != of || gotDir != c.Dir {
			t.Errorf("estimate asked for %q, %q", gotOf, gotDir)
		}
		return Estimate{GitBytes: 3 << 30, TreeBytes: 2 << 30, FreeBytes: 1 << 30, Enough: false}, nil
	}
	created, err := ensureClone(context.Background(), noGit{t}, c, t.TempDir(), est)
	if created || !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("created = %v, err = %v; want a refusal with ErrBoxUnavailable", created, err)
	}
	for _, want := range []string{"3.0 GiB", "2.0 GiB", "1.0 GiB is free", "6.0 GiB"} { // .git, files, free; the total with margin
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
		}
	}
	if _, serr := os.Lstat(filepath.Join(parent, "box-clones")); !os.IsNotExist(serr) {
		t.Errorf("a refused clone must create nothing, Lstat = %v", serr)
	}
}

func TestCreateEstimateErrorDoesNotBlock(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, false)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "box-clones", "work")}
	est := func(context.Context, string, string) (Estimate, error) {
		return Estimate{}, errors.New("statfs failed")
	}
	if created, err := ensureClone(context.Background(), g, c, t.TempDir(), est); err != nil || !created {
		t.Fatalf("created = %v, err = %v; an estimate that fails must not block the clone", created, err)
	}
}

func TestCreateEnoughProceedsAndExistingCloneIsNotEstimated(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, false)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "box-clones", "work")}
	calls := 0
	est := func(context.Context, string, string) (Estimate, error) {
		calls++
		return Estimate{Enough: true}, nil
	}
	if created, err := ensureClone(context.Background(), g, c, t.TempDir(), est); err != nil || !created || calls != 1 {
		t.Fatalf("created = %v, err = %v, estimates = %d; want true, nil, 1", created, err, calls)
	}
	// The second spawn finds the clone; only a first clone is judged by the disk.
	notEnough := func(context.Context, string, string) (Estimate, error) {
		calls++
		return Estimate{Enough: false}, nil
	}
	if created, err := ensureClone(context.Background(), g, c, t.TempDir(), notEnough); err != nil || created || calls != 1 {
		t.Fatalf("existing clone: created = %v, err = %v, estimates = %d; want false, nil, 1", created, err, calls)
	}
}

func TestPrepareRefusesNotEnough(t *testing.T) {
	f := newShareFixture(t)
	e := f.env(t)
	e.Estimate = func(context.Context, string, string) (Estimate, error) {
		return Estimate{GitBytes: 1, Enough: false}, nil
	}
	if _, _, err := Prepare(context.Background(), e, f.c, false); !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "not enough free disk") {
		t.Fatalf("Prepare = %v, want the not-enough refusal", err)
	}
}
