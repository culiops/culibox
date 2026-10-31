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
	ctxErr []error // each refresh's ctx.Err() at the time it ran
	result clone.RefreshResult

	rets      []clone.ReturnRequest
	retCtx    []context.Context // the context each return ran on
	retResult clone.ReturnResult
	order     []string // "ret" / "refresh", in call order
	retDelay  time.Duration
	active    int // returns running now, and the most at once
	maxActive int
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
	h.refresh = func(ctx context.Context, _ clone.Git, _ clone.Docker, r clone.RefreshRequest) clone.RefreshResult {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, r)
		f.order = append(f.order, "refresh")
		f.ctxErr = append(f.ctxErr, ctx.Err())
		res := f.result
		res.Skipped = r.Skip
		return res
	}
	h.ret = func(ctx context.Context, _ clone.Git, _ clone.Docker, r clone.ReturnRequest) clone.ReturnResult {
		f.mu.Lock()
		f.rets = append(f.rets, r)
		f.retCtx = append(f.retCtx, ctx)
		f.order = append(f.order, "ret")
		f.active++
		f.maxActive = max(f.maxActive, f.active)
		res, delay := f.retResult, f.retDelay
		f.mu.Unlock()
		time.Sleep(delay)
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		return res
	}
	f.h = h
	return f
}

func (f *refreshFake) retCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rets)
}

func (f *refreshFake) lastRet(t *testing.T) clone.ReturnRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rets) == 0 {
		t.Fatal("clone.Return was not called")
	}
	return f.rets[len(f.rets)-1]
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
	// The listing error joins the refresh's own error; it does not replace it.
	want := "from the refresh; cannot check whether another instance's box holds the clone: docker ps: exit status 1: ?[31mCannot connect"
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
	if info.Error != "" {
		t.Fatalf("error = %q; the expired wait must not reach the user", info.Error)
	}
	if n := len(f.ctxErr); n != 2 || f.ctxErr[n-1] != nil { // [first spawn, timed-out wait]
		t.Fatalf("the skipped refresh ran on the expired context: %v", f.ctxErr)
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
	ret := &session.ReturnSummary{Trigger: session.ReturnOnSpawn, Home: []string{"box/feat"}}
	want := session.CloneInfo{
		Refreshed: true, Skipped: "in-use", Error: "e", Base: "master", Branch: "feat",
		Ahead: 1, Behind: 2, FFFrom: "a1", FFTo: "b2", FFCount: 3, FFRefused: true,
		HeadInHost: &yes, StaleBranches: []string{"old"}, Rewritten: true, Unverified: true,
		Return: ret,
	}
	got := cloneInfoOf(r, ret)
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
		clone.LabelCloneList + "=DEV":    {"clonelist-1"},
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
	if want := []string{clone.LabelCloneFetch + "=DEV", clone.LabelCloneRefresh + "=DEV", clone.LabelCloneList + "=DEV"}; !reflect.DeepEqual(queried, want) {
		t.Fatalf("queried %q, want %q", queried, want)
	}
	if want := []string{"clonefetch-1", "clonefetch-2", "clonerefresh-1", "stuck", "clonelist-1"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed %q, want %q", removed, want)
	}
	if n != 4 {
		t.Fatalf("count = %d, want 4 (the failed removal is not counted)", n)
	}

	// A failed listing of one label still sweeps the other.
	removed = nil
	failing := func(_ context.Context, label string) ([]string, error) {
		if label == clone.LabelCloneFetch+"=DEV" {
			return nil, errors.New("docker ps: down")
		}
		return []string{"clonerefresh-1"}, nil
	}
	if n := sweepCloneContainers(context.Background(), failing, remove, "DEV"); n != 2 || !reflect.DeepEqual(removed, []string{"clonerefresh-1", "clonerefresh-1"}) {
		t.Fatalf("count = %d, removed %q", n, removed)
	}
}

func TestCloneUnavailableReason(t *testing.T) {
	for _, c := range []struct {
		name string
		goos string
		h    *cloneHooks
		want string
	}{
		{"not linux", "darwin", nil, "private copies need Linux for now"},
		{"linux without hooks", "linux", nil, "private copies need Linux for now"},
		{"linux, no layer", "linux", &cloneHooks{unavailable: "no containment layer"}, "no containment layer"},
		{"linux, available", "linux", &cloneHooks{}, ""},
	} {
		if got := cloneUnavailableReason(c.goos, c.h); got != c.want {
			t.Errorf("%s: cloneUnavailableReason = %q, want %q", c.name, got, c.want)
		}
	}
}

