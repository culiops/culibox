package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// shareFixture is a repo with the agent-box shares on disk (untracked, as the
// local-only CLAUDE.md and docs/superpowers are), a memory root holding the
// repo's project memory, and a not-yet-created clone dir.
type shareFixture struct {
	of, dir, home, mem string
	c                  config.Clone
}

func newShareFixture(t *testing.T) shareFixture {
	t.Helper()
	of := makeOf(t, true)
	mustWrite(t, filepath.Join(of, "CLAUDE.md"), "rules")
	mustWrite(t, filepath.Join(of, "docs", "superpowers", "plan.md"), "plan")
	mem := t.TempDir()
	mustWrite(t, filepath.Join(mem, "projects", claudeProjectSlug(of), "memory", "MEMORY.md"), "m")
	dir := filepath.Join(t.TempDir(), "box-clones", "work")
	return shareFixture{
		of: of, dir: dir, home: t.TempDir(), mem: mem,
		c: config.Clone{Of: of, Dir: dir, Shares: []string{"CLAUDE.md", "docs/superpowers"}},
	}
}

func (f shareFixture) env(t *testing.T) Env {
	t.Helper()
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	return Env{Git: g, Home: f.home, MemoryRoot: f.mem}
}

// ensure creates the clone up front, for cases that plant something in it.
func (f shareFixture) ensure(t *testing.T) {
	t.Helper()
	if _, err := EnsureClone(context.Background(), f.env(t).Git, f.c, f.home); err != nil {
		t.Fatal(err)
	}
}

func TestCheckOfRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, of string) (path, home string, protected []box.ProtectedDir)
	}{
		{"of is a symlink", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			link := filepath.Join(t.TempDir(), "link")
			mustSymlink(t, of, link)
			return link, t.TempDir(), nil
		}},
		{".git is a file", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			if err := os.RemoveAll(filepath.Join(of, ".git")); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(of, ".git"), "gitdir: /elsewhere\n")
			return of, t.TempDir(), nil
		}},
		{".git/commondir", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			mustWrite(t, filepath.Join(of, ".git", "commondir"), "../other\n")
			return of, t.TempDir(), nil
		}},
		{".git/objects/info/alternates", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			mustWrite(t, filepath.Join(of, ".git", "objects", "info", "alternates"), "/elsewhere/objects\n")
			return of, t.TempDir(), nil
		}},
		{"of is home", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			return of, of, nil
		}},
		{"of is the parent of home", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			return of, filepath.Join(of, "sub"), nil
		}},
		{"of is /", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			return "/", t.TempDir(), nil
		}},
		{"of/.git inside a protected dir", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			return of, t.TempDir(), []box.ProtectedDir{{Path: of, What: "a protected dir", Why: "test", Inside: true}}
		}},
		{"of does not exist", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			return filepath.Join(of, "nope"), t.TempDir(), nil
		}},
		// A config that cannot be read as git reads it: nothing in it can be checked.
		{".git/config a symlink", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			target := filepath.Join(t.TempDir(), "config")
			mustWrite(t, target, "[core]\n")
			if err := os.Remove(filepath.Join(of, ".git", "config")); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, target, filepath.Join(of, ".git", "config"))
			return of, t.TempDir(), nil
		}},
		{".git/config git would reject", func(t *testing.T, of string) (string, string, []box.ProtectedDir) {
			mustWrite(t, filepath.Join(of, ".git", "config"), "[core\n[remote \"o\"]\n\tpromisor = true\n")
			return of, t.TempDir(), nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			of := makeOf(t, false)
			path, home, protected := tc.setup(t, of)
			err := CheckOf(path, home, protected)
			if !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("CheckOf = %v, want ErrUnboxableWorkspace", err)
			}
		})
	}
	for _, home := range []string{"", "relative/home"} {
		t.Run("home "+strconv.Quote(home), func(t *testing.T) {
			if err := CheckOf(makeOf(t, false), home, nil); !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("CheckOf with home %q = %v, want ErrUnboxableWorkspace", home, err)
			}
		})
	}
	// A partial clone is a working clone_of (the daemon refuses one only where a
	// box may have written its config); a large config is still read; an
	// [include] is not followed (a founder trade-off, stated in SECURITY.md).
	t.Run("a partial clone and an include and a large config pass", func(t *testing.T) {
		for _, set := range [][2]string{{"extensions.partialClone", "origin"}, {"remote.origin.promisor", "true"},
			{"remote.origin.partialclonefilter", "blob:none"}, {"include.path", "/elsewhere/config"}} {
			of := makeOf(t, false)
			fixtureGit(t, of, "config", set[0], set[1])
			if err := CheckOf(of, t.TempDir(), nil); err != nil {
				t.Errorf("%s=%s: CheckOf = %v, want nil", set[0], set[1], err)
			}
		}
		of := makeOf(t, false)
		f, err := os.OpenFile(filepath.Join(of, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2000; i++ { // ~100 KiB of branch sections, over a clone's 64 KiB bound
			fmt.Fprintf(f, "[branch \"b%04d\"]\n\tremote = origin\n\tmerge = refs/heads/b%04d\n", i, i)
		}
		f.Close()
		if err := CheckOf(of, t.TempDir(), nil); err != nil {
			t.Errorf("a large config: CheckOf = %v, want nil", err)
		}
	})
	t.Run("a plain repo passes", func(t *testing.T) {
		of := makeOf(t, false)
		if err := CheckOf(of, t.TempDir(), nil); err != nil {
			t.Fatalf("CheckOf = %v", err)
		}
	})
}

func roByBox(ms []box.Mount) map[string]box.Mount {
	out := map[string]box.Mount{}
	for _, m := range ms {
		out[m.Box] = m
	}
	return out
}

func TestPrepareMounts(t *testing.T) {
	f := newShareFixture(t)
	cm, _, err := Prepare(context.Background(), f.env(t), f.c, true)
	if err != nil {
		t.Fatal(err)
	}
	if cm.Hash != f.c.Hash() {
		t.Errorf("Hash = %q, want %q", cm.Hash, f.c.Hash())
	}
	memBox := "/home/agent/.claude/projects/" + claudeProjectSlug(f.dir) + "/memory"
	want := map[string]box.Mount{
		filepath.Join(f.of, ".git"):                 {Host: filepath.Join(f.of, ".git"), Box: filepath.Join(f.of, ".git")},
		filepath.Join(f.dir, "CLAUDE.md"):           {Host: filepath.Join(f.of, "CLAUDE.md"), Box: filepath.Join(f.dir, "CLAUDE.md")},
		filepath.Join(f.dir, "docs", "superpowers"): {Host: filepath.Join(f.of, "docs", "superpowers"), Box: filepath.Join(f.dir, "docs", "superpowers")},
		memBox: {Host: filepath.Join(f.mem, "projects", claudeProjectSlug(f.of), "memory"), Box: memBox, Optional: true},
	}
	got := roByBox(cm.RO)
	if len(cm.RO) != len(want) || len(got) != len(want) {
		t.Fatalf("RO = %+v, want %d distinct mounts", cm.RO, len(want))
	}
	for dst, w := range want {
		if got[dst] != w {
			t.Errorf("mount at %s = %+v, want %+v", dst, got[dst], w)
		}
	}
	if st, err := os.Lstat(filepath.Join(f.dir, "CLAUDE.md")); err != nil || !st.Mode().IsRegular() || st.Size() != 0 {
		t.Errorf("CLAUDE.md mountpoint: %v %v, want an empty regular file", st, err)
	}
	if st, err := os.Lstat(filepath.Join(f.dir, "docs", "superpowers")); err != nil || !st.IsDir() {
		t.Errorf("docs/superpowers mountpoint: %v %v, want a directory", st, err)
	}
	// A second Prepare (the next spawn) accepts the mountpoints it made.
	if _, _, err := Prepare(context.Background(), f.env(t), f.c, true); err != nil {
		t.Errorf("second Prepare: %v", err)
	}
}

func TestPrepareNoMemory(t *testing.T) {
	cases := []struct {
		name       string
		withMemory bool
		edit       func(t *testing.T, f *shareFixture, e *Env)
	}{
		{"withMemory false", false, func(*testing.T, *shareFixture, *Env) {}},
		{"no memory root", true, func(_ *testing.T, _ *shareFixture, e *Env) { e.MemoryRoot = "" }},
		{"memory dir absent", true, func(t *testing.T, f *shareFixture, _ *Env) {
			if err := os.RemoveAll(filepath.Join(f.mem, "projects")); err != nil {
				t.Fatal(err)
			}
		}},
		{"memory dir a symlink", true, func(t *testing.T, f *shareFixture, _ *Env) {
			p := filepath.Join(f.mem, "projects", claudeProjectSlug(f.of), "memory")
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, t.TempDir(), p)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newShareFixture(t)
			e := f.env(t)
			tc.edit(t, &f, &e)
			cm, _, err := Prepare(context.Background(), e, f.c, tc.withMemory)
			if err != nil {
				t.Fatal(err)
			}
			if len(cm.RO) != 3 {
				t.Errorf("RO = %+v, want .git and the two shares only", cm.RO)
			}
			for _, m := range cm.RO {
				// Not "/home/agent/": in a box Go's TMPDIR lives under /home/agent,
				// so t.TempDir() share mounts would match. Memory is the only
				// mount under the Claude projects dir.
				if strings.HasPrefix(m.Box, "/home/agent/.claude/projects/") {
					t.Errorf("unexpected memory mount %+v", m)
				}
			}
		})
	}
}

