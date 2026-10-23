package box

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// statFake answers the in-box `stat` exec of the context-mount check with out
// (or err) and records each call; every other Run goes on to the fake.
type statFake struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	err   error
}

func (s *statFake) install(f *fakeClient) {
	f.runHook = func(args []string) (string, bool, error) {
		if len(args) < 3 || args[0] != "exec" || args[2] != "stat" {
			return "", false, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, slices.Clone(args))
		return s.out, true, s.err
	}
}

func (s *statFake) set(out string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out, s.err = out, err
}

func (s *statFake) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func ctxMounts(ids ...FileID) []Mount {
	var out []Mount
	for i, id := range ids {
		p := fmt.Sprintf("/srv/ctx%d", i)
		out = append(out, Mount{Host: p, Box: p, ID: &id})
	}
	return out
}

const changedMsg = "the mounted path is not the checked one (it changed while the box started); refusing"

func TestContextMountMatchStartsTheBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{out: "10:20\n11:21"}
	sf.install(f)
	ms := ctxMounts(FileID{10, 20}, FileID{11, 21})
	_, _, name, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", name, "stat", "-c", "%d:%i", "--", "/srv/ctx0", "/srv/ctx1"}
	if sf.n() != 1 || !slices.Equal(sf.calls[0], want) {
		t.Fatalf("in-box stat calls = %v, want one: %v", sf.calls, want)
	}
	if f.stops() != 0 {
		t.Fatalf("a matching box was stopped")
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live != 1 {
		t.Fatalf("active = %d, want the session counted", live)
	}
}

func TestContextMountMismatchRemovesACreatedBox(t *testing.T) {
	for _, c := range []struct {
		name, out string
		err       error
		bad       string // the path the refusal must name
		extra     string
	}{
		{"second differs", "10:20\n11:99", nil, "/srv/ctx1", ""},
		{"first differs", "10:21\n11:21", nil, "/srv/ctx0", ""},
		{"short output", "10:20", nil, "/srv/ctx1", ""},
		{"stat failed", "", errors.New("docker exec: exit status 1: stat: cannot statx"), "/srv/ctx0", "in-box check failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, f := newTestManagerWithFake(t)
			sf := &statFake{out: c.out, err: c.err}
			sf.install(f)
			_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{Context: "c"}, []string{"claude"}, nil,
				ctxMounts(FileID{10, 20}, FileID{11, 21}))
			if err == nil {
				t.Fatal("a box whose mount is not the checked path started")
			}
			if !errors.Is(err, ErrContextMountChanged) || !errors.Is(err, ErrUnboxableWorkspace) {
				t.Fatalf("err = %v, want ErrContextMountChanged and ErrUnboxableWorkspace", err)
			}
			if want := "context_paths " + c.bad + ": " + changedMsg; !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if c.extra != "" && !strings.Contains(err.Error(), c.extra) {
				t.Fatalf("err = %v, want %q", err, c.extra)
			}
			if f.runs() != 1 || f.stops() != 1 {
				t.Fatalf("runs=%d stops=%d, want the created box removed", f.runs(), f.stops())
			}
			for n, live := range m.active {
				if live != 0 {
					t.Fatalf("active[%s] = %d after a refused spawn", n, live)
				}
			}
		})
	}
}

// A box that already runs another session is never removed: the refusal tells
// the user to end those sessions, so the next spawn recreates the box.
func TestContextMountMismatchKeepsALiveBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{out: "10:20"}
	sf.install(f)
	dir := t.TempDir()
	ms := ctxMounts(FileID{10, 20})
	if _, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms); err != nil {
		t.Fatal(err)
	}
	sf.set("10:77", nil)
	_, _, _, err := m.Wrap(context.Background(), dir, "s2", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err == nil || !errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, want the mount refusal", err)
	}
	if !strings.Contains(err.Error(), "still runs 1 session(s)") || !strings.Contains(err.Error(), "end them so the box is recreated") {
		t.Fatalf("err = %v, want the remedy for a live box", err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("stops=%d runs=%d, want the live box kept and reused", f.stops(), f.runs())
	}
	if sf.n() != 2 {
		t.Fatalf("stat ran %d times, want once per spawn (reuse is checked too)", sf.n())
	}
}

// An idle reused box has nothing to lose: it is removed, so the next spawn
// gets a fresh mount.
func TestContextMountMismatchRemovesAnIdleReusedBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{out: "10:20"}
	sf.install(f)
	dir := t.TempDir()
	ms := ctxMounts(FileID{10, 20})
	_, _, name, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	sf.set("10:77", nil)
	if _, _, _, err := m.Wrap(context.Background(), dir, "s2", BoxKey{Context: "c"}, []string{"claude"}, nil, ms); err == nil || !errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, want the mount refusal", err)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the idle box removed, not recreated", f.runs(), f.stops())
	}
}

// Mounts without an ID (every non-context mount, and context mounts where the
// check is not made) cost no docker call.
func TestContextMountNoIDNoExec(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{}
	sf.install(f)
	ms := []Mount{{Host: "/srv/a", Box: "/srv/a"}}
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms); err != nil {
		t.Fatal(err)
	}
	if sf.n() != 0 {
		t.Fatalf("stat ran %d times for mounts without an ID", sf.n())
	}
}

func TestContextMountID(t *testing.T) {
	d := ctxTmp(t)
	dir := filepath.Join(d, "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	id, err := ContextMountID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		if id != nil {
			t.Fatalf("ContextMountID = %+v off Linux, want nil (no in-box check)", id)
		}
		return
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(dir, &st); err != nil {
		t.Fatal(err)
	}
	if id == nil || id.Dev != uint64(st.Dev) || id.Ino != st.Ino {
		t.Fatalf("ContextMountID = %+v, want %d:%d", id, st.Dev, st.Ino)
	}
	lid, err := ContextMountID(link)
	if err != nil {
		t.Fatal(err)
	}
	if lid == nil || *lid == *id {
		t.Fatalf("a symlink must report its own identity, not its target's: %+v vs %+v", lid, id)
	}
	if _, err := ContextMountID(filepath.Join(d, "missing")); err == nil || !errors.Is(err, ErrUnboxableWorkspace) ||
		!strings.Contains(err.Error(), "context_paths "+filepath.Join(d, "missing")+": ") {
		t.Fatalf("missing path: err = %v, want a context_paths refusal", err)
	}
}
