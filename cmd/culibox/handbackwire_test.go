package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/handback"
	"github.com/culiops/culibox/internal/session"
)

type inspectCall struct {
	root  string
	since time.Time
}

// realDir is a fresh directory spelled as its real path, the form the box
// layer labels a workspace with and the scan walks.
func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// stubHandback is a handbackHooks over a temp store whose inspection is a stub.
type stubHandback struct {
	*handbackHooks
	mu     sync.Mutex
	calls  []inspectCall
	rep    handback.Report
	err    error
	clock  time.Time
	panics bool
}

func newStubHandback(t *testing.T, owned func(string) bool) *stubHandback {
	t.Helper()
	s := &stubHandback{clock: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	s.handbackHooks = newHandbackHooks(t.TempDir(), owned)
	s.inspect = func(_ context.Context, root *os.Root, since time.Time, _ handback.Limits) (handback.Report, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.panics {
			panic("hostile input")
		}
		s.calls = append(s.calls, inspectCall{root.Name(), since})
		s.clock = s.clock.Add(time.Minute) // time passes during the walk
		return s.rep, s.err
	}
	s.now = func() time.Time {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.clock
	}
	return s
}

func (s *stubHandback) got() []inspectCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]inspectCall(nil), s.calls...)
}

// recordOf reads one record's file straight from the store, zero-flag records
// included (Pending lists only records that still hold a flag).
func recordOf(t *testing.T, s handback.Store, key string) handback.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.Dir, handback.RecordID(key)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r handback.Record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func onePDF() handback.Report {
	return handback.Report{Inspected: 3, Flags: []handback.Flag{
		{Path: "a.pdf", Classes: []handback.Class{handback.ClassMismatch}, Detail: "ELF executable named .pdf", SHA256: strings.Repeat("0a", 32)},
	}}
}

