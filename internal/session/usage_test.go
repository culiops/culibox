package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		{"claude-opus-4-6", 0, false}, // 200k unless [1m], which the transcript model id does not carry: unknown, no guessed %
		{"claude-sonnet-4-6", 0, false},
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

// Transcript-shaped records, trimmed from real Claude Code JSONL (issue #79).
const (
	asstUsage = `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":6,"cache_read_input_tokens":200000,"cache_creation_input_tokens":27448,"output_tokens":300}}}`
	// compactBoundary is what Claude Code appends after an auto-compact: the
	// pre-compact size is gone from the transcript's usage lines, postTokens is
	// the size of the new context.
	compactBoundary = `{"type":"system","subtype":"compact_boundary","content":"Conversation compacted","compactMetadata":{"trigger":"auto","preTokens":227454,"postTokens":25340}}`
	// synthetic429 is what a failed API call leaves behind: a fake assistant
	// message with zero usage and the <synthetic> model.
	synthetic429 = `{"type":"assistant","isApiErrorMessage":true,"message":{"role":"assistant","model":"<synthetic>","usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}`
)

// After an auto-compact the last usage line still carries the PRE-compact size
// (227,454 tokens = 22% of 1M); the tab must show the post-compact size instead.
func TestParseUsageTail_compactAfterLastUsageShowsPostTokens(t *testing.T) {
	lines := asstUsage + "\n" + `{"type":"user","message":{"role":"user"}}` + "\n" + compactBoundary + "\n"
	u, ok := parseUsageTail([]byte(lines))
	if !ok || u.ContextTokens != 25340 {
		t.Fatalf("got %+v ok=%v, want ContextTokens=25340", u, ok)
	}
}

func TestParseUsageTail_compactIsMarkedPostCompact(t *testing.T) {
	u, ok := parseUsageTail([]byte(asstUsage + "\n" + compactBoundary + "\n"))
	if !ok || !u.PostCompact {
		t.Fatalf("got %+v ok=%v, want PostCompact=true", u, ok)
	}
	plain, _ := parseUsageTail([]byte(asstUsage + "\n"))
	if plain.PostCompact {
		t.Fatalf("a plain usage line must not be marked post-compact: %+v", plain)
	}
}

// The tail may start after the last reply, leaving only the boundary.
func TestParseUsageTail_compactBoundaryAloneIsEnough(t *testing.T) {
	u, ok := parseUsageTail([]byte(`{"type":"user","message":{"role":"user"}}` + "\n" + compactBoundary + "\n"))
	if !ok || u.ContextTokens != 25340 || !u.PostCompact {
		t.Fatalf("got %+v ok=%v, want 25340 post-compact", u, ok)
	}
}

// A failed API call ends the transcript on a <synthetic> zero-usage assistant
// message; the tab and the steer rail must keep the last real reply instead.
func TestParseUsageTail_syntheticErrorMessageIsSkipped(t *testing.T) {
	u, ok := parseUsageTail([]byte(asstUsage + "\n" + synthetic429 + "\n"))
	if !ok || u.ContextTokens != 227454 || u.Model != "claude-opus-4-8" {
		t.Fatalf("got %+v ok=%v, want the real reply (227454, claude-opus-4-8)", u, ok)
	}
}

// Each skip criterion stands alone: a <synthetic> model, an API-error flag, or
// zero usage, even when the other markers are missing.
func TestParseUsageTail_eachSkipCriterionStandsAlone(t *testing.T) {
	for name, bad := range map[string]string{
		"synthetic model": `{"message":{"role":"assistant","model":"<synthetic>","usage":{"input_tokens":5,"cache_read_input_tokens":5}}}`,
		"api error flag":  `{"isApiErrorMessage":true,"message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":5,"cache_read_input_tokens":5}}}`,
		"zero usage":      `{"message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":0}}}`,
	} {
		u, ok := parseUsageTail([]byte(asstUsage + "\n" + bad + "\n"))
		if !ok || u.ContextTokens != 227454 {
			t.Errorf("%s: got %+v ok=%v, want the earlier real reply", name, u, ok)
		}
	}
}

// Claude Code rounds the context % to the nearest point, it does not truncate
// (227,454 / 1M = 22.7% shows as 23%; truncating showed 22 on 9 of 20 sessions).
func TestUsageForRoundsPctLikeClaudeCode(t *testing.T) {
	for _, c := range []struct {
		tokens string
		want   int
	}{
		{`"input_tokens":6,"cache_read_input_tokens":200000,"cache_creation_input_tokens":27448`, 23}, // 22.7
		{`"input_tokens":224000`, 22},  // 22.4
		{`"input_tokens":999600`, 100}, // 99.96 rounds up to the clamp
		{`"input_tokens":1`, 0},        // never negative, never nil
	} {
		m, home := newLocatorManager(t)
		writeTxLines(t, home, locCwd, "aaaa-1", time.Now(),
			`{"message":{"role":"assistant","model":"claude-opus-4-8","usage":{`+c.tokens+`}}}`)
		u, ok := m.UsageFor("s1")
		if !ok || u.Pct == nil || *u.Pct != c.want {
			t.Errorf("%s: got %+v ok=%v, want pct %d", c.tokens, u, ok, c.want)
		}
	}
}

// 1 of 745 real transcripts ended in ~320 KB of attachment records after the
// last reply: the usage line sat outside the 256 KiB tail and the tab went blank.
func TestUsageForWidensTheReadWhenTheTailHasNoUsage(t *testing.T) {
	m, home := newLocatorManager(t)
	att := `{"type":"attachment","attachment":{"type":"file","content":"` + strings.Repeat("x", 3000) + `"}}`
	lines := []string{asstUsage}
	for i := 0; i < 110; i++ { // ~330 KB
		lines = append(lines, att)
	}
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), lines...)
	u, ok := m.UsageFor("s1")
	if !ok || u.ContextTokens != 227454 || u.Model != "claude-opus-4-8" {
		t.Fatalf("got %+v ok=%v, want the usage line beyond the first 256 KiB", u, ok)
	}
}

// With no token count to show (a boundary lacking postTokens) the API must not
// invent a 0% bar either.
func TestUsageForNoPctWithoutATokenCount(t *testing.T) {
	m, home := newLocatorManager(t)
	bare := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":150000}}`
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), asstUsage, bare)
	u, ok := m.UsageFor("s1")
	if !ok || u.ContextTokens != 0 || u.Pct != nil {
		t.Fatalf("got %+v ok=%v, want 0 tokens and no pct", u, ok)
	}
}

// A boundary without postTokens (older Claude Code) must still never fall back
// to the stale pre-compact number: zero tokens renders as a blank tab.
func TestParseUsageTail_compactWithoutPostTokensIsBlankNotStale(t *testing.T) {
	bare := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":150000}}`
	u, _ := parseUsageTail([]byte(asstUsage + "\n" + bare + "\n"))
	if u.ContextTokens != 0 {
		t.Fatalf("got %+v, want ContextTokens=0", u)
	}
}
