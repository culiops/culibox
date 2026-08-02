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