func TestScanInPlace(t *testing.T) {
	ws := realDir(t)
	key := handback.InPlaceKey(ws)
	started := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

	t.Run("a known start scans from it and records the scan start", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = onePDF()
		before := h.clock
		note, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err != nil {
			t.Fatal(err)
		}
		if c := h.got(); len(c) != 1 || c[0].root != ws || !c[0].since.Equal(started) {
			t.Fatalf("inspect calls = %+v, want one from %v", c, started)
		}
		want := session.HandbackNote{ID: handback.RecordID(key), Count: 1, InPlace: true}
		if note != want {
			t.Fatalf("note = %+v, want %+v", note, want)
		}
		last, ok, err := h.store.LastScan(key)
		if err != nil || !ok || !last.Equal(before) {
			t.Fatalf("LastScan = %v %v %v, want the clock before the walk (%v)", last, ok, err, before)
		}
	})

	t.Run("an unknown start with a prior record scans from its LastScan", func(t *testing.T) {
		h := newStubHandback(t, nil)
		prior := time.Date(2026, 11, 22, 10, 30, 0, 0, time.UTC)
		if _, err := h.store.Merge(key, handback.Record{Kind: handback.KindInPlace, Workspace: ws}, prior, handback.Report{}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ScanInPlace(context.Background(), ws, started, false); err != nil {
			t.Fatal(err)
		}
		if c := h.got(); len(c) != 1 || !c[0].since.Equal(prior) {
			t.Fatalf("inspect calls = %+v, want one from the prior LastScan %v", c, prior)
		}
		if r := recordOf(t, h.store, key); strings.Contains(r.Truncated, "start time unknown") {
			t.Fatalf("a prior record means the start is known: %+v", r)
		}
	})

	t.Run("an unknown start with no record says what was not checked", func(t *testing.T) {
		h := newStubHandback(t, nil)
		if _, err := h.ScanInPlace(context.Background(), ws, started, false); err != nil {
			t.Fatal(err)
		}
		if c := h.got(); len(c) != 1 || !c[0].since.Equal(started) {
			t.Fatalf("inspect calls = %+v, want one from the guess %v", c, started)
		}
		want := "start time unknown after a restart; files written before 2026-11-22T09:00:00Z were not checked"
		if r := recordOf(t, h.store, key); r.Truncated != want {
			t.Fatalf("Truncated = %q, want %q", r.Truncated, want)
		}
	})

	t.Run("a scan with nothing flagged still writes a record", func(t *testing.T) {
		h := newStubHandback(t, nil)
		note, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err != nil || note.Count != 0 || note.ID == "" {
			t.Fatalf("note = %+v, %v", note, err)
		}
		if _, ok, err := h.store.LastScan(key); err != nil || !ok {
			t.Fatalf("LastScan ok=%v err=%v: the box-stop scan has nothing to start from", ok, err)
		}
	})

	// R8: a scan that stopped at a cap did not look at every file, so the next
	// scan must start where this one did.
	t.Run("a truncated scan does not move the window past what it did not check", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = handback.Report{Truncated: "walk time budget of 30s reached", Incomplete: true}
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
			t.Fatal(err)
		}
		last, ok, err := h.store.LastScan(key)
		if err != nil || !ok || !last.Equal(started) {
			t.Fatalf("LastScan = %v %v %v, want the scan's own since %v", last, ok, err, started)
		}
		// And the unknown-start sentence joins the cap's, not replaces it.
		h2 := newStubHandback(t, nil)
		h2.rep = handback.Report{Truncated: "walk time budget of 30s reached", Incomplete: true}
		if _, err := h2.ScanInPlace(context.Background(), ws, started, false); err != nil {
			t.Fatal(err)
		}
		if r := recordOf(t, h2.store, key); !strings.Contains(r.Truncated, "walk time budget") || !strings.Contains(r.Truncated, "start time unknown") {
			t.Fatalf("Truncated = %q", r.Truncated)
		}
	})

	// R15: a scan that could not look says so in a record, rather than only in a log.
	t.Run("an inspection error is recorded, not lost", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.err = errors.New("read dir: input/output error")
		note, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err != nil || note.Count != 0 {
			t.Fatalf("note = %+v, %v", note, err)
		}
		r := recordOf(t, h.store, key)
		if !strings.Contains(r.Truncated, "could not be read") || !r.LastScan.Equal(started) {
			t.Fatalf("record = %+v, want a note and the window left at the scan's own start", r)
		}
	})

	t.Run("a workspace that cannot be opened is recorded with the reason", func(t *testing.T) {
		h := newStubHandback(t, nil)
		gone := filepath.Join(ws, "gone")
		note, err := h.ScanInPlace(context.Background(), gone, started, true)
		if err != nil || note.Count != 0 || note.ID == "" {
			t.Fatalf("note = %+v, %v", note, err)
		}
		r := recordOf(t, h.store, handback.InPlaceKey(gone))
		if !strings.HasPrefix(r.Truncated, "the workspace could not be opened: ") || !r.LastScan.Equal(started) {
			t.Fatalf("record = %+v", r)
		}
		if len(h.got()) != 0 {
			t.Fatal("a workspace that could not be opened was walked")
		}
	})

	// R11: the window starts at the earlier of the session's start and the last
	// scan, so what no scan checked is never skipped.
	t.Run("a known start later than the last scan still covers the gap", func(t *testing.T) {
		h := newStubHandback(t, nil)
		prior := started.Add(-5 * time.Hour)
		if _, err := h.store.Merge(key, handback.Record{Kind: handback.KindInPlace, Workspace: ws}, prior, handback.Report{}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
			t.Fatal(err)
		}
		if c := h.got(); len(c) != 1 || !c[0].since.Equal(prior) {
			t.Fatalf("inspect calls = %+v, want one from the earlier LastScan %v", c, prior)
		}
	})
	t.Run("a known start earlier than the last scan is kept", func(t *testing.T) {
		h := newStubHandback(t, nil)
		if _, err := h.store.Merge(key, handback.Record{Kind: handback.KindInPlace, Workspace: ws}, started.Add(time.Hour), handback.Report{}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
			t.Fatal(err)
		}
		if c := h.got(); len(c) != 1 || !c[0].since.Equal(started) {
			t.Fatalf("inspect calls = %+v, want one from the session start %v", c, started)
		}
	})

	// R13: hostile input reaching a bug must not take the daemon down.
	t.Run("a panic in the walk is recorded and does not escape", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.panics = true
		note, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err != nil || note.Count != 0 {
			t.Fatalf("note = %+v, %v", note, err)
		}
		r := recordOf(t, h.store, key)
		if r.Truncated != "the scan stopped with an internal error" || !r.LastScan.Equal(started) {
			t.Fatalf("record = %+v", r)
		}
	})

	t.Run("a record that cannot be written is an error, not silence", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = onePDF()
		dir := h.store.Dir
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, filepath.Join(dir, handback.RecordID(key)+".json")); err != nil {
			t.Fatal(err)
		}
		_, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err == nil || !strings.Contains(err.Error(), "cannot record") {
			t.Fatalf("err = %v, want a record error", err)
		}
		if b, _ := os.ReadFile(other); string(b) != "x" {
			t.Fatalf("the symlink target was written: %q", b)
		}
	})
}