func TestPrepareRefuses(t *testing.T) {
	sourceRefusals := []string{"of/docs is a symlink", "missing share source", "share source a FIFO", "share source overlaps a protected dir"}
	cases := []struct {
		name   string
		shares []string
		// setup plants the bad state and returns a dir that must stay empty.
		setup     func(t *testing.T, f shareFixture) (untouched string)
		protected func(f shareFixture) []box.ProtectedDir
		msg       string
	}{
		{"of/docs is a symlink", []string{"docs/superpowers"}, func(t *testing.T, f shareFixture) string {
			real := filepath.Join(f.of, "real-docs")
			if err := os.Rename(filepath.Join(f.of, "docs"), real); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, real, filepath.Join(f.of, "docs"))
			return ""
		}, nil, "is a symlink"},
		{"missing share source", []string{"nope"}, func(*testing.T, shareFixture) string { return "" }, nil, "nope"},
		{"share source a FIFO", []string{"fifo"}, func(t *testing.T, f shareFixture) string {
			if err := syscall.Mkfifo(filepath.Join(f.of, "fifo"), 0o644); err != nil {
				t.Fatal(err)
			}
			return ""
		}, nil, "directory or a regular file"},
		{"clone docs is a symlink", []string{"docs/superpowers"}, func(t *testing.T, f shareFixture) string {
			f.ensure(t)
			elsewhere := t.TempDir()
			mustSymlink(t, elsewhere, filepath.Join(f.dir, "docs"))
			return elsewhere
		}, nil, "docs is a symlink (the box may have planted it); remove it and spawn again"},
		{"clone CLAUDE.md is a directory", []string{"CLAUDE.md"}, func(t *testing.T, f shareFixture) string {
			f.ensure(t)
			mustMkdir(t, filepath.Join(f.dir, "CLAUDE.md"))
			return ""
		}, nil, "CLAUDE.md"},
		{"clone CLAUDE.md is a symlink", []string{"CLAUDE.md"}, func(t *testing.T, f shareFixture) string {
			f.ensure(t)
			elsewhere := t.TempDir()
			mustSymlink(t, filepath.Join(elsewhere, "x"), filepath.Join(f.dir, "CLAUDE.md"))
			return elsewhere
		}, nil, "is a symlink"},
		{"clone docs is a file", []string{"docs/superpowers"}, func(t *testing.T, f shareFixture) string {
			f.ensure(t)
			mustWrite(t, filepath.Join(f.dir, "docs"), "x")
			return ""
		}, nil, "docs"},
		{"share source overlaps a protected dir", []string{"docs/superpowers"}, func(*testing.T, shareFixture) string { return "" },
			func(f shareFixture) []box.ProtectedDir {
				return []box.ProtectedDir{{Path: filepath.Join(f.of, "docs"), What: "a protected dir", Why: "test", Inside: true}}
			}, "protected dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newShareFixture(t)
			f.c.Shares = tc.shares
			untouched := tc.setup(t, f)
			e := f.env(t)
			if tc.protected != nil {
				e.Protected = tc.protected(f)
			}
			_, _, err := Prepare(context.Background(), e, f.c, true)
			if !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("Prepare = %v, want ErrUnboxableWorkspace", err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q does not mention %q", err, tc.msg)
			}
			if untouched != "" {
				if ents, _ := os.ReadDir(untouched); len(ents) != 0 {
					t.Errorf("a mountpoint was created through the link: %s holds %d entries", untouched, len(ents))
				}
			}
			// A share whose source is refused gets no mountpoint in the clone.
			if slices.Contains(sourceRefusals, tc.name) {
				for _, rel := range tc.shares {
					if _, err := os.Lstat(filepath.Join(f.dir, rel)); err == nil {
						t.Errorf("mountpoint %s created for a refused source", rel)
					}
				}
			}
		})
	}
}

