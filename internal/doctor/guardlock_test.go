package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if len(rows) != 4 {
		t.Fatalf("want current a, leftover b and c, and the not-ours warning for d: %+v", rows)
	}
	if r := rows[3]; r.Status != Warn || !strings.HasPrefix(r.Detail, "/Users/u/d: CLAUDE.md was already locked when CuliBox came to lock it") ||
		!strings.Contains(r.Detail, "will not unlock it") || !strings.Contains(r.Detail, "docs/MACOS.md") {
		t.Fatalf("not-ours row = %+v", r)
	}
	// No box of this instance holds it (or it cannot be told): no row.
	if rows := guardLockRows("darwin", boxFacts{Locks: locks[4:], Running: []string{"other"}, RunningKnown: true}); len(rows) != 0 {
		t.Fatalf("no running holder, no row: %v", rows)
	}
	if rows := guardLockRows("darwin", boxFacts{Locks: locks[4:]}); len(rows) != 0 {
		t.Fatalf("unknown running, no row: %v", rows)
	}
	if rows := guardLockRows("linux", boxFacts{Locks: locks[4:], Running: []string{"ab-box-1"}, RunningKnown: true}); rows != nil {
		t.Fatalf("linux: %v", rows)
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

// The printed unlock command works pasted as is, even after doctor shows $HOME
// as "~": run it through sh with a stub chflags and see the absolute path.
func TestGuardLockUnlockCommandPastes(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "it's a dir")
	rows := guardLockRows("darwin", boxFacts{Home: home, RunningKnown: true,
		Locks: []box.GuardLock{{Workspace: ws, File: "CLAUDE.md", Ours: true}}})
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	shown := Redact(rows[0].Detail, home, "")
	i := strings.Index(shown, "chflags nouchg ")
	if i < 0 {
		t.Fatalf("no command in %q", shown)
	}
	if strings.Contains(shown[i:], "~") {
		t.Fatalf("the command shows ~: %q", shown[i:])
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "chflags"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", shown[i:])
	cmd.Env = []string{"PATH=" + bin, "HOME=" + home}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "nouchg\n" + filepath.Join(ws, "CLAUDE.md") + "\n"
	if string(out) != want {
		t.Fatalf("chflags got %q, want %q", out, want)
	}
}