// The real walk end to end: a disguised executable written after the start is
// flagged and recorded; nothing else is.
func elfExecHead() []byte {
	elf := make([]byte, 64) // a 64-bit little-endian ELF executable head
	copy(elf, "\x7fELF")
	elf[4], elf[5], elf[6], elf[16] = 2, 1, 1, 2
	return elf
}

// R11 end to end: session B starts long after the last scan on record, and a
// file written in the gap (by a process session A left behind) is still found.
func TestScanInPlaceCoversTheGapBeforeTheSessionStart(t *testing.T) {
	ws := realDir(t)
	key := handback.InPlaceKey(ws)
	h := newHandbackHooks(t.TempDir(), nil)
	lastScan := time.Now().Add(-2 * time.Hour)
	if _, err := h.store.Merge(key, handback.Record{Kind: handback.KindInPlace, Workspace: ws}, lastScan, handback.Report{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "gap.pdf"), elfExecHead(), 0o644); err != nil {
		t.Fatal(err)
	}
	// Session B "started" an hour from now (clock skew, or simply long after
	// the file): scanning from its start alone would skip the file.
	sessionStart := time.Now().Add(time.Hour)
	note, err := h.ScanInPlace(context.Background(), ws, sessionStart, true)
	if err != nil || note.Count != 1 {
		t.Fatalf("note = %+v, %v; want the gap file flagged", note, err)
	}
}