// returnWorkAsync runs ReturnWork in a goroutine; the channel yields its summary.
func returnWorkAsync(h *cloneHooks, ctx context.Context, c config.Clone, o session.ReturnOpts) <-chan session.ReturnSummary {
	done := make(chan session.ReturnSummary, 1)
	go func() { done <- h.ReturnWork(ctx, c, o) }()
	return done
}

// A return and a refresh of one clone never overlap, and neither do an exit
// return and a "now" return: both rewrite the same refs and state file.
func TestCloneReturnTakesTheCloneLock(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}

	// A spawn holds the clone: an exit return waits for it.
	_, releaseSpawn := f.h.Refresh(context.Background(), c, session.RefreshOpts{})
	exit := returnWorkAsync(f.h, context.Background(), c, session.ReturnOpts{Trigger: session.ReturnOnExit})
	select {
	case <-exit:
		t.Fatal("a return ran while a refresh of the same clone held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	if f.retCount() != 0 {
		t.Fatal("clone.Return ran under another holder's lock")
	}
	releaseSpawn()
	select {
	case <-exit:
	case <-time.After(5 * time.Second):
		t.Fatal("the return did not run after the refresh released")
	}

	// A refresh waits for a return in progress.
	f.retDelay = 200 * time.Millisecond
	now := returnWorkAsync(f.h, context.Background(), c, session.ReturnOpts{Trigger: session.ReturnNow})
	for deadline := time.Now().Add(5 * time.Second); f.retCount() < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the \"now\" return did not start")
		}
	}
	start := time.Now()
	_, rel := f.h.Refresh(context.Background(), c, session.RefreshOpts{})
	rel()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("a refresh ran while a return of the same clone was in progress")
	}
	<-now

	// An exit return and a "now" return of one clone run one after the other.
	f.mu.Lock()
	f.maxActive = 0
	f.mu.Unlock()
	a := returnWorkAsync(f.h, context.Background(), c, session.ReturnOpts{Trigger: session.ReturnOnExit})
	b := returnWorkAsync(f.h, context.Background(), c, session.ReturnOpts{Trigger: session.ReturnNow})
	<-a
	<-b
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxActive != 1 {
		t.Fatalf("%d returns of one clone ran at once, want 1", f.maxActive)
	}
}

func TestCloneReturnSkipsLiveSessionUnlessNow(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}
	live := func() bool { return true }

	sum := f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: session.ReturnOnExit, LiveSession: live})
	if sum.Skipped != clone.SkipInUse || sum.Trigger != session.ReturnOnExit || f.retCount() != 0 {
		t.Fatalf("exit with a live session: %+v, %d return call(s); want in-use and none", sum, f.retCount())
	}
	sum = f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: session.ReturnOnSpawn, LiveSession: live})
	if sum.Skipped != clone.SkipInUse || f.retCount() != 0 {
		t.Fatalf("spawn with a live session: %+v", sum)
	}
	sum = f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: session.ReturnNow, LiveSession: live})
	if sum.Skipped != "" || f.retCount() != 1 || f.lastRet(t).Mode != clone.ReturnForced {
		t.Fatalf("now with a live session: %+v, %d call(s), mode %v; want one forced call", sum, f.retCount(), f.lastRet(t).Mode)
	}
}

// LiveSession is asked only once the lock is held.
func TestCloneReturnChecksLiveAfterLock(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	var heldAtCall int
	live := func() bool {
		f.h.lockMu.Lock()
		if ch := f.h.locks[dir]; ch != nil && len(ch) == 1 {
			heldAtCall++
		}
		f.h.lockMu.Unlock()
		return false
	}
	f.h.ReturnWork(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.ReturnOpts{Trigger: session.ReturnOnExit, LiveSession: live})
	if heldAtCall != 1 {
		t.Fatalf("LiveSession called %d time(s) with the lock held, want 1", heldAtCall)
	}
}

