package box

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// swapForFIFOAt makes openDirHook replace dir (a host path) with a FIFO the
// first time the open of rel (relative to its root) is about to happen: the race a
// box of the profile can win between a listing or an Lstat and the open. What
// was at dir is kept beside it.
func swapForFIFOAt(t *testing.T, rel, dir string) *bool {
	t.Helper()
	swapped := new(bool)
	old := openDirHook
	t.Cleanup(func() { openDirHook = old })
	openDirHook = func(name string) {
		if *swapped || name != rel {
			return
		}
		*swapped = true
		if err := os.Rename(dir, dir+".moved"); err != nil {
			t.Errorf("moving %s away: %v", dir, err)
			return
		}
		if err := syscall.Mkfifo(dir, 0o600); err != nil {
			t.Errorf("mkfifo: %v", err)
		}
	}
	return swapped
}

// returnsPromptly runs f and fails the test if it does not return within a few
// seconds: an open of a FIFO with no writer never returns.
func returnsPromptly(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked on a FIFO", what)
	}
}

// A FIFO where a directory is opened fails at once, with an error.
func TestOpenDirInRefusesAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "p"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	returnsPromptly(t, "eachEntry", func() {
		if err := eachEntry(context.Background(), root, "p", func(fs.DirEntry) bool { return true }); err == nil {
			t.Error("eachEntry on a FIFO: no error")
		}
	})
	returnsPromptly(t, "openDirIn", func() {
		if f, err := openDirIn(root, "p"); err == nil {
			f.Close()
			t.Error("openDirIn on a FIFO: no error")
		}
	})
}

// Adoption runs under opMu: a home's projects dir, one of its workspace
// folders, or the folder's workspace folder (written by live boxes of the
// profile) swapped for a FIFO right before it is opened leaves the entries in
// the home and returns, never blocks.
func TestAdoptionDoesNotBlockOnAFIFO(t *testing.T) {
	for _, tc := range []struct{ name, rel, side string }{
		{"home projects dir", ".claude/projects", "home"},
		{"home workspace folder", ".claude/projects/-w", "home"},
		{"folder's workspace folder", "-w", "conv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, calls := adoptManager(t)
			ws := realTemp(t)
			name, _ := m.BoxName(ws, convKey("p"))
			home := m.stateDir(name)
			seedProjects(t, home, map[string]string{"-w/a.jsonl": "A"})
			conv, err := ensureConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"))
			if err != nil {
				t.Fatal(err)
			}
			at := filepath.Join(home, filepath.FromSlash(tc.rel))
			if tc.side == "conv" {
				if err := os.Mkdir(filepath.Join(conv, "-w"), 0o700); err != nil {
					t.Fatal(err)
				}
				at = filepath.Join(conv, "-w")
			}
			swapped := swapForFIFOAt(t, tc.rel, at)
			returnsPromptly(t, "adoptConversations", func() { m.adoptConversations(context.Background(), name, conv) })
			if !*swapped {
				t.Fatalf("%s was never opened", tc.rel)
			}
			if len(*calls) != 0 {
				t.Fatalf("moved through a swapped folder: %q", *calls)
			}
		})
	}
}

// A directory of a cache swapped for a FIFO while reclaim walks it (under opMu)
// is skipped, never waited on.
func TestCacheWalkDoesNotBlockOnAFIFO(t *testing.T) {
	home := t.TempDir()
	sub := filepath.Join(home, ".npm", "sub")
	if err := os.MkdirAll(filepath.Join(sub, "deeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	swapped := swapForFIFOAt(t, ".npm/sub", sub)
	returnsPromptly(t, "makeRemovable", func() { makeRemovable(context.Background(), root, ".npm") })
	if !*swapped {
		t.Fatal(".npm/sub was never opened")
	}
}

// The folder's workspace folder swapped for a link to another folder in it
// after the Lstat: os.Root would follow the link, the identity check refuses it.
func TestAdoptionRefusesALinkSwappedInAfterTheCheck(t *testing.T) {
	m, _, calls := adoptManager(t)
	ws := realTemp(t)
	name, _ := m.BoxName(ws, convKey("p"))
	home := m.stateDir(name)
	seedProjects(t, home, map[string]string{"-w/a.jsonl": "A"})
	conv, err := ensureConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(conv, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := openDirHook
	t.Cleanup(func() { openDirHook = old })
	openDirHook = func(rel string) {
		if rel != "-w" {
			return
		}
		if err := os.Remove(filepath.Join(conv, "-w")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink("other", filepath.Join(conv, "-w")); err != nil {
			t.Error(err)
		}
	}
	m.adoptConversations(context.Background(), name, conv)
	if len(*calls) != 0 || readOr(filepath.Join(home, ".claude", "projects", "-w", "a.jsonl")) != "A" {
		t.Fatalf("moved through a link swapped in: %q", *calls)
	}
	if _, err := os.Lstat(filepath.Join(conv, "other", "a.jsonl")); err == nil {
		t.Fatal("the conversation landed in the link's target")
	}
}

// Doctor's size walk reads folders live boxes write: a directory swapped for a
// FIFO while it is walked is not counted (Sized false), never waited on past
// doctor's timeout.
func TestConversationsUsageDoesNotBlockOnAFIFO(t *testing.T) {
	root := t.TempDir()
	d, err := ensureConversationsDir(root, "aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(d, "-w")
	if err := os.MkdirAll(filepath.Join(sub, "deeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	swapped := swapForFIFOAt(t, "-w", sub)
	var u ConversationsStats
	returnsPromptly(t, "ConversationsUsage", func() { u, err = ConversationsUsage(context.Background(), root) })
	if !*swapped {
		t.Fatal("-w was not opened through openDirIn")
	}
	if err != nil || u.Folders != 1 || u.Sized {
		t.Fatalf("usage = %+v %v, want 1 folder, not Sized", u, err)
	}
}
