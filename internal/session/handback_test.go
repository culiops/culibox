package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

type scanCall struct {
	ws         string
	since      time.Time
	sinceKnown bool
}

type fakeHandback struct {
	mu         sync.Mutex
	calls      []scanCall
	notScanned []scanCall
	begun      []string
	ended      []string
	note       HandbackNote
	err        error
	panics     bool
}

func (f *fakeHandback) BeginExit(ws string) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begun = append(f.begun, ws)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ended = append(f.ended, ws)
	}
}

func (f *fakeHandback) NotScanned(cwd string, since time.Time) (HandbackNote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notScanned = append(f.notScanned, scanCall{ws: cwd, since: since})
	return HandbackNote{ID: "ns", InPlace: true, Truncated: true}, f.err
}

func (f *fakeHandback) ScanInPlace(_ context.Context, ws string, since time.Time, known bool) (HandbackNote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, scanCall{ws, since, known})
	if f.panics {
		panic("hostile input")
	}
	return f.note, f.err
}

func (f *fakeHandback) got() []scanCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scanCall(nil), f.calls...)
}

func handbackManager(h HandbackHooks) *Manager {
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "exit 0"}}, nil, 1<<16, time.Minute)
	m.SetHandback(h)
	return m
}

func TestHandbackWorkInPlaceOnly(t *testing.T) {
	ws, _ := filepath.EvalSymlinks(t.TempDir())
	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	note := HandbackNote{ID: "abc", Count: 3, InPlace: true}
	mk := func(mut func(*Session)) *Session {
		s := &Session{ID: "claude-1", Cwd: ws, workspace: ws, Created: created, Runtime: config.RuntimeBox}
		mut(s)
		return s
	}
	t.Run("boxed in-place session is scanned from its start minus skew", func(t *testing.T) {
		f := &fakeHandback{note: note}
		m := handbackManager(f)
		s := mk(func(*Session) {})
		m.handbackWork(s)
		calls := f.got()
		if len(calls) != 1 {
			t.Fatalf("calls = %+v, want one", calls)
		}
		if calls[0].ws != ws || !calls[0].since.Equal(created.Add(-2*time.Second)) || !calls[0].sinceKnown {
			t.Fatalf("call = %+v, want ws %q since %v known", calls[0], ws, created.Add(-2*time.Second))
		}
		if got := s.Info().Handback; got == nil || *got != note {
			t.Fatalf("Info().Handback = %+v, want %+v", got, note)
		}
	})
	for name, mut := range map[string]func(*Session){
		"clone session":   func(s *Session) { s.CloneOf = "/home/u/repo" },
		"companion shell": func(s *Session) { s.ParentID = "claude-1" },
		"host-tier":       func(s *Session) { s.Runtime = config.RuntimeHost },
	} {
		t.Run(name+" is not scanned", func(t *testing.T) {
			f := &fakeHandback{note: note}
			m := handbackManager(f)
			s := mk(mut)
			m.handbackWork(s)
			if calls := f.got(); len(calls) != 0 {
				t.Fatalf("calls = %+v, want none", calls)
			}
			if s.Info().Handback != nil {
				t.Fatalf("Info().Handback = %+v, want nil", s.Info().Handback)
			}
		})
	}
	// Host review 2026-11-23 (#346): a workspace that could not be vouched for
	// at spawn leaves a record saying it was not scanned, not only a log line.
	t.Run("no recorded workspace is recorded as not scanned", func(t *testing.T) {
		f := &fakeHandback{note: note}
		m := handbackManager(f)
		s := mk(func(s *Session) { s.workspace = "" })
		m.handbackWork(s)
		if c := f.got(); len(c) != 0 {
			t.Fatalf("scans = %+v, want none", c)
		}
		if len(f.notScanned) != 1 || f.notScanned[0].ws != ws || !f.notScanned[0].since.Equal(created.Add(-2*time.Second)) {
			t.Fatalf("NotScanned calls = %+v, want one for %q", f.notScanned, ws)
		}
		if got := s.Info().Handback; got == nil || !got.Truncated || got.ID != "ns" {
			t.Fatalf("Info().Handback = %+v, want the not-scanned note", got)
		}
	})
	t.Run("a failing scan leaves no note", func(t *testing.T) {
		f := &fakeHandback{err: errors.New("record is a symlink")}
		m := handbackManager(f)
		s := mk(func(*Session) {})
		m.handbackWork(s)
		if len(f.got()) != 1 || s.Info().Handback != nil {
			t.Fatalf("calls=%d note=%+v", len(f.got()), s.Info().Handback)
		}
	})
	t.Run("no hooks is a no-op", func(t *testing.T) {
		m := handbackManager(nil)
		m.handbackWork(mk(func(*Session) {})) // must not panic
	})
	// R12: Cwd is only a spelling; by exit another box may have put a symlink
	// where it was. The scan takes the path the box was made over.
	t.Run("the path recorded at spawn is scanned, whatever Cwd leads to now", func(t *testing.T) {
		f := &fakeHandback{}
		m := handbackManager(f)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(t.TempDir(), link); err != nil { // Cwd now leads elsewhere
			t.Fatal(err)
		}
		m.handbackWork(mk(func(s *Session) { s.Cwd = link }))
		if c := f.got(); len(c) != 1 || c[0].ws != ws {
			t.Fatalf("calls = %+v, want ws %q", c, ws)
		}
	})
	// R13: a panic in the scan must not take the daemon down.
	t.Run("a panicking scan does not escape", func(t *testing.T) {
		f := &fakeHandback{panics: true}
		m := handbackManager(f)
		s := mk(func(*Session) {})
		m.handbackWork(s) // must not panic
		if len(f.got()) != 1 || s.Info().Handback != nil {
			t.Fatalf("calls=%d note=%+v", len(f.got()), s.Info().Handback)
		}
	})
}

