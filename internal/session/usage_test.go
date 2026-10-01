package session

import (
	"os"
	"path/filepath"
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

func TestTranscriptPath_newestAfterCreated(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.jsonl")
	os.WriteFile(old, []byte("{}"), 0o644)
	os.Chtimes(old, time.Unix(1000, 0), time.Unix(1000, 0))
	newf := filepath.Join(dir, "new.jsonl")
	os.WriteFile(newf, []byte("{}"), 0o644)
	os.Chtimes(newf, time.Unix(9000, 0), time.Unix(9000, 0))
	got, ok := transcriptPath("/x", home, time.Unix(5000, 0))
	if !ok || got != newf {
		t.Fatalf("got %q ok=%v want %q", got, ok, newf)
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

// A box mounts only its spawn cwd at the same path on both sides, so only a
// directory at or under it names the same files on the host. Anywhere else (the
// box's own home, /tmp in the container) would open an unrelated host file.
func TestLiveCwd(t *testing.T) {
	cases := []struct {
		raw, spawn string
		boxed      bool
		want       string
	}{
		{"/work/proj/.claude/worktrees/wt", "/work/proj", false, "/work/proj/.claude/worktrees/wt"},
		{"/home/u/.config/superpowers/worktrees/p/x", "/work/proj", false, "/home/u/.config/superpowers/worktrees/p/x"},
		{"/work/proj/.claude/worktrees/wt", "/work/proj", true, "/work/proj/.claude/worktrees/wt"},
		{"/work/proj", "/work/proj/", true, "/work/proj"},
		{"/home/agent", "/work/proj", true, ""},
		{"/work/proj-evil", "/work/proj", true, ""},
		{"", "/work/proj", false, ""},
	}
	for _, c := range cases {
		if got := liveCwd(c.raw, c.spawn, c.boxed); got != c.want {
			t.Errorf("liveCwd(%q,%q,%v)=%q want %q", c.raw, c.spawn, c.boxed, got, c.want)
		}
	}
}
