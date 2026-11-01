package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/claude/culiops-agent-box": "-home-claude-culiops-agent-box",
		"/home/x/my.app_v2":              "-home-x-my-app-v2", // "." and "_" also become "-"
		"/tmp/a.b/c_d":                   "-tmp-a-b-c-d",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseUsageTail(t *testing.T) {
	// two assistant turns; the LAST usage wins; iterations must NOT be summed.
	lines := `{"type":"x","effort":"high"}
{"message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1,"cache_read_input_tokens":10,"cache_creation_input_tokens":100}}}
{"message":{"role":"user"}}
{"message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":2,"cache_read_input_tokens":20,"cache_creation_input_tokens":200,"iterations":[{"input_tokens":999}]}}}
`
	u, ok := parseUsageTail([]byte(lines))
	if !ok || u.ContextTokens != 222 || u.Model != "claude-opus-4-8" || u.Effort != "high" {
		t.Fatalf("got %+v ok=%v", u, ok)
	}
}

func TestParseUsageTail_none(t *testing.T) {
	if _, ok := parseUsageTail([]byte(`{"message":{"role":"user"}}`)); ok {
		t.Fatal("expected ok=false when no usage")
	}
}

func TestContextMax(t *testing.T) {
	if m, ok := contextMax("claude-opus-4-8"); !ok || m != 1_000_000 {
		t.Fatalf("opus max=%d ok=%v", m, ok)
	}
	if _, ok := contextMax("mystery"); ok {
		t.Fatal("unknown model should be ok=false")
	}
}

func TestContextMaxCurrentModels(t *testing.T) {
	cases := []struct {
		model string
		max   int
		ok    bool
	}{
		{"claude-opus-5-5", 1_000_000, true},
		{"claude-sonnet-5-5", 1_000_000, true},
		{"claude-haiku-5-5", 1_000_000, true},
		{"claude-fable-5-1", 1_000_000, true},
		{"claude-mythos-5", 1_000_000, true},
		{"claude-opus-4-8", 1_000_000, true},
		{"claude-opus-4-7", 1_000_000, true},
		{"claude-opus-4-6", 1_000_000, true},
		{"claude-sonnet-4-6", 1_000_000, true},
		{"claude-haiku-4-5-20251001", 200_000, true},
		{"<synthetic>", 0, false},
		{"", 0, false},
		{"gpt-5", 0, false},
		{"claude-sonnet-4-5", 0, false},
	}
	for _, c := range cases {
		m, ok := contextMax(c.model)
		if m != c.max || ok != c.ok {
			t.Errorf("contextMax(%q) = (%d, %v), want (%d, %v)", c.model, m, ok, c.max, c.ok)
		}
	}
}

func TestIsClaudeBin(t *testing.T) {
	for _, c := range []struct {
		bin  string
		want bool
	}{
		{"/usr/bin/claude", true}, {"claude", true}, {"/home/x/.local/bin/claude", true},
		{"zsh", false}, {"/bin/zsh", false}, {"", false},
	} {
		if got := isClaudeBin(c.bin); got != c.want {
			t.Errorf("isClaudeBin(%q)=%v want %v", c.bin, got, c.want)
		}
	}
}

func TestIsAgentBin(t *testing.T) {
	for _, c := range []struct {
		bin  string
		want bool
	}{
		{"/usr/bin/claude", true}, {"claude", true}, {"/home/x/.local/bin/codex", true}, {"codex", true},
		{"zsh", false}, {"/bin/bash", false}, {"", false}, {"/opt/claude-tools/run", false},
	} {
		if got := isAgentBin(c.bin); got != c.want {
			t.Errorf("isAgentBin(%q)=%v want %v", c.bin, got, c.want)
		}
	}
}

