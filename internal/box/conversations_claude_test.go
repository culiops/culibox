package box

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// The key is per (instance, workspace, account, profile): what decides it is
// pinned, and nothing else (a scope change) can enter it, since it takes no
// other input.
func TestConversationsKeyInputs(t *testing.T) {
	base := ConversationsKey("PROD", "/w", "", "p")
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(base) {
		t.Fatalf("key %q is not 12 lowercase hex", base)
	}
	if got := ConversationsKey("PROD", "/w", "default", "p"); got != base {
		t.Errorf(`account "default" = %q, want the same key as "" (%q)`, got, base)
	}
	for name, k := range map[string]string{
		"instance":  ConversationsKey("DEV", "/w", "", "p"),
		"workspace": ConversationsKey("PROD", "/w2", "", "p"),
		"account":   ConversationsKey("PROD", "/w", "work", "p"),
		"profile":   ConversationsKey("PROD", "/w", "", "q"),
	} {
		if k == base {
			t.Errorf("a different %s gives the same key", name)
		}
	}
	// The separator keeps the fields apart: moving a byte across one changes the key.
	if ConversationsKey("PROD", "/w", "a", "bc") == ConversationsKey("PROD", "/w", "ab", "c") {
		t.Error("fields run together")
	}
	// Pinned: a change to the derivation would hide every existing folder.
	if got, want := ConversationsKey("PROD", "/home/u/repo", "", "claude"), "132b31134ce4"; got != want {
		t.Errorf("pinned key = %q, want %q", got, want)
	}
}

func TestEnsureConversationsDirCreatesPrivateFolder(t *testing.T) {
	root := t.TempDir()
	key := ConversationsKey("TEST", "/w", "", "p")
	dir, err := ensureConversationsDir(root, key)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "boxstate", ".conversations", key); dir != want || ConversationsDir(root, key) != want {
		t.Fatalf("dir = %q (ConversationsDir %q), want %q", dir, ConversationsDir(root, key), want)
	}
	for _, p := range []string{filepath.Dir(dir), dir} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want a 0700 directory", p, fi, err)
		}
	}
	// Idempotent, and what a session wrote stays.
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := ensureConversationsDir(root, key); err != nil || again != dir {
		t.Fatalf("second call: %q %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err != nil {
		t.Fatalf("content lost: %v", err)
	}
	// Not a box name: reclaim's listing never takes it for a home.
	if isBoxName(conversationsDirName) {
		t.Fatal(".conversations reads as a box name")
	}
}

