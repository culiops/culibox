package clone

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// link makes rel a symlink to target in the host work tree and commits it.
func (x *mergeFx) link(rel, target string) {
	x.t.Helper()
	x.linkUntracked(rel, target)
	fixtureGit(x.t, x.of, "add", "--", rel)
	fixtureGit(x.t, x.of, "commit", "-q", "-m", "link "+rel)
}

// linkUntracked makes rel a symlink to target and leaves it untracked.
func (x *mergeFx) linkUntracked(rel, target string) {
	x.t.Helper()
	p := filepath.Join(x.of, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		x.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		x.t.Fatal(err)
	}
}

// wantGuardedBy runs the merge and wants it refused as guarded with path among
// the named paths.
func wantGuardedBy(t *testing.T, x *mergeFx, path string) *MergeResult {
	t.Helper()
	before := x.tip()
	a := &auditRec{}
	got := x.run(newRec(t), a)
	wantMergeReason(t, got, MergeGuarded)
	if !slices.Contains(got.Paths, path) {
		t.Fatalf("Paths = %q, want %q", got.Paths, path)
	}
	if x.tip() != before || len(a.calls) != 0 {
		t.Fatalf("main moved or audit written: %q", a.calls)
	}
	return got
}

// A host CLAUDE.md that is a link to another file in the repo: the box's change
// to that file is what the host's agent loads.
func TestMergeHomeLinkedClaudeMdTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("target", map[string]string{"docs/agent.md": "host\n"})
	x.link("CLAUDE.md", "docs/agent.md")
	x.box("main", map[string]string{"docs/agent.md": "box\n"})
	wantGuardedBy(t, x, "docs/agent.md")
}

func TestMergeHomeLinkedMcpJSONTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("target", map[string]string{"config/mcp.json": "{}\n"})
	x.link(".mcp.json", "config/mcp.json")
	x.box("main", map[string]string{"config/mcp.json": "{\"x\":1}\n"})
	wantGuardedBy(t, x, "config/mcp.json")
}

func TestMergeHomeLinkedClaudeDirTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("target", map[string]string{"tooling/claude/settings.json": "{}\n"})
	x.link(".claude", "tooling/claude")
	x.box("main", map[string]string{"tooling/claude/settings.json": "{\"hooks\":{}}\n"})
	wantGuardedBy(t, x, "tooling/claude/settings.json")
}

// A link at a nested protected name is found through the index, not the top.
func TestMergeHomeNestedLinkedNameTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("target", map[string]string{"shared/rules.md": "host\n"})
	x.link("pkg/sub/AGENTS.md", "../../shared/rules.md")
	x.box("main", map[string]string{"shared/rules.md": "box\n"})
	wantGuardedBy(t, x, "shared/rules.md")
}

// A link inside a protected folder points out of it.
func TestMergeHomeLinkInsideProtectedFolderTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("target", map[string]string{"docs/rules/a.mdc": "host\n"})
	x.link(".cursor/rules", "../docs/rules")
	x.box("main", map[string]string{"docs/rules/a.mdc": "box\n"})
	wantGuardedBy(t, x, "docs/rules/a.mdc")
}

// A relative core.hooksPath whose parent is a tracked link re-points the hooks.
func TestMergeHomeHooksPathBehindLinkTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("hooks", map[string]string{"scripts/hooks/pre-commit": "#!/bin/sh\n"})
	x.link("tools", "scripts")
	fixtureGit(t, x.of, "config", "core.hooksPath", "tools/hooks")
	x.box("main", map[string]string{"scripts/hooks/pre-commit": "#!/bin/sh\necho box\n"})
	wantGuardedBy(t, x, "scripts/hooks/pre-commit")
}

// An import target that sits behind a linked folder.
func TestMergeHomeImportBehindLinkedFolderDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{"CLAUDE.md": "@docs/ai/rules.md\n", "agent-docs/rules.md": "host\n"})
	x.link("docs/ai", "../agent-docs")
	x.box("main", map[string]string{"agent-docs/rules.md": "box\n"})
	wantGuardedBy(t, x, "agent-docs/rules.md")
}

