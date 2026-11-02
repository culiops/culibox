package web

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/session"
)

// With no host default snapshot and several boxes, the box key does not include
// the profile token, so boxes can carry different identities. Freshest-wins
// would flip between them as each writes; the lexically smallest box name wins
// instead, whatever the order or freshness.
func TestRateLimitsDefaultNoHostStableAcrossBoxes(t *testing.T) {
	now := time.Now().Unix()
	older := session.BoxFile{Box: "box-1", Account: "default", Data: boxRL(now-300, 11, 12, now+3600)}
	fresher := session.BoxFile{Box: "box-2", Account: "default", Data: boxRL(now-5, 55, 66, now+3600)}
	absent := func() string { return filepath.Join(t.TempDir(), "absent.json") }
	for name, files := range map[string][]session.BoxFile{
		"smaller first": {older, fresher},
		"smaller last":  {fresher, older},
	} {
		s := &Server{RateLimitsFile: absent(), BoxFiles: boxFilesOf(files...)}
		for i := 0; i < 3; i++ {
			m := s.rateLimitsByAccount()
			if m["default"].FiveHour == nil || m["default"].FiveHour.UsedPercentage != 11 {
				t.Fatalf("%s poll %d: want box-1's snapshot (11), got %+v", name, i, m["default"])
			}
		}
	}
	// box-1 refreshing never changes the pick (it stays box-1).
	older.Data = boxRL(now-1, 13, 14, now+3600)
	s := &Server{RateLimitsFile: absent(), BoxFiles: boxFilesOf(fresher, older)}
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 13 {
		t.Fatalf("got %+v", m["default"])
	}
	// An invalid smaller box (garbage) is skipped, not chosen.
	bad := session.BoxFile{Box: "box-0", Account: "default", Data: []byte("not json")}
	s = &Server{RateLimitsFile: absent(), BoxFiles: boxFilesOf(bad, fresher, older)}
	if m := s.rateLimitsByAccount(); m["default"].FiveHour.UsedPercentage != 13 {
		t.Fatalf("invalid box-0 should be skipped: %+v", m["default"])
	}
}
