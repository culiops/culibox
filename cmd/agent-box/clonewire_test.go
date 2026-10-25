package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
)

func noClones() []config.Clone { return nil }

func TestNewCloneHooksLinuxOnly(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()

	if h, err := newCloneHooks("darwin", &boxLayer{}, home, "", data, nil, noClones); h != nil || err != nil {
		t.Fatalf("darwin: got %v, %v; want nil, nil", h, err)
	}

	// Linux without a layer: hooks are still installed (the box-owned guard
	// needs no layer), the error says why, and Prepare refuses.
	h, err := newCloneHooks("linux", nil, home, "", data, nil, noClones)
	if err == nil || h == nil {
		t.Fatalf("linux without a layer: got %v, %v; want hooks and an error", h, err)
	}
	dir := filepath.Join(home, "clone")
	if _, owned := h.BoxOwned(dir); owned {
		t.Fatal("nothing owns the dir yet")
	}
	_, perr := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false)
	if !errors.Is(perr, box.ErrBoxUnavailable) {
		t.Fatalf("Prepare without a layer: want ErrBoxUnavailable, got %v", perr)
	}
	if b, _ := os.ReadFile(h.file); len(b) != 0 {
		t.Fatalf("an unavailable Prepare must not record a dir, file has %q", b)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	h, err = newCloneHooks("linux", &boxLayer{}, home, "", data, nil, noClones)
	if err != nil || h == nil || h.unavailable != "" {
		t.Fatalf("linux with a layer: got %v, %v; want ready hooks", h, err)
	}
}

func testHooks(t *testing.T, clones func() []config.Clone) *cloneHooks {
	t.Helper()
	return &cloneHooks{
		clones: clones,
		file:   filepath.Join(t.TempDir(), cloneDirsFile),
		prepare: func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) {
			return box.CloneMounts{}, false, nil
		},
	}
}

func TestCloneHooksBoxOwnedReadsCurrentProfiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "work", "clone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cur := []config.Clone(nil)
	h := testHooks(t, func() []config.Clone { return cur })
	if why, owned := h.BoxOwned(dir); owned {
		t.Fatalf("no clone profile yet, but %s is owned: %s", dir, why)
	}
	// A profile saved after boot: the next call must see it.
	cur = []config.Clone{{Of: filepath.Join(home, "repo"), Dir: dir}}
	if _, owned := h.BoxOwned(dir); !owned {
		t.Fatalf("%s should be box-owned once a profile clones into it", dir)
	}
	cur = nil
	if _, owned := h.BoxOwned(dir); owned {
		t.Fatalf("%s should be free again after the profile is gone (never prepared)", dir)
	}
}

func TestPreparedDirStaysOwnedWithoutTheProfile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "work", "clone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, noClones)
	if _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second Prepare adds no second line.
	if _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(h.file); string(b) != dir+"\n" {
		t.Fatalf("record = %q, want one line", b)
	}
	// The profile list no longer contains the clone (edited or deleted).
	if _, owned := h.BoxOwned(filepath.Join(dir, "sub")); !owned {
		t.Fatal("a prepared clone dir must stay box-owned after its profile is gone")
	}
	if _, owned := h.BoxOwned(filepath.Join(home, "other")); owned {
		t.Fatal("an unrelated dir must not be owned")
	}
	fi, err := os.Stat(h.file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("record mode: %v %v", fi, err)
	}
}

func TestRecordWrittenBeforePrepareRuns(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "clone")
	h := testHooks(t, noClones)
	boom := errors.New("clone failed midway")
	h.prepare = func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) {
		if b, _ := os.ReadFile(h.file); string(b) != dir+"\n" {
			t.Errorf("record at prepare time = %q, want %q", b, dir+"\n")
		}
		return box.CloneMounts{}, false, boom
	}
	if _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false); !errors.Is(err, boom) {
		t.Fatalf("want the prepare error, got %v", err)
	}
	if _, owned := h.BoxOwned(dir); !owned {
		t.Fatal("a half-made clone must stay owned")
	}
}

func TestRecordFailureFailsPrepareClosed(t *testing.T) {
	home := t.TempDir()
	h := testHooks(t, noClones)
	h.file = filepath.Join(home, "missing-dir", cloneDirsFile) // tmp file cannot be created
	called := false
	h.prepare = func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) {
		called = true
		return box.CloneMounts{}, false, nil
	}
	if _, err := h.Prepare(context.Background(), config.Clone{Of: "/r", Dir: filepath.Join(home, "c")}, false); err == nil || called {
		t.Fatalf("an unwritable record must refuse before creating anything: err=%v called=%v", err, called)
	}
	if _, err := h.Prepare(context.Background(), config.Clone{Of: "/r", Dir: "relative/dir"}, false); err == nil || called {
		t.Fatalf("a non-absolute dir must be refused: err=%v called=%v", err, called)
	}
}