// A path a config names behind a linked folder (the script an .mcp.json runs).
func TestMergeHomeConfigRefBehindLinkDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{".mcp.json": mcpNode, "real/mcp/server.js": "host\n"})
	x.link("tools", "real")
	x.box("main", map[string]string{"real/mcp/server.js": "box\n"})
	wantGuardedBy(t, x, "real/mcp/server.js")
}

// A git-config file in the work tree reached through a link.
func TestMergeHomeGitConfigBehindLinkDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("cfg", map[string]string{"real/inc.gitconfig": "[user]\n\tname = h\n"})
	x.link("cfg", "real")
	fixtureGit(t, x.of, "config", "include.path", "../cfg/inc.gitconfig")
	x.box("main", map[string]string{"real/inc.gitconfig": "[core]\n\tpager = x\n"})
	wantGuardedBy(t, x, "real/inc.gitconfig")
}

// Untracked links at the top: the host's tools load them like tracked ones.
func TestMergeHomeUntrackedLinkAtTopTargetDenied(t *testing.T) {
	for name, c := range map[string]struct {
		link, target string // the untracked link, and what it says
		real         string // the tracked file it leads to
		keep         string // a tracked file that makes the link's folder real
	}{
		"name":       {".mcp.json", "config/mcp.json", "config/mcp.json", ""},
		"pattern":    {".env.local", "config/env.txt", "config/env.txt", ""},
		"dir":        {".claude", "config", "config/settings.json", ""},
		"top path":   {".github", "gh", "gh/copilot-instructions.md", ""},
		"claude cfg": {".claude/settings.json", "../config/settings.json", "config/settings.json", ".claude/keep"},
	} {
		t.Run(name, func(t *testing.T) {
			x := newMergeFx(t, "main")
			files := map[string]string{c.real: "host\n"}
			if c.keep != "" {
				files[c.keep] = "k\n"
			}
			x.commit("target", files)
			x.linkUntracked(c.link, c.target)
			x.box("main", map[string]string{c.real: "box\n"})
			wantGuardedBy(t, x, c.real)
		})
	}
}

// Each link met on the way is protected too: the box replacing one with a file
// changes what the protected name loads.
func TestMergeHomeLinkChainIntermediateDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	out := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(out, []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.link("docs/a.md", out) // leaves the repo
	x.link("CLAUDE.md", "docs/a.md")
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side")
	if err := os.Remove(filepath.Join(x.of, "docs", "a.md")); err != nil {
		t.Fatal(err)
	}
	x.commit("replace", map[string]string{"docs/a.md": "box\n"})
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.out("rev-parse", "HEAD"))
	fixtureGit(t, x.of, "checkout", "-q", "main")
	wantGuardedBy(t, x, "docs/a.md")
}

// A protected link that leaves the repo, and one that no protected name leads
// to, never block a merge.
func TestMergeHomeLinkOutsideOrUnrelatedDoesNotBlock(t *testing.T) {
	x := newMergeFx(t, "main")
	out := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(out, []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.link("CLAUDE.md", out)
	x.link(".mcp.json", "../outside/mcp.json")
	x.commit("v2", map[string]string{"docs/v2/x.md": "host\n"})
	x.link("docs/latest", "v2")
	x.box("main", map[string]string{"docs/v2/x.md": "box\n", "src/new.go": "package x\n"})
	wantMerged(t, x.run(newRec(t), &auditRec{}))
}

// A link loop at a protected name is refused, naming it.
func TestMergeHomeLinkLoopGuarded(t *testing.T) {
	x := newMergeFx(t, "main")
	x.link("loop", ".mcp.json")
	x.link(".mcp.json", "loop")
	x.box("main", map[string]string{"src/new.go": "package x\n"})
	got := wantGuardedBy(t, x, ".mcp.json")
	if !strings.Contains(got.Detail, "link") {
		t.Fatalf("Detail = %q", got.Detail)
	}
}

func TestMergeHomeLinkTooManyHopsGuarded(t *testing.T) {
	x := newMergeFx(t, "main")
	for i := range mergeLinkHops + 2 {
		x.link("hop"+string(rune('a'+i%26))+string(rune('a'+i/26)), "hop"+string(rune('a'+(i+1)%26))+string(rune('a'+(i+1)/26)))
	}
	x.link("CLAUDE.md", "hopaa")
	x.box("main", map[string]string{"src/new.go": "package x\n"})
	wantGuardedBy(t, x, "CLAUDE.md")
}

// One pass makes one extra git call, a pathspec-limited ls-files -s.
func TestMergeHomeLinkCheckOneLsFiles(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{"docs/agent.md": "host\n"})
	x.link("CLAUDE.md", "docs/agent.md")
	x.box("main", map[string]string{"src/new.go": "package x\n"})
	rec := newRec(t)
	wantMerged(t, x.run(rec, &auditRec{}))
	n := 0
	for _, c := range rec.calls {
		if slices.Contains(c.args, "ls-files") && slices.Contains(c.args, "-s") {
			n++
			if !slices.Contains(c.args, "--") || c.args[len(c.args)-1] == "--" {
				t.Fatalf("ls-files -s has no pathspec: %q", c.args)
			}
		}
		if slices.Contains(c.args, "ls-files") && !slices.Contains(c.args, "--") {
			t.Fatalf("a whole-repo ls-files: %q", c.args)
		}
	}
	if n != 1 {
		t.Fatalf("ls-files -s ran %d times, want 1", n)
	}
}