func TestEnsureConversationsDirRefusesBadKeysAndLinks(t *testing.T) {
	for _, k := range []string{"", "..", "../../etc", "0bbd0df0bc8", "0BBD0DF0BC8A", "0bbd0df0bc8a/x"} {
		if _, err := ensureConversationsDir(t.TempDir(), k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	key := ConversationsKey("TEST", "/w", "", "p")

	// A symlinked .conversations: refused, and nothing is made where it points.
	root := t.TempDir()
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "boxstate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "boxstate", ".conversations")); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConversationsDir(root, key); err == nil {
		t.Fatal("symlinked .conversations accepted")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("created through the link: %v", ents)
	}

	// A symlinked key folder, even one pointing inside boxstate: refused.
	root = t.TempDir()
	conv := filepath.Join(root, "boxstate", ".conversations")
	if err := os.MkdirAll(conv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../ab-box-000000000000", filepath.Join(conv, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConversationsDir(root, key); err == nil {
		t.Fatal("symlinked key folder accepted")
	}

	// A file where the folder belongs: refused.
	root = t.TempDir()
	conv = filepath.Join(root, "boxstate", ".conversations")
	if err := os.MkdirAll(conv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conv, key), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConversationsDir(root, key); err == nil {
		t.Fatal("a file accepted as the folder")
	}
}

// convKey is the BoxKey of a profile's spawn: Profile alone turns the
// conversations folder on.
func convKey(profile string) BoxKey { return BoxKey{Profile: profile} }

// The folder is the one writable mount besides the workspace, the home and the
// out dir, and it lands on the projects dir of the home.
func TestRunArgsMountsTheConversationsFolderReadWrite(t *testing.T) {
	m := newTestManager(t)
	conv := ConversationsDir(m.opts.StateRoot, "0123456789ab")
	got := m.runArgs("ab-box-abc", "/w", nil, false, false, "", DefaultMemory, conv)
	home := slices.Index(got, "type=bind,src="+m.stateDir("ab-box-abc")+",dst="+boxHome)
	at := slices.Index(got, "type=bind,src="+conv+",dst=/home/agent/.claude/projects")
	if at < 0 || home < 0 || at < home {
		t.Fatalf("want the folder mounted read-write after the home: %q", got)
	}
	if n := countFlag(got, "--mount"); n != 4 {
		t.Fatalf("want 4 mounts, got %d: %q", n, got)
	}
	// "" leaves the argv exactly as before.
	if plain := m.runArgs("ab-box-abc", "/w", nil, false, false, "", DefaultMemory, ""); hasDst(plain, ConversationsBox) {
		t.Fatalf("no folder, no mount: %q", plain)
	}
}

func TestWrapMountsTheProfilesConversationsFolder(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := ConversationsKey("TEST", ws, "", "p")
	conv := ConversationsDir(m.opts.StateRoot, key)
	if src := f.sources[name][ConversationsBox]; src != conv {
		t.Fatalf("mounted %q at %s, want %q", src, ConversationsBox, conv)
	}
	if fi, err := os.Lstat(conv); err != nil || !fi.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	// The mountpoint exists in the home, made by us, so docker makes nothing as root.
	if fi, err := os.Lstat(filepath.Join(m.stateDir(name), ".claude", "projects")); err != nil || !fi.IsDir() {
		t.Fatalf("mountpoint not created: %v", err)
	}
	if got := m.ConversationsKeyOf(name); got != key {
		t.Fatalf("ConversationsKeyOf = %q, want %q", got, key)
	}

	// Without a profile: no folder, as before.
	m2, f2 := newTestManagerWithFake(t)
	_, _, name2, err := m2.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasDst(f2.runArgs[0], ConversationsBox) || m2.ConversationsKeyOf(name2) != "" {
		t.Fatalf("a spawn with no profile got a folder: %q", f2.runArgs[0])
	}
}

// The box name does not change: the folder is not identity.
func TestConversationsProfileIsNotIdentity(t *testing.T) {
	m := newTestManager(t)
	ws := realTemp(t)
	a, _ := m.BoxName(ws, BoxKey{})
	b, _ := m.BoxName(ws, convKey("p"))
	if a != b {
		t.Fatalf("profile renamed the box: %s vs %s", a, b)
	}
}

// Clone mode mounts the memory dir inside the projects dir: its stub must now be
// in the conversations folder, or docker creates it there as root.
func TestNestedMountStubLandsInTheConversationsFolder(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	ws := realTemp(t)
	mem := t.TempDir()
	box := ConversationsBox + "/-w/memory"
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, []Mount{{Host: mem, Box: box}})
	if err != nil {
		t.Fatal(err)
	}
	conv := ConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"))
	if fi, err := os.Lstat(filepath.Join(conv, "-w", "memory")); err != nil || !fi.IsDir() {
		t.Fatalf("stub not in the conversations folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(m.stateDir(name), ".claude", "projects", "-w")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stub made in the home, under the mount: %v", err)
	}
}

