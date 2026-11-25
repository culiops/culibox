package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxedws"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
)

func noClones() []config.Clone { return nil }

func TestNewCloneHooks(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()

	// Without a layer: hooks are still installed (the box-owned guard needs no
	// layer), the error says why, and Prepare refuses.
	h, err := newCloneHooks(nil, home, "", data, "DEV", nil, noClones)
	if err == nil || h == nil {
		t.Fatalf("without a layer: got %v, %v; want hooks and an error", h, err)
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
	h, err = newCloneHooks(&boxLayer{dockerBin: "/usr/bin/docker", image: "img:1"}, home, "", data, "DEV", nil, noClones)
	if err != nil || h == nil || h.unavailable != "" {
		t.Fatalf("linux with a layer: got %v, %v; want ready hooks", h, err)
	}
	if d, ok := h.docker.(clone.ExecDocker); !ok || d.Bin != "/usr/bin/docker" || len(d.Args) != 0 || h.image != "img:1" || h.instance != "DEV" ||
		h.dataDir != data || h.listBoxes == nil || h.refresh == nil {
		t.Fatalf("refresh wiring: docker=%v image=%q instance=%q dataDir=%q listBoxes=%v refresh=%v",
			h.docker, h.image, h.instance, h.dataDir, h.listBoxes != nil, h.refresh != nil)
	}
}

// On macOS the clone containers (refresh, return listing) must run on the box
// VM's docker, never the user's own daemon: a darwin layer's hooks carry the
// VM's global docker flags on every call.
func TestNewCloneHooksDarwinRunsOnTheVMDocker(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	l, _, d := darwinLayer(t, false)
	h, err := newCloneHooks(l, t.TempDir(), "", t.TempDir(), "DEV", nil, noClones)
	if err != nil || h == nil || h.unavailable != "" {
		t.Fatalf("darwin with a layer: got %v, %v; want ready hooks", h, err)
	}
	if err := h.docker.Remove(context.Background(), "agent-box-clonerefresh-x"); err != nil {
		t.Fatal(err)
	}
	want := "--host unix://" + l.vm.Config().DockerSocket() + " --config " + l.vm.Config().DockerConfigDir() + " rm -f agent-box-clonerefresh-x"
	lines := strings.Split(strings.TrimSpace(d.log()), "\n")
	if got := lines[len(lines)-1]; got != want {
		t.Fatalf("clone docker call = %q, want %q", got, want)
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
	// "pick a folder outside the copy" is the New session dialog's way out; a profile save has no such dialog.
	if strings.Contains(err.Error(), "web UI") || strings.Contains(err.Error(), "outside the copy") {
		t.Fatalf("a clone_of refusal carries the New session hint: %v", err)
	}
}

// darwinCloneHooks are newCloneHooks over a darwin layer whose VM shares only
// its root (the fake home), with Prepare's clone step replaced by a recorder.
func darwinCloneHooks(t *testing.T) (h *cloneHooks, shared string, called *bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	l, _, _ := darwinLayer(t, false)
	shared = l.vm.Config().Root
	h, err := newCloneHooks(l, shared, "", t.TempDir(), "DEV", nil, noClones)
	if err != nil {
		t.Fatal(err)
	}
	// The VM mounts only the data-root subfolders, so pin the share list this
	// test is about.
	h.shares = func() (box.ShareView, error) {
		return box.ShareView{Applied: []box.VMShare{{Path: shared, Writable: true}}, Home: shared, Fold: true}, nil
	}
	called = new(bool)
	h.prepare = func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) {
		*called = true
		return box.CloneMounts{}, false, nil
	}
	return h, shared, called
}

// gitRepo makes dir a directory holding an empty .git directory, enough for
// clone.CheckProfile.
func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The box VM sees only the directories it shares. A clone_of or clone dir
// outside them is refused before anything is recorded or created, at spawn
// and at save, naming the field and the shares.
func TestCloneRefusesPathsTheVMDoesNotShare(t *testing.T) {
	h, shared, called := darwinCloneHooks(t)
	outside := t.TempDir()
	link := filepath.Join(shared, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	gitRepo(t, filepath.Join(outside, "repo"))
	in := gitRepo(t, filepath.Join(shared, "repo"))
	for _, c := range []struct {
		name  string
		clone config.Clone
		field string
	}{
		{"clone_of outside", config.Clone{Of: filepath.Join(outside, "repo"), Dir: filepath.Join(shared, "c1")}, "clone_of"},
		{"clone_of through a link out", config.Clone{Of: filepath.Join(link, "repo"), Dir: filepath.Join(shared, "c2")}, "clone_of"},
		{"clone dir outside", config.Clone{Of: in, Dir: filepath.Join(outside, "c3")}, "default_cwd"},
	} {
		_, _, err := h.Prepare(context.Background(), c.clone, false)
		var ns *box.NotSharedError
		if !errors.Is(err, box.ErrUnboxableWorkspace) || !errors.As(err, &ns) {
			t.Errorf("%s: Prepare = %v; want a *NotSharedError", c.name, err)
		}
		if *called {
			t.Errorf("%s: the clone was made", c.name)
		}
		if _, owned := h.BoxOwned(c.clone.Dir); owned {
			t.Errorf("%s: a refused clone dir was recorded", c.name)
		}
		if err := cloneSaveCheck(h, shared, nil)(c.clone); err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Errorf("%s: save check = %v; want a refusal", c.name, err)
		}
	}

	ok := config.Clone{Of: in, Dir: filepath.Join(shared, "copy")}
	if err := cloneSaveCheck(h, shared, nil)(ok); err != nil {
		t.Fatalf("shared paths: save check = %v", err)
	}
	if _, _, err := h.Prepare(context.Background(), ok, false); err != nil || !*called {
		t.Fatalf("shared paths: Prepare = %v, called = %v", err, *called)
	}
}

// Without a VM (Linux) docker sees the host's own paths: nothing is refused for
// where it lives.
func TestCloneWithoutAVMHasNoShareCheck(t *testing.T) {
	home := t.TempDir()
	h := testHooks(t, noClones)
	c := config.Clone{Of: gitRepo(t, filepath.Join(t.TempDir(), "repo")), Dir: filepath.Join(t.TempDir(), "copy")}
	if err := cloneSaveCheck(h, home, nil)(c); err != nil {
		t.Fatalf("save check = %v", err)
	}
	if _, _, err := h.Prepare(context.Background(), c, false); err != nil {
		t.Fatalf("Prepare = %v", err)
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
	if strings.Contains(err.Error(), "web UI") {
		t.Fatalf("a context_paths refusal carries the New session hint: %v", err)
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
	// without a copy shows up here as zero. PushRepo is the one exception: the
	// session sets it at spawn from the GitHub binding, not from a refresh.
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).Name == "PushRepo" {
			continue
		}
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

// The boot pass sweeps leftover clone containers wherever docker is reachable:
// on Linux, and on macOS through the VM's docker while the VM runs. A stopped
// VM is left alone: no container runs in it, and docker is not reachable.
func TestBootSweepsCloneContainers(t *testing.T) {
	sweep := "ps -a --filter label=" + clone.LabelCloneFetch + "=DEV"

	lin := newFakeDocker(t)
	ll, err := newBoxLayer(config.Config{DockerBin: lin.bin}, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bootReconcileAndSweep(context.Background(), ll, "DEV")
	if !strings.Contains(lin.log(), sweep+" ") {
		t.Errorf("linux: no clone sweep on the host docker:\n%s", lin.log())
	}

	l, fc, d := darwinLayer(t, false)
	fc.running = true
	bootReconcileAndSweep(context.Background(), l, "DEV")
	vm := "--host unix://" + l.vm.Config().DockerSocket() + " --config " + l.vm.Config().DockerConfigDir() + " "
	if !strings.Contains(d.log(), vm+sweep+" ") {
		t.Errorf("darwin, VM running: no clone sweep on the VM's docker:\n%s", d.log())
	}

	l, fc, d = darwinLayer(t, false)
	bootReconcileAndSweep(context.Background(), l, "DEV")
	if strings.Contains(d.log(), " ps ") || fc.called("start") {
		t.Errorf("darwin, VM stopped: the boot pass reached docker or started the VM:\n%s", d.log())
	}
}

func TestCloneUnavailableReason(t *testing.T) {
	for _, c := range []struct {
		name string
		h    *cloneHooks
		want string
	}{
		{"no hooks", nil, "clone mode is not set up on this instance"},
		{"no layer", &cloneHooks{unavailable: "no containment layer"}, "no containment layer"},
		{"available", &cloneHooks{}, ""},
	} {
		if got := cloneUnavailableReason(c.h); got != c.want {
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

func TestReturnWorkLiveSkipMatrix(t *testing.T) {
	for _, c := range []struct {
		trig string
		skip bool
	}{
		{session.ReturnOnExit, true},
		{session.ReturnOnIdle, false},
		{session.ReturnOnHost, false},
		{session.ReturnNow, false},
	} {
		t.Run(c.trig, func(t *testing.T) {
			f := newRefreshFake(t)
			cl := config.Clone{Of: "/r", Dir: cloneDir(t)}
			sum := f.h.ReturnWork(context.Background(), cl, session.ReturnOpts{Trigger: c.trig, LiveSession: func() bool { return true }})
			if sum.Trigger != c.trig {
				t.Fatalf("trigger %q, want %q", sum.Trigger, c.trig)
			}
			if c.skip {
				if sum.Skipped != clone.SkipInUse || f.retCount() != 0 {
					t.Fatalf("%+v, %d return call(s); want in-use and none", sum, f.retCount())
				}
				return
			}
			if sum.Skipped != "" || f.retCount() != 1 {
				t.Fatalf("%+v, %d return call(s); want one run", sum, f.retCount())
			}
		})
	}
}

// Whatever the trigger, a box of another instance still skips the return.
func TestReturnWorkOtherInstanceSkipsEveryTrigger(t *testing.T) {
	for _, trig := range []string{session.ReturnOnIdle, session.ReturnOnHost} {
		f := newRefreshFake(t)
		dir := cloneDir(t)
		f.boxes = []box.BoxInfo{{Name: "prod-box", State: "running", Workspace: dir, Instance: "PROD"}}
		sum := f.h.ReturnWork(context.Background(), config.Clone{Of: "/r", Dir: dir}, session.ReturnOpts{Trigger: trig, LiveSession: never})
		if sum.Skipped != clone.SkipOtherInstance || f.retCount() != 0 {
			t.Fatalf("%s: %+v, %d return call(s); want other-instance and none", trig, sum, f.retCount())
		}
	}
}

func TestReturnModeOf(t *testing.T) {
	for _, c := range []struct {
		trig string
		off  bool
		want clone.ReturnMode
	}{
		{session.ReturnOnExit, false, clone.ReturnAuto},
		{session.ReturnOnSpawn, false, clone.ReturnAuto},
		{session.ReturnOnIdle, false, clone.ReturnAuto},
		{session.ReturnOnHost, false, clone.ReturnAuto},
		{session.ReturnOnExit, true, clone.ReturnDry},
		{session.ReturnOnSpawn, true, clone.ReturnDry},
		{session.ReturnOnIdle, true, clone.ReturnDry},
		{session.ReturnOnHost, true, clone.ReturnDry},
		{session.ReturnNow, false, clone.ReturnForced},
		{session.ReturnNow, true, clone.ReturnForced},
	} {
		if got := returnModeOf(config.Clone{NoAutoReturn: c.off}, c.trig); got != c.want {
			t.Errorf("%s off=%v: mode %v, want %v", c.trig, c.off, got, c.want)
		}
	}
}

func TestReturnSummaryOfUncommitted(t *testing.T) {
	three := 3
	got := returnSummaryOf(session.ReturnOnIdle, "/r", nil, clone.ReturnResult{Uncommitted: &three, UncommittedCut: true})
	if got.Uncommitted == nil || *got.Uncommitted != 3 || !got.UncommittedCut {
		t.Fatalf("summary = %+v, want uncommitted 3 and cut", got)
	}
	// The summary owns its count: changing the result's afterwards must not reach it.
	three = 9
	if *got.Uncommitted != 3 {
		t.Fatalf("summary shares the result's pointer: %d", *got.Uncommitted)
	}
	// Unknown stays unknown, never zero.
	got = returnSummaryOf(session.ReturnOnIdle, "/r", nil, clone.ReturnResult{})
	if got.Uncommitted != nil || got.UncommittedCut {
		t.Fatalf("summary = %+v, want an unknown count", got)
	}
	zero := 0
	got = returnSummaryOf(session.ReturnOnIdle, "/r", nil, clone.ReturnResult{Uncommitted: &zero})
	if got.Uncommitted == nil || *got.Uncommitted != 0 {
		t.Fatalf("summary = %+v, want a known zero", got)
	}
}

func TestReturnSummaryCarriesProfile(t *testing.T) {
	f := newRefreshFake(t)
	cl := config.Clone{Of: "/r", Dir: cloneDir(t), Profile: "dev"}
	if sum := f.h.ReturnWork(context.Background(), cl, session.ReturnOpts{Trigger: session.ReturnOnIdle}); sum.Profile != "dev" {
		t.Fatalf("ran: profile %q, want dev", sum.Profile)
	}
	f.h.unavailable = "no layer"
	if sum := f.h.ReturnWork(context.Background(), cl, session.ReturnOpts{Trigger: session.ReturnOnIdle}); sum.Profile != "dev" {
		t.Fatalf("skipped: profile %q, want dev", sum.Profile)
	}
}

func TestNotHomeUncommittedOnly(t *testing.T) {
	h := testHooks(t, noClones)
	h.dataDir = t.TempDir()
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	two := 2
	if err := clone.WriteReturnState(h.dataDir, dir, clone.ReturnState{At: at, Base: "main", Uncommitted: &two, UncommittedCut: true}); err != nil {
		t.Fatal(err)
	}
	got := h.NotHome(c)
	if got == nil || got.Uncommitted == nil || *got.Uncommitted != 2 || !got.UncommittedCut || len(got.Branches) != 0 || got.Error != "" {
		t.Fatalf("NotHome = %+v, want a state with 2 uncommitted (cut) and nothing else", got)
	}
	// Zero and unknown show nothing.
	zero := 0
	for _, u := range []*int{&zero, nil} {
		if err := clone.WriteReturnState(h.dataDir, dir, clone.ReturnState{At: at, Base: "main", Uncommitted: u}); err != nil {
			t.Fatal(err)
		}
		if got := h.NotHome(c); got != nil {
			t.Fatalf("uncommitted %v: NotHome = %+v, want nil", u, got)
		}
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
		{false, session.ReturnOnIdle, clone.ReturnAuto},
		{true, session.ReturnOnHost, clone.ReturnDry},
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
	got := returnSummaryOf(session.ReturnNow, "/r", nil, clone.ReturnResult{
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

// checkShared: the clone_of is only read (a read-only share suffices), the copy
// is written, and a share not applied yet says so.
func TestCheckSharedClone(t *testing.T) {
	base := resolvedTemp(t)
	of, dir := filepath.Join(base, "ro", "repo"), filepath.Join(base, "rw", "copy")
	for _, d := range []string{of, filepath.Dir(dir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	view := box.ShareView{
		Applied: []box.VMShare{{Path: filepath.Join(base, "ro")}, {Path: filepath.Join(base, "rw"), Writable: true}},
		Listed:  []box.VMShare{{Path: filepath.Join(base, "pending"), Writable: true}},
		Home:    base,
	}
	h := &cloneHooks{shares: func() (box.ShareView, error) { return view, nil }}
	if err := h.checkShared(config.Clone{Of: of, Dir: dir}); err != nil {
		t.Fatalf("clone_of under a read-only share and the copy under a read-write one: %v", err)
	}
	var ns *box.NotSharedError
	err := h.checkShared(config.Clone{Of: of, Dir: filepath.Join(base, "ro", "copy")})
	if !errors.As(err, &ns) || !ns.ReadOnly {
		t.Errorf("the copy under a read-only share: want ReadOnly, got %v", err)
	}
	err = h.checkShared(config.Clone{Of: of, Dir: filepath.Join(base, "pending", "copy")})
	if !errors.As(err, &ns) || !ns.Pending {
		t.Errorf("a pending copy dir: want Pending, got %v", err)
	}
	h.shares = func() (box.ShareView, error) { return box.ShareView{}, errors.New("boom") }
	if err := h.checkShared(config.Clone{Of: of, Dir: dir}); !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Errorf("an unreadable share list must fail closed, got %v", err)
	}
	h.shares = nil
	if err := h.checkShared(config.Clone{Of: of, Dir: dir}); err != nil {
		t.Errorf("no VM: nothing is refused, got %v", err)
	}
}

// C2: an unshared clone_of is offered as a read-only share (rw would let an
// escape plant .git/hooks); an unshared copy as a read-write one.
func TestCheckSharedCloneHintModes(t *testing.T) {
	base := resolvedTemp(t)
	of, dir := filepath.Join(base, "orig"), filepath.Join(base, "copies", "c")
	h := &cloneHooks{shares: func() (box.ShareView, error) {
		return box.ShareView{Applied: []box.VMShare{{Path: filepath.Join(base, "copies"), Writable: true}}, Home: base}, nil
	}}
	err := h.checkShared(config.Clone{Of: of, Dir: dir})
	var ns *box.NotSharedError
	if !errors.As(err, &ns) || ns.NeedWrite || !strings.HasSuffix(err.Error(), "`culibox box share add ~/orig -ro`") {
		t.Errorf("clone_of: %v", err)
	}
	h.shares = func() (box.ShareView, error) {
		return box.ShareView{Applied: []box.VMShare{{Path: of}}, Home: base}, nil
	}
	err = h.checkShared(config.Clone{Of: of, Dir: dir})
	if !errors.As(err, &ns) || !ns.NeedWrite || !strings.HasSuffix(err.Error(), "`culibox box share add ~/copies/c`") {
		t.Errorf("copy: %v", err)
	}
}

func resolvedTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ReturnForRepo runs the host-trigger return for each clone whose clone_of is
// the folder or holds it, by whole path components and with symlinks resolved;
// a profile-less (fallback) entry is never returned.
func TestReturnForRepoMatching(t *testing.T) {
	base := resolvedTemp(t)
	of, sibling := filepath.Join(base, "b"), filepath.Join(base, "bc")
	inside := filepath.Join(of, "sub", "deep")
	for _, d := range []string{inside, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(of, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		cwd   string
		of    string
		prof  string
		wantN int
	}{
		{"same folder", of, of, "p", 1},
		{"folder inside the repo", inside, of, "p", 1},
		{"sibling with a shared prefix", sibling, of, "p", 0},
		{"the parent of the repo", base, of, "p", 0},
		{"clone_of reached through a symlink", inside, link, "p", 1},
		{"cwd reached through a symlink", filepath.Join(link, "sub"), of, "p", 1},
		{"a profile-less fallback entry", of, of, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshFake(t)
			dir := cloneDir(t)
			f.h.clones = func() []config.Clone { return []config.Clone{{Of: tc.of, Dir: dir, Profile: tc.prof}} }
			sums := f.h.ReturnForRepo(context.Background(), tc.cwd)
			if len(sums) != tc.wantN || f.retCount() != tc.wantN {
				t.Fatalf("%d summaries, %d returns, want %d", len(sums), f.retCount(), tc.wantN)
			}
			if tc.wantN == 1 && (sums[0].Trigger != session.ReturnOnHost || sums[0].Profile != tc.prof) {
				t.Fatalf("summary = %+v, want trigger host and profile %q", sums[0], tc.prof)
			}
		})
	}
}

// Clones of the repo come back in Dir order, each with its own profile.
func TestReturnForRepoDirOrder(t *testing.T) {
	f := newRefreshFake(t)
	of := resolvedTemp(t)
	a, b := cloneDir(t), cloneDir(t)
	if a > b {
		a, b = b, a
	}
	f.h.clones = func() []config.Clone {
		return []config.Clone{{Of: of, Dir: b, Profile: "pb"}, {Of: of, Dir: a, Profile: "pa"}}
	}
	sums := f.h.ReturnForRepo(context.Background(), of)
	if len(sums) != 2 || sums[0].Profile != "pa" || sums[1].Profile != "pb" {
		t.Fatalf("summaries = %+v, want pa then pb", sums)
	}
}

// The ctx is one budget. The clone running when it ends reports whatever its
// return gave; the clones after it are "deferred" without running, and each
// clone the budget cut short continues in the background under the host trigger.
func TestReturnForRepoBudget(t *testing.T) {
	f := newRefreshFake(t)
	of := resolvedTemp(t)
	d1, d2, d3 := cloneDir(t), cloneDir(t), cloneDir(t)
	f.h.clones = func() []config.Clone {
		return []config.Clone{{Of: of, Dir: d1, Profile: "p1"}, {Of: of, Dir: d2, Profile: "p2"}, {Of: of, Dir: d3, Profile: "p3"}}
	}
	var first atomic.Bool
	bg := make(chan clone.ReturnRequest, 4)
	f.h.ret = func(ctx context.Context, _ clone.Git, _ clone.Docker, r clone.ReturnRequest) clone.ReturnResult {
		if r.Clone.Dir == d1 && first.CompareAndSwap(false, true) {
			<-ctx.Done() // the foreground run of the first clone outlives the budget
			return clone.ReturnResult{NotHome: []clone.NotHome{{Branch: "box/x", Reason: clone.ReasonDeferred}}}
		}
		bg <- r
		return clone.ReturnResult{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sums := f.h.ReturnForRepo(ctx, of)
	if len(sums) != 3 {
		t.Fatalf("%d summaries, want 3", len(sums))
	}
	if len(sums[0].NotHome) != 1 || sums[0].NotHome[0].Reason != clone.ReasonDeferred || sums[0].Skipped != "" {
		t.Fatalf("the clone running at the deadline: %+v", sums[0])
	}
	for i, p := range []string{"p2", "p3"} {
		s := sums[i+1]
		if s.Skipped != clone.ReasonDeferred || s.Trigger != session.ReturnOnHost || s.Profile != p {
			t.Fatalf("summary %d = %+v, want host/deferred for %s", i+1, s, p)
		}
	}
	// Background: all three clones continue, host trigger, a fresh long budget.
	seen := map[string]bool{}
	for range 3 {
		select {
		case r := <-bg:
			seen[r.Clone.Profile] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("the background return did not run for every clone: %v", seen)
		}
	}
	if !seen["p1"] || !seen["p2"] || !seen["p3"] {
		t.Fatalf("background ran for %v, want all three", seen)
	}
}

// A clone whose return finished in budget is not continued; neither is a
// summary that only failed.
func TestReturnForRepoNoBackgroundWhenDone(t *testing.T) {
	f := newRefreshFake(t)
	of, dir := resolvedTemp(t), cloneDir(t)
	f.h.clones = func() []config.Clone { return []config.Clone{{Of: of, Dir: dir, Profile: "p"}} }
	f.retResult = clone.ReturnResult{Home: []string{"box/a"}, NotHome: []clone.NotHome{{Branch: "box/b", Reason: clone.ReasonNotFF}}}
	sums := f.h.ReturnForRepo(context.Background(), of)
	time.Sleep(50 * time.Millisecond)
	if len(sums) != 1 || f.retCount() != 1 {
		t.Fatalf("%d summaries, %d returns, want 1 and 1 (no background run)", len(sums), f.retCount())
	}
}

// Review focus 3: with docker down (the box listing fails) the host spawn is not
// blocked and the summary says the work was not checked.
func TestReturnForRepoDockerDownDoesNotBlock(t *testing.T) {
	f := newRefreshFake(t)
	of, dir := resolvedTemp(t), cloneDir(t)
	f.h.clones = func() []config.Clone { return []config.Clone{{Of: of, Dir: dir, Profile: "p"}} }
	f.err = errors.New("cannot connect to the docker daemon")
	start := time.Now()
	sums := f.h.ReturnForRepo(context.Background(), of)
	if time.Since(start) > 2*time.Second {
		t.Fatal("a failing box listing held the spawn")
	}
	if len(sums) != 1 || sums[0].Skipped == "" || sums[0].Error == "" || f.retCount() != 0 {
		t.Fatalf("summary = %+v (returns %d), want a skip that carries the listing error and no return run", sums, f.retCount())
	}
}

// I3: a listing or host check cut by the budget (an Error) and a lock wait cut
// by it (skipped in-use) both leave work unchecked, so each continues in the
// background like a deferred one.
func TestReturnForRepoContinuesWorkCutByTheBudget(t *testing.T) {
	t.Run("a check cut by the deadline carries an error", func(t *testing.T) {
		f := newRefreshFake(t)
		of, dir := resolvedTemp(t), cloneDir(t)
		f.h.clones = func() []config.Clone { return []config.Clone{{Of: of, Dir: dir, Profile: "p"}} }
		bg := make(chan struct{}, 4)
		var first atomic.Bool
		f.h.ret = func(ctx context.Context, _ clone.Git, _ clone.Docker, _ clone.ReturnRequest) clone.ReturnResult {
			if first.CompareAndSwap(false, true) {
				<-ctx.Done()
				return clone.ReturnResult{Error: "timed out"}
			}
			bg <- struct{}{}
			return clone.ReturnResult{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		sums := f.h.ReturnForRepo(ctx, of)
		if len(sums) != 1 || sums[0].Error == "" {
			t.Fatalf("summaries = %+v, want the clone's own error kept", sums)
		}
		select {
		case <-bg:
		case <-time.After(5 * time.Second):
			t.Fatal("a return cut by the deadline was not continued in the background")
		}
	})
	t.Run("a lock wait cut by the deadline is in-use", func(t *testing.T) {
		f := newRefreshFake(t)
		of, dir := resolvedTemp(t), cloneDir(t)
		f.h.clones = func() []config.Clone { return []config.Clone{{Of: of, Dir: dir, Profile: "p"}} }
		release, ok := f.h.lockClone(context.Background(), cloneKey(dir))
		if !ok {
			t.Fatal("could not take the clone lock")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		sums := f.h.ReturnForRepo(ctx, of)
		if len(sums) != 1 || sums[0].Skipped != clone.SkipInUse || f.retCount() != 0 {
			t.Fatalf("summaries = %+v (returns %d), want skipped in-use and no return yet", sums, f.retCount())
		}
		release() // the background continuation waits on the same lock
		deadline := time.After(5 * time.Second)
		for f.retCount() == 0 {
			select {
			case <-deadline:
				t.Fatal("a lock wait cut by the deadline was not continued in the background")
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
}

// I5: a profile whose copy was never made (no box spawned yet) has nothing to
// return: no summary, no return, nothing recorded.
func TestReturnForRepoSkipsACopyThatWasNeverMade(t *testing.T) {
	base := resolvedTemp(t)
	of := filepath.Join(base, "repo")
	missing := filepath.Join(base, "never-made")
	notDir := filepath.Join(base, "afile")
	for _, d := range []string{of} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newRefreshFake(t)
	f.h.clones = func() []config.Clone {
		return []config.Clone{{Of: of, Dir: missing, Profile: "a"}, {Of: of, Dir: notDir, Profile: "b"}}
	}
	sums := f.h.ReturnForRepo(context.Background(), of)
	if len(sums) != 0 || f.retCount() != 0 {
		t.Fatalf("%d summaries, %d returns, want none for a copy that does not exist", len(sums), f.retCount())
	}
	f.mu.Lock()
	lists := f.lists
	f.mu.Unlock()
	if lists != 0 {
		t.Fatalf("docker was asked %d times for a copy that does not exist", lists)
	}
}

// inPlaceRegistry is a boxed-workspace record in a fresh data dir.
func inPlaceRegistry(t *testing.T) (*boxedws.Registry, string) {
	t.Helper()
	data := t.TempDir()
	reg, _, err := boxedws.Open(data, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	return reg, data
}

// A folder boxed in place without git is never a clone source (#371): its .git,
// if any, was written by a box. Every path that runs host git against clone_of
// goes through checkOf, so each refuses it; an entry with no flag (recorded
// before the flag existed) is allowed here (the save asks for an ack instead).
func TestCheckOfRefusesFolderBoxedInPlaceWithoutGit(t *testing.T) {
	home := t.TempDir()
	nongit := filepath.Join(home, "plain")
	legacy := filepath.Join(home, "legacy")
	free := filepath.Join(home, "free")
	nested := filepath.Join(nongit, "sub", "repo")
	for _, d := range []string{nongit, legacy, free, nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reg, data := inPlaceRegistry(t)
	if err := reg.RecordInPlace(nongit, false); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(legacy); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, noClones)
	h.boxed = reg
	h.dataDir = data

	for _, of := range []string{nongit, nested} {
		err := h.checkOf(of)
		if !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Fatalf("checkOf(%s) = %v, want ErrUnboxableWorkspace", of, err)
		}
		// The release names the entry that matched (the folder boxed in place,
		// not clone_of) and its git_guarded flag; never "remove the entry",
		// which would also drop the folder's file-API containment (#371 C2).
		for _, want := range []string{"boxed in place when it was not a git repo", "may be box-written",
			filepath.Join(data, "boxed-workspaces.json"), fmt.Sprintf("set %q to true under \"git_guarded\"", nongit), "stop CuliBox"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("checkOf(%s) = %q, missing %q", of, err, want)
			}
		}
		for _, bad := range []string{"remove", "delete"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("checkOf(%s) = %q tells the user to %s the entry", of, err, bad)
			}
		}
	}
	if err := h.checkOf(nested); !strings.Contains(err.Error(), "it is inside "+nongit) {
		t.Errorf("checkOf(%s) = %q, want it to say which recorded folder it is inside", nested, err)
	}
	for _, of := range []string{legacy, free} {
		if err := h.checkOf(of); err != nil {
			t.Errorf("checkOf(%s) = %v, want nil (unknown and none pass here)", of, err)
		}
	}

	// Prepare, HostOrigin, the save check and a return all refuse it.
	ran := false
	h.prepare = func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) {
		ran = true
		return box.CloneMounts{}, false, nil
	}
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: nongit, Dir: filepath.Join(home, "copy")}, false); !errors.Is(err, box.ErrUnboxableWorkspace) || ran {
		t.Fatalf("Prepare: err=%v ran=%v", err, ran)
	}
	if _, _, err := h.Prepare(context.Background(), config.Clone{Of: legacy, Dir: filepath.Join(home, "copy")}, false); err != nil || !ran {
		t.Fatalf("Prepare of an unknown entry: err=%v ran=%v", err, ran)
	}
	if err := cloneSaveCheck(h, home, nil)(config.Clone{Of: nongit, Dir: filepath.Join(home, "copy2")}); !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("save check: %v", err)
	}
	if g, err := clone.NewExecGit(); err == nil {
		h.env.Git = g
		if _, err := h.HostOrigin(context.Background(), nongit); !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Fatalf("HostOrigin: %v", err)
		}
	}
	retRan := false
	h.ret = func(context.Context, clone.Git, clone.Docker, clone.ReturnRequest) clone.ReturnResult {
		retRan = true
		return clone.ReturnResult{}
	}
	s := h.doReturn(context.Background(), config.Clone{Of: nongit, Dir: filepath.Join(home, "copy"), Profile: "p"}, session.ReturnNow, clone.ReturnForced)
	if retRan || !strings.Contains(s.Error, "boxed in place") {
		t.Fatalf("doReturn ran=%v error=%q", retRan, s.Error)
	}
}

func TestBoxedRecorderFlagsInPlaceSpawnsOnly(t *testing.T) {
	data := t.TempDir()
	reg, _, err := boxedws.Open(data, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	inPlace, copyDir := t.TempDir(), t.TempDir()
	rec := boxedRecorder(reg, func() []config.Clone {
		return []config.Clone{{Of: "/elsewhere", Dir: copyDir, Profile: "p"}}
	})
	if err := rec(inPlace, ""); err != nil {
		t.Fatal(err)
	}
	if err := rec(copyDir, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := reg.InPlaceStatus(inPlace); got != boxedws.StatusNonGit {
		t.Errorf("in place spawn: status %q, want nongit", got)
	}
	if got, _ := reg.InPlaceStatus(copyDir); got != boxedws.StatusUnknown {
		t.Errorf("clone dir spawn: status %q, want unknown (no flag)", got)
	}
	if got := reg.List(); len(got) != 2 {
		t.Errorf("both are recorded: %v", got)
	}

	// A spawn in a private copy the profiles do not name (a recorded old clone
	// dir, a subfolder of one) is a copy too: the copy's directory is recorded,
	// with no in-place flag, and the cwd is not recorded as non-git.
	other, sub := t.TempDir(), ""
	sub = filepath.Join(other, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rec(sub, other); err != nil {
		t.Fatal(err)
	}
	if got, _ := reg.InPlaceStatus(other); got != boxedws.StatusUnknown {
		t.Errorf("a spawn in a copy: status %q, want unknown (no flag)", got)
	}
	if got, _ := reg.InPlaceStatus(sub); got == boxedws.StatusNonGit {
		t.Errorf("a spawn in a copy must not be recorded non-git: %q", got)
	}
}

// gitMustNotRun fails the test if the host git is asked to run.
type gitMustNotRun struct{ t *testing.T }

func (g gitMustNotRun) Run(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
	g.t.Errorf("host git ran: %v", args)
	return nil, errors.New("git must not run")
}

// The estimate endpoint's hook runs checkOf before any git or walk: a repo a
// box owns gets the refusal and git never runs there.
func TestEstimateCopyRefusesBoxOwnedBeforeAnyGit(t *testing.T) {
	home := t.TempDir()
	otherClone := filepath.Join(home, "work", "other-clone")
	if err := os.MkdirAll(filepath.Join(otherClone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, func() []config.Clone {
		return []config.Clone{{Of: filepath.Join(home, "repo"), Dir: otherClone}}
	})
	h.env = clone.Env{Git: gitMustNotRun{t}, Home: home}
	_, err := h.EstimateCopy(context.Background(), otherClone, filepath.Join(home, "box-clones", "x"))
	if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "is box-owned") {
		t.Fatalf("EstimateCopy of a box-owned repo = %v, want the box-owned refusal", err)
	}
	for _, bad := range [][2]string{{"relative", "/abs"}, {"/abs", "relative"}} {
		if _, err := h.EstimateCopy(context.Background(), bad[0], bad[1]); !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Errorf("EstimateCopy(%q, %q) = %v, want ErrUnboxableWorkspace", bad[0], bad[1], err)
		}
	}
	h.unavailable = "no git"
	if _, err := h.EstimateCopy(context.Background(), filepath.Join(home, "repo"), filepath.Join(home, "c")); !errors.Is(err, box.ErrBoxUnavailable) {
		t.Errorf("an unavailable clone mode = %v, want ErrBoxUnavailable", err)
	}
}

// Host git runs in of only when no box ever ran there: a folder with an
// unknown in-place history passes checkOf (the save asks about it), and the
// dialog's estimate comes before that answer, so it gets the .git walk alone,
// partial. Prepare's first-copy check (env.Estimate) is the same (#371 S1).
func TestCopyEstimateRunsGitOnlyWhereNoBoxRan(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "legacy")
	if err := os.MkdirAll(filepath.Join(legacy, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, _ := inPlaceRegistry(t)
	if err := reg.Add(legacy); err != nil { // unknown: recorded before the flag
		t.Fatal(err)
	}
	h := testHooks(t, noClones)
	h.boxed = reg
	h.env = clone.Env{Git: gitMustNotRun{t}, Home: home}
	e, err := h.EstimateCopy(context.Background(), legacy, filepath.Join(home, "box-clones", "x"))
	if err != nil || !e.Partial || e.TreeBytes != 0 || e.GitBytes <= 0 {
		t.Fatalf("EstimateCopy of an unknown folder = %+v, %v; want partial, tree 0, git > 0, and no git run", e, err)
	}
	h.env.Estimate = h.copyEstimate
	if e, err := h.env.Estimate(context.Background(), legacy, filepath.Join(home, "box-clones", "y")); err != nil || !e.Partial {
		t.Fatalf("Prepare's estimate of an unknown folder = %+v, %v; want partial and no git run", e, err)
	}
}

func TestEstimateCopyOfAHostRepo(t *testing.T) {
	g, err := clone.NewExecGit()
	if err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	of := filepath.Join(home, "repo")
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(clone.CleanEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	if err := os.WriteFile(filepath.Join(of, "f"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "f"}, {"commit", "-q", "-m", "x"}} {
		if _, err := g.Run(context.Background(), of, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	h := testHooks(t, noClones)
	h.env = clone.Env{Git: g, Home: home}
	e, err := h.EstimateCopy(context.Background(), of, filepath.Join(home, "box-clones", "x"))
	if err != nil || e.TreeBytes != 5 || e.GitBytes <= 0 || !e.Enough {
		t.Fatalf("EstimateCopy = %+v, %v; want tree 5, git > 0, enough", e, err)
	}
}

// CopyDirOf answers only on a positive match of an already resolved path (#371).
func TestCloneHooksCopyDirOf(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cloneDir := filepath.Join(home, "box-clones", "work")
	hostRepo := filepath.Join(home, "code", "hostrepo")
	for _, d := range []string{filepath.Join(cloneDir, "sub"), hostRepo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := testHooks(t, func() []config.Clone { return []config.Clone{{Of: hostRepo, Dir: cloneDir}} })

	if dir, ok := h.CopyDirOf(cloneDir); !ok || dir != cloneDir {
		t.Fatalf("the clone dir: %q, %v", dir, ok)
	}
	if dir, ok := h.CopyDirOf(filepath.Join(cloneDir, "sub")); !ok || dir != cloneDir {
		t.Fatalf("below the clone dir: %q, %v", dir, ok)
	}
	// A box plants <clone>/src -> a host repo. The spawn resolves the path first,
	// so the answer is asked about the host repo, which is not a copy.
	if err := os.Symlink(hostRepo, filepath.Join(cloneDir, "src")); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(cloneDir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if dir, ok := h.CopyDirOf(real); ok {
		t.Fatalf("a host repo reached through a planted link is a copy: %q", dir)
	}
	// A sibling that only shares the name prefix is not inside.
	if _, ok := h.CopyDirOf(cloneDir + "-other"); ok {
		t.Fatal("a sibling with a common prefix matched")
	}
	// A relative or unclean path never matches.
	if _, ok := h.CopyDirOf("work/sub"); ok {
		t.Fatal("a relative path matched")
	}
}

// A clone dir never has a link in its path, so a candidate that resolves to
// anything but itself is skipped, not followed: a box can rewrite a clone dir
// nested in another clone's tree to point at a host folder. One that cannot be
// resolved is skipped too.
func TestCloneHooksCopyDirOfSkipsLinkedAndUnresolvable(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	target := filepath.Join(home, "real-clone")
	hostRepo := filepath.Join(home, "code", "hostrepo")
	for _, d := range []string{target, hostRepo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(home, "clone-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(home, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	// An outer clone holds a nested clone dir, which its box replaced by a link
	// to a host repo.
	outer := filepath.Join(home, "outer-clone")
	if err := os.MkdirAll(outer, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(outer, "inner")
	if err := os.Symlink(hostRepo, nested); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, func() []config.Clone {
		return []config.Clone{{Dir: loop}, {Dir: filepath.Join(home, "missing")}, {Dir: link}, {Dir: nested}}
	})
	for name, real := range map[string]string{
		"a linked clone dir":        target,
		"the nested replaced dir":   hostRepo,
		"a dir that does not exist": filepath.Join(home, "missing", "x"),
		"a link loop":               loop,
	} {
		if dir, ok := h.CopyDirOf(real); ok {
			t.Errorf("%s: matched %q", name, dir)
		}
	}
	// The outer clone itself, spelled plainly, still matches.
	h2 := testHooks(t, func() []config.Clone { return []config.Clone{{Dir: nested}, {Dir: outer}} })
	if dir, ok := h2.CopyDirOf(outer); !ok || dir != outer {
		t.Errorf("the plain outer clone: %q, %v", dir, ok)
	}
	if _, ok := h2.CopyDirOf(hostRepo); ok {
		t.Error("the host repo matched through the replaced nested dir")
	}
}

// The clone-dirs record counts when it is readable, and contributes nothing when
// it is not: unlike BoxOwned, which then reports everything owned.
func TestCloneHooksCopyDirOfRecord(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	dir := filepath.Join(home, "old-clone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHooks(t, noClones)
	if err := os.WriteFile(h.file, []byte(dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.CopyDirOf(dir); !ok || got != dir {
		t.Fatalf("a recorded dir: %q, %v", got, ok)
	}
	if err := os.WriteFile(h.file, []byte("not/absolute\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.CopyDirOf(dir); ok {
		t.Fatal("a corrupt record matched")
	}
	if _, owned := h.BoxOwned(dir); !owned {
		t.Fatal("precondition: BoxOwned fails closed on the same record")
	}
}

// lazyGit is the host git with lazy fetching left on, as a git that predates
// GIT_NO_LAZY_FETCH (or ignores it) runs: =0 also keeps git upload-pack, which a
// local clone runs in the source repo, from turning it off by itself.
type lazyGit struct{ g clone.ExecGit }

func lazyOn(env []string) []string {
	out := append([]string(nil), env...)
	for i, kv := range out {
		if strings.HasPrefix(kv, "GIT_NO_LAZY_FETCH=") {
			out[i] = "GIT_NO_LAZY_FETCH=0"
		}
	}
	return out
}

func (l lazyGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return l.g.Run(ctx, dir, lazyOn(env), args...)
}

func (l lazyGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	return l.g.RunStdin(ctx, dir, lazyOn(env), stdin, args...)
}

func (l lazyGit) Stream(ctx context.Context, dir string, env []string, stdin io.Reader, stdout io.Writer, args ...string) error {
	return l.g.Stream(ctx, dir, lazyOn(env), stdin, stdout, args...)
}

// noContainer is a docker that runs nothing: the refusals below come first.
type noContainer struct{}

func (noContainer) RunStream(context.Context, []string, io.Writer) error {
	return errors.New("no container in this test")
}
func (noContainer) Remove(context.Context, string) error { return nil }

// A partial clone is refused as clone_of only where a box may have written its
// config: inside a folder boxed in place before CuliBox classified it (#371,
// second follow-up). The first copy's git clone and the bundle import read
// clone_of through a transport and rely on GIT_NO_LAZY_FETCH, which an older git
// ignores; the fixture proves the clone then runs the promisor remote's command.
// checkOf gates Prepare (the clone), a return (the bundle import), the save
// check and the estimate, so each refuses before any git runs. A folder no box
// ran in is the user's own: the same repo passes there.
func TestCheckOfRefusesAPartialCloneABoxMayHaveWritten(t *testing.T) {
	g, err := clone.NewExecGit()
	if err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	home := t.TempDir()
	of := filepath.Join(home, "legacy")
	marker := filepath.Join(t.TempDir(), "MARKER")
	if strings.Contains(marker, "'") {
		t.Skip("the temp dir has a quote in it")
	}
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(clone.CleanEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	run := func(args ...string) string {
		out, err := g.Run(ctx, of, env, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(of, "f.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "master")
	run("add", "f.txt")
	run("commit", "-q", "-m", "f")
	blob := run("rev-parse", "HEAD:f.txt")
	if err := os.Remove(filepath.Join(of, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Skipf("the blob is not a loose object (%v)", err)
	}
	// A ref naming a missing commit: what makes the copy's clone lazy-fetch.
	if err := os.WriteFile(filepath.Join(of, ".git", "refs", "heads", "main"), []byte(strings.Repeat("2", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("config", "remote.origin.url", filepath.Join(home, "nowhere"))
	run("config", "remote.origin.promisor", "true")
	run("config", "remote.origin.uploadpack", "touch '"+marker+"'; false")
	noMarker := func(t *testing.T) {
		t.Helper()
		if _, err := os.Stat(marker); err == nil {
			os.Remove(marker)
			t.Fatal("host git lazy-fetched: the promisor remote's upload-pack command ran on the host")
		}
	}
	old := lazyGit{g}

	t.Run("fixture: the copy's git clone fires on a git without GIT_NO_LAZY_FETCH", func(t *testing.T) {
		_, _ = clone.EnsureClone(ctx, old, config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "copy")}, home)
		if _, err := os.Stat(marker); err != nil {
			t.Skip("this git does not lazy-fetch through a local clone of the fixture")
		}
		os.Remove(marker)
	})

	reg, _ := inPlaceRegistry(t)
	if err := reg.Add(of); err != nil { // no flag: boxed in place before the record kept one
		t.Fatal(err)
	}
	h := testHooks(t, noClones)
	h.boxed = reg
	h.env = clone.Env{Git: old, Home: home}
	h.prepare = clone.Prepare
	h.ret = clone.Return
	h.docker = noContainer{}
	h.image = "img"
	refused := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(fmt.Sprint(err), "is a partial clone") ||
			!strings.Contains(fmt.Sprint(err), of+" was boxed in place before CuliBox recorded whether it was a git repo") {
			t.Fatalf("err = %v, want the partial-clone refusal naming the entry", err)
		}
	}
	t.Run("checkOf", func(t *testing.T) { refused(t, h.checkOf(of)) })
	t.Run("Prepare: the first copy", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "copy")
		_, _, err := h.Prepare(ctx, config.Clone{Of: of, Dir: dir}, false)
		noMarker(t)
		refused(t, err)
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the copy dir was made: %v", err)
		}
	})
	t.Run("a return: the bundle import", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "copy")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		s := h.doReturn(ctx, config.Clone{Of: of, Dir: dir, Profile: "p"}, session.ReturnNow, clone.ReturnForced)
		noMarker(t)
		if !strings.Contains(s.Error, "is a partial clone") {
			t.Fatalf("doReturn error = %q, want the partial-clone refusal", s.Error)
		}
	})
	t.Run("the save check and the estimate", func(t *testing.T) {
		refused(t, cloneSaveCheck(h, home, nil)(config.Clone{Of: of, Dir: filepath.Join(home, "copy")}))
		_, err := h.EstimateCopy(ctx, of, filepath.Join(home, "box-clones", "x"))
		refused(t, err)
		noMarker(t)
	})
	t.Run("a folder no box ran in is the user's own: allowed", func(t *testing.T) {
		mine, _ := inPlaceRegistry(t)
		h.boxed = mine
		if err := h.checkOf(of); err != nil {
			t.Fatalf("checkOf of the user's own partial clone = %v, want nil", err)
		}
		if err := clone.CheckOf(of, home, nil); err != nil {
			t.Fatalf("clone.CheckOf of a partial clone = %v, want nil", err)
		}
	})
}

func TestDoReturnPassesMergeAudit(t *testing.T) {
	var req clone.ReturnRequest
	h := &cloneHooks{
		clones: func() []config.Clone { return nil },
		file:   filepath.Join(t.TempDir(), cloneDirsFile),
		ret: func(_ context.Context, _ clone.Git, _ clone.Docker, r clone.ReturnRequest) clone.ReturnResult {
			req = r
			return clone.ReturnResult{}
		},
	}
	c := config.Clone{Of: "/home/u/repo", Dir: "/c/r", Profile: "dev"}

	// No audit sink: the closure fails closed.
	h.doReturn(context.Background(), c, session.ReturnOnIdle, clone.ReturnAuto)
	if req.MergeAudit == nil {
		t.Fatal("MergeAudit not set")
	}
	if err := req.MergeAudit("clone_merge_home", "d"); err == nil {
		t.Fatal("MergeAudit with no audit sink returned nil, want an error")
	}

	var got []broker.CredEvent
	h.audit = func(e broker.CredEvent) error { got = append(got, e); return nil }
	h.doReturn(context.Background(), c, session.ReturnOnIdle, clone.ReturnAuto)
	if err := req.MergeAudit("clone_merge_home", "d"); err != nil {
		t.Fatal(err)
	}
	want := broker.CredEvent{Action: "clone_merge_home", Kind: "clone", Name: "dev", Actor: "culibox", Detail: "d"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("events = %+v, want [%+v]", got, want)
	}

	// The sink's error reaches the caller.
	h.audit = func(broker.CredEvent) error { return errors.New("db locked") }
	if err := req.MergeAudit("clone_merge_home", "d"); err == nil {
		t.Fatal("audit error swallowed")
	}
}

func TestReturnSummaryOfMerge(t *testing.T) {
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	m := &clone.MergeResult{
		At: at, Branch: "main", From: "aaa", To: "bbb", Merged: false, Commits: 2, Files: 5,
		Reason: clone.MergeGuarded, Detail: "d", Paths: []string{".envrc", ".claude/x"},
	}
	got := returnSummaryOf(session.ReturnOnIdle, "/r", nil, clone.ReturnResult{Merge: m})
	g := got.Merge
	if g == nil || !g.At.Equal(at) || g.Branch != "main" || g.From != "aaa" || g.To != "bbb" || g.Merged ||
		g.Commits != 2 || g.Files != 5 || g.Reason != "guarded" || g.Detail != "d" || !slices.Equal(g.Paths, []string{".envrc", ".claude/x"}) {
		t.Fatalf("Merge = %+v, want a field-by-field copy of %+v", g, m)
	}
	m.Paths[0] = "changed"
	m.Branch = "other"
	if g.Paths[0] != ".envrc" || g.Branch != "main" {
		t.Fatalf("summary shares the result's memory: %+v", g)
	}
	if got := returnSummaryOf(session.ReturnOnIdle, "/r", nil, clone.ReturnResult{}); got.Merge != nil {
		t.Fatalf("Merge = %+v, want nil without a merge", got.Merge)
	}
}

func TestNotHomeShowsMerge(t *testing.T) {
	h := testHooks(t, noClones)
	h.dataDir = t.TempDir()
	dir := cloneDir(t)
	c := config.Clone{Of: "/r", Dir: dir}
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	st := clone.ReturnState{At: at, Base: "main", Merge: &clone.MergeResult{
		At: at, Branch: "main", From: "aaa", To: "bbb", Merged: true, Commits: 1, Files: 2,
	}}
	if err := clone.WriteReturnState(h.dataDir, dir, st); err != nil {
		t.Fatal(err)
	}
	got := h.NotHome(c)
	if got == nil || got.Merge == nil || !got.Merge.Merged || got.Merge.To != "bbb" || got.Merge.Commits != 1 || len(got.Branches) != 0 {
		t.Fatalf("NotHome = %+v, want a state carrying only the merge", got)
	}
	got.Merge.Branch = "x"
	st.Merge.Paths = append(st.Merge.Paths, "p")
	if again := h.NotHome(c); again.Merge.Branch != "main" || len(again.Merge.Paths) != 0 {
		t.Fatalf("NotHome shares memory: %+v", again.Merge)
	}
}
