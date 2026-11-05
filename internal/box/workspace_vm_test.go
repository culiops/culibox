package box

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// flipTree is the tree the flip tests race on:
//
//	root/shared/team/proj   the workspace (ws)
//	root/st/proj            what the workspace path leads to once team is a link (state)
//	root/shared/swap        a link to root/st (the issue's exchange partner)
//
// A manager whose StateRoot is state sees ws become its own data directory
// after the flip, which is the folder a workspace must never be.
func flipTree(t *testing.T) (root, ws, state string) {
	t.Helper()
	root = ctxTmp(t)
	ws, state = filepath.Join(root, "shared", "team", "proj"), filepath.Join(root, "st", "proj")
	mkdirs(t, ws, state)
	if err := os.Symlink(filepath.Join(root, "st"), filepath.Join(root, "shared", "swap")); err != nil {
		t.Fatal(err)
	}
	return root, ws, state
}

// flipTeam makes the workspace path of flipTree lead to the other tree: team is
// moved aside and a link to root/st takes its name. It is what a path component
// swapped between the checks and `docker run` looks like.
func flipTeam(t *testing.T, root string) {
	t.Helper()
	team := filepath.Join(root, "shared", "team")
	if err := os.Rename(team, team+".real"); err != nil {
		t.Error(err)
		return
	}
	if err := os.Symlink(filepath.Join(root, "st"), team); err != nil {
		t.Error(err)
	}
}

// onFirstRun runs fn on the first `docker run` the fake sees, before it records
// the mounts as docker would resolve them, and leaves the run itself to the
// fake. It chains to a runHook already installed.
func onFirstRun(f *fakeClient, fn func()) {
	inner := f.runHook
	done := false
	f.runHook = func(args []string) (string, bool, error) {
		if len(args) > 0 && args[0] == "run" && !done {
			done = true
			fn()
		}
		if inner != nil {
			return inner(args)
		}
		return "", false, nil
	}
}

func inBoxStatArgv(name string, paths ...string) []string {
	return append([]string{"exec", name, "/usr/bin/stat", "-c", "%d:%i", "--"}, paths...)
}

// The workspace is read in the VM in the one batch, before the box exists, and
// then compared in the box by the existing in-box stat.
func TestWorkspaceIdentityIsReadInTheVMBeforeTheBoxIsCreated(t *testing.T) {
	v := &vmFake{}
	sf := &statFake{}
	m, _ := vmManager(t, v, sf)
	ws := ctxTmp(t)
	name, err := wrapWith(m, ws, "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.n() != 1 || !slices.Equal(v.calls[0], []string{ws}) {
		t.Fatalf("VM identity calls = %v, want one for the workspace %s", v.calls, ws)
	}
	if got := v.order(); got != "vmids,run,stat" {
		t.Fatalf("order = %q, want vmids,run,stat", got)
	}
	if want := inBoxStatArgv(name, ws); sf.n() != 1 || !slices.Equal(sf.calls[0], want) {
		t.Fatalf("in-box stat calls = %v, want one: %v", sf.calls, want)
	}
}

// An unreadable VM identity refuses the spawn before any box is made, and says
// it was the workspace, not a context path.
func TestWorkspaceVMReadErrorRefusesBeforeAnyBoxIsCreated(t *testing.T) {
	v := &vmFake{err: errors.New("path 1: a parent directory is a symlink, or the path is not clean")}
	m, f := vmManager(t, v, &statFake{})
	_, err := wrapWith(m, ctxTmp(t), "s1", nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
	}
	if errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, no context path is in this batch", err)
	}
	for _, want := range []string{"could not confirm workspace", "path 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
	if f.runs() != 0 {
		t.Fatalf("docker run called %d times", f.runs())
	}
}

// A path component swapped after the VM read the identity and before docker
// mounted the folder: the box shows another folder, so it is removed.
func TestWorkspaceFlippedAfterTheVMReadIsRefused(t *testing.T) {
	root, ws, _ := flipTree(t)
	v := &vmFake{}
	sf := &statFake{}
	m, f := vmManager(t, v, sf)
	onFirstRun(f, func() { flipTeam(t, root) })
	_, err := wrapWith(m, ws, "s1", nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
	}
	if !strings.Contains(err.Error(), "is not the one that was checked") {
		t.Fatalf("err = %v", err)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the created box removed", f.runs(), f.stops())
	}
	for n, live := range m.active {
		if live != 0 {
			t.Fatalf("active[%s] = %d after a refused spawn", n, live)
		}
	}
}

// The workspace goes first, ahead of every context path, in the one VM read.
func TestVMBatchPutsTheWorkspaceFirst(t *testing.T) {
	v := &vmFake{ids: []FileID{{44, 393}, {44, 394}}}
	sf := &statFake{out: "44:393\n44:394"}
	m, f := vmManager(t, v, sf)
	ws := ctxTmp(t)
	name, err := wrapWith(m, ws, "s1", checkMounts(2))
	if err != nil {
		t.Fatal(err)
	}
	// The checked workspace is not one of the read-only extras: docker is still
	// given it once, writable, as before.
	var wsMounts []string
	for i, a := range f.runArgs[0] {
		if a == "--mount" && strings.Contains(f.runArgs[0][i+1], ",dst="+ws) {
			wsMounts = append(wsMounts, f.runArgs[0][i+1])
		}
	}
	if want := []string{bindMount(ws, ws, false)}; !slices.Equal(wsMounts, want) {
		t.Fatalf("workspace mounts in docker run = %v, want exactly %v", wsMounts, want)
	}
	if want := []string{ws, "/srv/ctx0", "/srv/ctx1"}; v.n() != 1 || !slices.Equal(v.calls[0], want) {
		t.Fatalf("VM identity calls = %v, want one: %v", v.calls, want)
	}
	if want := inBoxStatArgv(name, ws, "/srv/ctx0", "/srv/ctx1"); sf.n() != 1 || !slices.Equal(sf.calls[0], want) {
		t.Fatalf("in-box stat calls = %v, want one: %v", sf.calls, want)
	}
}

// A VM identity batch that fails with a context path in it is still the context
// refusal too, so a caller that tells those apart keeps doing so.
func TestVMReadErrorNamesTheWorkspaceAndTheContextPaths(t *testing.T) {
	v := &vmFake{err: errors.New("the VM is stopped")}
	m, _ := vmManager(t, v, &statFake{})
	ws := ctxTmp(t)
	_, err := wrapWith(m, ws, "s1", checkMounts(2))
	for _, target := range []error{ErrWorkspaceMountChanged, ErrBoxUnavailable, ErrContextMountChanged, ErrUnboxableWorkspace} {
		if !errors.Is(err, target) {
			t.Fatalf("err = %v, want it to be %v", err, target)
		}
	}
	want := "the box VM could not confirm workspace " + ws + " and context_paths /srv/ctx0, /srv/ctx1 (the VM is stopped); refusing"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to contain %q", err, want)
	}
}