func TestCloneReturnSkipsOtherInstanceBox(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	f.boxes = []box.BoxInfo{{Name: "prod-box", State: "running", Workspace: dir, Instance: "PROD"}}
	c := config.Clone{Of: "/r", Dir: dir}
	for _, trig := range []string{session.ReturnOnExit, session.ReturnNow} {
		sum := f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: trig, LiveSession: never})
		if sum.Skipped != clone.SkipOtherInstance || sum.Error != "" || f.retCount() != 0 {
			t.Fatalf("%s: %+v, %d return call(s); want other-instance, no error, no call", trig, sum, f.retCount())
		}
	}

	// A listing that fails fails closed, with the error.
	f.boxes, f.err = nil, errors.New("docker ps: down")
	sum := f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: session.ReturnOnExit})
	if sum.Skipped != clone.SkipOtherInstance || !strings.Contains(sum.Error, "cannot check") || f.retCount() != 0 {
		t.Fatalf("listing error: %+v", sum)
	}
}

func TestCloneReturnModeFollowsTheSwitch(t *testing.T) {
	f := newRefreshFake(t)
	f.h.env.Home, f.h.env.Protected = "/home/u", []box.ProtectedDir{{Path: "/p"}}
	f.h.dataDir = t.TempDir()
	dir := cloneDir(t)
	for _, c := range []struct {
		off  bool
		trig string
		want clone.ReturnMode
	}{
		{false, session.ReturnOnExit, clone.ReturnAuto},
		{false, session.ReturnOnSpawn, clone.ReturnAuto},
		{true, session.ReturnOnExit, clone.ReturnDry},
		{true, session.ReturnOnSpawn, clone.ReturnDry},
		{false, session.ReturnNow, clone.ReturnForced},
		{true, session.ReturnNow, clone.ReturnForced},
	} {
		cl := config.Clone{Of: "/r", Dir: dir, NoAutoReturn: c.off}
		f.h.ReturnWork(context.Background(), cl, session.ReturnOpts{Trigger: c.trig})
		r := f.lastRet(t)
		if r.Mode != c.want {
			t.Errorf("off=%v %s: mode %v, want %v", c.off, c.trig, r.Mode, c.want)
		}
		if !reflect.DeepEqual(r.Clone, cl) || r.Image != "img:1" || r.Instance != "DEV" || r.DataDir != f.h.dataDir ||
			r.TempDir != filepath.Join(f.h.dataDir, "clone-fetch") || r.Home != "/home/u" || !reflect.DeepEqual(r.Protected, f.h.env.Protected) {
			t.Errorf("request = %+v", r)
		}
	}
}

func TestCloneReturnUnavailable(t *testing.T) {
	f := newRefreshFake(t)
	f.h.unavailable = "no containment layer"
	sum := f.h.ReturnWork(context.Background(), config.Clone{Of: "/r", Dir: cloneDir(t)}, session.ReturnOpts{Trigger: session.ReturnOnExit})
	if sum.Skipped != "unavailable" || sum.Trigger != session.ReturnOnExit || f.retCount() != 0 {
		t.Fatalf("summary = %+v, %d return call(s)", sum, f.retCount())
	}
}

// A lock wait that outlives the caller's deadline is a skip, not a return.
func TestCloneReturnLockWaitTimesOut(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	_, held := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.RefreshOpts{})
	defer held()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sum := f.h.ReturnWork(ctx, config.Clone{Of: "/r", Dir: dir}, session.ReturnOpts{Trigger: session.ReturnNow})
	if sum.Skipped != clone.SkipInUse || f.retCount() != 0 {
		t.Fatalf("summary = %+v, %d return call(s); want in-use and none", sum, f.retCount())
	}
}

func TestCloneReturnSummary(t *testing.T) {
	at := time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	got := returnSummaryOf(session.ReturnNow, clone.ReturnResult{
		At: at, Base: "main", Home: []string{"box/a"},
		NotHome:  []clone.NotHome{{Branch: "b", Reason: clone.ReasonNotFF, Detail: "d"}},
		Warnings: []string{"w"}, Error: "e",
	})
	want := session.ReturnSummary{
		Trigger: session.ReturnNow, At: at, Base: "main", Home: []string{"box/a"},
		NotHome:  []session.NotHomeBranch{{Branch: "b", Reason: clone.ReasonNotFF, Detail: "d"}},
		Warnings: []string{"w"}, Error: "e",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("returnSummaryOf =\n%+v\nwant\n%+v", got, want)
	}
}

