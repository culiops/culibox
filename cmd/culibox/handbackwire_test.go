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

// stubHandback is a handbackHooks over a temp store whose inspection is a stub.
type stubHandback struct {
	*handbackHooks
	mu    sync.Mutex
	calls []inspectCall
	rep   handback.Report
	err   error
	clock time.Time
}

func newStubHandback(t *testing.T, owned func(string) bool) *stubHandback {
	t.Helper()
	s := &stubHandback{clock: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	s.handbackHooks = newHandbackHooks(t.TempDir(), owned)
	s.inspect = func(_ context.Context, root string, since time.Time, _ handback.Limits) (handback.Report, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, inspectCall{root, since})
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
	ws := "/home/u/work"
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
		h.rep = handback.Report{Truncated: "walk time budget of 30s reached"}
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err != nil {
			t.Fatal(err)
		}
		last, ok, err := h.store.LastScan(key)
		if err != nil || !ok || !last.Equal(started) {
			t.Fatalf("LastScan = %v %v %v, want the scan's own since %v", last, ok, err, started)
		}
		// And the unknown-start sentence joins the cap's, not replaces it.
		h2 := newStubHandback(t, nil)
		h2.rep = handback.Report{Truncated: "walk time budget of 30s reached"}
		if _, err := h2.ScanInPlace(context.Background(), ws, started, false); err != nil {
			t.Fatal(err)
		}
		if r := recordOf(t, h2.store, key); !strings.Contains(r.Truncated, "walk time budget") || !strings.Contains(r.Truncated, "start time unknown") {
			t.Fatalf("Truncated = %q", r.Truncated)
		}
	})

	t.Run("an inspection error is returned and records nothing", func(t *testing.T) {
		h := newStubHandback(t, nil)
		h.err = errors.New("open /home/u/work: no such file or directory")
		if _, err := h.ScanInPlace(context.Background(), ws, started, true); err == nil {
			t.Fatal("want the error")
		}
		if recs, _ := h.store.Pending(); len(recs) != 0 {
			t.Fatalf("records = %+v", recs)
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
func TestScanInPlaceRealWalk(t *testing.T) {
	ws := t.TempDir()
	elf := make([]byte, 64) // a 64-bit little-endian ELF executable head
	copy(elf, "\x7fELF")
	elf[4], elf[5], elf[6], elf[16] = 2, 1, 1, 2
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
	ws := "/home/u/work"
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
	t.Run("zero flags still merge but are not listed", func(t *testing.T) {
		h := newStubHandback(t, nil)
		// An earlier return left a flag for this branch; the new scan found none.
		first := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/x": onePDF()},
		})
		if len(first.Flagged) != 1 {
			t.Fatalf("setup: %+v", first)
		}
		sum := returnSummaryOf(session.ReturnOnExit, of, h.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/x": {Inspected: 4}},
		})
		// The earlier flag is still pending (only marking it reviewed clears it),
		// so the branch still lists it.
		if len(sum.Flagged) != 1 || sum.Flagged[0].Count != 1 {
			t.Fatalf("Flagged = %+v", sum.Flagged)
		}
		h2 := newStubHandback(t, nil)
		sum = returnSummaryOf(session.ReturnOnExit, of, h2.handbackHooks, clone.ReturnResult{
			Handback: map[string]handback.Report{"box/y": {Inspected: 4}},
		})
		if len(sum.Flagged) != 0 {
			t.Fatalf("Flagged = %+v, want none", sum.Flagged)
		}
		if r := recordOf(t, h2.store, handback.CloneKey(of, "y")); r.Branch != "box/y" || r.Inspected != 4 {
			t.Fatalf("a zero-flag branch must still be merged: %+v", r)
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
