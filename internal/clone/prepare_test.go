package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
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
		"/home/claude/culiops-agent-box/.worktrees/m3a": "-home-claude-culiops-agent-box--worktrees-m3a",
		"/home/claude/box-clones/culiops-agent-box":     "-home-claude-box-clones-culiops-agent-box",
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