func TestCloneRefreshRunsReturnFirst(t *testing.T) {
	f := newRefreshFake(t)
	f.retResult = clone.ReturnResult{Base: "main", Home: []string{"box/feat"}}
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}
	info, release := f.h.Refresh(context.Background(), c, session.RefreshOpts{LiveSession: never, ReturnBudget: time.Minute})
	release()
	if !reflect.DeepEqual(f.order, []string{"ret", "refresh"}) {
		t.Fatalf("call order = %v, want the return first", f.order)
	}
	if info.Return == nil || info.Return.Trigger != session.ReturnOnSpawn || !reflect.DeepEqual(info.Return.Home, []string{"box/feat"}) {
		t.Fatalf("info.Return = %+v", info.Return)
	}
	if r := f.lastRet(t); r.Mode != clone.ReturnAuto || r.Clone.Dir != dir {
		t.Fatalf("return request = %+v", r)
	}
	if dl, ok := f.retCtx[0].Deadline(); !ok || time.Until(dl) > time.Minute {
		t.Fatalf("the return ran without the budget as its deadline (ok=%v)", ok)
	}

	// The switch off: a dry run, so "N branches not home" still has a source.
	f.order = nil
	_, rel2 := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir, NoAutoReturn: true}, session.RefreshOpts{LiveSession: never, ReturnBudget: time.Minute})
	rel2()
	if r := f.lastRet(t); r.Mode != clone.ReturnDry {
		t.Fatalf("mode = %v, want dry with the switch off", r.Mode)
	}

	// No budget, no return.
	f.order = nil
	_, rel3 := f.h.Refresh(context.Background(), c, session.RefreshOpts{LiveSession: never})
	rel3()
	if !reflect.DeepEqual(f.order, []string{"refresh"}) {
		t.Fatalf("call order without a budget = %v", f.order)
	}
}

func TestCloneRefreshNoReturnWhenSkipped(t *testing.T) {
	budget := session.RefreshOpts{ReturnBudget: time.Minute}

	f := newRefreshFake(t)
	o := budget
	o.Created = true
	info, rel := f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: cloneDir(t)}, o)
	rel()
	if f.retCount() != 0 || info.Return != nil {
		t.Fatalf("created: %d return call(s), Return=%+v", f.retCount(), info.Return)
	}

	o = budget
	o.LiveSession = func() bool { return true }
	info, rel = f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: cloneDir(t)}, o)
	rel()
	if f.retCount() != 0 || info.Return != nil {
		t.Fatalf("in-use: %d return call(s), Return=%+v", f.retCount(), info.Return)
	}

	dir := cloneDir(t)
	f.boxes = []box.BoxInfo{{Name: "prod-box", State: "running", Workspace: dir, Instance: "PROD"}}
	o = budget
	o.LiveSession = never
	info, rel = f.h.Refresh(context.Background(), config.Clone{Of: "/r", Dir: dir}, o)
	rel()
	if f.retCount() != 0 || info.Return != nil {
		t.Fatalf("other-instance: %d return call(s), Return=%+v", f.retCount(), info.Return)
	}
}