func TestCheckProfile(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(t *testing.T, f *shareFixture)
		allow bool
	}{
		{"absent dir and absent share source", func(_ *testing.T, f *shareFixture) {
			f.c.Shares = append(f.c.Shares, "not-there-yet")
		}, true},
		{"existing clone dir", func(t *testing.T, f *shareFixture) { mustMkdir(t, f.dir) }, true},
		{"dir is a file", func(t *testing.T, f *shareFixture) { mustWrite(t, f.dir, "x") }, false},
		{"dir is a symlink", func(t *testing.T, f *shareFixture) {
			mustMkdir(t, filepath.Dir(f.dir))
			mustSymlink(t, t.TempDir(), f.dir)
		}, false},
		{"existing share source is a symlink", func(t *testing.T, f *shareFixture) {
			if err := os.Remove(filepath.Join(f.of, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, filepath.Join(t.TempDir(), "secret"), filepath.Join(f.of, "CLAUDE.md"))
		}, false},
		{"of fails CheckOf", func(t *testing.T, f *shareFixture) {
			mustWrite(t, filepath.Join(f.of, ".git", "commondir"), "x")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newShareFixture(t)
			tc.edit(t, &f)
			err := CheckProfile(f.c, f.home, nil)
			if tc.allow && err != nil {
				t.Fatalf("CheckProfile = %v, want nil", err)
			}
			if !tc.allow && !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("CheckProfile = %v, want ErrUnboxableWorkspace", err)
			}
		})
	}
}

func TestMemoryMountNeedsAnAbsoluteRoot(t *testing.T) {
	f := newShareFixture(t)
	if _, ok := MemoryMount(f.mem, f.of, f.dir); !ok {
		t.Fatal("absolute memory root: no mount")
	}
	// A relative root would be Lstat'd against the daemon's cwd: run from a dir
	// where it resolves, so only the IsAbs check can refuse it.
	rel, err := filepath.Rel(filepath.Dir(f.mem), f.mem)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(f.mem))
	for _, root := range []string{"", rel} {
		if m, ok := MemoryMount(root, f.of, f.dir); ok {
			t.Errorf("MemoryMount(%q) = %+v, want none", root, m)
		}
	}
}