// A home whose projects dir the box replaced with a link or a file is refused:
// docker would resolve the mount target through it.
func TestHomeProjectsDirMustBeAPlainDirectory(t *testing.T) {
	for _, plant := range []string{"symlink", "file"} {
		t.Run(plant, func(t *testing.T) {
			m, f := newTestManagerWithFake(t)
			ws := realTemp(t)
			name, _ := m.BoxName(ws, convKey("p"))
			claude := filepath.Join(m.stateDir(name), ".claude")
			if err := os.MkdirAll(claude, 0o700); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(claude, "projects")
			var err error
			if plant == "symlink" {
				err = os.Symlink(t.TempDir(), p)
			} else {
				err = os.WriteFile(p, nil, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err == nil {
				t.Fatal("rigged projects dir accepted")
			}
			if f.runs() != 0 {
				t.Fatal("a box was started")
			}
		})
	}
}

// reuseMounts: the folder is judged like the out dir, never as an extra mount.
func TestReuseWithConversationsFolder(t *testing.T) {
	ctx := context.Background()
	start := func(t *testing.T, key BoxKey) (*Manager, *fakeClient, string, string) {
		m, f := newTestManagerWithFake(t)
		ws := realTemp(t)
		_, _, name, err := m.Wrap(ctx, ws, "s1", key, []string{"bash"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m, f, ws, name
	}
	dropConv := func(f *fakeClient, name string) { // a box from before the folder
		f.mounts[name] = slices.DeleteFunc(f.mounts[name], func(d string) bool { return d == ConversationsBox })
		delete(f.sources[name], ConversationsBox)
	}

	t.Run("same folder reused", func(t *testing.T) {
		m, f, ws, name := start(t, convKey("p"))
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("p"), []string{"bash"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if f.runs() != 1 || f.stops() != 0 {
			t.Fatalf("runs=%d stops=%d", f.runs(), f.stops())
		}
		if m.ConversationsKeyOf(name) != ConversationsKey("TEST", ws, "", "p") {
			t.Fatal("key lost")
		}
	})
	t.Run("live box from before the folder is reused with the home key", func(t *testing.T) {
		m, f, ws, name := start(t, convKey("p"))
		dropConv(f, name)
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("p"), []string{"bash"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if f.runs() != 1 || f.stops() != 0 {
			t.Fatalf("a live box must be reused: runs=%d stops=%d", f.runs(), f.stops())
		}
		if got := m.ConversationsKeyOf(name); got != "" {
			t.Fatalf("the box writes in its home, key = %q", got)
		}
	})
	t.Run("idle box from before the folder is recreated with it", func(t *testing.T) {
		m, f, ws, name := start(t, convKey("p"))
		dropConv(f, name)
		m.ReleaseBox(name)
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("p"), []string{"bash"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if f.runs() != 2 || f.stops() != 1 || !hasDst(f.runArgs[1], ConversationsBox) {
			t.Fatalf("runs=%d stops=%d", f.runs(), f.stops())
		}
	})
	t.Run("box with a folder reused by a spawn without a profile", func(t *testing.T) {
		m, f, ws, name := start(t, convKey("p"))
		m.ReleaseBox(name)
		if _, _, _, err := m.Wrap(ctx, ws, "s2", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if f.runs() != 1 || f.stops() != 0 {
			t.Fatalf("the folder is not an extra mount: runs=%d stops=%d", f.runs(), f.stops())
		}
	})
	t.Run("another profiles folder live is reused with that key", func(t *testing.T) {
		m, f, ws, name := start(t, convKey("p"))
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("q"), []string{"bash"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if f.runs() != 1 || f.stops() != 0 {
			t.Fatalf("a live box must be reused: runs=%d stops=%d", f.runs(), f.stops())
		}
		if got := m.ConversationsKeyOf(name); got != ConversationsKey("TEST", ws, "", "p") {
			t.Fatalf("the session writes where the box mounts, key = %q", got)
		}
	})
	t.Run("another profiles folder idle is recreated with this one", func(t *testing.T) {
		m, f, ws, name := start(t, convKey("p"))
		m.ReleaseBox(name)
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("q"), []string{"bash"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		want := ConversationsKey("TEST", ws, "", "q")
		if f.runs() != 2 || f.stops() != 1 || f.sources[name][ConversationsBox] != ConversationsDir(m.opts.StateRoot, want) {
			t.Fatalf("runs=%d stops=%d src=%q", f.runs(), f.stops(), f.sources[name][ConversationsBox])
		}
		if m.ConversationsKeyOf(name) != want {
			t.Fatal("key not updated")
		}
	})
	t.Run("unreadable folder source refused", func(t *testing.T) {
		m, f, ws, _ := start(t, convKey("p"))
		f.bindErr = errors.New("boom")
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("p"), []string{"bash"}, nil, nil); !errors.Is(err, ErrBoxUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}
