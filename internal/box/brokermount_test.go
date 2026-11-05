package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bound box mounts its OWN broker socket directory, and the broker takes the
// box for whoever's socket the request arrives on. A box that mounts another
// box's directory would be taken for that box, so the manager never creates one
// and never reuses one.

const otherBroker = "ab-box-ffffffffffff"

// brokerManager is a manager whose boxes may be bound (fenced).
func brokerManager(t *testing.T) (*Manager, *fakeClient) {
	t.Helper()
	m, f := newTestManagerWithFake(t)
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: okVerify})
	return m, f
}

// ownBrokerMount is the mount the broker hands a bound spawn for box name; its
// directory exists, as the broker creates it before the box starts.
func ownBrokerMount(t *testing.T, m *Manager, name string) Mount {
	t.Helper()
	dir := BrokerDir(m.opts.StateRoot, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return Mount{Host: dir, Box: BrokerMountDest}
}

func TestBrokerDirIsPerBoxUnderTheStateRoot(t *testing.T) {
	got := BrokerDir("/s", "ab-box-0123456789ab")
	if want := filepath.Join("/s", "broker", "b", "0123456789ab"); got != want {
		t.Fatalf("BrokerDir = %q, want %q", got, want)
	}
	if BrokerMountDest != "/opt/agentbox-broker" {
		t.Fatalf("BrokerMountDest = %q", BrokerMountDest)
	}
}

// The reviewer's flip: the session layer named the box N and prepared N's broker
// directory, then a symlink on the workspace path was repointed, so Wrap names
// another box M. It must refuse BEFORE anything is created: M with N's directory
// mounted would be taken for N from then on.
func TestWrapRefusesToCreateABoxWithAnotherBoxsBrokerDir(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expect bool // the session layer passes the name it planned
	}{{"planned name passed", true}, {"mount host alone", false}} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := brokerManager(t)
			dirA, dirB := t.TempDir(), t.TempDir()
			link := filepath.Join(t.TempDir(), "ws")
			if err := os.Symlink(dirA, link); err != nil {
				t.Fatal(err)
			}
			key := BoxKey{Binding: "h1"}
			planned, err := m.BoxName(link, key)
			if err != nil {
				t.Fatal(err)
			}
			mount := ownBrokerMount(t, m, planned)
			if err := os.Remove(link); err != nil { // the flip
				t.Fatal(err)
			}
			if err := os.Symlink(dirB, link); err != nil {
				t.Fatal(err)
			}
			flipped, err := m.BoxName(link, key)
			if err != nil || flipped == planned {
				t.Fatalf("the flip did not change the name: %s %s %v", planned, flipped, err)
			}
			if tc.expect {
				key.Expect = planned
			}
			_, _, _, err = m.Wrap(context.Background(), link, "s", key, []string{"bash"}, nil, []Mount{mount})
			if !errors.Is(err, ErrBoxUnavailable) {
				t.Fatalf("err = %v, want ErrBoxUnavailable", err)
			}
			if f.runs() != 0 || f.stops() != 0 {
				t.Fatalf("a container was created or removed: runs=%d stops=%d", f.runs(), f.stops())
			}
			if _, err := os.Stat(m.stateDir(flipped)); !os.IsNotExist(err) {
				t.Fatalf("the refused box's home was prepared: %v", err)
			}
		})
	}
}

func TestWrapKeepsABoxItsOwnBrokerDir(t *testing.T) {
	m, f := brokerManager(t)
	dir := t.TempDir()
	key := BoxKey{Binding: "h1"}
	name, err := m.BoxName(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	key.Expect = name
	_, _, got, err := m.Wrap(context.Background(), dir, "s", key, []string{"bash"}, nil, []Mount{ownBrokerMount(t, m, name)})
	if err != nil || got != name {
		t.Fatalf("Wrap = %s, %v, want %s", got, err, name)
	}
	last := f.runArgs[len(f.runArgs)-1]
	if !strings.Contains(strings.Join(last, " "), "src="+BrokerDir(m.opts.StateRoot, name)+",dst="+BrokerMountDest) {
		t.Fatalf("the box was not created with its own broker dir: %v", last)
	}
}

// A symlinked spelling of the state root is still the box's own directory.
func TestWrapAcceptsASymlinkedSpellingOfItsOwnBrokerDir(t *testing.T) {
	m, _ := brokerManager(t)
	dir := t.TempDir()
	key := BoxKey{Binding: "h1"}
	name, err := m.BoxName(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	own := ownBrokerMount(t, m, name)
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(m.opts.StateRoot, link); err != nil {
		t.Fatal(err)
	}
	spelled := Mount{Host: BrokerDir(link, name), Box: BrokerMountDest}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s", key, []string{"bash"}, nil, []Mount{spelled}); err != nil {
		t.Fatalf("a symlinked spelling of the box's own dir was refused (%s vs %s): %v", spelled.Host, own.Host, err)
	}
}

func TestWrapRefusesAnotherBoxsBrokerDirOnAnyBox(t *testing.T) {
	m, f := brokerManager(t)
	dir := t.TempDir()
	mount := Mount{Host: BrokerDir(m.opts.StateRoot, otherBroker), Box: BrokerMountDest}
	if err := os.MkdirAll(mount.Host, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s", BoxKey{Binding: "h1"}, []string{"bash"}, nil, []Mount{mount}); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrBoxUnavailable", err)
	}
	if f.runs() != 0 {
		t.Fatalf("a box was created with another box's broker dir: runs=%d", f.runs())
	}
}

