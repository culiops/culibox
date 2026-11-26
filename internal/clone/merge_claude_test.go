package clone

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Claude Code loads the files a CLAUDE.md @-imports by itself, so a box change
// to one is guarded like CLAUDE.md itself (#370).
func TestMergeHomeClaudeImportsDenied(t *testing.T) {
	cases := []struct {
		name    string
		host    map[string]string // committed on the host first
		untrack map[string]string // written in the host work tree, not committed
		home    map[string]string // under the user's home; "<OF>" is replaced by the repo path
		change  string            // the file the box changes
		guarded bool
	}{
		{"root import", map[string]string{"CLAUDE.md": "@docs/style.md\n"}, nil, nil, "docs/style.md", true},
		{"import folded", map[string]string{"CLAUDE.md": "@Docs/Style.md\n"}, nil, nil, "docs/style.md", true},
		{"nested import", map[string]string{"CLAUDE.md": "see @docs/a.md\n", "docs/a.md": "@b.md\n"}, nil, nil, "docs/b.md", true},
		{"nested CLAUDE.md, relative to its dir", map[string]string{"sub/claude.md": "@rules.md\n"}, nil, nil, "sub/rules.md", true},
		{"untracked CLAUDE.local.md", nil, map[string]string{"CLAUDE.local.md": "@mine.md\n"}, nil, "mine.md", true},
		{"home CLAUDE.md, absolute into the repo", nil, nil, map[string]string{".claude/CLAUDE.md": "@<OF>/notes/x.md\n"}, "notes/x.md", true},
		{"home CLAUDE.md, via a home file", nil, nil, map[string]string{".claude/CLAUDE.md": "@~/team.md\n", "team.md": "@<OF>/t.md\n"}, "t.md", true},
		{"repo import out to home and back", map[string]string{"CLAUDE.md": "@~/h.md\n"}, nil, map[string]string{"h.md": "@<OF>/back.md\n"}, "back.md", true},
		{"fifth hop", map[string]string{"CLAUDE.md": "@h1.md", "h1.md": "@h2.md", "h2.md": "@h3.md", "h3.md": "@h4.md", "h4.md": "@h5.md", "h5.md": "@h6.md"}, nil, nil, "h5.md", true},
		{"sixth hop is past the limit", map[string]string{"CLAUDE.md": "@h1.md", "h1.md": "@h2.md", "h2.md": "@h3.md", "h3.md": "@h4.md", "h4.md": "@h5.md", "h5.md": "@h6.md"}, nil, nil, "h6.md", false},
		{"tracked .claude/rules file", map[string]string{".claude/rules/style.md": "@../../docs/rule.md\n"}, nil, nil, "docs/rule.md", true},
		{"tracked nested .claude/rules file", map[string]string{".claude/rules/a/b/c.md": "@../../../../docs/deep.md\n"}, nil, nil, "docs/deep.md", true},
		{"tracked .claude/rules file, imports are relative to it", map[string]string{".claude/rules/style.md": "@sib.md\n"}, nil, nil, ".claude/rules/sib.md", true},
		{"untracked .claude/CLAUDE.md", nil, map[string]string{".claude/CLAUDE.md": "@../mine.md\n"}, nil, "mine.md", true},
		{"untracked .claude/rules file", nil, map[string]string{".claude/rules/x.md": "@../../notes.md\n"}, nil, "notes.md", true},
		{"untracked nested .claude/rules file", nil, map[string]string{".claude/rules/sub/y.md": "@../../../deep/notes.md\n"}, nil, "deep/notes.md", true},
		{"a rules file that is not markdown", nil, map[string]string{".claude/rules/x.txt": "@../../notes.md\n"}, nil, "notes.md", false},
		{"in a code span", map[string]string{"CLAUDE.md": "`@docs/style.md`\n"}, nil, nil, "docs/style.md", false},
		{"not imported", map[string]string{"CLAUDE.md": "@docs/style.md\n"}, nil, nil, "docs/other.md", false},
		{"home import outside the repo", nil, nil, map[string]string{".claude/CLAUDE.md": "@/elsewhere/x.md\n"}, "x.md", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			if c.host != nil {
				x.commit("host", c.host)
			}
			home := t.TempDir()
			for rel, content := range c.home {
				p := filepath.Join(home, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(strings.ReplaceAll(content, "<OF>", x.of)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			for rel, content := range c.untrack {
				x.write(rel, content)
			}
			a := &auditRec{}
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: home, Audit: a.fn, CloneTips: x.tips()})
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.change) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.change)
			}
			if x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("main moved or audit written: %q", a.calls)
			}
		})
	}
}