// A guessed start reaches the hooks as sinceKnown=false.
func TestHandbackWorkGuessedStart(t *testing.T) {
	f := &fakeHandback{}
	m := handbackManager(f)
	ws, _ := filepath.EvalSymlinks(t.TempDir())
	s := &Session{ID: "claude-1", Cwd: ws, workspace: ws, Created: time.Now(), Runtime: config.RuntimeBox, createdGuessed: true}
	m.handbackWork(s)
	if c := f.got(); len(c) != 1 || c[0].sinceKnown {
		t.Fatalf("calls = %+v, want one with sinceKnown=false", c)
	}
}

// The exit path calls handbackWork for a boxed in-place session whose agent ends.
func TestExitRunsHandbackScan(t *testing.T) {
	f := &fakeHandback{note: HandbackNote{ID: "r1", Count: 1, InPlace: true}}
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "exit 0"}, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	m.SetHandback(f)
	dir := t.TempDir()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want, _ := filepath.EvalSymlinks(dir)
	if s.workspace != want {
		t.Fatalf("workspace = %q, want the real path %q", s.workspace, want)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Info().Handback == nil {
		if time.Now().After(deadline) {
			t.Fatalf("Info().Handback never set; calls = %+v", f.got())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c := f.got(); len(c) != 1 || !c[0].sinceKnown || c[0].ws != want {
		t.Fatalf("calls = %+v, want one of %q with a known start", c, want)
	}
}

// The workspace is only vouched for when the box layer says naming a box after
// it gives the box Wrap made: a path that moved in between leaves the session
// unscanned.
type namedBoxes struct {
	*fakeBoxes
	match bool
}

func (n namedBoxes) NamedFor(string, box.BoxKey, string) bool { return n.match }

func TestSpawnWorkspaceMustMatchTheBox(t *testing.T) {
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	if got := spawnWorkspace(namedBoxes{&fakeBoxes{}, true}, dir, box.BoxKey{}, "ab-box-a"); got != real {
		t.Fatalf("spawnWorkspace = %q, want %q", got, real)
	}
	if got := spawnWorkspace(namedBoxes{&fakeBoxes{}, false}, dir, box.BoxKey{}, "ab-box-a"); got != "" {
		t.Fatalf("a box that is not the one named after the path: spawnWorkspace = %q, want none", got)
	}
	if got := spawnWorkspace(&fakeBoxes{}, dir, box.BoxKey{}, "ab-box-a"); got != real {
		t.Fatalf("a Boxes with no check is taken at its word: spawnWorkspace = %q, want %q", got, real)
	}
	if got := spawnWorkspace(&fakeBoxes{}, filepath.Join(dir, "missing"), box.BoxKey{}, "ab-box-a"); got != "" {
		t.Fatalf("an unresolvable path: spawnWorkspace = %q, want none", got)
	}
}

// The workspace survives a restart through the sidecar, and a sidecar record
// with no start time is a guessed start (the reachable form of "start lost").
func TestReconcileKeepsTheWorkspaceAndGuessesAMissingStart(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("bash-1")
	path := filepath.Join(t.TempDir(), "sessions.json")
	ws, _ := filepath.EvalSymlinks(t.TempDir())
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	sc.put("bash-1", sessionMeta{Agent: "bash", Cwd: filepath.Join(ws, "renamed"), Workspace: ws, Runtime: config.RuntimeBox, Box: "ab-box-x"}) // no Created
	m := newReconcileManager(t, fb, path)
	m.SetBoxes(&fakeBoxes{})
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("bash-1") })
	s, ok := m.Get("bash-1")
	if !ok {
		t.Fatal("session not recovered")
	}
	if s.workspace != ws || !s.createdGuessed {
		t.Fatalf("workspace = %q guessed = %v, want %q and true", s.workspace, s.createdGuessed, ws)
	}
}