// A refusal by another rule does not pay for the link check.
func TestMergeHomeLinkCheckSkippedWhenAlreadyDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"CLAUDE.md": "x\n"})
	rec := newRec(t)
	wantMergeReason(t, x.run(rec, &auditRec{}), MergeGuarded)
	for _, c := range rec.calls {
		if slices.Contains(c.args, "ls-files") && slices.Contains(c.args, "-s") {
			t.Fatalf("ls-files -s ran for an already refused merge: %q", c.args)
		}
	}
}

// Past the caps the merge is refused, never waved through.
func TestMergeHomeLinkCapsFailClosed(t *testing.T) {
	t.Run("protected set", func(t *testing.T) {
		old := mergeLinkSetMax
		mergeLinkSetMax = 2
		t.Cleanup(func() { mergeLinkSetMax = old })
		x := newMergeFx(t, "main")
		x.link("a/CLAUDE.md", "../a.txt")
		x.link("b/CLAUDE.md", "../a.txt")
		x.link("c/CLAUDE.md", "../a.txt")
		x.box("main", map[string]string{"src/new.go": "package x\n"})
		wantMergeReason(t, x.run(newRec(t), &auditRec{}), MergeFailed)
	})
	t.Run("lstat calls", func(t *testing.T) {
		old := mergeLinkLstatMax
		mergeLinkLstatMax = 3
		t.Cleanup(func() { mergeLinkLstatMax = old })
		x := newMergeFx(t, "main")
		x.commit("host", map[string]string{"a/b/c/d/e.md": "h\n"})
		x.link("CLAUDE.md", "a/b/c/d/e.md")
		x.box("main", map[string]string{"src/new.go": "package x\n"})
		wantMergeReason(t, x.run(newRec(t), &auditRec{}), MergeFailed)
	})
}

