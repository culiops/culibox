package session

import (
	"strings"
	"testing"
)

func TestChildEnv_StripsClaudeSessionMarkers(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "abc")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("KEEP_ME", "yes")

	joined := strings.Join(childEnv([]string{"EXTRA=1"}), "\n") + "\n"

	for _, bad := range []string{"CLAUDE_CODE_CHILD_SESSION=", "CLAUDE_CODE_SESSION_ID=", "CLAUDECODE="} {
		if strings.Contains(joined, bad) {
			t.Errorf("child env must not contain %q (nested-session marker)", bad)
		}
	}
	if !strings.Contains(joined, "KEEP_ME=yes\n") {
		t.Error("normal env var was dropped")
	}
	if !strings.Contains(joined, "EXTRA=1\n") {
		t.Error("launcher extra env not appended")
	}
	if !strings.Contains(joined, "TERM=") {
		t.Error("TERM not set for the child")
	}
}
