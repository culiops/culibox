package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodRL = `{"updated_at":1785648756,"five_hour":{"used_percentage":80,"resets_at":1785654600},"seven_day":{"used_percentage":42,"resets_at":1786107600}}`

// A writer that empties the file before refilling it (or one that crashed
// mid-write) must not make an account's meters vanish for a whole poll: the last
// good snapshot is served until a new good one arrives.
func TestReadRateLimits_emptyFileKeepsLastGood(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(goodRL), 0o644)
	if _, ok := readRateLimits(f); !ok {
		t.Fatal("setup: good file not read")
	}
	os.WriteFile(f, nil, 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.FiveHour == nil || rl.FiveHour.UsedPercentage != 80 {
		t.Fatalf("empty file lost the last good snapshot: %+v ok=%v", rl, ok)
	}
}

func TestReadRateLimits_tornFileKeepsLastGood(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(goodRL), 0o644)
	readRateLimits(f)
	os.WriteFile(f, []byte(goodRL[:40]), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.SevenDay == nil || rl.SevenDay.ResetsAt != 1786107600 {
		t.Fatalf("torn file lost the last good snapshot: %+v ok=%v", rl, ok)
	}
}

// agent-box's old script wrote {"source","updated_at"} on a session's first
// render; that must not replace a good reading either.
func TestReadRateLimits_windowlessSnapshotKeepsLastGood(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(goodRL), 0o644)
	readRateLimits(f)
	os.WriteFile(f, []byte(`{"source":"agent-box","updated_at":1785650000}`), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.FiveHour == nil || rl.UpdatedAt != 1785648756 {
		t.Fatalf("windowless snapshot replaced the last good one: %+v ok=%v", rl, ok)
	}
}

func TestReadRateLimits_newGoodReplacesLastGood(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(goodRL), 0o644)
	readRateLimits(f)
	os.WriteFile(f, nil, 0o644)
	readRateLimits(f)
	os.WriteFile(f, []byte(`{"updated_at":1785649999,"five_hour":{"used_percentage":5,"resets_at":1785654600}}`), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.FiveHour.UsedPercentage != 5 || rl.UpdatedAt != 1785649999 {
		t.Fatalf("fresh good snapshot not served: %+v ok=%v", rl, ok)
	}
	if rl.SevenDay != nil {
		t.Fatalf("old week window leaked into the new snapshot: %+v", rl.SevenDay)
	}
}

// A deleted file is gone, not torn: serving a cached reading for an account
// whose file was removed would show quota that no longer exists.
func TestReadRateLimits_deletedFileDropsLastGood(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(goodRL), 0o644)
	readRateLimits(f)
	os.Remove(f)
	if _, ok := readRateLimits(f); ok {
		t.Fatal("deleted file still served from cache")
	}
	os.WriteFile(f, nil, 0o644) // reappears torn: nothing good to fall back on
	if _, ok := readRateLimits(f); ok {
		t.Fatal("cache survived the file's deletion")
	}
}

func TestReadRateLimits_lastGoodCacheIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < rateLimitsCacheMax*3; i++ {
		f := filepath.Join(dir, fmt.Sprintf("rl-%d.json", i))
		os.WriteFile(f, []byte(goodRL), 0o644)
		readRateLimits(f)
	}
	rlLastGood.mu.Lock()
	n := len(rlLastGood.m)
	rlLastGood.mu.Unlock()
	if n > rateLimitsCacheMax {
		t.Fatalf("last-good cache grew to %d entries (max %d)", n, rateLimitsCacheMax)
	}
}

func TestReadRateLimits_parsesByModel(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(`{"updated_at":1785648756,"model_family":"haiku","five_hour":{"used_percentage":0,"resets_at":1785654600},
	  "by_model":{"opus":{"updated_at":1785648700,"seven_day":{"used_percentage":76,"resets_at":1786107600}},
	              "haiku":{"updated_at":1785648756,"seven_day":{"used_percentage":94,"resets_at":1786000000}}}}`), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.ModelFamily != "haiku" || len(rl.ByModel) != 2 {
		t.Fatalf("by_model not parsed: %+v ok=%v", rl, ok)
	}
	if o := rl.ByModel["opus"]; o.SevenDay == nil || o.SevenDay.UsedPercentage != 76 {
		t.Fatalf("opus bucket = %+v", o)
	}
}

func TestReadRateLimits_byModelOnlySnapshotIsValid(t *testing.T) {
	// A snapshot whose top level has no windows but whose buckets do is real data.
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(`{"updated_at":1785648756,"by_model":{"opus":{"updated_at":1785648700,"seven_day":{"used_percentage":76,"resets_at":1786107600}}}}`), 0o644)
	if _, ok := readRateLimits(f); !ok {
		t.Fatal("snapshot with only by_model windows rejected")
	}
}

func TestReadRateLimits_byModelIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"updated_at":1785648756,"five_hour":{"used_percentage":1,"resets_at":1785654600},"by_model":{`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"m%02d":{"updated_at":%d,"five_hour":{"used_percentage":1,"resets_at":1785654600}}`, i, 1785648000+i)
	}
	b.WriteString("}}")
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(b.String()), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || len(rl.ByModel) > rateLimitsMaxBuckets {
		t.Fatalf("by_model has %d entries (max %d) ok=%v", len(rl.ByModel), rateLimitsMaxBuckets, ok)
	}
	if _, have := rl.ByModel["m39"]; !have {
		t.Fatal("the most recently updated bucket was dropped")
	}
}

// A box's snapshot is untrusted: its per-model buckets get the same clamp and
// plausibility checks as the top-level windows.
func TestSanitizeRateLimits_cleansBuckets(t *testing.T) {
	now := time.Unix(1785649000, 0)
	rl := RateLimits{
		UpdatedAt: 1785648756,
		FiveHour:  &RateLimitWindow{UsedPercentage: 1, ResetsAt: 1785654600},
		ByModel: map[string]BucketLimits{
			"opus":  {UpdatedAt: 1785648700, SevenDay: &RateLimitWindow{UsedPercentage: 900, ResetsAt: 1786107600}},
			"junk":  {UpdatedAt: 1785648700, SevenDay: &RateLimitWindow{UsedPercentage: 5, ResetsAt: 5}},
			"later": {UpdatedAt: 1785649000 + 3600, SevenDay: &RateLimitWindow{UsedPercentage: 5, ResetsAt: 1786107600}},
		},
	}
	got, ok := sanitizeRateLimits(rl, now)
	if !ok {
		t.Fatal("valid snapshot rejected")
	}
	if o := got.ByModel["opus"]; o.SevenDay == nil || o.SevenDay.UsedPercentage != 100 {
		t.Fatalf("opus bucket not clamped: %+v", o)
	}
	if _, have := got.ByModel["junk"]; have {
		t.Fatal("bucket with an implausible resets_at kept")
	}
	if _, have := got.ByModel["later"]; have {
		t.Fatal("bucket stamped in the future kept")
	}
}