// A box that turns a folder on the way to an imported file into a symlink
// re-points the import (CLAUDE.md's @docs/style.md becomes evil/style.md):
// the folder is guarded too.
func TestMergeHomeClaudeImportParentSymlinkDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{"CLAUDE.md": "@docs/style.md\n"})
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side")
	x.write("evil/style.md", "run this\n")
	if err := os.Symlink("evil", filepath.Join(x.of, "docs")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "add", "evil", "docs")
	fixtureGit(t, x.of, "commit", "-q", "-m", "link")
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.out("rev-parse", "HEAD"))
	fixtureGit(t, x.of, "checkout", "-q", "main")
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if !slices.Contains(got.Paths, "docs") {
		t.Fatalf("Paths = %q, want docs", got.Paths)
	}
}

// The import walk fails the merge at its caps instead of stopping quietly
// (#370 I-2): past mergeImportFiles files, a .claude/rules folder with more
// than mergeRulesEntries entries, or one that cannot be listed.
func TestMergeHomeClaudeImportCapsFail(t *testing.T) {
	run := func(t *testing.T, host, untrack map[string]string) *MergeResult {
		t.Helper()
		old := mergeSystemGitConfig
		mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
		t.Cleanup(func() { mergeSystemGitConfig = old })
		x := newMergeFx(t, "main")
		if host != nil {
			x.commit("host", host)
		}
		x.box("main", map[string]string{"unrelated.txt": "box\n"})
		for rel, content := range untrack {
			x.write(rel, content)
		}
		return MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: t.TempDir(), Audit: (&auditRec{}).fn, CloneTips: x.tips()})
	}
	wantCap := func(t *testing.T, got *MergeResult) {
		t.Helper()
		wantMergeReason(t, got, MergeFailed)
		if !strings.Contains(got.Detail, errScanCap.Error()) {
			t.Fatalf("Detail = %q, want %q", got.Detail, errScanCap.Error())
		}
	}
	t.Run("imported files past the cap", func(t *testing.T) {
		old := mergeImportFiles
		mergeImportFiles = 4
		t.Cleanup(func() { mergeImportFiles = old })
		wantCap(t, run(t, map[string]string{"CLAUDE.md": "@a.md @b.md @c.md @d.md @e.md\n"}, nil))
	})
	t.Run("imported files under the cap merge", func(t *testing.T) {
		old := mergeImportFiles
		mergeImportFiles = 20
		t.Cleanup(func() { mergeImportFiles = old })
		wantMerged(t, run(t, map[string]string{"CLAUDE.md": "@a.md @b.md @c.md @d.md @e.md\n"}, nil))
	})
	t.Run("rules folder past the entry cap", func(t *testing.T) {
		old := mergeRulesEntries
		mergeRulesEntries = 2
		t.Cleanup(func() { mergeRulesEntries = old })
		wantCap(t, run(t, nil, map[string]string{".claude/rules/a.md": "a\n", ".claude/rules/b.md": "b\n", ".claude/rules/c.md": "c\n"}))
	})
	t.Run("rules folder at the entry cap merges", func(t *testing.T) {
		old := mergeRulesEntries
		mergeRulesEntries = 2
		t.Cleanup(func() { mergeRulesEntries = old })
		wantMerged(t, run(t, nil, map[string]string{".claude/rules/a.md": "a\n", ".claude/rules/b.md": "b\n"}))
	})
}