func TestCloneHooksNotHome(t *testing.T) {
	h := testHooks(t, noClones)
	h.dataDir = t.TempDir()
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}

	if got := h.NotHome(c); got != nil {
		t.Fatalf("no state file: %+v, want nil", got)
	}

	at := time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	st := clone.ReturnState{At: at, Base: "main", NotHome: []clone.NotHome{
		{Branch: "feat", Reason: clone.ReasonNotFF, Detail: "d"},
		{Branch: "x", Reason: clone.ReasonCheckedOut},
	}}
	if err := clone.WriteReturnState(h.dataDir, dir, st); err != nil {
		t.Fatal(err)
	}
	want := &session.NotHomeState{At: at, Base: "main", Branches: []session.NotHomeBranch{
		{Branch: "feat", Reason: clone.ReasonNotFF, Detail: "d"},
		{Branch: "x", Reason: clone.ReasonCheckedOut},
	}}
	if got := h.NotHome(c); !reflect.DeepEqual(got, want) {
		t.Fatalf("NotHome = %+v, want %+v", got, want)
	}

	// Nothing to show and no error: nil. An error alone is worth showing.
	if err := clone.WriteReturnState(h.dataDir, dir, clone.ReturnState{At: at, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if got := h.NotHome(c); got != nil {
		t.Fatalf("empty not-home: %+v, want nil", got)
	}
	if err := clone.WriteReturnState(h.dataDir, dir, clone.ReturnState{At: at, Error: "listing failed"}); err != nil {
		t.Fatal(err)
	}
	if got := h.NotHome(c); got == nil || got.Error != "listing failed" || len(got.Branches) != 0 {
		t.Fatalf("error state: %+v", got)
	}

	// A state file that cannot be read is reported, not silently hidden.
	name := filepath.Join(h.dataDir, "clone-return")
	entries, _ := os.ReadDir(name)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			if err := os.WriteFile(filepath.Join(name, e.Name()), []byte("{broken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := h.NotHome(c); got == nil || got.Error == "" {
		t.Fatalf("corrupt state: %+v, want an Error", got)
	}

	// No data dir, no state.
	h.dataDir = ""
	if got := h.NotHome(c); got != nil {
		t.Fatalf("no data dir: %+v", got)
	}
}

// clone_of is host-owned when the clone is made, but a profile saved or a dir
// recorded later can make it a box's: host git must not run there on any trigger.
func TestCloneReturnRefusesBoxOwnedCloneOf(t *testing.T) {
	type setup func(t *testing.T, f *refreshFake) string // returns clone_of
	for _, tc := range []struct {
		name string
		set  setup
	}{
		{"recorded dir", func(t *testing.T, f *refreshFake) string {
			of := cloneDir(t)
			if err := f.h.record(of); err != nil {
				t.Fatal(err)
			}
			return of
		}},
		{"inside another profile's clone", func(t *testing.T, f *refreshFake) string {
			other := cloneDir(t)
			f.h.clones = func() []config.Clone { return []config.Clone{{Of: "/elsewhere", Dir: other}} }
			return filepath.Join(other, "sub")
		}},
		{"corrupt record", func(t *testing.T, f *refreshFake) string {
			if err := os.WriteFile(f.h.file, []byte("not/absolute\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return cloneDir(t)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshFake(t)
			f.h.dataDir = t.TempDir()
			dir := cloneDir(t)
			of := tc.set(t, f)
			c := config.Clone{Of: of, Dir: dir}
			for _, trig := range []string{session.ReturnOnExit, session.ReturnNow} {
				sum := f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: trig})
				if sum.Error == "" || sum.Trigger != trig || sum.At.IsZero() || len(sum.Home) != 0 {
					t.Fatalf("%s: summary = %+v; want a set Error", trig, sum)
				}
			}
			info, release := f.h.Refresh(context.Background(), c, session.RefreshOpts{LiveSession: never, ReturnBudget: time.Minute})
			release()
			if info.Return == nil || info.Return.Trigger != session.ReturnOnSpawn || info.Return.Error == "" {
				t.Fatalf("spawn-time return = %+v; want a set Error", info.Return)
			}
			if f.retCount() != 0 {
				t.Fatalf("clone.Return ran %d time(s) for a box-owned clone_of", f.retCount())
			}
			if _, ok, _ := clone.ReadReturnState(f.h.dataDir, dir); ok {
				t.Fatal("a refused return wrote a state file")
			}
			if len(f.reqs) != 1 {
				t.Fatalf("the refresh must still run after a refused spawn return: %d call(s)", len(f.reqs))
			}
		})
	}
}

// A panic in clone.Return must not leave the clone locked, from ReturnWork or
// from the return inside Refresh.
func TestCloneReturnPanicReleasesLock(t *testing.T) {
	f := newRefreshFake(t)
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}
	f.h.ret = func(context.Context, clone.Git, clone.Docker, clone.ReturnRequest) clone.ReturnResult {
		panic("boom")
	}
	expectPanic := func(fn func()) {
		t.Helper()
		defer func() {
			if p := recover(); p != "boom" {
				t.Fatalf("recovered %v, want the original panic", p)
			}
		}()
		fn()
	}
	free := func(what string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rel, ok := f.h.lockClone(ctx, dir)
		if !ok {
			t.Fatalf("%s left the clone locked", what)
		}
		rel()
	}
	expectPanic(func() {
		f.h.ReturnWork(context.Background(), c, session.ReturnOpts{Trigger: session.ReturnOnExit})
	})
	free("a panicking ReturnWork")
	expectPanic(func() {
		f.h.Refresh(context.Background(), c, session.RefreshOpts{LiveSession: never, ReturnBudget: time.Minute})
	})
	free("a panicking return inside Refresh")
}
