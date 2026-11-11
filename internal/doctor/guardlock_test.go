package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
)

func TestGuardLockRows(t *testing.T) {
	locks := []box.GuardLock{
		{Workspace: "/Users/u/a", File: "CLAUDE.md", Ours: true, Boxes: []string{"ab-box-1"},
			Sessions: []box.LockSession{{Box: "ab-box-1", ID: "claude-3"}}},
		{Workspace: "/Users/u/a", File: ".mcp.json", Ours: true, Boxes: []string{"ab-box-1"},
			Sessions: []box.LockSession{{Box: "ab-box-1", ID: "claude-3"}}},
		{Workspace: "/Users/u/b", File: "AGENTS.md", Ours: true, Boxes: []string{"ab-box-gone"}},
		{Workspace: "/Users/u/c", File: "CLAUDE.md", Ours: true}, // a clear that failed
		{Workspace: "/Users/u/d", File: "CLAUDE.md", Ours: false, Boxes: []string{"ab-box-1"}},
	}
	if rows := guardLockRows("linux", boxFacts{Locks: locks, RunningKnown: true}); rows != nil {
		t.Fatalf("linux has no locks: %v", rows)
	}
	if rows := guardLockRows("darwin", boxFacts{RunningKnown: true}); len(rows) != 0 {
		t.Fatalf("no locks, no row: %v", rows)
	}
	rows := guardLockRows("darwin", boxFacts{Locks: locks, Running: []string{"ab-box-1"}, RunningKnown: true})
	if len(rows) != 3 {
		t.Fatalf("want current a, leftover b and c (d is the user's own): %+v", rows)
	}
	if r := rows[0]; r.Status != Pass || !strings.Contains(r.Detail, "/Users/u/a: .mcp.json, CLAUDE.md locked on your Mac while its session runs (claude-3)") ||
		!strings.Contains(r.Detail, "letter case") {
		t.Fatalf("current row = %+v", r)
	}
	for i, ws := range []string{"/Users/u/b", "/Users/u/c"} {
		r := rows[i+1]
		if r.Status != Warn || !strings.HasPrefix(r.Detail, ws+": ") || !strings.Contains(r.Detail, "chflags nouchg '"+ws+"/") {
			t.Fatalf("leftover row %d = %+v", i, r)
		}
	}
	// The VM status is unknown: whether a box runs cannot be told.
	rows = guardLockRows("darwin", boxFacts{Locks: locks[:1]})
	if len(rows) != 1 || rows[0].Status != Warn || !strings.Contains(rows[0].Detail, "cannot be checked") {
		t.Fatalf("unknown row = %+v", rows)
	}
	rows = guardLockRows("darwin", boxFacts{LocksErr: errors.New("bad json"), RunningKnown: true})
	if len(rows) != 1 || rows[0].Status != Warn || !strings.Contains(rows[0].Detail, "chflags nouchg") {
		t.Fatalf("unreadable records = %+v", rows)
	}
}

// Review #96 (4): the printed unlock line is safe to paste for any folder name.
func TestGuardLockRowsQuoteTheUnlockLine(t *testing.T) {
	locks := []box.GuardLock{{Workspace: "/Users/u/x'$(cmd)'", File: "CLAUDE.md", Ours: true}}
	rows := guardLockRows("darwin", boxFacts{Locks: locks, RunningKnown: true})
	want := `chflags nouchg '/Users/u/x'\''$(cmd)'\''/CLAUDE.md'`
	if len(rows) != 1 || !strings.HasSuffix(rows[0].Detail, want) {
		t.Fatalf("rows = %+v, want the line to end %s", rows, want)
	}
}
