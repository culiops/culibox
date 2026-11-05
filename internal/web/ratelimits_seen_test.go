package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/session"
)

// The host statusLine writes one file for every claude on the machine, so a
// haiku and an opus session on one token make it flip between two sets of
// numbers (measured: 76% and 94% weekly). The reader remembers what it has seen
// per window, keyed by resets_at, so both readings reach the client.

func seenWeek(rl RateLimits) map[int64]float64 {
	out := map[int64]float64{}
	if rl.Seen != nil {
		for _, r := range rl.Seen.SevenDay {
			out[r.ResetsAt] = r.UsedPercentage
		}
	}
	return out
}

func writeRL(t *testing.T, f string, updated int64, five float64, fiveReset int64, week float64, weekReset int64) {
	t.Helper()
	body := fmt.Sprintf(`{"updated_at":%d,"five_hour":{"used_percentage":%v,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}}`,
		updated, five, fiveReset, week, weekReset)
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRateLimits_remembersBothModelsReadings(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	f := filepath.Join(t.TempDir(), "rl.json")

	writeRL(t, f, now.Unix(), 9, fiveR, 76, weekA) // opus-like
	readRateLimitsAt(f, now)
	writeRL(t, f, now.Unix()+5, 0, fiveR, 94, weekB) // haiku-like
	readRateLimitsAt(f, now)
	writeRL(t, f, now.Unix()+10, 9, fiveR, 77, weekA) // opus again
	rl, ok := readRateLimitsAt(f, now)
	if !ok {
		t.Fatal("read failed")
	}
	got := seenWeek(rl)
	if len(got) != 2 || got[weekA] != 77 || got[weekB] != 94 {
		t.Fatalf("seen week readings = %v, want {%d:77 %d:94}", got, weekA, weekB)
	}
	// The file's own (top-level) numbers are still the last writer's.
	if rl.SevenDay.UsedPercentage != 77 {
		t.Fatalf("top level = %v", rl.SevenDay.UsedPercentage)
	}
}

func TestReadRateLimits_seenKeepsTheHighestPerReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA := now.Add(5 * 24 * time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	f := filepath.Join(t.TempDir(), "rl.json")
	writeRL(t, f, now.Unix(), 9, fiveR, 77, weekA)
	readRateLimitsAt(f, now)
	writeRL(t, f, now.Unix()+5, 9, fiveR, 70, weekA) // lower reading of the same window
	rl, _ := readRateLimitsAt(f, now)
	if got := seenWeek(rl)[weekA]; got != 77 {
		t.Fatalf("remembered %v for the window, want the highest (77)", got)
	}
}

func TestReadRateLimits_seenDropsWindowsThatHaveReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(24*time.Hour).Unix(), now.Add(5*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	f := filepath.Join(t.TempDir(), "rl.json")
	writeRL(t, f, now.Unix(), 9, fiveR, 94, weekA)
	readRateLimitsAt(f, now)
	later := now.Add(30 * time.Hour) // weekA has reset
	writeRL(t, f, later.Unix(), 3, later.Add(time.Hour).Unix(), 10, weekB)
	rl, _ := readRateLimitsAt(f, later)
	got := seenWeek(rl)
	if _, have := got[weekA]; have || len(got) != 1 || got[weekB] != 10 {
		t.Fatalf("seen week readings = %v, want only the live window %d", got, weekB)
	}
}

func TestReadRateLimits_seenIsCappedPerWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fiveR := now.Add(2 * time.Hour).Unix()
	f := filepath.Join(t.TempDir(), "rl.json")
	var last RateLimits
	for i := 1; i <= rateLimitsSeenMax+3; i++ {
		writeRL(t, f, now.Unix()+int64(i), 1, fiveR, float64(i), now.Add(time.Duration(i)*24*time.Hour).Unix())
		last, _ = readRateLimitsAt(f, now)
	}
	got := seenWeek(last)
	if len(got) != rateLimitsSeenMax {
		t.Fatalf("seen %d week readings, want %d: %v", len(got), rateLimitsSeenMax, got)
	}
	// The oldest resets_at are evicted: the newest windows are kept.
	for i := 4; i <= rateLimitsSeenMax+3; i++ {
		if _, have := got[now.Add(time.Duration(i)*24*time.Hour).Unix()]; !have {
			t.Fatalf("window %d was evicted, the oldest should go first: %v", i, got)
		}
	}
}

func TestReadRateLimits_tornReadServesRememberedReadings(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	f := filepath.Join(t.TempDir(), "rl.json")
	writeRL(t, f, now.Unix(), 9, fiveR, 76, weekA)
	readRateLimitsAt(f, now)
	writeRL(t, f, now.Unix()+5, 0, fiveR, 94, weekB)
	readRateLimitsAt(f, now)
	os.WriteFile(f, nil, 0o644)
	rl, ok := readRateLimitsAt(f, now)
	if got := seenWeek(rl); !ok || got[weekA] != 76 || got[weekB] != 94 {
		t.Fatalf("torn read lost the remembered readings: %v ok=%v", got, ok)
	}
}

func TestReadRateLimits_ignoresSeenWrittenInTheFile(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := filepath.Join(t.TempDir(), "rl.json")
	body := fmt.Sprintf(`{"updated_at":%d,"seven_day":{"used_percentage":10,"resets_at":%d},"seen":{"seven_day":[{"used_percentage":99,"resets_at":%d}]}}`,
		now.Unix(), now.Add(time.Hour).Unix(), now.Add(2*time.Hour).Unix())
	os.WriteFile(f, []byte(body), 0o644)
	rl, _ := readRateLimitsAt(f, now)
	if got := seenWeek(rl); len(got) != 1 || got[now.Add(time.Hour).Unix()] != 10 {
		t.Fatalf("a seen block from the file was trusted: %v", got)
	}
}

func TestReadRateLimits_deletedFileForgetsSeen(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	f := filepath.Join(t.TempDir(), "rl.json")
	writeRL(t, f, now.Unix(), 9, fiveR, 76, weekA)
	readRateLimitsAt(f, now)
	os.Remove(f)
	readRateLimitsAt(f, now)
	writeRL(t, f, now.Unix()+5, 0, fiveR, 94, weekB)
	rl, _ := readRateLimitsAt(f, now)
	if got := seenWeek(rl); len(got) != 1 {
		t.Fatalf("readings survived the file's deletion: %v", got)
	}
}

func TestMergeBoxRateLimits_remembersReadingsAcrossPolls(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	box := "box-seen-" + t.Name()
	s := &Server{}
	poll := func(data []byte) RateLimits {
		s.BoxFiles = boxFilesOf(session.BoxFile{Box: box, Account: "work", Data: data})
		out := map[string]RateLimits{}
		s.mergeBoxRateLimits(out, now)
		return out["work"]
	}
	fiveR := now.Add(2 * time.Hour).Unix()
	poll(boxRL(now.Unix(), 9, 76, weekA))
	rl := poll([]byte(fmt.Sprintf(`{"updated_at":%d,"five_hour":{"used_percentage":0,"resets_at":%d},"seven_day":{"used_percentage":94,"resets_at":%d}}`, now.Unix()+5, fiveR, weekB)))
	if got := seenWeek(rl); got[weekA] != 76 || got[weekB] != 94 {
		t.Fatalf("box seen week readings = %v", got)
	}
}
