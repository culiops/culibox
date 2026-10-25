package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
)

func noClones() []config.Clone { return nil }

func TestNewCloneHooksLinuxOnly(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()

	if h, err := newCloneHooks("darwin", &boxLayer{}, home, "", data, "DEV", nil, noClones); h != nil || err != nil {
		t.Fatalf("darwin: got %v, %v; want nil, nil", h, err)
	}

	// Linux without a layer: hooks are still installed (the box-owned guard
	// needs no layer), the error says why, and Prepare refuses.
	h, err := newCloneHooks("linux", nil, home, "", data, "DEV", nil, noClones)
	if err == nil || h == nil {
		t.Fatalf("linux without a layer: got %v, %v; want hooks and an error", h, err)
	}
	dir := filepath.Join(home, "clone")
	if _, owned := h.BoxOwned(dir); owned {
		t.Fatal("nothing owns the dir yet")
	}
	_, _, perr := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false)
	if !errors.Is(perr, box.ErrBoxUnavailable) {
		t.Fatalf("Prepare without a layer: want ErrBoxUnavailable, got %v", perr)
	}
	if b, _ := os.ReadFile(h.file); len(b) != 0 {
		t.Fatalf("an unavailable Prepare must not record a dir, file has %q", b)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	h, err = newCloneHooks("linux", &boxLayer{dockerBin: "/usr/bin/docker", image: "img:1"}, home, "", data, "DEV", nil, noClones)
	if err != nil || h == nil || h.unavailable != "" {
		t.Fatalf("linux with a layer: got %v, %v; want ready hooks", h, err)
	}
	if h.docker != (clone.ExecDocker{Bin: "/usr/bin/docker"}) || h.image != "img:1" || h.instance != "DEV" ||
		h.dataDir != data || h.listBoxes == nil || h.refresh == nil {
		t.Fatalf("refresh wiring: docker=%v image=%q instance=%q dataDir=%q listBoxes=%v refresh=%v",
			h.docker, h.image, h.instance, h.dataDir, h.listBoxes != nil, h.refresh != nil)
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
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second Prepare adds no second line.
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false); err != nil {
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
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "r"), Dir: dir}, false); !errors.Is(err, boom) {
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
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: "/r", Dir: filepath.Join(home, "c")}, false); err == nil || called {
		t.Fatalf("an unwritable record must refuse before creating anything: err=%v called=%v", err, called)
	}
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: "/r", Dir: "relative/dir"}, false); err == nil || called {
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
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: "/r", Dir: filepath.Join(home, "c")}, false); err == nil {
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
		_, _, err := h.Prepare(context.Background(), config.Clone{Of: of, Dir: mine}, false)
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
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: otherClone, Dir: mine}, false); !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("a recorded clone dir as clone_of: want ErrUnboxableWorkspace, got %v", err)
	}
	if called {
		t.Fatal("clone.Prepare ran for a recorded clone dir as clone_of")
	}

	// A host-owned repo is fine.
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: filepath.Join(home, "repo"), Dir: mine}, false); err != nil || !called {
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

// refreshFake is a cloneHooks ready for Refresh, with the box listing and the
// clone package's Refresh replaced by recorders.
type refreshFake struct {
	h *cloneHooks

	mu     sync.Mutex
	boxes  []box.BoxInfo
	err    error
	lists  int
	reqs   []clone.RefreshRequest
	result clone.RefreshResult
}

func newRefreshFake(t *testing.T) *refreshFake {
	t.Helper()
	f := &refreshFake{}
	h := testHooks(t, noClones)
	h.image, h.instance, h.dataDir = "img:1", "DEV", "/data"
	h.listBoxes = func(context.Context) ([]box.BoxInfo, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lists++
		return f.boxes, f.err
	}
	h.refresh = func(_ context.Context, _ clone.Git, _ clone.Docker, r clone.RefreshRequest) clone.RefreshResult {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, r)
		res := f.result
		res.Skipped = r.Skip
		return res
	}
	f.h = h
	return f
}

func (f *refreshFake) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

func (f *refreshFake) lastReq(t *testing.T) clone.RefreshRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("clone.Refresh was not called")
	}
	return f.reqs[len(f.reqs)-1]
}

// cloneDir makes a clone dir and returns it with symlinks resolved, the form
// the box workspace labels carry.
func cloneDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func never() bool { return false }

// The daily loop: a session ends, its box idles for the TTL, and the next spawn
// of the same profile must still refresh.
func TestCloneRefreshRunsOverOwnIdleBox(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	f.boxes = []box.BoxInfo{{Name: "own", State: "running", Workspace: dir, Instance: "DEV"}}
	c := config.Clone{Of: "/host/repo", Dir: dir}
	info, release := f.h.Refresh(context.Background(), c, session.RefreshOpts{LiveSession: never})
	defer release()
	r := f.lastReq(t)
	if r.Skip != "" || info.Skipped != "" {
		t.Fatalf("an idle box of this instance must not block the refresh: skip=%q info=%+v", r.Skip, info)
	}
	if !reflect.DeepEqual(r.Clone, c) || r.Image != "img:1" || r.Instance != "DEV" || r.DataDir != "/data" {
		t.Fatalf("request = %+v", r)
	}
	if f.listCount() != 1 {
		t.Fatalf("listed %d times, want 1", f.listCount())
	}
}