// R12: the workspace path is the one the box was made over. If it is a symlink
// by scan time (another box swapped it), the host must not walk where it leads.
func TestScanInPlaceRefusesAWorkspaceThatWasSwapped(t *testing.T) {
	t.Run("a directory replaced by a symlink is not walked", func(t *testing.T) {
		parent := realDir(t)
		ws := filepath.Join(parent, "proj")
		if err := os.Mkdir(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		secret := realDir(t) // stands for ~/.aws
		if err := os.WriteFile(filepath.Join(secret, "creds.pdf"), elfExecHead(), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(ws, filepath.Join(parent, "proj.moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, ws); err != nil {
			t.Fatal(err)
		}
		h := newHandbackHooks(t.TempDir(), nil)
		note, err := h.ScanInPlace(context.Background(), ws, time.Now().Add(-time.Hour), true)
		if err != nil || note.Count != 0 || note.ID == "" {
			t.Fatalf("note = %+v, %v", note, err)
		}
		r := recordOf(t, h.store, handback.InPlaceKey(ws))
		if r.Truncated != "the workspace path changed since the session started; it was not scanned" || len(r.Flags) != 0 {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("a parent directory replaced by a symlink is not walked", func(t *testing.T) {
		root := realDir(t)
		parent := filepath.Join(root, "code")
		ws := filepath.Join(parent, "proj")
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		other := realDir(t)
		if err := os.Mkdir(filepath.Join(other, "proj"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "proj", "x.pdf"), elfExecHead(), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(parent, parent+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, parent); err != nil {
			t.Fatal(err)
		}
		h := newHandbackHooks(t.TempDir(), nil)
		note, err := h.ScanInPlace(context.Background(), ws, time.Now().Add(-time.Hour), true)
		if err != nil || note.Count != 0 {
			t.Fatalf("note = %+v, %v", note, err)
		}
		if r := recordOf(t, h.store, handback.InPlaceKey(ws)); !strings.Contains(r.Truncated, "path changed") || len(r.Flags) != 0 {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("the box-stop scan applies the same check", func(t *testing.T) {
		parent := realDir(t)
		ws := filepath.Join(parent, "proj")
		if err := os.Mkdir(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		h := newHandbackHooks(t.TempDir(), nil)
		prior := time.Now().Add(-time.Hour)
		if _, err := h.store.Merge(handback.InPlaceKey(ws), handback.Record{Kind: handback.KindInPlace, Workspace: ws}, prior, handback.Report{}); err != nil {
			t.Fatal(err)
		}
		secret := realDir(t)
		if err := os.WriteFile(filepath.Join(secret, "creds.pdf"), elfExecHead(), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Rename(ws, filepath.Join(parent, "proj.moved"))
		if err := os.Symlink(secret, ws); err != nil {
			t.Fatal(err)
		}
		h.OnStop("box-1", ws)
		r := recordOf(t, h.store, handback.InPlaceKey(ws))
		if len(r.Flags) != 0 || !strings.Contains(r.Truncated, "path changed") || !r.LastScan.Equal(prior) {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("a stable path is scanned", func(t *testing.T) {
		ws := realDir(t)
		if err := os.WriteFile(filepath.Join(ws, "a.pdf"), elfExecHead(), 0o644); err != nil {
			t.Fatal(err)
		}
		h := newHandbackHooks(t.TempDir(), nil)
		note, err := h.ScanInPlace(context.Background(), ws, time.Now().Add(-time.Hour), true)
		if err != nil || note.Count != 1 {
			t.Fatalf("note = %+v, %v", note, err)
		}
	})
}

func TestScanInPlaceRealWalk(t *testing.T) {
	ws := realDir(t)
	elf := elfExecHead()
	if err := os.WriteFile(filepath.Join(ws, "report.pdf"), elf, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHandbackHooks(t.TempDir(), nil)
	note, err := h.ScanInPlace(context.Background(), ws, time.Now().Add(-time.Hour), true)
	if err != nil || note.Count != 1 || !note.InPlace {
		t.Fatalf("note = %+v, %v", note, err)
	}
	recs, _ := h.store.Pending()
	if len(recs) != 1 || recs[0].Flags[0].Path != "report.pdf" || recs[0].Kind != handback.KindInPlace || recs[0].Workspace != ws {
		t.Fatalf("records = %+v", recs)
	}
}

func TestOnStopScansInPlaceWorkspace(t *testing.T) {
	ws := realDir(t)
	prior := time.Date(2026, 11, 22, 10, 30, 0, 0, time.UTC)
	seed := func(h *stubHandback, ws string) {
		t.Helper()
		if _, err := h.store.Merge(handback.InPlaceKey(ws), handback.Record{Kind: handback.KindInPlace, Workspace: ws}, prior, handback.Report{}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a workspace with a record is scanned from its LastScan", func(t *testing.T) {
		h := newStubHandback(t, func(string) bool { return false })
		h.rep = onePDF()
		seed(h, ws)
		h.OnStop("box-1", ws)
		if c := h.got(); len(c) != 1 || c[0].root != ws || !c[0].since.Equal(prior) {
			t.Fatalf("inspect calls = %+v, want one of %s from %v", c, ws, prior)
		}
		recs, _ := h.store.Pending()
		if len(recs) != 1 || len(recs[0].Flags) != 1 || !recs[0].LastScan.After(prior) {
			t.Fatalf("records = %+v, want the new flag and a later LastScan", recs)
		}
	})
	t.Run("a panic in the scan does not escape the hook", func(t *testing.T) {
		h := newStubHandback(t, func(string) bool { panic("owned check blew up") })
		seed(h, ws)
		h.OnStop("box-1", ws) // must not panic
		h2 := newStubHandback(t, func(string) bool { return false })
		h2.panics = true
		seed(h2, ws)
		h2.OnStop("box-1", ws) // must not panic
	})
	t.Run("a workspace with no record is not scanned", func(t *testing.T) {
		h := newStubHandback(t, func(string) bool { return false })
		h.OnStop("box-1", ws)
		if c := h.got(); len(c) != 0 {
			t.Fatalf("inspect calls = %+v, want none", c)
		}
	})
	t.Run("a clone dir is not scanned", func(t *testing.T) {
		h := newStubHandback(t, func(dir string) bool { return dir == ws })
		seed(h, ws)
		h.OnStop("box-1", ws)
		if c := h.got(); len(c) != 0 {
			t.Fatalf("inspect calls = %+v, want none", c)
		}
	})
	t.Run("an unknown workspace is not scanned", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.OnStop("box-1", "")
		if c := h.got(); len(c) != 0 {
			t.Fatalf("inspect calls = %+v, want none", c)
		}
	})
}

func TestReturnSummaryFlagged(t *testing.T) {
	of := "/home/u/repo"
	t.Run("a branch with flags is recorded and listed", func(t *testing.T) {
		h := newStubHandback(t, nil)
		rep := onePDF()
		rep.Flags = append(rep.Flags, handback.Flag{Path: "run.sh", Classes: []handback.Class{handback.ClassScript}, Detail: "script with exec bit"})
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Home: []string{"box/x"}, Handback: map[string]handback.Report{"box/x": rep},
		})
		want := []session.HandbackNote{{ID: handback.RecordID(handback.CloneKey(of, "x")), Count: 2, Branch: "box/x"}}
		if len(sum.Flagged) != 1 || sum.Flagged[0] != want[0] {
			t.Fatalf("Flagged = %+v, want %+v", sum.Flagged, want)
		}
		recs, err := h.store.Pending()
		if err != nil || len(recs) != 1 || recs[0].Kind != handback.KindClone || recs[0].CloneOf != of || recs[0].Branch != "box/x" || len(recs[0].Flags) != 2 {
			t.Fatalf("records = %+v, %v", recs, err)
		}
	})
	t.Run("a zero-flag branch is merged but not listed", func(t *testing.T) {
		h := newStubHandback(t, nil)
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/y": {Inspected: 4}},
		})
		if len(sum.Flagged) != 0 {
			t.Fatalf("Flagged = %+v, want none", sum.Flagged)
		}
		if r := recordOf(t, h.store, handback.CloneKey(of, "y")); r.Branch != "box/y" || r.Inspected != 4 {
			t.Fatalf("a zero-flag branch must still be merged: %+v", r)
		}
	})
	// R14: Flagged reports what this return found. Older flags that are still
	// pending stay in the record (only "reviewed" clears them) but a return that
	// flags nothing does not announce them again.
	t.Run("a return that flags nothing on a branch with older pending flags lists nothing", func(t *testing.T) {
		h := newStubHandback(t, nil)
		first := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/x": onePDF()},
		})
		if len(first.Flagged) != 1 || first.Flagged[0].Count != 1 {
			t.Fatalf("setup: %+v", first)
		}
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/x": {Inspected: 4}},
		})
		if len(sum.Flagged) != 0 {
			t.Fatalf("Flagged = %+v, want none: this return flagged nothing", sum.Flagged)
		}
		if recs, _ := h.store.Pending(); len(recs) != 1 || len(recs[0].Flags) != 1 {
			t.Fatalf("the older flag must stay pending: %+v", recs)
		}
	})
	t.Run("the count is this return's, not the record's", func(t *testing.T) {
		h := newStubHandback(t, nil)
		returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/x": onePDF()},
		})
		second := handback.Report{Flags: []handback.Flag{{Path: "run.sh", Classes: []handback.Class{handback.ClassScript}, Detail: "script with exec bit"}}}
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/x": second},
		})
		if len(sum.Flagged) != 1 || sum.Flagged[0].Count != 1 {
			t.Fatalf("Flagged = %+v, want Count 1 (this return's), though the record holds 2", sum.Flagged)
		}
		if recs, _ := h.store.Pending(); len(recs) != 1 || len(recs[0].Flags) != 2 {
			t.Fatalf("records = %+v", recs)
		}
	})
	t.Run("a record that cannot be written is logged in the summary", func(t *testing.T) {
		h := newStubHandback(t, nil)
		if err := os.MkdirAll(h.store.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, filepath.Join(h.store.Dir, handback.RecordID(handback.CloneKey(of, "x"))+".json")); err != nil {
			t.Fatal(err)
		}
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Home: []string{"box/x"}, Warnings: []string{"earlier warning"}, Handback: map[string]handback.Report{"box/x": onePDF()},
		})
		if len(sum.Flagged) != 0 || len(sum.Warnings) != 2 || sum.Warnings[0] != "earlier warning" ||
			!strings.Contains(sum.Warnings[1], "box/x: 1 file(s) flagged for review, but they could not be recorded") {
			t.Fatalf("summary = %+v", sum)
		}
		if b, _ := os.ReadFile(other); string(b) != "x" {
			t.Fatalf("the symlink target was written: %q", b)
		}
	})
	t.Run("no hooks records nothing", func(t *testing.T) {
		sum := returnSummaryOf(session.ReturnOnExit, of, nil, clone.ReturnResult{Handback: map[string]handback.Report{"box/x": onePDF()}})
		if len(sum.Flagged) != 0 || len(sum.Warnings) != 0 {
			t.Fatalf("summary = %+v", sum)
		}
	})
}

// doReturn wires the hooks through: a return whose result carries a report
// lands in the store under the profile's clone_of.
func TestDoReturnRecordsFlags(t *testing.T) {
	hb := newStubHandback(t, nil)
	h := &cloneHooks{
		clones:   func() []config.Clone { return nil },
		file:     filepath.Join(t.TempDir(), cloneDirsFile),
		handback: hb.handbackHooks,
		ret: func(context.Context, clone.Git, clone.Docker, clone.ReturnRequest) clone.ReturnResult {
			return clone.ReturnResult{Home: []string{"box/x"}, Handback: map[string]handback.Report{"box/x": onePDF()}}
		},
	}
	sum := h.doReturn(context.Background(), config.Clone{Of: "/home/u/repo", Dir: "/c/r"}, session.ReturnOnExit, clone.ReturnAuto)
	if len(sum.Flagged) != 1 || sum.Flagged[0].Branch != "box/x" || sum.Flagged[0].Count != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

// The box manager is built before the hooks that need the clone layer exist, so
// its OnStop is late-bound through the layer.
func TestBoxLayerOnStopIsLateBound(t *testing.T) {
	l := &boxLayer{}
	o := l.options(config.Config{})
	if o.OnStop == nil {
		t.Fatal("the box options carry no OnStop")
	}
	o.OnStop("box-0", "/w") // nothing installed yet: must not panic
	var gotName, gotWS string
	l.setOnStop(func(name, ws string) { gotName, gotWS = name, ws })
	o.OnStop("box-1", "/home/u/work")
	if gotName != "box-1" || gotWS != "/home/u/work" {
		t.Fatalf("hook got (%q, %q)", gotName, gotWS)
	}
	l.setOnStop(nil)
	o.OnStop("box-2", "/x")
	if gotName != "box-1" {
		t.Fatal("a removed hook was still called")
	}
}

// R16: a note says when its scan was cut short, so a count of 0 is not read
// later as "nothing flagged".
func TestHandbackNoteSaysTruncated(t *testing.T) {
	ws := realDir(t)
	started := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	t.Run("in-place scan cut short", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = handback.Report{Truncated: "walk time budget of 30s reached"}
		note, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err != nil || note.Count != 0 || !note.Truncated {
			t.Fatalf("note = %+v, %v; want Count 0, Truncated", note, err)
		}
	})
	t.Run("in-place scan that stopped on a path change", func(t *testing.T) {
		h := newStubHandback(t, nil)
		note, err := h.ScanInPlace(context.Background(), filepath.Join(ws, "gone"), started, true)
		if err != nil || !note.Truncated {
			t.Fatalf("note = %+v, %v; want Truncated", note, err)
		}
	})
	t.Run("in-place scan that ran in full", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = handback.Report{Inspected: 2}
		note, err := h.ScanInPlace(context.Background(), ws, started, true)
		if err != nil || note.Truncated {
			t.Fatalf("note = %+v, %v; want not Truncated", note, err)
		}
	})
	t.Run("a clone branch with nothing flagged but cut short is listed", func(t *testing.T) {
		of := "/home/u/repo"
		h := newStubHandback(t, nil)
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Home: []string{"box/x", "box/y"},
			Handback: map[string]handback.Report{
				"box/x": {Truncated: "inspection unavailable"},
				"box/y": {Inspected: 4},
			},
		})
		want := session.HandbackNote{ID: handback.RecordID(handback.CloneKey(of, "x")), Count: 0, Branch: "box/x", Truncated: true}
		if len(sum.Flagged) != 1 || sum.Flagged[0] != want {
			t.Fatalf("Flagged = %+v, want only %+v", sum.Flagged, want)
		}
	})
}

