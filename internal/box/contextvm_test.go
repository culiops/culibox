package box

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// vmFake stands in for Options.VMIDs (the macOS VM identity read) and keeps one
// log with the fake docker client's run and in-box stat, so a test can say in
// what order they happened.
type vmFake struct {
	mu    sync.Mutex
	log   []string
	calls [][]string
	ids   []FileID
	err   error
}

func (v *vmFake) hook(_ context.Context, paths []string) ([]FileID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.log = append(v.log, "vmids")
	v.calls = append(v.calls, slices.Clone(paths))
	if v.err != nil {
		return nil, v.err
	}
	return slices.Clone(v.ids), nil
}

func (v *vmFake) logged(s string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.log = append(v.log, s)
}

func (v *vmFake) order() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return strings.Join(v.log, ",")
}

func (v *vmFake) n() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.calls)
}

// vmManager is a manager whose Options.VMIDs is v's (nil v = no hook), over a
// fake client whose docker run and in-box stat are logged into v.
func vmManager(t *testing.T, v *vmFake, sf *statFake) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	o := Options{DockerBin: "/usr/bin/docker", Image: "culiops/agent-box:base", Instance: "TEST",
		IdleTTL: time.Hour, StateRoot: t.TempDir(), Client: f}
	if v != nil {
		o.VMIDs = v.hook
		f.runHook = func([]string) (string, bool, error) { v.logged("run"); return "", false, nil }
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if sf != nil {
		sf.install(f)
		if v != nil {
			inner := f.stdoutHook
			f.stdoutHook = func(args []string) (string, bool, error) {
				if len(args) > 2 && args[2] == "/usr/bin/stat" {
					v.logged("stat")
				}
				return inner(args)
			}
		}
	}
	return m, f
}

// checkMounts are context mounts the host recorded no identity for (macOS).
func checkMounts(n int) []Mount {
	ms := ctxMounts(make([]FileID, n)...)
	for i := range ms {
		ms[i].ID, ms[i].CheckID = nil, true
	}
	return ms
}

func wrapWith(m *Manager, dir, sess string, ms []Mount) (string, error) {
	_, _, name, err := m.Wrap(context.Background(), dir, sess, BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	return name, err
}

func TestVMIDsAreTakenOnceBeforeTheBoxIsCreatedAndCompared(t *testing.T) {
	v := &vmFake{ids: []FileID{{44, 393}, {44, 394}}}
	sf := &statFake{out: "44:393\n44:394"}
	m, f := vmManager(t, v, sf)
	ms := checkMounts(2)
	name, err := wrapWith(m, t.TempDir(), "s1", ms)
	if err != nil {
		t.Fatal(err)
	}
	// VM identity -> docker run (the mount) -> in-box compare.
	if got := v.order(); got != "vmids,run,stat" {
		t.Fatalf("order = %q, want vmids,run,stat", got)
	}
	if v.n() != 1 || !slices.Equal(v.calls[0], []string{"/srv/ctx0", "/srv/ctx1"}) {
		t.Fatalf("hook calls = %v, want one call with both paths", v.calls)
	}
	if sf.n() != 1 {
		t.Fatalf("in-box stat ran %d times, want 1: the VM ids must feed the existing compare", sf.n())
	}
	if ms[0].ID != nil || ms[1].ID != nil {
		t.Fatal("the caller's mounts were written to")
	}
	if f.stops() != 0 {
		t.Fatal("a matching box was stopped")
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live != 1 {
		t.Fatalf("active = %d, want the session counted", live)
	}
}

// The window the check closes: the path flipped after the VM read its identity
// and before docker mounted it, so the box shows another file.
func TestVMIDsMismatchWithTheMountRemovesTheCreatedBox(t *testing.T) {
	v := &vmFake{ids: []FileID{{44, 393}, {44, 394}}}
	sf := &statFake{out: "44:393\n44:999"}
	m, f := vmManager(t, v, sf)
	_, err := wrapWith(m, t.TempDir(), "s1", checkMounts(2))
	if err == nil || !errors.Is(err, ErrContextMountChanged) || !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("err = %v, want ErrContextMountChanged and ErrUnboxableWorkspace", err)
	}
	if want := "context_paths /srv/ctx1: " + changedMsg; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the created box removed", f.runs(), f.stops())
	}
}

func TestVMIDsErrorRefusesBeforeAnyBoxIsCreated(t *testing.T) {
	for name, v := range map[string]*vmFake{
		"hook error":  {err: errors.New("reading file identities in the box VM: path 1: a component is a symlink")},
		"short reply": {ids: []FileID{{44, 393}}},
		"long reply":  {ids: []FileID{{44, 393}, {44, 394}, {44, 395}}},
	} {
		t.Run(name, func(t *testing.T) {
			sf := &statFake{out: "44:393\n44:394"}
			m, f := vmManager(t, v, sf)
			_, err := wrapWith(m, t.TempDir(), "s1", checkMounts(2))
			if err == nil || !errors.Is(err, ErrContextMountChanged) || !errors.Is(err, ErrUnboxableWorkspace) {
				t.Fatalf("err = %v, want ErrContextMountChanged and ErrUnboxableWorkspace", err)
			}
			for _, want := range []string{"context_paths /srv/ctx0, /srv/ctx1: ", "refusing"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want %q", err, want)
				}
			}
			if f.runs() != 0 || sf.n() != 0 {
				t.Fatalf("runs=%d stats=%d, want no box and no in-box check", f.runs(), sf.n())
			}
			for n, live := range m.active {
				if live != 0 {
					t.Fatalf("active[%s] = %d after a refused spawn", n, live)
				}
			}
		})
	}
}