func TestReturnSummaryCopiesFlagged(t *testing.T) {
	r := &ReturnSummary{Flagged: []HandbackNote{{ID: "a", Count: 2, Branch: "box/x"}}}
	c := r.copy()
	c.Flagged[0].Count = 9
	if r.Flagged[0].Count != 2 {
		t.Fatal("copy shares Flagged with the original")
	}
}

// Host review 2026-11-23 (#346): an exclusive or guard-locked box stops as soon
// as it is released, and its stop hook must already know that the session's
// exit scan is coming, or it records a false "not checked" note.
func TestExitScanIsAnnouncedBeforeTheBoxIsReleased(t *testing.T) {
	f := &fakeHandback{note: HandbackNote{ID: "r1", InPlace: true}}
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "exit 0"}, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	var begunAtRelease []string
	boxes.onRelease = func() {
		f.mu.Lock()
		begunAtRelease = append([]string(nil), f.begun...)
		f.mu.Unlock()
	}
	m.SetBoxes(boxes)
	m.SetHandback(f)
	dir := t.TempDir()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want, _ := filepath.EvalSymlinks(dir)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.ended)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the exit scan never ended; begun %v", f.begun)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(begunAtRelease) != 1 || begunAtRelease[0] != want {
		t.Fatalf("begun at release = %v, want [%s]", begunAtRelease, want)
	}
	if len(f.got()) != 1 {
		t.Fatalf("scans = %+v, want one, ended after it", f.got())
	}
}

// Round 2 of the #346 rework review: Kill releases the box itself, before the
// retention goroutine wakes; the scan must be announced on that path too, and
// ended once it has run.
func TestKillAnnouncesTheExitScanBeforeRelease(t *testing.T) {
	f := &fakeHandback{note: HandbackNote{ID: "r1", InPlace: true}}
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	var begunAtRelease []string
	boxes.onRelease = func() {
		f.mu.Lock()
		begunAtRelease = append([]string(nil), f.begun...)
		f.mu.Unlock()
	}
	m.SetBoxes(boxes)
	m.SetHandback(f)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if len(begunAtRelease) != 1 {
		t.Fatalf("begun at release = %v, want the scan announced first", begunAtRelease)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.ended)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the announced scan was never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