// R21: only a walk that stopped early rolls the window back. A reason that
// comes back on every scan (an unreadable directory) must let it advance, or a
// reviewed file would flag again at every exit.
func TestScanInPlaceWindowRollbackIsForIncompleteWalksOnly(t *testing.T) {
	ws := realDir(t)
	key := handback.InPlaceKey(ws)
	started := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

	t.Run("a truncated but complete walk advances the window", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = handback.Report{Truncated: "3 entries could not be read"}
		before := h.clock
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
			t.Fatal(err)
		}
		if last, _, _ := h.store.LastScan(key); !last.Equal(before) {
			t.Fatalf("LastScan = %v, want the scan start %v", last, before)
		}
	})
	t.Run("an incomplete walk rolls the window back", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.rep = handback.Report{Truncated: "time budget of 30s used up", Incomplete: true}
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
			t.Fatal(err)
		}
		if last, _, _ := h.store.LastScan(key); !last.Equal(started) {
			t.Fatalf("LastScan = %v, want %v", last, started)
		}
		if r := recordOf(t, h.store, key); r.LastScanComplete {
			t.Fatalf("record = %+v, want last_scan_complete false", r)
		}
	})
	t.Run("an unreadable directory and a script: reviewed once, not flagged again", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads chmod 000 directories")
		}
		ws := realDir(t)
		if err := os.MkdirAll(filepath.Join(ws, "pgdata"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(ws, "pgdata"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(ws, "pgdata"), 0o755) })
		if err := os.WriteFile(filepath.Join(ws, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		h := newHandbackHooks(t.TempDir(), nil)
		key := handback.InPlaceKey(ws)
		note, err := h.ScanInPlace(context.Background(), ws, time.Now().Add(-time.Hour), true)
		if err != nil || note.Count != 1 {
			t.Fatalf("scan 1: note = %+v, %v", note, err)
		}
		rec := recordOf(t, h.store, key)
		if !strings.Contains(rec.Truncated, "could not be read") || rec.LastScan.Before(time.Now().Add(-time.Minute)) {
			t.Fatalf("scan 1 must advance LastScan despite the unreadable directory: %+v", rec)
		}
		if err := h.store.Reviewed(rec.ID, rec.LastScan); err != nil {
			t.Fatal(err)
		}
		// Scan 2 reaches back (the exit scan looks at the last check) and
		// sees run.sh again; it is already reviewed.
		note, err = h.ScanInPlace(context.Background(), ws, time.Now().Add(-time.Hour), true)
		if err != nil || note.Count != 0 {
			t.Fatalf("scan 2: note = %+v, %v; run.sh was reviewed", note, err)
		}
		if p, _ := h.store.Pending(); len(p) != 1 || len(p[0].Flags) != 0 || p[0].Truncated == "" {
			t.Fatalf("pending = %+v, want only the unreadable-entries note", p)
		}
	})
}