// Claude Code's EnterWorktree moves the agent's working directory without a
// chdir of the process; only the transcript records it, on every entry's `cwd`.
// The LAST such entry is the live one, even when it comes after the last usage
// line, and ExitWorktree is just a later entry carrying the old dir again.
func TestParseUsageTail_cwdIsTheLastRecorded(t *testing.T) {
	lines := `{"cwd":"/work/proj","message":{"role":"user"}}
{"cwd":"/work/proj/.claude/worktrees/wt","message":{"role":"assistant","model":"m","usage":{"input_tokens":1}}}
{"cwd":"/work/proj/.claude/worktrees/wt2","message":{"role":"user"}}
`
	u, ok := parseUsageTail([]byte(lines))
	if !ok || u.Cwd != "/work/proj/.claude/worktrees/wt2" {
		t.Fatalf("got %+v ok=%v", u, ok)
	}
}

func TestParseUsageTail_relativeCwdIgnored(t *testing.T) {
	lines := `{"cwd":"/work/proj","message":{"role":"user"}}
{"cwd":"wt","message":{"role":"assistant","model":"m","usage":{"input_tokens":1}}}
`
	u, _ := parseUsageTail([]byte(lines))
	if u.Cwd != "/work/proj" {
		t.Fatalf("Cwd=%q want the last ABSOLUTE one", u.Cwd)
	}
}

// mkCheckouts builds a main checkout (a .git dir) with a linked worktree under
// .claude/worktrees/wt (a .git FILE pointing into .git/worktrees/wt) and a
// submodule (a .git file pointing into .git/modules), each with a sub/ dir.
func mkCheckouts(t *testing.T) (main, wt, sm string) {
	t.Helper()
	main = filepath.Join(t.TempDir(), "proj")
	wt = filepath.Join(main, ".claude", "worktrees", "wt")
	sm = filepath.Join(main, "vendor", "lib")
	for _, d := range []string{filepath.Join(main, ".git", "worktrees", "wt"), filepath.Join(main, "sub"),
		filepath.Join(wt, "sub", "deep"), filepath.Join(sm, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+main+"/.git/worktrees/wt\n"), 0o644)
	os.WriteFile(filepath.Join(sm, ".git"), []byte("gitdir: ../../.git/modules/lib\n"), 0o644)
	return main, wt, sm
}

// The transcript cwd moves on every Bash `cd`, not only on EnterWorktree, and a
// path the agent prints is relative to its CHECKOUT, not to wherever its shell
// last cd'd. So only a move into a different linked worktree changes the base,
// and the base is that worktree's root.
func TestLiveCwd(t *testing.T) {
	main, wt, sm := mkCheckouts(t)
	cases := []struct {
		name, raw, spawn string
		boxed            bool
		want             string
	}{
		{"entered a worktree", wt, main, false, wt},
		{"cd'd into a worktree subdir", filepath.Join(wt, "sub", "deep"), main, false, wt},
		{"cd'd into a main-checkout subdir", filepath.Join(main, "sub"), main, false, ""},
		{"cd'd outside any checkout", t.TempDir(), main, false, ""},
		{"submodule is not a worktree", filepath.Join(sm, "sub"), main, false, ""},
		{"spawned inside that worktree", filepath.Join(wt, "sub"), wt, false, ""},
		{"boxed, worktree under the mount", wt, main, true, wt},
		{"boxed, worktree outside the mount", wt, filepath.Join(main, "sub"), true, ""},
		{"relative", "wt", main, false, ""},
		{"empty", "", main, false, ""},
	}
	for _, c := range cases {
		if got := liveCwd(c.raw, []string{c.spawn}, c.boxed); got != c.want {
			t.Errorf("%s: liveCwd(%q,%q,%v)=%q want %q", c.name, c.raw, c.spawn, c.boxed, got, c.want)
		}
	}
}

// A submodule checked out inside a worktree has gitdir
// <main>/.git/worktrees/<wt>/modules/<sub>: it contains "/worktrees/" but is not
// itself a linked worktree. Only a gitdir whose PARENT is named worktrees is.
func TestWorktreeRootRejectsSubmoduleOfAWorktree(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ".git"), []byte("gitdir: /m/.git/worktrees/wt/modules/sub\n"), 0o644)
	if r, ok := worktreeRoot(d); ok {
		t.Fatalf("submodule of a worktree taken as a worktree root %q", r)
	}
}
