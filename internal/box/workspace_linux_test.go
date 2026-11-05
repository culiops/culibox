//go:build linux

package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// flipManager is a manager whose state root is the folder the workspace path
// leads to once it is flipped, over a fresh fake.
func flipManager(t *testing.T, state string) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: state, Client: f})
	if err != nil {
		t.Fatal(err)
	}
	return m, f
}

// The deterministic gate. The path is flipped after every check and before the
// box is created: docker mounts the folder the link now leads to, and the box
// that runs shows it. The spawn is refused and the box removed before any agent
// starts in it.
func TestWrapRefusesAWorkspaceFlippedBeforeDockerRun(t *testing.T) {
	root, ws, state := flipTree(t)
	m, f := flipManager(t, state)
	name := mustBoxName(t, m, ws)
	onFirstRun(f, func() { flipTeam(t, root) })

	argv, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
	}
	// The box was created by this spawn, so the text says it changed while the
	// box started, and points to the requirement that docker must see host files
	// as they are: a repeat for every folder means it does not.
	want := "workspace " + ws + ": the mounted folder is not the one that was checked (it changed while the box started); refusing. " +
		"If this repeats for every folder, CuliBox's docker does not see host files as they are " +
		"(for example Docker Desktop for Linux or a remote docker host); see README, Requirements."
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the created box removed", f.runs(), f.stops())
	}
	if argv != nil {
		t.Fatalf("argv = %v, want none for a refused spawn", argv)
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live != 0 {
		t.Fatalf("active[%s] = %d after a refused spawn", name, live)
	}
}

// The issue's race: the workspace's parent exchanged with a link, over and over,
// while boxes are started. Docker resolves the link when it runs, so a box may
// come up on the wrong folder; none may be allowed to start.
func TestWrapNeverStartsABoxOnAWorkspaceExchangedWithALink(t *testing.T) {
	root, ws, state := flipTree(t)
	team, swap := filepath.Join(root, "shared", "team"), filepath.Join(root, "shared", "swap")
	realID, stateID := lstatID(t, ws), lstatID(t, state)
	if realID == stateID {
		t.Fatal("the two folders share an identity; the test cannot tell them apart")
	}
	flip := func() error {
		return unix.Renameat2(unix.AT_FDCWD, team, unix.AT_FDCWD, swap, unix.RENAME_EXCHANGE)
	}
	if err := flip(); err != nil { // also proves the filesystem can exchange a directory and a link
		t.Skipf("RENAME_EXCHANGE unavailable here: %v", err)
	}
	if err := flip(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// team must be the directory again for the temp dir's removal.
		if fi, err := os.Lstat(team); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			_ = flip()
		}
	})
	stop, flips := make(chan struct{}), make(chan int)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		n := 0
		for {
			select {
			case <-stop:
				flips <- n
				return
			default:
			}
			if flip() != nil {
				flips <- n
				return
			}
			n++
		}
	}()

	const runs = 200
	var accepted, refused, leaked int
	for i := 0; i < runs; i++ {
		m, f := flipManager(t, state)
		_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
		if err != nil {
			refused++
			continue
		}
		f.mu.Lock()
		got, ok := f.mountIDs[name][ws]
		f.mu.Unlock()
		switch {
		case !ok:
			t.Errorf("run %d: the box %s has no mount of %s on record", i, name, ws)
		case got == stateID:
			leaked++
		case got == realID:
			accepted++
		default:
			t.Errorf("run %d: the box shows %+v, which is neither folder", i, got)
		}
	}
	close(stop)
	n := <-flips
	wg.Wait()
	t.Logf("%d runs while %d flips: %d started on the real folder, %d refused, %d started on the folder the link leads to", runs, n, accepted, refused, leaked)
	if n == 0 {
		t.Fatal("nothing was flipped; the test proved nothing")
	}
	if leaked != 0 {
		t.Fatalf("%d of %d boxes started on the folder the flip swapped in", leaked, runs)
	}
}

// A box left idle whose workspace folder was deleted and made again shows the
// old, unlinked folder. It is removed, and the next spawn gets a good one.
func TestWrapRefusesAnIdleBoxWhoseWorkspaceWasReplaced(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := filepath.Join(ctxTmp(t), "ws")
	mkdirs(t, ws)
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	replaceDir(t, ws)

	_, _, _, err = m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !strings.Contains(err.Error(), "the box was removed, spawn again") {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and the idle box removed", err)
	}
	// A reused box was not created by this spawn: it was replaced since, and the
	// folder is not the one at fault for every folder, so no requirement is cited.
	if want := "workspace " + ws + ": the mounted folder is not the one that was checked (it was replaced since the box started); refusing"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), "README") {
		t.Fatalf("err = %v, a reused box's refusal must not cite the docker requirement", err)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the idle box removed, not recreated", f.runs(), f.stops())
	}
	if _, _, _, err = m.Wrap(context.Background(), ws, "s3", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("the spawn after the removal: %v", err)
	}
	if f.runs() != 2 {
		t.Fatalf("runs = %d, want a fresh box", f.runs())
	}
}

// The same with a session still running in the box: it is kept, and the refusal
// says how to get a good box.
func TestWrapKeepsALiveBoxWhoseWorkspaceWasReplaced(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := filepath.Join(ctxTmp(t), "ws")
	mkdirs(t, ws)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	replaceDir(t, ws)

	_, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !strings.Contains(err.Error(), "still runs 1 session(s) and is kept") {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and the live box kept", err)
	}
	if !strings.Contains(err.Error(), "(it was replaced since the box started)") {
		t.Fatalf("err = %v, want the reused-box text", err)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("runs=%d stops=%d, want the live box kept", f.runs(), f.stops())
	}
}

// replaceDir moves dir aside and makes a new one at its path.
func replaceDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Rename(dir, dir+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
