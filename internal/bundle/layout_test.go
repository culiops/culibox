package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeLayoutFlags(t *testing.T) {
	l, ok := LayoutFor("/usr/local/bin/claude") // absolute path must still match
	if !ok {
		t.Fatal("no layout for claude")
	}
	flags := l.Flags("/b")
	joined := strings.Join(flags, " ")

	for _, want := range []string{"--strict-mcp-config", "--setting-sources user", "--mcp-config /b/mcp.json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	// --mcp-config is variadic; last position would swallow a following argument.
	if flags[len(flags)-1] == "--mcp-config" || flags[len(flags)-2] == "--mcp-config" {
		t.Fatalf("--mcp-config must not be last: %q", joined)
	}
}

// The quick-shell profile must never be handed agent flags.
func TestShellBinsHaveNoLayout(t *testing.T) {
	for _, bin := range []string{"bash", "zsh", "/usr/bin/zsh", "sh"} {
		if _, ok := LayoutFor(bin); ok {
			t.Fatalf("%q must not get an agent layout", bin)
		}
	}
}

// A bind mount whose source does not exist makes docker fail the whole box, so
// optional entries are filtered out rather than assumed present.
func TestMountsSkipAbsentSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := LayoutFor("claude")
	got := l.MountsFor(dir, "/home/agent", func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if len(got) != 1 {
		t.Fatalf("expected only the existing entry, got %+v", got)
	}
	if !strings.HasSuffix(got[0].Box, "/.claude/CLAUDE.md") {
		t.Fatalf("wrong mount target: %+v", got[0])
	}
}