// Resolution, component by component.
func TestLinkResolve(t *testing.T) {
	of := t.TempDir()
	mk := func(rel string) {
		if err := os.MkdirAll(filepath.Join(of, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ln := func(rel, target string) {
		mk(filepath.ToSlash(filepath.Dir(rel)))
		if err := os.Symlink(target, filepath.Join(of, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	mk("real/dir")
	if err := os.WriteFile(filepath.Join(of, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln("a", "real")
	ln("b", "a/dir")                              // through a link
	ln("up", "real/dir/../..")                    // dot-dot in a target
	ln("abs", filepath.Join(of, "real", "dir"))   // absolute, inside
	ln("esc", "../elsewhere")                     // out
	ln("absout", filepath.Join(t.TempDir(), "x")) // absolute, out
	ln("gone", "nothing/here")                    // dangling
	ln("self", "self")

	cases := []struct {
		in      string
		real    string
		outside bool
		links   []string
		errIs   error
	}{
		{in: "file.txt", real: "file.txt"},
		{in: "real/dir", real: "real/dir"},
		{in: "a/dir/x.md", real: "real/dir/x.md", links: []string{"a"}},
		{in: "b/x.md", real: "real/dir/x.md", links: []string{"b", "a"}},
		{in: "up", real: "", links: []string{"up"}},
		{in: "abs/x", real: "real/dir/x", links: []string{"abs"}},
		{in: "esc/x", outside: true, links: []string{"esc"}},
		{in: "absout", outside: true, links: []string{"absout"}},
		{in: "gone/more", real: "nothing/here/more", links: []string{"gone"}},
		{in: "missing/x/y", real: "missing/x/y"},
		{in: "file.txt/under", real: "file.txt/under"}, // not a folder: nothing is there
		{in: "../x", outside: true},
		{in: "self", errIs: errLinkLoop, links: nil},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			w, err := newLinkWalker(of)
			if err != nil {
				t.Fatal(err)
			}
			defer w.close()
			real, outside, err := w.resolve(c.in)
			if c.errIs != nil {
				if !errors.Is(err, c.errIs) {
					t.Fatalf("err = %v, want %v", err, c.errIs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if outside != c.outside || (!outside && strings.Join(real, "/") != c.real) {
				t.Fatalf("resolve(%q) = %q, outside %v; want %q, outside %v", c.in, real, outside, c.real, c.outside)
			}
			if got := w.linkPaths(); !slices.Equal(got, c.links) && len(c.links)+len(got) > 0 {
				t.Fatalf("links = %q, want %q", got, c.links)
			}
		})
	}
}

func TestLinkDeniesMatching(t *testing.T) {
	d := newLinkDenies()
	d.add("docs/Agent.md")
	d.add("tooling/claude")
	for p, want := range map[string]bool{
		"docs/agent.md": true, "DOCS/AGENT.MD": true, "docs": true, "tooling/Claude/settings.json": true,
		"tooling": true, "docs/agent.md.bak": false, "doc": false, "src/a.go": false,
	} {
		if got := d.denied(p); got != want {
			t.Errorf("denied(%q) = %v, want %v", p, got, want)
		}
	}
	d.add("") // a link to the repo top: everything
	if !d.denied("src/a.go") {
		t.Error("a link to the repo top denies nothing")
	}
}

func TestParseLsFilesStage(t *testing.T) {
	good := "120000 " + strings.Repeat("a", 40) + " 0\tCLAUDE.md\x00100644 " + strings.Repeat("b", 40) + " 0\ta\tb\x00"
	es, err := parseLsFilesStage([]byte(good))
	if err != nil || len(es) != 2 || es[0].mode != "120000" || es[0].path != "CLAUDE.md" || es[1].path != "a\tb" {
		t.Fatalf("es=%+v err=%v", es, err)
	}
	if es, err := parseLsFilesStage(nil); err != nil || len(es) != 0 {
		t.Fatalf("empty: %+v %v", es, err)
	}
	for name, bad := range map[string]string{
		"cut short": "120000 " + strings.Repeat("a", 40) + " 0\tCLAUDE.md",
		"no tab":    "120000 " + strings.Repeat("a", 40) + " 0 CLAUDE.md\x00",
		"bad mode":  "12x000 " + strings.Repeat("a", 40) + " 0\tCLAUDE.md\x00",
		"no path":   "120000 " + strings.Repeat("a", 40) + " 0\t\x00",
	} {
		if _, err := parseLsFilesStage([]byte(bad)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The files the hook managers and editor tasks read are followed through links
// like the other config files: a link there leads the read, and the host's tool,
// to another file.
func TestMergeHomeLinkedHookManagerConfigsDenied(t *testing.T) {
	t.Run("pre-commit config linked, tracked", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"config/pc.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: ls1/x.sh\n"})
		x.link(".pre-commit-config.yaml", "config/pc.yaml")
		x.box("main", map[string]string{"ls1/other.sh": "box\n"})
		wantGuardedBy(t, x, "ls1/other.sh")
	})
	t.Run("pre-commit config linked, the target", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"config/pc.yaml": "repos: []\n"})
		x.link(".pre-commit-config.yaml", "config/pc.yaml")
		x.box("main", map[string]string{"config/pc.yaml": "box\n"})
		wantGuardedBy(t, x, "config/pc.yaml")
	})
	t.Run("vscode tasks linked, untracked", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"cfg/tasks.json": `{"tasks":[{"command":"./ls2/t.sh"}]}`})
		x.linkUntracked(".vscode/tasks.json", "../cfg/tasks.json")
		x.box("main", map[string]string{"cfg/tasks.json": "{}\n"})
		wantGuardedBy(t, x, "cfg/tasks.json")
	})
	t.Run("vscode tasks linked, what they name", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"cfg/tasks.json": `{"tasks":[{"command":"./ls3/t.sh"}]}`})
		x.linkUntracked(".vscode/tasks.json", "../cfg/tasks.json")
		x.box("main", map[string]string{"ls3/other.sh": "box\n"})
		wantGuardedBy(t, x, "ls3/other.sh")
	})
	t.Run("lefthook linked, untracked", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"cfg/lh.yml": "pre-commit:\n  commands:\n    l:\n      run: ./ls4/l.sh\n"})
		x.linkUntracked("lefthook-local.yml", "cfg/lh.yml")
		x.box("main", map[string]string{"cfg/lh.yml": "box\n"})
		wantGuardedBy(t, x, "cfg/lh.yml")
	})
	t.Run("overcommit linked, untracked", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"cfg/oc.yml": "x: 1\n"})
		x.linkUntracked(".overcommit.yml", "cfg/oc.yml")
		x.box("main", map[string]string{"cfg/oc.yml": "box\n"})
		wantGuardedBy(t, x, "cfg/oc.yml")
	})
}