func TestClaudeProjectSlug(t *testing.T) {
	for in, want := range map[string]string{
		"/home/user/culiops-agent-box/.worktrees/m3a": "-home-user-culiops-agent-box--worktrees-m3a",
		"/home/user/box-clones/culiops-agent-box":     "-home-user-box-clones-culiops-agent-box",
	} {
		if got := claudeProjectSlug(in); got != want {
			t.Errorf("claudeProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrepareReportsCreated(t *testing.T) {
	f := newShareFixture(t)
	for i, want := range []bool{true, false} {
		_, created, err := Prepare(context.Background(), f.env(t), f.c, false)
		if err != nil {
			t.Fatal(err)
		}
		if created != want {
			t.Errorf("Prepare #%d: created = %v, want %v", i+1, created, want)
		}
	}
}

func TestPrepareStartsTrustedRecord(t *testing.T) {
	f := newShareFixture(t)
	e := f.env(t)
	e.DataDir = t.TempDir()
	_, tip, err := BaseBranch(context.Background(), e.Git, f.of)
	if err != nil || tip == "" {
		t.Fatalf("BaseBranch = %q, %v", tip, err)
	}
	if _, _, err := Prepare(context.Background(), e, f.c, false); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadBaseRecord(e.DataDir, f.dir)
	if err != nil || !rec.Exists || rec.Unverified || !slices.Equal(rec.SHAs, []string{tip}) {
		t.Fatalf("rec=%+v err=%v; want trusted [%s]", rec, err, tip)
	}
}

func TestPrepareResetsRecordOnRecreate(t *testing.T) {
	f := newShareFixture(t)
	e := f.env(t)
	e.DataDir = t.TempDir()
	_, tip, err := BaseBranch(context.Background(), e.Git, f.of)
	if err != nil || tip == "" {
		t.Fatalf("BaseBranch = %q, %v", tip, err)
	}
	// A stale record for the same dir: unverified, with a SHA the host never had.
	if err := AppendBase(e.DataDir, f.dir, shaN(7)); err != nil {
		t.Fatal(err)
	}
	if rec, _ := ReadBaseRecord(e.DataDir, f.dir); !rec.Unverified {
		t.Fatalf("setup: want an unverified record, got %+v", rec)
	}
	if _, created, err := Prepare(context.Background(), e, f.c, false); err != nil || !created {
		t.Fatalf("Prepare created=%v err=%v", created, err)
	}
	rec, err := ReadBaseRecord(e.DataDir, f.dir)
	if err != nil || rec.Unverified || !slices.Equal(rec.SHAs, []string{tip}) {
		t.Fatalf("rec=%+v err=%v; want trusted [%s]", rec, err, tip)
	}
}

// A new clone made while the host has no base branch has no tip to vouch for
// it, so an older record for the same dir must go, not survive as trusted.
func TestPrepareDropsStaleRecordWithoutTip(t *testing.T) {
	f := newShareFixture(t)
	fixtureGit(t, f.of, "branch", "-M", "trunk")
	e := f.env(t)
	e.DataDir = t.TempDir()
	if err := ResetBaseRecord(e.DataDir, f.dir, shaN(7)); err != nil {
		t.Fatal(err)
	}
	if _, created, err := Prepare(context.Background(), e, f.c, false); err != nil || !created {
		t.Fatalf("Prepare created=%v err=%v", created, err)
	}
	if rec, err := ReadBaseRecord(e.DataDir, f.dir); err != nil || rec.Exists {
		t.Fatalf("rec=%+v err=%v; want no record", rec, err)
	}
	// With no record to remove, the removal is quiet.
	if err := removeBaseRecord(e.DataDir, f.dir); err != nil {
		t.Fatalf("removeBaseRecord on a missing record: %v", err)
	}
}

func TestPrepareNoRecordWithoutDataDir(t *testing.T) {
	f := newShareFixture(t)
	e := f.env(t)
	// An empty DataDir must not become a relative path in the working directory.
	t.Chdir(t.TempDir())
	if _, _, err := Prepare(context.Background(), e, f.c, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(baseRecordDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(%s) = %v, want not exist", baseRecordDir, err)
	}
}

func TestPrepareSkipsUnsharedMemoryMount(t *testing.T) {
	f := newShareFixture(t)
	e := f.env(t)
	var asked []string
	e.Visible = func(host string) bool { asked = append(asked, host); return false }
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for i := 0; i < 2; i++ {
		cm, _, err := Prepare(context.Background(), e, f.c, true)
		if err != nil {
			t.Fatalf("prepare %d: %v", i, err)
		}
		for _, m := range cm.RO {
			if strings.HasPrefix(m.Box, "/home/agent/.claude/projects/") {
				t.Errorf("unshared memory folder was mounted: %+v", m)
			}
		}
	}
	host := MemoryDir(f.mem, f.of)
	if len(asked) != 2 || asked[0] != host {
		t.Errorf("Visible asked %q, want the memory dir %q twice", asked, host)
	}
	want := "clone: the clone memory folder " + host + " is not shared with the box VM; boxes start without it (add it in Settings › VM folders)"
	if got := strings.Count(buf.String(), want); got != 1 {
		t.Errorf("log line count = %d, want 1; log:\n%s", got, buf.String())
	}
	// A visible folder is still mounted.
	e.Visible = func(string) bool { return true }
	cm, _, err := Prepare(context.Background(), e, f.c, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cm.RO) != 4 {
		t.Errorf("RO = %+v, want .git, two shares and the memory mount", cm.RO)
	}
}

// #235 review: clone_of and share sources are read-only binds, so a program
// folder in them does not refuse them (a config folder still does).
func TestCheckOfAndShareSourceAcceptAProgramDir(t *testing.T) {
	of := makeOf(t, false)
	if err := os.MkdirAll(filepath.Join(of, "tmp", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		dir string
		run func(p []box.ProtectedDir) error
	}{
		"clone_of .git": {filepath.Join(of, ".git"), func(p []box.ProtectedDir) error { return CheckOf(of, t.TempDir(), p) }},
		"share source":  {filepath.Join(of, "tmp", "sub"), func(p []box.ProtectedDir) error { _, err := shareSource(of, "tmp", p); return err }},
	} {
		if err := c.run([]box.ProtectedDir{box.ProgramDirProtected(c.dir)}); err != nil {
			t.Errorf("%s with a program folder: %v", name, err)
		}
		if err := c.run([]box.ProtectedDir{box.ConfigDirProtected(c.dir)}); !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Errorf("%s: a config folder must still refuse: %v", name, err)
		}
	}
}

// A real partial clone (git clone --filter=blob:none) is a working clone_of: the
// first copy is made from it with the host git and has HEAD checked out (#371,
// the regression the second follow-up fixed).
func TestARealPartialCloneIsACloneOf(t *testing.T) {
	g := newRec(t)
	src := makeOf(t, false)
	fixtureGit(t, src, "config", "uploadpack.allowFilter", "true")
	base := t.TempDir()
	of := filepath.Join(base, "partial")
	// Built with lazy fetching on: the partial clone's own checkout fetches HEAD's blobs.
	env := append(lazyEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	if _, err := g.g.Run(context.Background(), "", env, "clone", "-q", "--filter=blob:none", "file://"+src, of); err != nil {
		t.Fatal(err)
	}
	if why := PartialCloneWhy(of); why == "" {
		t.Fatal("the fixture is not a partial clone")
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckOf(of, home, nil); err != nil {
		t.Fatalf("CheckOf of a partial clone = %v, want nil", err)
	}
	c := config.Clone{Of: of, Dir: filepath.Join(base, "copy")}
	created, err := EnsureClone(context.Background(), g, c, home)
	if err != nil || !created {
		t.Fatalf("EnsureClone = %v, %v; want a new copy", created, err)
	}
	if b, err := os.ReadFile(filepath.Join(c.Dir, "a")); err != nil || string(b) != "a" {
		t.Fatalf("the copy's work tree: %q, %v", b, err)
	}
}

// PartialCloneWhy reads .git/config, and .git/config.worktree when
// extensions.worktreeConfig is on, as git would (#371).
func TestPartialCloneWhy(t *testing.T) {
	repo := func(t *testing.T, cfg, wt string) string {
		of := t.TempDir()
		mustWrite(t, filepath.Join(of, ".git", "config"), cfg)
		if wt != "" {
			mustWrite(t, filepath.Join(of, ".git", "config.worktree"), wt)
		}
		return of
	}
	const promisor = "[remote \"o\"]\n\tpromisor = true\n"
	for _, c := range []struct {
		name, cfg, wt string
		want          bool
	}{
		{"plain", "[core]\n\tbare = false\n", "", false},
		{"partialClone", "[extensions]\n\tpartialClone = o\n", "", true},
		{"(B) an unnamed [remote]", "[remote]\n\tpromisor = true\n", "", true},
		{"(C) [remote \"\"]", "[remote \"\"]\n\tpromisor = true\n", "", true},
		{"(F) partialclonefilter", "[remote \"o\"]\n\tpartialclonefilter = blob:none\n", "", true},
		{"promisor = 0x0 is false", "[remote \"o\"]\n\tpromisor = 0x0\n", "", false},
		{"(D) config.worktree with worktreeConfig on", "[extensions]\n\tworktreeConfig = true\n", promisor, true},
		{"config.worktree with worktreeConfig off is not read", "[core]\n", promisor, false},
		{"a config git would reject", "[core\n", "", true},
		{"a config.worktree git would reject", "[extensions]\n\tworktreeConfig\n", "[core\n", true},
	} {
		if got := PartialCloneWhy(repo(t, c.cfg, c.wt)) != ""; got != c.want {
			t.Errorf("%s: PartialCloneWhy != \"\" is %v, want %v", c.name, got, c.want)
		}
	}
	of := repo(t, "[extensions]\n\tworktreeConfig = 1\n", "")
	target := filepath.Join(t.TempDir(), "wt")
	mustWrite(t, target, promisor)
	mustSymlink(t, target, filepath.Join(of, ".git", "config.worktree"))
	if PartialCloneWhy(of) == "" {
		t.Error("a symlinked config.worktree must not pass")
	}
	if why := PartialCloneWhy(filepath.Join(t.TempDir(), "missing")); why != "" {
		t.Errorf("a missing repo: %q, want \"\" (the other checks report it)", why)
	}
}