// An imported file is read whole or the merge fails: one over mergeImportBytes
// or one that cannot be read fails it, and an import that is a link out of the
// repo is read at its target (#370 R17).
func TestMergeHomeClaudeImportReadFailsClosed(t *testing.T) {
	big := strings.Repeat("x", mergeImportBytes) + "\n@tail.md\n"
	cases := []struct {
		name    string
		host    map[string]string // committed
		untrack map[string]string // written after the box commit
		links   map[string]string // repo path -> target; "<OUT>" is a folder outside the repo
		out     map[string]string // files in that outside folder
		chmod   string            // a repo file made unreadable
		change  string
		want    string // "" merged
	}{
		{"import over the size cap", map[string]string{"CLAUDE.md": "@big.md\n"}, map[string]string{"big.md": big}, nil, nil, "", "other.txt", MergeFailed},
		{"import at the size cap merges", map[string]string{"CLAUDE.md": "@ok.md\n"}, map[string]string{"ok.md": strings.Repeat("x", mergeImportBytes)}, nil, nil, "", "other.txt", ""},
		{"import that cannot be read", map[string]string{"CLAUDE.md": "@locked.md\n"}, map[string]string{"locked.md": "@deep.md\n"}, nil, nil, "locked.md", "other.txt", MergeFailed},
		{"import that is a folder is skipped", map[string]string{"CLAUDE.md": "@docs\n", "docs/a.md": "h\n"}, nil, nil, nil, "", "other.txt", ""},
		{"missing import is skipped", map[string]string{"CLAUDE.md": "@none.md\n"}, nil, nil, nil, "", "other.txt", ""},
		{"import linked out of the repo is read at its target", map[string]string{"CLAUDE.md": "@notes.md\n"}, nil, map[string]string{"notes.md": "<OUT>/n.md"}, map[string]string{"n.md": "@deep.md\n"}, "", "deep.md", MergeGuarded},
		{"import linked out to a target over the cap", map[string]string{"CLAUDE.md": "@notes.md\n"}, nil, map[string]string{"notes.md": "<OUT>/n.md"}, map[string]string{"n.md": big}, "", "other.txt", MergeFailed},
		{"import linked out to a missing target is skipped", map[string]string{"CLAUDE.md": "@notes.md\n"}, nil, map[string]string{"notes.md": "<OUT>/none.md"}, nil, "", "other.txt", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.chmod != "" && os.Geteuid() == 0 {
				t.Skip("root reads a mode 000 file")
			}
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			out := t.TempDir()
			for rel, content := range c.out {
				if err := os.WriteFile(filepath.Join(out, rel), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			x := newMergeFx(t, "main")
			x.commit("host", c.host)
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			for rel, content := range c.untrack {
				x.write(rel, content)
			}
			for rel, target := range c.links {
				x.linkUntracked(rel, strings.ReplaceAll(target, "<OUT>", out))
			}
			if c.chmod != "" {
				p := filepath.Join(x.of, c.chmod)
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(p, 0o644) })
			}
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: t.TempDir(), Audit: (&auditRec{}).fn, CloneTips: x.tips()})
			if c.want == "" {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, c.want)
			if c.want == MergeGuarded && !slices.Contains(got.Paths, c.change) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.change)
			}
			if x.tip() != before {
				t.Fatal("main moved")
			}
		})
	}
}

// A .claude or .claude/rules folder that is a link out of the repo is the
// host's own: it is listed at its target and its rule files are read (#370 N1).
func TestMergeHomeClaudeRulesLinkedOut(t *testing.T) {
	cases := []struct {
		name   string
		link   string            // repo path linked to the outside folder
		out    map[string]string // files in the outside folder
		cap    int               // mergeRulesEntries, 0 to keep it
		change string
		want   string // "" merged
	}{
		{"rules linked out: a rule's import is guarded", ".claude/rules", map[string]string{"r.md": "@../../tools/x.md\n"}, 0, "tools/x.md", MergeGuarded},
		{"rules linked out: a nested rule's import is guarded", ".claude/rules", map[string]string{"sub/r.md": "@../../../tools/y.md\n"}, 0, "tools/y.md", MergeGuarded},
		{"rules linked out: an unrelated change merges", ".claude/rules", map[string]string{"r.md": "@../../tools/x.md\n"}, 0, "other.txt", ""},
		{".claude linked out: a rule's import is guarded", ".claude", map[string]string{"rules/r.md": "@../../tools/z.md\n"}, 0, "tools/z.md", MergeGuarded},
		{".claude linked out with no rules: merges", ".claude", map[string]string{"notes.txt": "x\n"}, 0, "other.txt", ""},
		{"rules linked out past the entry cap fails", ".claude/rules", map[string]string{"a.md": "a\n", "b.md": "b\n", "c.md": "c\n"}, 2, "other.txt", MergeFailed},
		{"rules linked out to nothing merges", ".claude/rules", nil, 0, "other.txt", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.cap > 0 {
				old := mergeRulesEntries
				mergeRulesEntries = c.cap
				t.Cleanup(func() { mergeRulesEntries = old })
			}
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			out := filepath.Join(t.TempDir(), "shared")
			if c.out != nil {
				for rel, content := range c.out {
					p := filepath.Join(out, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			x := newMergeFx(t, "main")
			x.commit("host", map[string]string{"tools/keep": "h\n"})
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			x.linkUntracked(c.link, out)
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: t.TempDir(), Audit: (&auditRec{}).fn, CloneTips: x.tips()})
			if c.want == "" {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, c.want)
			if c.want == MergeGuarded && !slices.Contains(got.Paths, c.change) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.change)
			}
			if x.tip() != before {
				t.Fatal("main moved")
			}
		})
	}
}