// A box that already exists with another box's broker directory (made by a
// build that allowed it) is never trusted: idle, it is removed and recreated
// with its own; live, the spawn is refused with the remedy.
func TestReusedBoxWithAnotherBoxsBrokerDir(t *testing.T) {
	setup := func(t *testing.T) (*Manager, *fakeClient, string, []Mount, string) {
		m, f := brokerManager(t)
		dir := t.TempDir()
		key := BoxKey{Binding: "h1"}
		name, err := m.BoxName(dir, key)
		if err != nil {
			t.Fatal(err)
		}
		mounts := []Mount{ownBrokerMount(t, m, name)}
		if _, _, _, err := m.Wrap(context.Background(), dir, "s1", key, []string{"bash"}, nil, mounts); err != nil {
			t.Fatal(err)
		}
		f.sources[name][BrokerMountDest] = BrokerDir(m.opts.StateRoot, otherBroker) // as an older build could have made it
		return m, f, name, mounts, dir
	}
	key := BoxKey{Binding: "h1"}

	t.Run("idle: removed and recreated with its own", func(t *testing.T) {
		m, f, name, mounts, dir := setup(t)
		m.ReleaseBox(name)
		if _, _, got, err := m.Wrap(context.Background(), dir, "s2", key, []string{"bash"}, nil, mounts); err != nil || got != name {
			t.Fatalf("Wrap = %s, %v", got, err)
		}
		if f.stops() != 1 || f.runs() != 2 {
			t.Fatalf("want the idle box removed and recreated: stops=%d runs=%d", f.stops(), f.runs())
		}
		if src := f.sources[name][BrokerMountDest]; src != BrokerDir(m.opts.StateRoot, name) {
			t.Fatalf("the recreated box mounts %q, want its own %q", src, BrokerDir(m.opts.StateRoot, name))
		}
	})

	t.Run("live: refused with the remedy, never removed", func(t *testing.T) {
		m, f, _, mounts, dir := setup(t)
		_, _, _, err := m.Wrap(context.Background(), dir, "s2", key, []string{"bash"}, nil, mounts)
		if !errors.Is(err, ErrBoxUnavailable) {
			t.Fatalf("err = %v, want ErrBoxUnavailable", err)
		}
		if !strings.Contains(err.Error(), "broker") || !strings.Contains(err.Error(), "end them") {
			t.Fatalf("the refusal must say what is wrong and what to do: %v", err)
		}
		if f.stops() != 0 || f.runs() != 1 {
			t.Fatalf("a live box must never be removed or replaced: stops=%d runs=%d", f.stops(), f.runs())
		}
		m.mu.Lock()
		live := m.active[boundBoxName(t, m, dir)]
		m.mu.Unlock()
		if live != 1 {
			t.Fatalf("the refused spawn must not count as a session: active=%d", live)
		}
	})

	t.Run("its own dir: reused", func(t *testing.T) {
		m, f, name, mounts, dir := setup(t)
		f.sources[name][BrokerMountDest] = BrokerDir(m.opts.StateRoot, name)
		if _, _, _, err := m.Wrap(context.Background(), dir, "s2", key, []string{"bash"}, nil, mounts); err != nil {
			t.Fatal(err)
		}
		if f.stops() != 0 || f.runs() != 1 {
			t.Fatalf("a box with its own dir must be reused: stops=%d runs=%d", f.stops(), f.runs())
		}
	})
}

// boundBoxName is the box name for dir with the test key.
func boundBoxName(t *testing.T, m *Manager, dir string) string {
	t.Helper()
	n, err := m.BoxName(dir, BoxKey{Binding: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Fail closed: when docker cannot say where a running box's broker mount comes
// from, the box is neither reused nor removed. Idle or live, the spawn is
// refused and the box is left as it is (an idle one keeps its timer).
func TestBoxWhoseBrokerMountCannotBeReadIsNeitherReusedNorRemoved(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "idle"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			m, f := brokerManager(t)
			dir := t.TempDir()
			key := BoxKey{Binding: "h1"}
			boxName, err := m.BoxName(dir, key)
			if err != nil {
				t.Fatal(err)
			}
			mounts := []Mount{ownBrokerMount(t, m, boxName)}
			if _, _, _, err := m.Wrap(context.Background(), dir, "s1", key, []string{"bash"}, nil, mounts); err != nil {
				t.Fatal(err)
			}
			if !live {
				m.ReleaseBox(boxName)
			}
			f.bindErr = errors.New("docker inspect: daemon unreachable")
			_, _, _, err = m.Wrap(context.Background(), dir, "s2", key, []string{"bash"}, nil, mounts)
			if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "daemon unreachable") {
				t.Fatalf("err = %v, want ErrBoxUnavailable naming the cause", err)
			}
			if f.stops() != 0 || f.runs() != 1 {
				t.Fatalf("the box must be left alone: stops=%d runs=%d", f.stops(), f.runs())
			}
			m.mu.Lock()
			_, armed := m.timers[boxName]
			active := m.active[boxName]
			m.mu.Unlock()
			if live && active != 1 {
				t.Fatalf("the refused spawn must not count as a session: active=%d", active)
			}
			if !live && !armed {
				t.Fatal("an idle box refused for this must keep its idle timer")
			}
		})
	}
}