// A hook script that is itself a link to a script elsewhere in the repo.
func TestMergeHomeLinkedHuskyScriptTargetDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("target", map[string]string{"impl/pc.sh": "bash ls5/run.sh\n"})
	x.link(".husky/pre-commit", "../impl/pc.sh")
	x.box("main", map[string]string{"impl/pc.sh": "box\n"})
	wantGuardedBy(t, x, "impl/pc.sh")
}

// The script a config names is a link to another file: that file and its
// folder are refused, the folder because the script loads its neighbours.
func TestMergeHomeConfigRefLinkTargetFolderDenied(t *testing.T) {
	t.Run("the real file's folder", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{".mcp.json": mcpNode, "impl/server.js": "h\n", "impl/lib.js": "h\n"})
		x.link("tools/mcp/server.js", "../../impl/server.js")
		x.box("main", map[string]string{"impl/lib.js": "box\n"})
		wantGuardedBy(t, x, "impl/lib.js")
	})
	t.Run("an unrelated folder stays free", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{".mcp.json": mcpNode, "impl/server.js": "h\n"})
		x.link("tools/mcp/server.js", "../../impl/server.js")
		x.box("main", map[string]string{"other/lib.js": "box\n"})
		wantMerged(t, x.run(newRec(t), &auditRec{}))
	})
	t.Run("a link to a file at the top denies no folder", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{".mcp.json": mcpNode, "real.js": "h\n"})
		x.link("tools/mcp/server.js", "../../real.js")
		x.box("main", map[string]string{"README.md": "box\n"})
		wantMerged(t, x.run(newRec(t), &auditRec{}))
	})
	t.Run("a link that is not a config ref has no folder deny", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.commit("target", map[string]string{"docs/agent.md": "h\n", "docs/other.md": "h\n"})
		x.link("CLAUDE.md", "docs/agent.md")
		x.box("main", map[string]string{"docs/other.md": "box\n"})
		wantMerged(t, x.run(newRec(t), &auditRec{}))
	})
}

// A hooks folder that core.hooksPath names: the scripts in it are read.
func TestMergeHomeHooksPathScriptsRead(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("hooks", map[string]string{"tools/hooks/pre-commit": "#!/bin/sh\n# bash cm/commented.sh\nbash tools/lib/pc.sh\n"})
	fixtureGit(t, x.of, "config", "core.hooksPath", "tools/hooks")
	x.box("main", map[string]string{"tools/lib/pc.sh": "box\n", "cm/commented.sh": "box\n"})
	wantGuardedBy(t, x, "tools/lib/pc.sh")
	y := newMergeFx(t, "main")
	y.commit("hooks", map[string]string{"tools/hooks/pre-commit": "#!/bin/sh\n# bash cm/commented.sh\nbash tools/lib/pc.sh\n"})
	fixtureGit(t, y.of, "config", "core.hooksPath", "tools/hooks")
	y.box("main", map[string]string{"cm/commented.sh": "box\n"})
	wantMerged(t, y.run(newRec(t), &auditRec{}))
}