func TestCloneRefreshSkipsLiveSession(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	info, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir},
		session.RefreshOpts{LiveSession: func() bool { return true }})
	defer release()
	if r := f.lastReq(t); r.Skip != clone.SkipInUse || info.Skipped != clone.SkipInUse {
		t.Fatalf("skip = %q, info = %+v; want in-use", r.Skip, info)
	}
	if f.listCount() != 0 {
		t.Fatal("a live session skips without listing boxes")
	}
}

// A second spawn of the same clone waits for the first one's lock, and only
// then may ask whether a live session exists: the first spawn publishes its
// session before it releases.
func TestCloneRefreshChecksLiveAfterLock(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	var calls, heldAtCall int
	live := func() bool {
		calls++
		f.h.lockMu.Lock()
		if ch := f.h.locks[dir]; ch != nil && len(ch) == 1 {
			heldAtCall++
		}
		f.h.lockMu.Unlock()
		return false
	}
	_, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{LiveSession: live})
	release()
	if calls != 1 || heldAtCall != 1 {
		t.Fatalf("LiveSession called %d time(s), %d with the lock held; want 1, 1", calls, heldAtCall)
	}
}

func TestCloneRefreshSkipsOtherInstanceBox(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	f.boxes = []box.BoxInfo{{Name: "prod-box", State: "running", Workspace: dir, Instance: "PROD"}}
	info, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{LiveSession: never})
	defer release()
	if r := f.lastReq(t); r.Skip != clone.SkipOtherInstance || info.Skipped != clone.SkipOtherInstance {
		t.Fatalf("skip = %q, info = %+v; want other-instance", r.Skip, info)
	}
	if info.Error != "" {
		t.Fatalf("a held clone is a skip, not an error: %q", info.Error)
	}
}

// The workspace label is the resolved path: a clone dir reached through a
// symlink still matches it.
func TestCloneRefreshResolvesTheCloneDir(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	f.boxes = []box.BoxInfo{{Name: "prod-box", State: "running", Workspace: dir, Instance: "PROD"}}
	_, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: link}, session.RefreshOpts{})
	defer release()
	if r := f.lastReq(t); r.Skip != clone.SkipOtherInstance {
		t.Fatalf("skip = %q; want other-instance through the symlink", r.Skip)
	}
}

func TestCloneRefreshCreatedSkipsListing(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	info, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{Created: true})
	defer release()
	if r := f.lastReq(t); r.Skip != clone.SkipCreated || info.Skipped != clone.SkipCreated {
		t.Fatalf("skip = %q, info = %+v; want created", r.Skip, info)
	}
	if f.listCount() != 0 {
		t.Fatal("a new clone skips without listing boxes")
	}
}

func TestCloneRefreshListingErrorFailsClosed(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	f.err = errors.New("docker ps: exit status 1: \x1b[31mCannot connect")
	f.result = clone.RefreshResult{Error: "from the refresh"}
	info, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{LiveSession: never})
	defer release()
	if r := f.lastReq(t); r.Skip != clone.SkipOtherInstance {
		t.Fatalf("skip = %q; want other-instance", r.Skip)
	}
	want := "cannot check whether another instance's box holds the clone: docker ps: exit status 1: ?[31mCannot connect"
	if info.Error != want {
		t.Fatalf("error = %q\nwant    %q", info.Error, want)
	}

	// No listing at all fails closed the same way.
	f = newRefreshFake(t)
	f.h.listBoxes = nil
	info, release = f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{})
	defer release()
	if info.Skipped != clone.SkipOtherInstance || !strings.Contains(info.Error, "cannot check") {
		t.Fatalf("no listing: info = %+v", info)
	}
}

func TestCloneRefreshUnavailable(t *testing.T) {
	f := newRefreshFake(t)
	f.h.unavailable = "no containment layer"
	info, release := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: cloneDir(t)}, session.RefreshOpts{})
	release()
	if info.Error != "clone mode is unavailable (no containment layer)" || len(f.reqs) != 0 {
		t.Fatalf("info = %+v, refresh calls = %d", info, len(f.reqs))
	}
}

// refreshAsync runs Refresh in a goroutine; the channel yields its release.
func refreshAsync(h *cloneHooks, ctx context.Context, dir string) <-chan func() {
	done := make(chan func(), 1)
	go func() {
		_, release := h.Refresh(ctx, config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{})
		done <- release
	}()
	return done
}