// R24: an exit scan reaches back to the last check only when the box was not
// completely checked when it stopped.
func TestScanInPlaceExitWindow(t *testing.T) {
	ws := realDir(t)
	key := handback.InPlaceKey(ws)
	created := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	last := created.Add(-5 * time.Hour)
	run := func(t *testing.T, kind string, incomplete bool) (since time.Time) {
		t.Helper()
		h := newStubHandback(t, nil)
		base := handback.Record{Kind: handback.KindInPlace, Workspace: ws, LastScanKind: kind}
		if _, err := h.store.Merge(key, base, last, handback.Report{Incomplete: incomplete}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ScanInPlace(context.Background(), ws, created, true); err != nil {
			t.Fatal(err)
		}
		c := h.got()
		if len(c) != 1 {
			t.Fatalf("calls = %+v", c)
		}
		return c[0].since
	}
	if got := run(t, handback.ScanStop, false); !got.Equal(created) {
		t.Errorf("after a complete stop scan the window starts at the session (a host-built file between sessions is not flagged): got %v want %v", got, created)
	}
	if got := run(t, handback.ScanExit, false); !got.Equal(last) {
		t.Errorf("after an exit-only record the window looks back: got %v want %v", got, last)
	}
	if got := run(t, handback.ScanStop, true); !got.Equal(last) {
		t.Errorf("after an incomplete stop scan the window looks back: got %v want %v", got, last)
	}
	if got := run(t, "", false); !got.Equal(last) {
		t.Errorf("a record with no kind looks back: got %v want %v", got, last)
	}

	t.Run("the box-stop scan starts at the last scan and records itself as a stop", func(t *testing.T) {
		h := newStubHandback(t, nil)
		if _, err := h.store.Merge(key, handback.Record{Kind: handback.KindInPlace, Workspace: ws, LastScanKind: handback.ScanExit}, last, handback.Report{}); err != nil {
			t.Fatal(err)
		}
		h.OnStop("box-1", ws)
		if c := h.got(); len(c) != 1 || !c[0].since.Equal(last) {
			t.Fatalf("calls = %+v, want one from %v", c, last)
		}
		if r := recordOf(t, h.store, key); r.LastScanKind != handback.ScanStop || !r.LastScanComplete {
			t.Fatalf("record = %+v, want kind stop, complete", r)
		}
	})
	t.Run("an exit scan records itself as an exit", func(t *testing.T) {
		h := newStubHandback(t, nil)
		if _, err := h.ScanInPlace(context.Background(), ws, created, true); err != nil {
			t.Fatal(err)
		}
		if r := recordOf(t, h.store, key); r.LastScanKind != handback.ScanExit {
			t.Fatalf("record = %+v", r)
		}
	})
	t.Run("a scan that could not run is incomplete", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.err = errors.New("read dir: input/output error")
		if _, err := h.ScanInPlace(context.Background(), ws, created, true); err != nil {
			t.Fatal(err)
		}
		if r := recordOf(t, h.store, key); r.LastScanComplete {
			t.Fatalf("record = %+v, want last_scan_complete false", r)
		}
	})
}

