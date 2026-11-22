package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

type scanCall struct {
	ws         string
	since      time.Time
	sinceKnown bool
}

type fakeHandback struct {
	mu    sync.Mutex
	calls []scanCall
	note  HandbackNote
	err   error
}

func (f *fakeHandback) ScanInPlace(_ context.Context, ws string, since time.Time, known bool) (HandbackNote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, scanCall{ws, since, known})
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
	ws := t.TempDir()
	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	note := HandbackNote{ID: "abc", Count: 3, InPlace: true}
	mk := func(mut func(*Session)) *Session {
		s := &Session{ID: "claude-1", Cwd: ws, Created: created, Runtime: config.RuntimeBox}
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
		wantWS, _ := filepath.EvalSymlinks(ws)
		if calls[0].ws != wantWS || !calls[0].since.Equal(created.Add(-2*time.Second)) || !calls[0].sinceKnown {
			t.Fatalf("call = %+v, want ws %q since %v known", calls[0], wantWS, created.Add(-2*time.Second))
		}
		if got := s.Info().Handback; got == nil || *got != note {
			t.Fatalf("Info().Handback = %+v, want %+v", got, note)
		}
	})
	for name, mut := range map[string]func(*Session){
		"clone session":   func(s *Session) { s.CloneOf = "/home/u/repo" },
		"companion shell": func(s *Session) { s.ParentID = "claude-1" },
		"host-tier":       func(s *Session) { s.Runtime = config.RuntimeHost },
		"no workspace":    func(s *Session) { s.Cwd = "" },
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
	t.Run("a workspace that is gone is not scanned", func(t *testing.T) {
		f := &fakeHandback{}
		m := handbackManager(f)
		m.handbackWork(mk(func(s *Session) { s.Cwd = filepath.Join(ws, "missing") }))
		if len(f.got()) != 0 {
			t.Fatalf("calls = %+v, want none", f.got())
		}
	})
	t.Run("a workspace reached through a symlink is scanned by its real path", func(t *testing.T) {
		f := &fakeHandback{}
		m := handbackManager(f)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(ws, link); err != nil {
			t.Fatal(err)
		}
		m.handbackWork(mk(func(s *Session) { s.Cwd = link }))
		want, _ := filepath.EvalSymlinks(ws)
		if c := f.got(); len(c) != 1 || c[0].ws != want {
			t.Fatalf("calls = %+v, want ws %q", c, want)
		}
	})
}

func TestHandbackWorkGuessedStart(t *testing.T) {
	f := &fakeHandback{}
	m := handbackManager(f)
	s := &Session{ID: "claude-1", Cwd: t.TempDir(), Created: time.Now(), Runtime: config.RuntimeBox, createdGuessed: true}
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
	deadline := time.Now().Add(5 * time.Second)
	for s.Info().Handback == nil {
		if time.Now().After(deadline) {
			t.Fatalf("Info().Handback never set; calls = %+v", f.got())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c := f.got(); len(c) != 1 || !c[0].sinceKnown {
		t.Fatalf("calls = %+v, want one with a known start", c)
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