func TestCloneRefreshSerializesSameClone(t *testing.T) {
	f := newRefreshFake(t)
	a, b := cloneDir(t), cloneDir(t)
	_, releaseA := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: a}, session.RefreshOpts{})

	second := refreshAsync(f.h, context.Background(), a)
	select {
	case <-second:
		t.Fatal("a second refresh of the same clone ran while the first held the lock")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case release := <-refreshAsync(f.h, context.Background(), b):
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("a refresh of a different clone waited on another clone's lock")
	}

	releaseA()
	select {
	case release := <-second:
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("the second refresh did not run after the first released")
	}
}

func TestCloneRefreshLockWaitTimesOut(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	_, held := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	info, release := f.h.Refresh(ctx, config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{LiveSession: never})
	if r := f.lastReq(t); r.Skip != clone.SkipInUse || info.Skipped != clone.SkipInUse {
		t.Fatalf("skip = %q, info = %+v; want in-use after the lock wait timed out", r.Skip, info)
	}
	release() // a no-op: it must not free the lock the first spawn holds
	if _, ok := f.h.lockClone(ctx, dir); ok {
		t.Fatal("the timed-out refresh's release freed another spawn's lock")
	}

	held()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	rel, ok := f.h.lockClone(ctx2, dir)
	if !ok {
		t.Fatal("the lock stays taken after its holder released")
	}
	rel()
	rel() // releasing twice must not block or free a later holder's lock
}

// A panic after the lock is taken must not leave the clone locked forever.
func TestCloneRefreshPanicReleasesLock(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	f.h.refresh = func(context.Context, clone.Git, clone.Docker, clone.RefreshRequest) clone.RefreshResult {
		panic("boom")
	}
	func() {
		defer func() {
			if p := recover(); p != "boom" {
				t.Fatalf("recovered %v, want the original panic", p)
			}
		}()
		f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rel, ok := f.h.lockClone(ctx, dir)
	if !ok {
		t.Fatal("a panicking refresh left the clone locked")
	}
	rel()
}

func TestCloneInfoOf(t *testing.T) {
	yes := true
	r := clone.RefreshResult{
		Refreshed: true, Skipped: "in-use", Error: "e", Base: "master", Branch: "feat",
		Report: clone.Report{
			Head: "feat", HeadSHA: "abc", Ahead: 1, Behind: 2,
			FFFrom: "a1", FFTo: "b2", FFCount: 3, FFRefused: true, Stale: []string{"old"},
		},
		HeadInHost: &yes, Rewritten: true, Unverified: true,
	}
	want := session.CloneInfo{
		Refreshed: true, Skipped: "in-use", Error: "e", Base: "master", Branch: "feat",
		Ahead: 1, Behind: 2, FFFrom: "a1", FFTo: "b2", FFCount: 3, FFRefused: true,
		HeadInHost: &yes, StaleBranches: []string{"old"}, Rewritten: true, Unverified: true,
	}
	got := cloneInfoOf(r)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cloneInfoOf =\n%+v\nwant\n%+v", got, want)
	}
	// Every CloneInfo field is filled from the result: a field added later
	// without a copy shows up here as zero.
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("CloneInfo.%s is not copied", v.Type().Field(i).Name)
		}
	}
}

func TestSweepCloneContainers(t *testing.T) {
	listed := map[string][]string{
		clone.LabelCloneFetch + "=DEV":   {"clonefetch-1", "clonefetch-2"},
		clone.LabelCloneRefresh + "=DEV": {"clonerefresh-1", "stuck"},
	}
	var queried, removed []string
	list := func(_ context.Context, label string) ([]string, error) {
		queried = append(queried, label)
		return listed[label], nil
	}
	remove := func(_ context.Context, name string) error {
		removed = append(removed, name)
		if name == "stuck" {
			return errors.New("docker rm: busy")
		}
		return nil
	}
	n := sweepCloneContainers(context.Background(), list, remove, "DEV")
	if want := []string{clone.LabelCloneFetch + "=DEV", clone.LabelCloneRefresh + "=DEV"}; !reflect.DeepEqual(queried, want) {
		t.Fatalf("queried %q, want %q", queried, want)
	}
	if want := []string{"clonefetch-1", "clonefetch-2", "clonerefresh-1", "stuck"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed %q, want %q", removed, want)
	}
	if n != 3 {
		t.Fatalf("count = %d, want 3 (the failed removal is not counted)", n)
	}

	// A failed listing of one label still sweeps the other.
	removed = nil
	failing := func(_ context.Context, label string) ([]string, error) {
		if label == clone.LabelCloneFetch+"=DEV" {
			return nil, errors.New("docker ps: down")
		}
		return []string{"clonerefresh-1"}, nil
	}
	if n := sweepCloneContainers(context.Background(), failing, remove, "DEV"); n != 1 || !reflect.DeepEqual(removed, []string{"clonerefresh-1"}) {
		t.Fatalf("count = %d, removed %q", n, removed)
	}
}
