package statusline

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The previous snapshot is read to carry its other buckets over. It sits in a
// directory a box can write to, so it is read like hostile input: a link is not
// followed and a FIFO does not block the statusLine.
func TestScriptDoesNotFollowALinkAtTheSnapshotPath(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	planted := `{"by_model":{"haiku":{"updated_at":1,"seven_day":{"used_percentage":99,"resets_at":1793970000}}}}`
	if err := os.WriteFile(target, []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cfg, "agent-box-rate-limits.json")); err != nil {
		t.Fatal(err)
	}
	runIn(t, cfg, limitsInput("claude-opus-4-5", 9, 76, 1793970000))
	m := readSnap(t, cfg)
	bm, _ := m["by_model"].(map[string]any)
	if _, have := bm["haiku"]; have {
		t.Fatalf("buckets were read through the link: %v", m["by_model"])
	}
	if b, _ := os.ReadFile(target); string(b) != planted {
		t.Fatalf("the link target was written: %s", b)
	}
}

func TestScriptDoesNotBlockOnAFifoAtTheSnapshotPath(t *testing.T) {
	cfg := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(cfg, "agent-box-rate-limits.json"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	runIn(t, cfg, limitsInput("claude-opus-4-5", 9, 76, 1793970000))
	bucket(t, readSnap(t, cfg), "opus")
}
