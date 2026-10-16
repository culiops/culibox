package web

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadRateLimits(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(`{"updated_at":1785648756,"five_hour":{"used_percentage":80,"resets_at":1785654600},"seven_day":{"used_percentage":42,"resets_at":1786107600}}`), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.FiveHour.UsedPercentage != 80 || rl.SevenDay.ResetsAt != 1786107600 {
		t.Fatalf("got %+v ok=%v", rl, ok)
	}
}

func TestReadRateLimits_fractionalPercent(t *testing.T) {
	// The producer (abtop) emits fractional percentages; an int field would drop
	// the whole snapshot. Regression for the float bug caught in DEV.
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(`{"five_hour":{"used_percentage":7.000000000000001,"resets_at":1785673800}}`), 0o644)
	rl, ok := readRateLimits(f)
	if !ok || rl.FiveHour == nil || rl.FiveHour.UsedPercentage < 7 || rl.FiveHour.UsedPercentage >= 8 {
		t.Fatalf("fractional percent not parsed: %+v ok=%v", rl.FiveHour, ok)
	}
}

func TestReadRateLimits_missing(t *testing.T) {
	if _, ok := readRateLimits(filepath.Join(t.TempDir(), "nope.json")); ok {
		t.Fatal("missing file should be ok=false")
	}
	if _, ok := readRateLimits(""); ok {
		t.Fatal("empty path should be ok=false")
	}
}

func TestReadRateLimits_malformed(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(f, []byte(`not json`), 0o644)
	if _, ok := readRateLimits(f); ok {
		t.Fatal("malformed should be ok=false")
	}
	f2 := filepath.Join(t.TempDir(), "empty.json")
	os.WriteFile(f2, []byte(`{"updated_at":1}`), 0o644) // neither window
	if _, ok := readRateLimits(f2); ok {
		t.Fatal("no windows should be ok=false")
	}
}

// A host-tier agent can replace a rate-limits file with a FIFO; a plain
// os.ReadFile would block /api/meta forever on it.
func TestReadRateLimits_fifoDoesNotHang(t *testing.T) {
	f := filepath.Join(t.TempDir(), "agent-box-rate-limits.json")
	if err := syscall.Mkfifo(f, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := readRateLimits(f)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a FIFO must yield no data")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readRateLimits blocked on a FIFO")
	}
}
