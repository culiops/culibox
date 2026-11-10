package clone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

type call struct {
	dir  string
	env  []string
	args []string
}

// recGit records every call and forwards it to the real git.
type recGit struct {
	g     Git
	mu    sync.Mutex
	calls []call
}

func (r *recGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call{dir, slices.Clone(env), slices.Clone(args)})
	r.mu.Unlock()
	return r.g.Run(ctx, dir, env, args...)
}

func (r *recGit) reset() { r.mu.Lock(); r.calls = nil; r.mu.Unlock() }

func newRec(t *testing.T) *recGit {
	t.Helper()
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	return &recGit{g: g}
}

// fixtureGit runs setup git commands (not under test), with an author in the env.
func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	env := append(CleanEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	if _, err := g.Run(context.Background(), dir, env, args...); err != nil {
		t.Fatal(err)
	}
}

// makeOf builds a repo with two commits; identity adds repo-local user.name/email.
func makeOf(t *testing.T, identity bool) string {
	t.Helper()
	of := filepath.Join(t.TempDir(), "of")
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, of, "init", "-q")
	if identity {
		fixtureGit(t, of, "config", "user.name", "Local Name")
		fixtureGit(t, of, "config", "user.email", "local@example.com")
	}
	for _, f := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(of, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, of, "add", f)
		fixtureGit(t, of, "commit", "-q", "-m", f)
	}
	return of
}

func gitConfigIn(t *testing.T, g Git, dir, key string) (string, error) {
	t.Helper()
	out, err := g.Run(context.Background(), dir, CleanEnv(), "config", key)
	return strings.TrimSpace(string(out)), err
}

func TestEnsureCloneCreatesWithoutHardlinks(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "box-clones", "work")}
	created, err := EnsureClone(context.Background(), rec, c, t.TempDir())
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	n := 0
	err = filepath.WalkDir(filepath.Join(c.Dir, ".git", "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(c.Dir, p)
		src, serr := os.Stat(filepath.Join(of, rel))
		if serr != nil {
			return nil // a pack or file the source lacks cannot be shared with it
		}
		dst, _ := os.Stat(p)
		n++
		if os.SameFile(src, dst) {
			t.Errorf("%s shares an inode with the source repo", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no object file had a counterpart in the source; the hardlink check checked nothing")
	}
	hooks, _ := os.ReadDir(filepath.Join(c.Dir, ".git", "hooks"))
	if len(hooks) != 0 {
		t.Errorf("hooks dir has %d entries, want none (--template=)", len(hooks))
	}
	if got, err := gitConfigIn(t, rec, c.Dir, "remote.origin.url"); err != nil || got != originURL(of) {
		t.Errorf("origin = %q, %v; want %q", got, err, originURL(of))
	}
	for k, want := range map[string]string{"user.name": "Local Name", "user.email": "local@example.com"} {
		if got, err := gitConfigIn(t, rec, c.Dir, k); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", k, got, err, want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(c.Dir), ".agent-box-clone-*")); len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}

func TestEnsureCloneIdentityFallsBackToGlobal(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, false)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = G\n\temail = g@x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "c1")}
	if _, err := EnsureClone(context.Background(), rec, c, home); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"user.name": "G", "user.email": "g@x"} {
		if got, err := gitConfigIn(t, rec, c.Dir, k); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", k, got, err, want)
		}
	}
	// The global lookups: the user's HOME and no GIT_CONFIG_GLOBAL, nothing else changed.
	seen := 0
	for _, cl := range rec.calls {
		if !slices.Contains(cl.args, "--global") {
			continue
		}
		seen++
		if !slices.Contains(cl.env, "HOME="+home) || slices.Contains(cl.env, "HOME=/nonexistent") {
			t.Errorf("global lookup env %v: want HOME=%s", cl.env, home)
		}
		for _, kv := range cl.env {
			if strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
				t.Errorf("global lookup env still has %s", kv)
			}
		}
		if !slices.Contains(cl.env, "GIT_CONFIG_NOSYSTEM=1") {
			t.Errorf("global lookup env lost GIT_CONFIG_NOSYSTEM: %v", cl.env)
		}
	}
	if seen != 2 {
		t.Errorf("%d global lookups, want 2", seen)
	}

	// Neither a local nor a global identity: nothing is written.
	c2 := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "c2")}
	if _, err := EnsureClone(context.Background(), rec, c2, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_, err := gitConfigIn(t, rec, c2.Dir, "user.name")
	if code, ok := exitCode(err); !ok || code != 1 {
		t.Errorf("user.name in a clone with no identity anywhere: err=%v, want exit 1", err)
	}
}