func TestVMIDsErrorSaysWhyAndNamesNoValue(t *testing.T) {
	v := &vmFake{err: errors.New("reading file identities in the box VM: colima ssh: exit status 1: path 1: a component is a symlink")}
	m, _ := vmManager(t, v, &statFake{})
	_, err := wrapWith(m, t.TempDir(), "s1", checkMounts(1))
	if err == nil || !strings.Contains(err.Error(), "a component is a symlink") {
		t.Fatalf("err = %v, want the VM's reason in it", err)
	}
}

func TestVMIDsSkipMountsThatNeedNoneOrHaveOne(t *testing.T) {
	t.Run("only the unrecorded context mounts are asked", func(t *testing.T) {
		v := &vmFake{ids: []FileID{{44, 394}}}
		sf := &statFake{out: "10:20\n44:394"}
		m, _ := vmManager(t, v, sf)
		have := FileID{10, 20}
		ms := []Mount{
			{Host: "/srv/ctx0", Box: "/srv/ctx0", ID: &have, CheckID: true}, // recorded: not re-read
			{Host: "/srv/ctx1", Box: "/srv/ctx1", CheckID: true},
			{Host: "/srv/overlay", Box: "/srv/overlay", Optional: true}, // not a context mount: never checked
		}
		if _, err := wrapWith(m, t.TempDir(), "s1", ms); err != nil {
			t.Fatal(err)
		}
		if v.n() != 1 || !slices.Equal(v.calls[0], []string{"/srv/ctx1"}) {
			t.Fatalf("hook calls = %v, want one call for /srv/ctx1 only", v.calls)
		}
		if sf.n() != 1 || !slices.Contains(sf.calls[0], "/srv/ctx1") || slices.Contains(sf.calls[0], "/srv/overlay") {
			t.Fatalf("in-box stat calls = %v, want the two checked mounts and not the overlay", sf.calls)
		}
	})
	t.Run("all recorded so no hook call", func(t *testing.T) {
		v := &vmFake{}
		m, _ := vmManager(t, v, &statFake{out: "10:20\n11:21"})
		ms := ctxMounts(FileID{10, 20}, FileID{11, 21})
		for i := range ms {
			ms[i].CheckID = true
		}
		if _, err := wrapWith(m, t.TempDir(), "s1", ms); err != nil {
			t.Fatal(err)
		}
		if v.n() != 0 {
			t.Fatalf("hook called %d times for mounts that carry an ID", v.n())
		}
	})
	t.Run("plain mounts so no hook call", func(t *testing.T) {
		v := &vmFake{}
		sf := &statFake{}
		m, _ := vmManager(t, v, sf)
		if _, err := wrapWith(m, t.TempDir(), "s1", []Mount{{Host: "/srv/a", Box: "/srv/a"}}); err != nil {
			t.Fatal(err)
		}
		if v.n() != 0 || sf.n() != 0 {
			t.Fatalf("hook=%d stat=%d for a mount nobody asked to check", v.n(), sf.n())
		}
	})
}

// Without the hook (Linux, which records the identity on the host) a mount with
// no ID is unchecked, exactly as before.
func TestNoVMIDsHookLeavesMountsUnchecked(t *testing.T) {
	sf := &statFake{}
	m, _ := vmManager(t, nil, sf)
	if _, err := wrapWith(m, t.TempDir(), "s1", checkMounts(1)); err != nil {
		t.Fatal(err)
	}
	if sf.n() != 0 {
		t.Fatalf("in-box stat ran %d times with no hook and no ID", sf.n())
	}
}

// A reused box is checked too: the hook runs again for the new spawn, before
// the in-box compare, and a context dir swapped since the box started is refused.
func TestVMIDsReusedBoxIsCheckedAgain(t *testing.T) {
	v := &vmFake{ids: []FileID{{44, 393}}}
	sf := &statFake{out: "44:393"}
	m, f := vmManager(t, v, sf)
	dir := t.TempDir()
	if _, err := wrapWith(m, dir, "s1", checkMounts(1)); err != nil {
		t.Fatal(err)
	}
	// The directory was replaced: the VM now reads a new inode, the running box
	// still shows the old one.
	v.mu.Lock()
	v.ids, v.log = []FileID{{44, 777}}, nil
	v.mu.Unlock()
	_, err := wrapWith(m, dir, "s2", checkMounts(1))
	if err == nil || !errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, want the mount refusal for a swapped dir", err)
	}
	if got := v.order(); got != "vmids,stat" {
		t.Fatalf("order on reuse = %q, want vmids,stat (no second run)", got)
	}
	if v.n() != 2 {
		t.Fatalf("hook calls = %d, want one per spawn", v.n())
	}
	if !strings.Contains(err.Error(), "still runs 1 session(s)") {
		t.Fatalf("err = %v, want the live box kept and the remedy given", err)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("runs=%d stops=%d, want the live box kept", f.runs(), f.stops())
	}
}

// A VM identity that cannot be read on reuse refuses the spawn and leaves the
// box alone: nothing was created, and nothing is known to be wrong with it.
func TestVMIDsErrorOnReuseKeepsTheBox(t *testing.T) {
	v := &vmFake{ids: []FileID{{44, 393}}}
	sf := &statFake{out: "44:393"}
	m, f := vmManager(t, v, sf)
	dir := t.TempDir()
	name, err := wrapWith(m, dir, "s1", checkMounts(1))
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	v.mu.Lock()
	v.err = errors.New("the VM is stopped")
	v.mu.Unlock()
	if _, err = wrapWith(m, dir, "s2", checkMounts(1)); err == nil || !errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("runs=%d stops=%d, want the box left as it was", f.runs(), f.stops())
	}
	for n, live := range m.active {
		if live != 0 {
			t.Fatalf("active[%s] = %d after a refused spawn", n, live)
		}
	}
}
