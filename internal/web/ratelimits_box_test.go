package web

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/session"
)

func boxRL(updated int64, five, seven float64, resets int64) []byte {
	return []byte(fmt.Sprintf(`{"updated_at":%d,"five_hour":{"used_percentage":%v,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}}`,
		updated, five, resets, seven, resets))
}

func boxFilesOf(files ...session.BoxFile) func(string, int64) []session.BoxFile {
	return func(rel string, max int64) []session.BoxFile {
		if rel != boxRateLimitsRel || max != boxRateLimitsMax {
			return nil
		}
		return files
	}
}

func TestSanitizeRateLimits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fut := now.Add(3 * time.Hour).Unix() // inside both windows: a 5h window cannot reset more than 5h away
	win := func(p float64, r int64) *RateLimitWindow { return &RateLimitWindow{UsedPercentage: p, ResetsAt: r} }
	cases := []struct {
		name     string
		in       RateLimits
		ok       bool
		five     *RateLimitWindow
		sevenNil bool
	}{
		{"over 100 clamps", RateLimits{UpdatedAt: now.Unix(), FiveHour: win(150, fut)}, true, win(100, fut), true},
		{"negative clamps to 0", RateLimits{UpdatedAt: now.Unix(), FiveHour: win(-3, fut)}, true, win(0, fut), true},
		{"future updated_at dropped", RateLimits{UpdatedAt: now.Add(10 * time.Minute).Unix(), FiveHour: win(5, fut)}, false, nil, true},
		{"4 min future tolerated", RateLimits{UpdatedAt: now.Add(4 * time.Minute).Unix(), FiveHour: win(5, fut)}, true, win(5, fut), true},
		{"implausible resets_at drops that window", RateLimits{UpdatedAt: now.Unix(), FiveHour: win(5, 5), SevenDay: win(7, fut)}, true, nil, false},
		{"resets_at beyond 400 days drops window", RateLimits{UpdatedAt: now.Unix(), FiveHour: win(5, now.Add(401*24*time.Hour).Unix()), SevenDay: win(7, fut)}, true, nil, false},
		{"both windows dropped", RateLimits{UpdatedAt: now.Unix(), FiveHour: win(5, 5), SevenDay: win(7, 0)}, false, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sanitizeRateLimits(c.in, now)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, c.ok, got)
			}
			if !ok {
				return
			}
			if (got.FiveHour == nil) != (c.five == nil) || (c.five != nil && *got.FiveHour != *c.five) {
				t.Fatalf("five = %+v, want %+v", got.FiveHour, c.five)
			}
			if (got.SevenDay == nil) != c.sevenNil {
				t.Fatalf("seven = %+v", got.SevenDay)
			}
		})
	}
}

func TestRateLimitsBoxedNonDefaultAccount(t *testing.T) {
	now := time.Now().Unix()
	s := &Server{BoxFiles: boxFilesOf(session.BoxFile{Account: "work", Data: boxRL(now, 30, 40, now+3600)})}
	m := s.rateLimitsByAccount()
	if m["work"].FiveHour == nil || m["work"].FiveHour.UsedPercentage != 30 || m["work"].SevenDay.UsedPercentage != 40 {
		t.Fatalf("map = %+v", m)
	}
	if _, ok := m["default"]; ok {
		t.Fatal("no default data expected")
	}
}

// R7: the host default file is written by any host claude, boxes use the
// profile's own token, so they can be different subscriptions. With a host
// default snapshot present the host ALWAYS wins for default, even when older,
// so the value never flips identity between polls.
func TestRateLimitsDefaultHostPresentAlwaysWins(t *testing.T) {
	now := time.Now().Unix()
	host := filepath.Join(t.TempDir(), "rl.json")
	if err := os.WriteFile(host, boxRL(now-500, 10, 20, now+3600), 0o600); err != nil { // host OLDER than the box
		t.Fatal(err)
	}
	box := session.BoxFile{Account: "default", Data: boxRL(now-10, 55, 66, now+3600)}
	s := &Server{RateLimitsFile: host, BoxFiles: boxFilesOf(box)}
	for i := 0; i < 3; i++ { // stable across polls
		if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 10 || m["default"].SevenDay.UsedPercentage != 20 {
			t.Fatalf("poll %d: older host must still win for default: %+v", i, m["default"])
		}
	}
}

// With no host default snapshot at all, a box fills default; of several boxes
// the smallest box name with a VALID snapshot wins (see
// TestRateLimitsDefaultNoHostStableAcrossBoxes), and an untrustworthy box
// (future-dated, garbage) is skipped before that choice.
func TestRateLimitsDefaultNoHostBoxFills(t *testing.T) {
	now := time.Now().Unix()
	s := &Server{RateLimitsFile: filepath.Join(t.TempDir(), "absent.json"), BoxFiles: boxFilesOf(
		session.BoxFile{Box: "box-d", Account: "default", Data: boxRL(now-300, 11, 12, now+3600)},
		session.BoxFile{Box: "box-c", Account: "default", Data: boxRL(now-20, 55, 66, now+3600)},
		session.BoxFile{Box: "box-b", Account: "default", Data: boxRL(now+3600, 99, 99, now+3600)}, // future: dropped
		session.BoxFile{Box: "box-a", Account: "default", Data: []byte("not json")},
	)}
	m := s.rateLimitsByAccount()
	if m["default"].FiveHour == nil || m["default"].FiveHour.UsedPercentage != 55 {
		t.Fatalf("box should fill default: %+v", m["default"])
	}
}

