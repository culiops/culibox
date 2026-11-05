//go:build linux

package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every guard carries the identity of the file or directory it was derived from,
// and is marked for the in-box check: the box that docker creates must show these
// very files at these paths.
func TestInPlaceMountsAtRecordsEachGuardsIdentity(t *testing.T) {
	ws := realTemp(t)
	gitRepo(t, ws, "[core]\n\thooksPath = .husky/_\n")
	mkdirs(t, filepath.Join(ws, ".husky", "_"))
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "# rules\n")
	writeFile(t, filepath.Join(ws, ".mcp.json"), "{}\n")
	got, err := inPlaceMountsAt(workspace{Given: ws, Path: ws})
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", "CLAUDE.md", ".mcp.json", ".husky")
	for _, mt := range got {
		if !mt.CheckID || mt.kind != mountGuard {
			t.Errorf("guard %s: CheckID=%v kind=%v, want it checked as a guard", mt.Host, mt.CheckID, mt.kind)
		}
		if mt.ID == nil || *mt.ID != lstatID(t, mt.Host) {
			t.Errorf("guard %s: ID = %+v, want the %+v the host has for it", mt.Host, mt.ID, lstatID(t, mt.Host))
		}
	}
}

// guardFlipWorkspace is a repository workspace with a manager whose state root
// is the folder a flipped guard will lead to.
func guardFlipWorkspace(t *testing.T) (m *Manager, f *fakeClient, ws string) {
	t.Helper()
	m, f = guardMgr(t, true)
	ws = realTemp(t)
	gitRepo(t, ws, "")
	mkdirs(t, filepath.Join(ws, ".claude"))
	return m, f, ws
}

// A guard path flipped to a link after the guards were derived and before docker
// mounts it: docker follows the link and the read-only guard would show the
// state root. The box that comes up is refused and removed.
func TestWrapRefusesAGuardFlippedBeforeDockerRun(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	claude := filepath.Join(ws, ".claude")
	onFirstRun(f, func() {
		if err := os.Rename(claude, claude+".real"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(m.opts.StateRoot, claude); err != nil {
			t.Error(err)
		}
	})
	_, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
	}
	if !strings.Contains(err.Error(), "workspace "+claude+": ") {
		t.Fatalf("err = %v, want it to name %s", err, claude)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the created box removed", f.runs(), f.stops())
	}
}

// Guards are compared when a box is created, not when it is reused: a file bind
// mount pins its inode, and an editor that saves CLAUDE.md by rename leaves the
// live box with the old one. Refusing the next session for that would refuse
// every session after a save.
func TestGuardsAreNotRecheckedOnReuse(t *testing.T) {
	m, f, ws := guardFlipWorkspace(t)
	sf := &statFake{}
	sf.install(f)
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "# rules\n")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first := sf.calls[0]; len(first) < 8 || first[6] != ws || !slices.Contains(first, filepath.Join(ws, "CLAUDE.md")) {
		t.Fatalf("the creating spawn's stat = %v, want the workspace first and the guards after it", first)
	}
	// Saved by rename, as an editor does: the live box still shows the old inode.
	tmp := filepath.Join(ws, "CLAUDE.md.tmp")
	writeFile(t, tmp, "# rules, edited\n")
	if err := os.Rename(tmp, filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("the second session in the live box: %v", err)
	}
	if want := inBoxStatArgv(name, ws); sf.n() != 2 || !slices.Equal(sf.calls[1], want) {
		t.Fatalf("the reused spawn's stat = %v, want only the workspace: %v", sf.calls, want)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("runs=%d stops=%d, want the live box reused", f.runs(), f.stops())
	}
}
