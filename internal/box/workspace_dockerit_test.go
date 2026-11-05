//go:build dockerit

package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This file runs the workspace identity check against a real docker daemon (the
// host's H0 row; the box can only compile it). The fake used by the unit tests
// resolves the mount source itself, so only a real `docker run` shows that the
// folder the daemon mounted, and the in-box stat that reads it, agree with what
// the check does.

// flipClient is a Client that calls before on a `docker run`, then runs it. A
// test uses it to change the filesystem in the window between Wrap's checks and
// the daemon resolving the mount's source.
type flipClient struct {
	Client
	before func(args []string)
}

func (c flipClient) Run(ctx context.Context, args []string) (string, error) {
	if len(args) > 0 && args[0] == "run" && c.before != nil {
		c.before(args)
	}
	return c.Client.Run(ctx, args)
}

// itFlipManager is itManager over a flipClient, with the state root the
// workspace path leads to after the flip (see flipTree) and the in-place guards
// as asked.
func itFlipManager(t *testing.T, bin, image, state string, guard bool, before func(args []string)) *Manager {
	t.Helper()
	m, err := New(Options{
		DockerBin:    bin,
		Image:        image,
		Instance:     itInstance,
		StateRoot:    state,
		Client:       flipClient{Client: CLIClient{Bin: bin}, before: before},
		GuardInPlace: guard,
		EgressAll:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A path component swapped for a link after Wrap's checks and before the daemon
// mounts the folder: the daemon follows the link, the box shows another folder,
// the in-box compare notices, and no container is left.
func TestDockerItWorkspaceFlippedBeforeRunIsRefused(t *testing.T) {
	bin, image := itDocker(t)
	t.Cleanup(func() { itRemoveBoxes(bin) })
	root, ws, state := flipTree(t)
	flipped := false
	m := itFlipManager(t, bin, image, state, false, func([]string) {
		if !flipped {
			flipped = true
			flipTeam(t, root)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _, _, err := m.Wrap(ctx, ws, "it-flip", BoxKey{}, []string{"sleep", "1"}, nil, nil)
	if !flipped {
		t.Fatal("the flip never ran: docker run was not reached")
	}
	if !errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged", err)
	}
	if left := itBoxCount(t, bin); len(left) != 0 {
		t.Fatalf("containers left after the refused spawn: %v", left)
	}
}

// The same for a guard: with the in-place guards on, a guard path swapped for a
// link before the daemon mounts it would show the folder the link leads to,
// read-only, at the guard's path. The in-box compare of the guard refuses it and
// removes the box.
func TestDockerItGuardFlippedBeforeRunIsRefused(t *testing.T) {
	bin, image := itDocker(t)
	t.Cleanup(func() { itRemoveBoxes(bin) })
	ws := itRepo(t, ".githooks")
	state := t.TempDir()
	claude := filepath.Join(ws, ".claude")
	flipped := false
	m := itFlipManager(t, bin, image, state, true, func([]string) {
		if flipped {
			return
		}
		flipped = true
		if err := os.Rename(claude, claude+".real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(state, claude); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _, _, err := m.Wrap(ctx, ws, "it-guard-flip", BoxKey{}, []string{"sleep", "1"}, nil, nil)
	if !flipped {
		t.Fatal("the flip never ran: docker run was not reached")
	}
	if !errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged", err)
	}
	if left := itBoxCount(t, bin); len(left) != 0 {
		t.Fatalf("containers left after the refused spawn: %v", left)
	}
}
