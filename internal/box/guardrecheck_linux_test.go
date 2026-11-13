//go:build linux

package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flipGuardOnFirstRun makes the guard .claude of ws a link to the state root
// between the guards being derived and docker mounting them, as the flip tests
// do, and returns a func that puts the real folder back.
func flipGuardOnFirstRun(t *testing.T, m *Manager, f *fakeClient, ws string) (restore func()) {
	t.Helper()
	claude := filepath.Join(ws, ".claude")
	onFirstRun(f, func() {
		if err := os.Rename(claude, claude+".real"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(m.opts.StateRoot, claude); err != nil {
			t.Error(err)
		}
	})
	return func() {
		_ = os.Remove(claude)
		if err := os.Rename(claude+".real", claude); err != nil {
			t.Error(err)
		}
	}
}

// A box this spawn created that fails the guard check, and could not be removed
// (docker rm failed), is still running. The next spawn must not take it for a
// reused box, whose guards are not compared: it is removed first, and the spawn
// gets a box whose guards were checked.
func TestAGuardRefusedBoxThatCouldNotBeRemovedIsNotReusedUnchecked(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	restore := flipGuardOnFirstRun(t, m, f, ws)
	f.stopFails = 1
	name := mustBoxName(t, m, ws)
	_, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("err = %v, want the guard refusal with the failed removal named", err)
	}
	if st, _ := f.Inspect(context.Background(), name); st != StateRunning {
		t.Fatalf("test setup: the box is %v, want it still running after the failed removal", st)
	}
	restore()
	_, _, _, err = m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatalf("the next spawn, once the box can be removed: %v", err)
	}
	if f.runs() != 2 || f.stops() != 2 {
		t.Fatalf("runs=%d stops=%d, want the refused box removed again and a new one created, not reused", f.runs(), f.stops())
	}
}

// While the box still cannot be removed, the spawn is refused: it never runs in
// a box whose guard did not match.
func TestAGuardRefusedBoxThatStillCannotBeRemovedRefusesTheSpawn(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	restore := flipGuardOnFirstRun(t, m, f, ws)
	f.stopFails = 2
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err == nil {
		t.Fatal("the flipped guard was accepted")
	}
	restore()
	_, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("err = %v, want the spawn refused because the box could not be removed", err)
	}
	if f.runs() != 1 {
		t.Fatalf("runs = %d, want the refused box not reused and no new one created over it", f.runs())
	}
}

// A box created before the workspace identity check existed (no LabelIdentity)
// was never checked, so its guards are compared once: one whose guard changed
// since is removed when idle, like any reused box that no longer matches.
func TestAnUncheckedOldBoxHasItsGuardsComparedOnce(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "# rules\n")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	delete(f.labels[name], LabelIdentity) // what a box of an older build looks like
	tmp := filepath.Join(ws, "CLAUDE.md.tmp")
	writeFile(t, tmp, "# other\n")
	if err := os.Rename(tmp, filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !strings.Contains(err.Error(), "replaced since the box started") {
		t.Fatalf("err = %v, want the guard refused as replaced since the box started", err)
	}
	if f.stops() != 1 {
		t.Fatalf("stops = %d, want the idle old box removed", f.stops())
	}
}

// An old box whose guards match is reused, compared once: a later save by rename
// does not refuse the next session (the same tolerance a new box has).
func TestAnUncheckedOldBoxIsNotRecheckedAfterItsGuardsMatched(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "# rules\n")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	delete(f.labels[name], LabelIdentity)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("an old box whose guards match: %v", err)
	}
	tmp := filepath.Join(ws, "CLAUDE.md.tmp")
	writeFile(t, tmp, "# edited\n")
	if err := os.Rename(tmp, filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s3", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("the third session after a save by rename: %v", err)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("runs=%d stops=%d, want the one box reused", f.runs(), f.stops())
	}
}

// A box this build created carries the label, so its guards are never compared
// on reuse (see TestGuardsAreNotRecheckedOnReuse).
func TestANewBoxCarriesTheIdentityLabel(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.labels[name][LabelIdentity] != "1" {
		t.Fatalf("labels = %v, want %s=1", f.labels[name], LabelIdentity)
	}
}

// The identity label says a box was made by a checking build, not that it passed.
// A box left running by a failed removal at boot (Reconcile), whose guard check
// may never have finished (a crash between `docker run` and the check), is not
// reused as it is: the next spawn removes it first and creates one that is checked.
func TestAnOrphanReconcileCouldNotRemoveIsNotReusedUnchecked(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "# rules\n")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name) // idle, as after a daemon restart
	f.existing = append(f.existing, name)
	f.stopFails = 1
	if err := m.Reconcile(context.Background()); err == nil {
		t.Fatal("test setup: the removal was meant to fail")
	}
	if f.labels[name][LabelIdentity] != "1" {
		t.Fatal("test setup: the orphan must carry the identity label")
	}
	// The guard file was replaced meanwhile: a reused box would still show the old one.
	tmp := filepath.Join(ws, "CLAUDE.md.tmp")
	writeFile(t, tmp, "# other\n")
	if err := os.Rename(tmp, filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 2 || f.stops() != 2 {
		t.Fatalf("runs=%d stops=%d, want the orphan removed again and a checked box created, not reused", f.runs(), f.stops())
	}
}