// R22: the note counts what is new to the user, not what was cleared before.
func TestScanInPlaceCountsOnlyUnreviewedFlags(t *testing.T) {
	ws := realDir(t)
	key := handback.InPlaceKey(ws)
	h := newStubHandback(t, nil)
	h.rep = onePDF()
	started := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
		t.Fatal(err)
	}
	rec := recordOf(t, h.store, key)
	if err := h.store.Reviewed(rec.ID, rec.LastScan); err != nil {
		t.Fatal(err)
	}
	note, err := h.ScanInPlace(context.Background(), ws, started, true)
	if err != nil || note.Count != 0 {
		t.Fatalf("note = %+v, %v; the same file was already reviewed", note, err)
	}
}

func TestPrintFlags(t *testing.T) {
	var b strings.Builder
	printFlags(&b, handback.Report{Flags: []handback.Flag{
		{Path: "ln", Classes: []handback.Class{handback.ClassSymlinkOut}, Detail: "symlink points outside the workspace", Target: "/etc/passwd"},
		{Path: "a.pdf", Classes: []handback.Class{handback.ClassMismatch}, Detail: "ELF named .pdf", SHA256: strings.Repeat("0a", 32)},
	}})
	got := b.String()
	for _, want := range []string{
		"  ln  symlink points outside the workspace  -> /etc/passwd\n",
		"  a.pdf  ELF named .pdf  sha256 " + strings.Repeat("0a", 32) + "\n",
		"This is not a malware scan.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}

	b.Reset()
	printFlags(&b, handback.Report{})
	if b.String() != "Nothing flagged. This is not a malware scan.\n" {
		t.Errorf("finished scan, nothing flagged: %q", b.String())
	}

	// A scan that did not see everything cannot say "nothing flagged".
	b.Reset()
	printFlags(&b, handback.Report{Truncated: "time budget of 30s used up"})
	if b.String() != "Not everything was checked: time budget of 30s used up\n" {
		t.Errorf("cut-short scan, no flags: %q", b.String())
	}

	b.Reset()
	printFlags(&b, handback.Report{Flags: []handback.Flag{{Path: "x", Detail: "d"}}, Truncated: "cap"})
	if !strings.Contains(b.String(), "Flagged for your review (1):") || !strings.HasSuffix(b.String(), "Not everything was checked: cap\n") {
		t.Errorf("flags and a note: %q", b.String())
	}
}
