package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/session"
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
	fut := now.Add(24 * time.Hour).Unix()
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

func TestRateLimitsFresherWins(t *testing.T) {
	now := time.Now().Unix()
	host := filepath.Join(t.TempDir(), "rl.json")
	hostBody := func(updated int64) {
		if err := os.WriteFile(host, boxRL(updated, 10, 20, now+3600), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	box := session.BoxFile{Account: "default", Data: boxRL(now-100, 55, 66, now+3600)}
	s := &Server{RateLimitsFile: host, BoxFiles: boxFilesOf(box)}

	hostBody(now - 500) // host older: box wins
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 55 {
		t.Fatalf("box should win: %+v", m["default"])
	}
	hostBody(now - 10) // host newer: host wins
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 10 {
		t.Fatalf("host should win: %+v", m["default"])
	}
	// A box cannot win by claiming a future time.
	s.BoxFiles = boxFilesOf(session.BoxFile{Account: "default", Data: boxRL(now+3600, 99, 99, now+3600)})
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 10 {
		t.Fatalf("future-dated box must be ignored: %+v", m["default"])
	}
	// Garbage from a box is ignored; the host value stays.
	s.BoxFiles = boxFilesOf(session.BoxFile{Account: "default", Data: []byte("not json")})
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 10 {
		t.Fatalf("garbage box must be ignored: %+v", m["default"])
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
