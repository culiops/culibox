package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func limitsInput(model string, five, week float64, weekReset int) string {
	b, _ := json.Marshal(map[string]any{
		"model": map[string]any{"id": model},
		"rate_limits": map[string]any{
			"five_hour": map[string]any{"used_percentage": five, "resets_at": 1793900000},
			"seven_day": map[string]any{"used_percentage": week, "resets_at": weekReset},
		},
	})
	return string(b)
}

func bucket(t *testing.T, m map[string]any, family string) map[string]any {
	t.Helper()
	bm, _ := m["by_model"].(map[string]any)
	b, _ := bm[family].(map[string]any)
	if b == nil {
		t.Fatalf("no by_model[%q] in %v", family, m)
	}
	return b
}

func nBuckets(m map[string]any) int {
	bm, _ := m["by_model"].(map[string]any)
	return len(bm)
}

func weekPct(t *testing.T, b map[string]any) any {
	t.Helper()
	sd, _ := b["seven_day"].(map[string]any)
	return sd["used_percentage"]
}

func TestScriptRecordsModelFamilyBucket(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, limitsInput("claude-opus-4-5", 9, 76, 1793970000))
	m := readSnap(t, cfg)
	if m["model"] != "claude-opus-4-5" || m["model_family"] != "opus" {
		t.Fatalf("model/model_family = %v / %v", m["model"], m["model_family"])
	}
	if got := weekPct(t, bucket(t, m, "opus")); got != float64(76) {
		t.Fatalf("opus bucket week = %v", got)
	}
	// Legacy top-level windows stay, for readers that predate by_model.
	if fh, _ := m["five_hour"].(map[string]any); fh["used_percentage"] != float64(9) {
		t.Fatalf("top-level five_hour = %v", m["five_hour"])
	}
}

func TestScriptKeepsOtherFamilyBucket(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, limitsInput("claude-opus-4-5", 9, 76, 1793970000))
	runIn(t, cfg, limitsInput("claude-haiku-4-5", 0, 94, 1793948400))
	m := readSnap(t, cfg)
	// The newest write is the top level; the other family's reading survives.
	if m["model_family"] != "haiku" {
		t.Fatalf("model_family = %v", m["model_family"])
	}
	if got := weekPct(t, bucket(t, m, "opus")); got != float64(76) {
		t.Fatalf("opus bucket lost: %v", m["by_model"])
	}
	if got := weekPct(t, bucket(t, m, "haiku")); got != float64(94) {
		t.Fatalf("haiku bucket week = %v", got)
	}
}

func TestScriptReplacesSameFamilyBucket(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, limitsInput("claude-opus-4-5", 9, 76, 1793970000))
	runIn(t, cfg, limitsInput("claude-opus-4-6", 11, 77, 1793970000))
	m := readSnap(t, cfg)
	if n := nBuckets(m); n != 1 {
		t.Fatalf("by_model has %d entries, want 1: %v", n, m["by_model"])
	}
	if got := weekPct(t, bucket(t, m, "opus")); got != float64(77) {
		t.Fatalf("opus bucket not replaced: %v", m["by_model"])
	}
}

func TestScriptBoundsBucketCount(t *testing.T) {
	cfg := t.TempDir()
	for i := 0; i < 12; i++ {
		runIn(t, cfg, limitsInput("vendor-model-"+string(rune('a'+i)), 1, 2, 1793970000))
	}
	m := readSnap(t, cfg)
	if n := nBuckets(m); n > 8 {
		t.Fatalf("by_model grew to %d entries", n)
	}
	bucket(t, m, "vendor-model-l") // the newest survives
}

func TestScriptSurvivesCorruptPreviousSnapshot(t *testing.T) {
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "agent-box-rate-limits.json"), []byte(`{"by_mod`), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, cfg, limitsInput("claude-opus-4-5", 9, 76, 1793970000))
	bucket(t, readSnap(t, cfg), "opus")
}
