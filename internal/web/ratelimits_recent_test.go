package web

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/session"
)

// A stale snapshot (an idle or dead box) in the lexically smaller box must not
// win over a fresh one: only snapshots updated within 15 minutes compete on box
// name.
func TestRateLimitsDefaultNoHostPrefersRecentSnapshot(t *testing.T) {
	now := time.Now().Unix()
	absent := func() string { return filepath.Join(t.TempDir(), "absent.json") }
	stale := session.BoxFile{Box: "box-1", Account: "default", Data: boxRL(now-3600, 11, 12, now+3600)}
	fresh := session.BoxFile{Box: "box-2", Account: "default", Data: boxRL(now-60, 55, 66, now+3600)}
	for name, files := range map[string][]session.BoxFile{"stale first": {stale, fresh}, "fresh first": {fresh, stale}} {
		s := &Server{RateLimitsFile: absent(), BoxFiles: boxFilesOf(files...)}
		if m := s.rateLimitsByAccount(); m["default"].FiveHour == nil || m["default"].FiveHour.UsedPercentage != 55 {
			t.Fatalf("%s: want the fresh box-2 snapshot (55), got %+v", name, m["default"])
		}
	}
	// Two recent ones: the smaller name still wins.
	recent1 := session.BoxFile{Box: "box-0", Account: "default", Data: boxRL(now-14*60, 7, 8, now+3600)}
	s := &Server{RateLimitsFile: absent(), BoxFiles: boxFilesOf(fresh, recent1, stale)}
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 7 {
		t.Fatalf("want box-0 (recent, smallest name): %+v", m["default"])
	}
	// None recent: fall back to the freshest, whatever its box name.
	staler := session.BoxFile{Box: "box-0", Account: "default", Data: boxRL(now-7200, 3, 4, now+3600)}
	s = &Server{RateLimitsFile: absent(), BoxFiles: boxFilesOf(staler, stale)}
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 11 {
		t.Fatalf("none recent: want the freshest (box-1, 11): %+v", m["default"])
	}
}
