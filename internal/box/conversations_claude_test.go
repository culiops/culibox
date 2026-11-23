package box

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// seedProjects writes files under a box home's projects dir, as an older build's
// sessions left them; rel paths are below .claude/projects.
func seedProjects(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(home, ".claude", "projects", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type adoptCall struct{ from, to string }

func adoptManager(t *testing.T) (*Manager, *fakeClient, *[]adoptCall) {
	t.Helper()
	m, f := newTestManagerWithFake(t)
	var calls []adoptCall
	m.SetAdoptHook(func(from, to string) error {
		calls = append(calls, adoptCall{from, to})
		return nil
	})
	return m, f, &calls
}

func readOr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// D3: the first container on the new build takes the home's conversations into
// the folder, so the mount does not hide them from /resume.
func TestAdoptionMovesTheHomesConversations(t *testing.T) {
	m, _, calls := adoptManager(t)
	ws := realTemp(t)
	name, _ := m.BoxName(ws, convKey("p"))
	home := m.stateDir(name)
	seedProjects(t, home, map[string]string{
		"-w/a.jsonl":                 "A",
		"-w/a/subagents/agent.jsonl": "S",
		"-other/b.jsonl":             "B",
	})
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	conv := ConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"))
	for rel, want := range map[string]string{"-w/a.jsonl": "A", "-w/a/subagents/agent.jsonl": "S", "-other/b.jsonl": "B"} {
		if got := readOr(filepath.Join(conv, rel)); got != want {
			t.Errorf("%s in the folder = %q, want %q", rel, got, want)
		}
		if _, err := os.Lstat(filepath.Join(home, ".claude", "projects", rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still in the home: %v", rel, err)
		}
	}
	projects := filepath.Join(home, ".claude", "projects")
	want := []adoptCall{
		{filepath.Join(projects, "-other", "b.jsonl"), filepath.Join(conv, "-other", "b.jsonl")},
		{filepath.Join(projects, "-w", "a"), filepath.Join(conv, "-w", "a")},
		{filepath.Join(projects, "-w", "a.jsonl"), filepath.Join(conv, "-w", "a.jsonl")},
	}
	got := slices.Clone(*calls)
	slices.SortFunc(got, func(a, b adoptCall) int { return strings.Compare(a.from, b.from) })
	if !slices.Equal(got, want) {
		t.Fatalf("hook calls:\n got  %q\n want %q", got, want)
	}
}

// A name the folder already has stays in the home, and the folder's copy is untouched.
func TestAdoptionLeavesATakenNameInTheHome(t *testing.T) {
	m, _, calls := adoptManager(t)
	ws := realTemp(t)
	name, _ := m.BoxName(ws, convKey("p"))
	home := m.stateDir(name)
	seedProjects(t, home, map[string]string{"-w/a.jsonl": "home", "-w/d/x.jsonl": "home-d"})
	conv := ConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"))
	for rel, body := range map[string]string{"-w/a.jsonl": "folder", "-w/d/y.jsonl": "folder-d"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(conv, rel)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(conv, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(home, ".claude", "projects")
	if readOr(filepath.Join(conv, "-w/a.jsonl")) != "folder" || readOr(filepath.Join(projects, "-w/a.jsonl")) != "home" {
		t.Fatal("a taken file name was overwritten or moved")
	}
	if readOr(filepath.Join(conv, "-w/d/y.jsonl")) != "folder-d" || readOr(filepath.Join(projects, "-w/d/x.jsonl")) != "home-d" {
		t.Fatal("a taken directory name was merged or replaced")
	}
	if len(*calls) != 0 {
		t.Fatalf("hook told of moves that did not happen: %q", *calls)
	}
}

// Links, FIFOs and a linked workspace folder are never moved or followed.
func TestAdoptionMovesOnlyPlainFilesAndDirectories(t *testing.T) {
	m, _, calls := adoptManager(t)
	ws := realTemp(t)
	name, _ := m.BoxName(ws, convKey("p"))
	home := m.stateDir(name)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.jsonl"), []byte("S"), 0o600); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(filepath.Join(projects, "-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		os.Symlink(filepath.Join(outside, "secret.jsonl"), filepath.Join(projects, "-w", "link.jsonl")),
		os.Symlink(outside, filepath.Join(projects, "-w", "linkdir")),
		os.Symlink(outside, filepath.Join(projects, "-linked")),
		syscall.Mkfifo(filepath.Join(projects, "-w", "fifo.jsonl"), 0o600),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	conv := ConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"))
	for _, rel := range []string{"-w/link.jsonl", "-w/linkdir", "-linked", "-w/fifo.jsonl"} {
		if _, err := os.Lstat(filepath.Join(conv, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s reached the folder: %v", rel, err)
		}
		if _, err := os.Lstat(filepath.Join(projects, rel)); err != nil {
			t.Errorf("%s left the home: %v", rel, err)
		}
	}
	if readOr(filepath.Join(outside, "secret.jsonl")) != "S" {
		t.Fatal("the link's target was touched")
	}
	if len(*calls) != 0 {
		t.Fatalf("hook calls: %q", *calls)
	}
}

// Bounded: past the entry cap the rest stays in the home.
func TestAdoptionStopsAtTheEntryCap(t *testing.T) {
	old := maxAdoptEntries
	maxAdoptEntries = 3
	t.Cleanup(func() { maxAdoptEntries = old })
	m, _, calls := adoptManager(t)
	ws := realTemp(t)
	name, _ := m.BoxName(ws, convKey("p"))
	files := map[string]string{}
	for i := range 10 {
		files["-w/"+strconv.Itoa(i)+".jsonl"] = "x"
	}
	seedProjects(t, m.stateDir(name), files)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(*calls); n == 0 || n >= 3 {
		t.Fatalf("moved %d entries past a cap of 3 entries read", n)
	}
	left, _ := os.ReadDir(filepath.Join(m.stateDir(name), ".claude", "projects", "-w"))
	if len(left)+len(*calls) != 10 {
		t.Fatalf("left %d, moved %d: entries lost", len(left), len(*calls))
	}
}

// The hook failing stops the adoption before that move: analytics would count
// the moved transcript again.
func TestAdoptionStopsWhenTheHookFails(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	m.SetAdoptHook(func(from, to string) error { return errors.New("db locked") })
	ws := realTemp(t)
	name, _ := m.BoxName(ws, convKey("p"))
	seedProjects(t, m.stateDir(name), map[string]string{"-w/a.jsonl": "A", "-w/b.jsonl": "B"})
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatalf("an adoption failure must not fail the spawn: %v", err)
	}
	if f.runs() != 1 {
		t.Fatal("no box")
	}
	for _, n := range []string{"a.jsonl", "b.jsonl"} {
		if _, err := os.Lstat(filepath.Join(m.stateDir(name), ".claude", "projects", "-w", n)); err != nil {
			t.Fatalf("%s moved although the hook failed: %v", n, err)
		}
	}
}

// Only when the container is created: a running box's agent may be writing in
// its home.
func TestAdoptionOnlyWhenTheContainerIsCreated(t *testing.T) {
	m, f, calls := adoptManager(t)
	ws := realTemp(t)
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedProjects(t, m.stateDir(name), map[string]string{"-w/late.jsonl": "L"})
	if _, _, _, err := m.Wrap(context.Background(), ws, "s2", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 1 || len(*calls) != 0 {
		t.Fatalf("runs=%d calls=%q", f.runs(), *calls)
	}
	// No profile, no folder: nothing adopted either.
	m2, _, calls2 := adoptManager(t)
	name2, _ := m2.BoxName(ws, BoxKey{})
	seedProjects(t, m2.stateDir(name2), map[string]string{"-w/a.jsonl": "A"})
	if _, _, _, err := m2.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls2) != 0 {
		t.Fatalf("adopted with no folder: %q", *calls2)
	}
}