// A named account's host file and box snapshots share that account's own
// credential, so the fresher one wins, compared on min(UpdatedAt, now).
func TestRateLimitsNamedAccountFresherWins(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := now.Unix() + 3600
	acctRL := func(updated int64, five float64) RateLimits {
		return RateLimits{UpdatedAt: updated, FiveHour: &RateLimitWindow{UsedPercentage: five, ResetsAt: reset}}
	}
	run := func(hostUpdated, boxUpdated int64) float64 {
		s := &Server{BoxFiles: boxFilesOf(session.BoxFile{Account: "work", Data: boxRL(boxUpdated, 90, 90, reset)})}
		out := map[string]RateLimits{"work": acctRL(hostUpdated, 10)}
		s.mergeBoxRateLimits(out, now)
		return out["work"].FiveHour.UsedPercentage
	}
	n := now.Unix()
	if got := run(n-500, n-100); got != 90 {
		t.Fatalf("fresher box should win: %v", got)
	}
	if got := run(n-10, n-100); got != 10 {
		t.Fatalf("fresher host should win: %v", got)
	}
	// Garbage and future-dated boxes never win.
	s := &Server{BoxFiles: boxFilesOf(
		session.BoxFile{Account: "work", Data: boxRL(n+3600, 99, 99, reset)},
		session.BoxFile{Account: "work", Data: []byte("not json")},
	)}
	out := map[string]RateLimits{"work": acctRL(n-10, 10)}
	s.mergeBoxRateLimits(out, now)
	if out["work"].FiveHour.UsedPercentage != 10 {
		t.Fatalf("untrusted boxes must be ignored: %+v", out["work"])
	}
}

// Freshness is compared on min(UpdatedAt, now), so a box stamping inside the
// future slack gains at most "now". Host at now vs box at now+200s: both clamp
// to now, a tie, and the existing source stays.
func TestRateLimitsFutureStampTiesOnNow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := now.Unix() + 3600
	host := RateLimits{UpdatedAt: now.Unix(), FiveHour: &RateLimitWindow{UsedPercentage: 10, ResetsAt: reset}}
	s := &Server{BoxFiles: boxFilesOf(session.BoxFile{Account: "work", Data: boxRL(now.Unix()+200, 90, 90, reset)})}
	out := map[string]RateLimits{"work": host}
	s.mergeBoxRateLimits(out, now)
	if out["work"].FiveHour.UsedPercentage != 10 {
		t.Fatalf("tie must keep the existing source: %+v", out["work"])
	}
	// Host at now-1s: the box (clamped to now) is genuinely 1s fresher.
	host.UpdatedAt = now.Unix() - 1
	out = map[string]RateLimits{"work": host}
	s.mergeBoxRateLimits(out, now)
	if out["work"].FiveHour.UsedPercentage != 90 {
		t.Fatalf("box clamped to now should beat now-1s: %+v", out["work"])
	}
	// Two future-stamped sources tie on now; the existing one stays.
	host.UpdatedAt = now.Unix() + 250
	out = map[string]RateLimits{"work": host}
	s.mergeBoxRateLimits(out, now)
	if out["work"].FiveHour.UsedPercentage != 10 {
		t.Fatalf("future-vs-future must tie: %+v", out["work"])
	}
}

func TestSanitizeRateLimitsNegativeZero(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	negZero := math.Copysign(0, -1)
	rl := RateLimits{UpdatedAt: now.Unix(), FiveHour: &RateLimitWindow{UsedPercentage: negZero, ResetsAt: now.Unix() + 60}}
	got, ok := sanitizeRateLimits(rl, now)
	if !ok || math.Signbit(got.FiveHour.UsedPercentage) {
		t.Fatalf("-0 must become +0: %+v ok=%v", got.FiveHour, ok)
	}
}

func TestRateLimitsNoBoxFileUnchanged(t *testing.T) {
	host := filepath.Join(t.TempDir(), "rl.json")
	// 150% and an implausible resets_at on the HOST file stay as written: the
	// sanitizer applies to box input only.
	body := `{"updated_at":1,"five_hour":{"used_percentage":150,"resets_at":2}}`
	if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	want, ok := readRateLimits(host)
	if !ok {
		t.Fatal("setup")
	}
	for _, bf := range []func(string, int64) []session.BoxFile{nil, boxFilesOf()} {
		s := &Server{RateLimitsFile: host, BoxFiles: bf}
		m := s.rateLimitsByAccount()
		if len(m) != 1 || *m["default"].FiveHour != *want.FiveHour || m["default"].UpdatedAt != want.UpdatedAt {
			t.Fatalf("map = %+v want %+v", m, want)
		}
	}
}