func TestUnreadableRecordFailsClosed(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "anywhere")

	h := testHooks(t, noClones)
	if err := os.WriteFile(h.file, []byte("not/absolute\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	why, owned := h.BoxOwned(cwd)
	if !owned || !strings.Contains(why, h.file) {
		t.Fatalf("a corrupt record must report owned naming the file, got %q, %v", why, owned)
	}
	if _, err := h.Prepare(context.Background(), config.Clone{Of: "/r", Dir: filepath.Join(home, "c")}, false); err == nil {
		t.Fatal("Prepare must refuse over a corrupt record")
	}

	// A symlink at the record path is never followed.
	h = testHooks(t, noClones)
	target := filepath.Join(home, "target")
	if err := os.WriteFile(target, []byte(filepath.Join(home, "x")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.file); err != nil {
		t.Fatal(err)
	}
	if why, owned := h.BoxOwned(cwd); !owned || !strings.Contains(why, h.file) {
		t.Fatalf("a symlinked record must report owned, got %q, %v", why, owned)
	}
}

// A clone_of the box can write is a host code-execution path: the box plants
// .git/hooks or core.hooksPath there and the host's next git run in it fires
// them. Prepare, the save check and `clone fetch` all refuse it.
func TestPrepareRefusesBoxOwnedCloneOf(t *testing.T) {
	home := t.TempDir()
	otherClone := filepath.Join(home, "work", "other-clone")
	mine := filepath.Join(home, "work", "mine")
	if err := os.MkdirAll(otherClone, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	prep := func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) {
		called = true
		return box.CloneMounts{}, false, nil
	}

	// (a) another live profile's clone dir, and a repo nested in it.
	h := testHooks(t, func() []config.Clone {
		return []config.Clone{{Of: filepath.Join(home, "repo"), Dir: otherClone}}
	})
	h.prepare = prep
	for _, of := range []string{otherClone, filepath.Join(otherClone, "nested", "repo")} {
		_, err := h.Prepare(context.Background(), config.Clone{Of: of, Dir: mine}, false)
		if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "is box-owned") ||
			!strings.Contains(err.Error(), "a clone must be made from a host-owned repo") {
			t.Errorf("clone_of %s: want the box-owned refusal, got %v", of, err)
		}
	}
	if called {
		t.Fatal("clone.Prepare ran for a box-owned clone_of")
	}
	if b, _ := os.ReadFile(h.file); strings.Contains(string(b), mine) {
		t.Fatalf("a refused spawn recorded its dir: %q", b)
	}

	// (a) a dir known only from the clone-dirs record (its profile is gone).
	h = testHooks(t, noClones)
	h.prepare = prep
	if err := h.record(otherClone); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Prepare(context.Background(), config.Clone{Of: otherClone, Dir: mine}, false); !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("a recorded clone dir as clone_of: want ErrUnboxableWorkspace, got %v", err)
	}
	if called {
		t.Fatal("clone.Prepare ran for a recorded clone dir as clone_of")
	}

	// A host-owned repo is fine.
	if _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "repo"), Dir: mine}, false); err != nil || !called {
		t.Fatalf("a host-owned clone_of must pass: err=%v called=%v", err, called)
	}
}

func TestCloneSaveCheckRefusesBoxOwnedCloneOf(t *testing.T) {
	home := t.TempDir()
	otherClone := filepath.Join(home, "work", "other-clone")
	h := testHooks(t, func() []config.Clone {
		return []config.Clone{{Of: filepath.Join(home, "repo"), Dir: otherClone}}
	})
	check := cloneSaveCheck(h, home, nil)
	err := check(config.Clone{Of: otherClone, Dir: filepath.Join(home, "work", "mine")})
	if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "is box-owned") {
		t.Fatalf("save of a profile cloning another clone: want the box-owned refusal, got %v", err)
	}
}

func TestRecordRefusesControlCharacters(t *testing.T) {
	h := testHooks(t, noClones)
	for _, d := range []string{"/a\nb", "/a\x00b", "/a\rb", "/a\x7fb"} {
		if err := h.record(d); err == nil {
			t.Errorf("record(%q) accepted a control character", d)
		}
	}
	if b, _ := os.ReadFile(h.file); len(b) != 0 {
		t.Fatalf("record file = %q, want nothing written", b)
	}
	if usableDir("/a\nb") {
		t.Error("usableDir accepts a newline")
	}
}

func TestCloneHooksHostOriginRefusesBoxOwnedRepo(t *testing.T) {
	g, err := clone.NewExecGit()
	if err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "work", "clone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, func() []config.Clone { return []config.Clone{{Of: filepath.Join(home, "repo"), Dir: dir}} })
	if _, err := h.HostOrigin(context.Background(), dir); err == nil {
		t.Fatal("HostOrigin ran against a box-owned directory")
	}
	h.env.Git = g
	if _, err := h.HostOrigin(context.Background(), dir); err == nil {
		t.Fatal("HostOrigin ran against a box-owned directory with git available")
	}
	h.unavailable = "no containment layer"
	if _, err := h.HostOrigin(context.Background(), filepath.Join(home, "repo")); err == nil {
		t.Fatal("an unavailable clone layer must not read an origin")
	}
}

func TestContextSaveCheck(t *testing.T) {
	home := t.TempDir()
	otherClone := filepath.Join(home, "work", "other-clone")
	if err := os.MkdirAll(filepath.Join(otherClone, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(home, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, func() []config.Clone {
		return []config.Clone{{Of: filepath.Join(home, "repo"), Dir: otherClone}}
	})
	check := contextSaveCheck(h, home, nil, nil)
	id, err := check(docs)
	if err != nil {
		t.Fatalf("a plain host directory must pass: %v", err)
	}
	if want, _ := box.CheckContextPathID(docs, home, nil, nil); (id == nil) != (want == nil) || (id != nil && *id != *want) {
		t.Fatalf("the check returned identity %+v, want CheckContextPathID's %+v", id, want)
	}
	_, err = check(filepath.Join(otherClone, "sub"))
	if err == nil || !strings.Contains(err.Error(), "is inside a box-owned directory") || !strings.Contains(err.Error(), "another box writes there") {
		t.Fatalf("a path inside another box's clone: want the box-owned refusal, got %v", err)
	}
	if _, err := check(filepath.Join(home, "missing")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a missing path must be refused by CheckContextPath, got %v", err)
	}
	// No clone hooks (not Linux): only CheckContextPath.
	if _, err := contextSaveCheck(nil, home, nil, nil)(filepath.Join(otherClone, "sub")); err != nil {
		t.Fatalf("without hooks the box-owned refusal does not apply: %v", err)
	}
}