func TestEnsureCloneAcceptsExisting(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "c")}
	if created, err := EnsureClone(context.Background(), rec, c, t.TempDir()); err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	rec.reset()
	created, err := EnsureClone(context.Background(), rec, c, t.TempDir())
	if err != nil || created {
		t.Fatalf("second: created=%v err=%v", created, err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("an existing clone cost %d git calls, want 0: %v", len(rec.calls), rec.calls)
	}
}

func TestEnsureCloneRefusesExisting(t *testing.T) {
	const of = "/srv/of"
	cfgFor := func(urls ...string) string {
		s := "[core]\n\tbare = false\n[remote \"origin\"]\n"
		for _, u := range urls {
			s += "\turl = " + u + "\n"
		}
		return s
	}
	// plain builds dir/.git/config; every case starts from it and breaks one thing.
	plain := func(t *testing.T, dir, config string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		build func(t *testing.T, dir string)
		ok    bool
	}{
		{"valid", func(t *testing.T, dir string) { plain(t, dir, cfgFor(of)) }, true},
		{"origin elsewhere", func(t *testing.T, dir string) { plain(t, dir, cfgFor("/elsewhere")) }, false},
		{"origin with the .git suffix", func(t *testing.T, dir string) { plain(t, dir, cfgFor(of+"/.git")) }, false},
		{"no origin", func(t *testing.T, dir string) { plain(t, dir, "[core]\n\tbare = false\n") }, false},
		{"two origin urls", func(t *testing.T, dir string) { plain(t, dir, cfgFor(of, of)) }, false},
		{"unparsable config", func(t *testing.T, dir string) { plain(t, dir, "[remote \"origin\"\n\turl = "+of+"\n") }, false},
		{".git is a symlink", func(t *testing.T, dir string) {
			plain(t, dir+".real", cfgFor(of))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir+".real", ".git"), filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{".git is a gitdir: file", func(t *testing.T, dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{".git/config is a symlink", func(t *testing.T, dir string) {
			plain(t, dir+".real", cfgFor(of))
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir+".real", ".git", "config"), filepath.Join(dir, ".git", "config")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"dir is a symlink to a valid clone", func(t *testing.T, dir string) {
			plain(t, dir+".real", cfgFor(of))
			if err := os.Symlink(dir+".real", dir); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"dir is a regular file", func(t *testing.T, dir string) {
			if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"no .git at all", func(t *testing.T, dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o644); err != nil {
				t.Fatal(err) // non-empty: an empty dir now counts as "not created yet"
			}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRec(t)
			dir := filepath.Join(t.TempDir(), "c")
			tc.build(t, dir)
			created, err := EnsureClone(context.Background(), rec, config.Clone{Of: of, Dir: dir}, t.TempDir())
			if created {
				t.Error("created = true for an existing dir")
			}
			if len(rec.calls) != 0 {
				t.Errorf("%d git calls, want 0: %v", len(rec.calls), rec.calls)
			}
			if tc.ok {
				if err != nil {
					t.Fatalf("valid clone refused: %v", err)
				}
				return
			}
			if !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("err = %v, want ErrUnboxableWorkspace", err)
			}
			if !strings.Contains(err.Error(), "move it away and agent-box will create the clone") {
				t.Errorf("message lacks the action: %v", err)
			}
		})
	}
}

func TestEnsureCloneCleansUpOnFailure(t *testing.T) {
	rec := newRec(t)
	parent := t.TempDir()
	notRepo := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(notRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	c := config.Clone{Of: notRepo, Dir: filepath.Join(parent, "c")}
	created, err := EnsureClone(context.Background(), rec, c, t.TempDir())
	if created || !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("created=%v err=%v, want ErrBoxUnavailable", created, err)
	}
	if left, _ := filepath.Glob(filepath.Join(parent, ".agent-box-clone-*")); len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
	if _, err := os.Lstat(c.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("c.Dir exists after a failed clone: %v", err)
	}
}

func TestEnsureCloneGitArgv(t *testing.T) {
	t.Setenv("AB_SENTINEL", "x")
	rec := newRec(t)
	of := makeOf(t, true) // local identity: no global lookups
	parent := t.TempDir()
	c := config.Clone{Of: of, Dir: filepath.Join(parent, "c")}
	if _, err := EnsureClone(context.Background(), rec, c, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) == 0 || len(rec.calls[0].args) == 0 {
		t.Fatal("no git calls recorded")
	}
	repo := rec.calls[0].args[len(rec.calls[0].args)-1]
	if m, _ := filepath.Match(filepath.Join(parent, ".agent-box-clone-*", "repo"), repo); !m {
		t.Fatalf("clone target %q is not <parent>/.agent-box-clone-*/repo", repo)
	}
	// V1: origin is plain <of>, so there is no `remote set-url` between the clone
	// and the identity lookups.
	want := []string{
		fmt.Sprintf("|clone --no-hardlinks --template= -c core.hooksPath=/dev/null --no-recurse-submodules -- %s %s", of, repo),
		fmt.Sprintf("%s|-c core.hooksPath=/dev/null -c core.fsmonitor=false config --local --get user.name", of),
		fmt.Sprintf("%s|-c core.hooksPath=/dev/null -c core.fsmonitor=false config --local --get user.email", of),
		fmt.Sprintf("%s|config -- user.name Local Name", repo),
		fmt.Sprintf("%s|config -- user.email local@example.com", repo),
	}
	var got []string
	for _, cl := range rec.calls {
		got = append(got, cl.dir+"|"+strings.Join(cl.args, " "))
		if cl.dir != "" && cl.dir != of && cl.dir != repo {
			t.Errorf("git ran in %q; only %q or %q allowed", cl.dir, of, repo)
		}
		if cl.dir == c.Dir || strings.HasPrefix(cl.dir, c.Dir+string(filepath.Separator)) {
			t.Errorf("git ran in the final clone dir %q", cl.dir)
		}
		for _, kv := range cl.env {
			if strings.HasPrefix(kv, "AB_SENTINEL") {
				t.Errorf("process env leaked into git: %q", kv)
			}
		}
		if !slices.Equal(cl.env, CleanEnv()) {
			t.Errorf("env = %v, want CleanEnv()", cl.env)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("git calls:\n got  %q\n want %q", got, want)
	}
}

func TestEnsureCloneConcurrent(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "c")}
	var wg sync.WaitGroup
	results := make([]bool, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = EnsureClone(context.Background(), rec, c, t.TempDir())
		}()
	}
	wg.Wait()
	made := 0
	for i := range results {
		if errs[i] != nil {
			t.Errorf("call %d: %v", i, errs[i])
		}
		if results[i] {
			made++
		}
	}
	if made != 1 {
		t.Errorf("%d calls created the clone, want exactly 1", made)
	}
}

// A variable git reads from the environment must reach it only when the caller
// put it in env: nil/CleanEnv must not fall back to the process environment.
func TestExecGitUsesExactlyTheGivenEnv(t *testing.T) {
	t.Setenv("GIT_CONFIG_PARAMETERS", "'abtest.sentinel=AB_SENTINEL'")
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	ctx := context.Background()
	for name, env := range map[string][]string{"CleanEnv": CleanEnv(), "nil": nil} {
		out, err := g.Run(ctx, "", env, "config", "--get", "abtest.sentinel")
		if code, ok := exitCode(err); !ok || code != 1 {
			t.Errorf("%s: err = %v, want exit 1 (abtest.sentinel unset)", name, err)
		}
		if strings.Contains(string(out), "AB_SENTINEL") {
			t.Errorf("%s: process env reached git: %q", name, out)
		}
	}
	// Control: the same variable passed explicitly IS seen, so the probe can fail.
	out, err := g.Run(ctx, "", append(CleanEnv(), "GIT_CONFIG_PARAMETERS='abtest.sentinel=AB_SENTINEL'"), "config", "--get", "abtest.sentinel")
	if err != nil || !strings.Contains(string(out), "AB_SENTINEL") {
		t.Fatalf("control: out=%q err=%v; the probe cannot see an env var, so the test proves nothing", out, err)
	}
}

func TestEnsureCloneRefusesSymlinkInParentChain(t *testing.T) {
	const of = "/srv/of"
	mkTarget := func(t *testing.T) (base, target string) {
		base = t.TempDir()
		target = filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		return base, target
	}
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, base, target string) string // returns c.Dir
	}{
		{"absent dir, parent is a link", func(t *testing.T, base, target string) string {
			if err := os.Symlink(target, filepath.Join(base, "link")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(base, "link", "c")
		}},
		{"absent dir, grandparent is a link and the parent does not exist", func(t *testing.T, base, target string) string {
			if err := os.Symlink(target, filepath.Join(base, "link")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(base, "link", "new", "c")
		}},
		{"existing valid clone behind a linked parent", func(t *testing.T, base, target string) string {
			if err := os.MkdirAll(filepath.Join(target, "c", ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			cfg := "[remote \"origin\"]\n\turl = " + of + "\n"
			if err := os.WriteFile(filepath.Join(target, "c", ".git", "config"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(base, "link")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(base, "link", "c")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRec(t)
			base, target := mkTarget(t)
			dir := tc.build(t, base, target)
			before, _ := os.ReadDir(target)
			created, err := EnsureClone(context.Background(), rec, config.Clone{Of: of, Dir: dir}, t.TempDir())
			if created || !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("created=%v err=%v, want ErrUnboxableWorkspace", created, err)
			}
			if !strings.Contains(err.Error(), "is a symlink; agent-box creates clones only under real directories") {
				t.Errorf("message: %v", err)
			}
			if len(rec.calls) != 0 {
				t.Errorf("%d git calls, want 0", len(rec.calls))
			}
			after, _ := os.ReadDir(target)
			if len(after) != len(before) {
				t.Errorf("link target changed: %d entries before, %d after", len(before), len(after))
			}
		})
	}
}

// fakeOriginGit answers every call with out/err and records the calls.
type fakeOriginGit struct {
	out   string
	err   error
	calls []call
}

func (f *fakeOriginGit) Run(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{dir, slices.Clone(env), slices.Clone(args)})
	return []byte(f.out), f.err
}

func TestHostOriginURLUsesHardenedGit(t *testing.T) {
	g := &fakeOriginGit{out: "https://github.com/culiops/agent-box.git\n"}
	got, err := HostOriginURL(context.Background(), g, "/home/u/repo")
	if err != nil || got != "https://github.com/culiops/agent-box.git" {
		t.Fatalf("HostOriginURL = %q, %v", got, err)
	}
	if len(g.calls) != 1 {
		t.Fatalf("%d git calls, want 1", len(g.calls))
	}
	c := g.calls[0]
	if c.dir != "/home/u/repo" {
		t.Fatalf("dir = %q, want the host repo", c.dir)
	}
	if !slices.Contains(c.args, "core.hooksPath=/dev/null") || !slices.Contains(c.args, "core.fsmonitor=false") {
		t.Fatalf("args %q lack the hardening flags", c.args)
	}
	if !slices.Equal(c.env, CleanEnv()) {
		t.Fatalf("env = %q, want CleanEnv", c.env)
	}
	if !slices.Contains(c.args, "remote.origin.url") || !slices.Contains(c.args, "--local") {
		t.Fatalf("args %q do not read the local remote.origin.url", c.args)
	}
}

func TestHostOriginURLUnsetIsEmpty(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true) // a repo with no remotes at all
	got, err := HostOriginURL(context.Background(), rec, of)
	if got != "" || err != nil {
		t.Fatalf("unset origin = %q, %v; want empty, nil", got, err)
	}
	fixtureGit(t, of, "remote", "add", "origin", "git@github.com:culiops/agent-box.git")
	if got, err := HostOriginURL(context.Background(), rec, of); err != nil || got != "git@github.com:culiops/agent-box.git" {
		t.Fatalf("set origin = %q, %v", got, err)
	}
	g := &fakeOriginGit{err: errors.New("boom")}
	if _, err := HostOriginURL(context.Background(), g, "/home/u/repo"); err == nil {
		t.Fatal("a failure other than unset must be an error")
	}
}

func TestEnsureCloneFillsAnEmptyCopyDir(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "box-clones", "work")}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := EnsureClone(context.Background(), rec, c, t.TempDir())
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if st, err := os.Stat(filepath.Join(c.Dir, ".git")); err != nil || !st.IsDir() {
		t.Fatalf(".git missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "a")); err != nil {
		t.Errorf("checked-out file missing: %v", err)
	}
	after, err := os.Stat(c.Dir)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("the copy dir changed inode (err=%v); a VM sharing it would not see the clone", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(c.Dir), ".agent-box-clone-*")); len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
	if got, err := gitConfigIn(t, rec, c.Dir, "remote.origin.url"); err != nil || got != originURL(of) {
		t.Errorf("origin = %q, %v", got, err)
	}
}

func TestEnsureCloneRefusesNonEmptyNonClone(t *testing.T) {
	rec := newRec(t)
	c := config.Clone{Of: makeOf(t, true), Dir: filepath.Join(t.TempDir(), "work")}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureClone(context.Background(), rec, c, t.TempDir())
	if created || !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "no .git directory") {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "stray")); err != nil {
		t.Errorf("stray file touched: %v", err)
	}
}

func TestEnsureCloneRefusesSymlinkedEmptyDir(t *testing.T) {
	rec := newRec(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	c := config.Clone{Of: makeOf(t, true), Dir: filepath.Join(base, "work")}
	if err := os.Symlink(real, c.Dir); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureClone(context.Background(), rec, c, t.TempDir())
	if created || !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if ents, _ := os.ReadDir(real); len(ents) != 0 {
		t.Errorf("the link target was written to: %v", ents)
	}
}
