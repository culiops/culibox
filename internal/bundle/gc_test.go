package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
)

const (
	hashA = "aaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbb"
)

var longAgo = time.Now().Add(-GCMinAge - 24*time.Hour)

// fakeDockerStore is a docker CLI standing in for one store: a container is a
// file under running/ or stopped/ holding the line `docker inspect` prints for
// it. `ps` without -a lists only the running ones, as docker does.
type fakeDockerStore struct {
	t   *testing.T
	dir string
}

func newFakeDockerStore(t *testing.T) *fakeDockerStore {
	t.Helper()
	d := t.TempDir()
	for _, sub := range []string{"running", "stopped"} {
		if err := os.Mkdir(filepath.Join(d, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
d='` + d + `'
sub=$1
shift
case "$sub" in
ps)
  [ -e "$d/fail-ps" ] && { echo 'Cannot connect to the Docker daemon' >&2; exit 1; }
  ls "$d/running"
  for a in "$@"; do [ "$a" = -a ] && ls "$d/stopped"; done
  ;;
inspect)
  [ -e "$d/fail-inspect" ] && { echo 'inspect failed' >&2; exit 1; }
  [ -e "$d/garbage-inspect" ] && { echo 'not what you asked for'; exit 0; }
  for a in "$@"; do
    [ -f "$d/running/$a" ] && cat "$d/running/$a"
    [ -f "$d/stopped/$a" ] && cat "$d/stopped/$a"
  done
  ;;
*) exit 64 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(d, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &fakeDockerStore{t: t, dir: d}
}

func (f *fakeDockerStore) client() box.CLIClient {
	return box.CLIClient{Bin: filepath.Join(f.dir, "docker")}
}

// add creates a container in state "running" or "stopped" mounting sources.
func (f *fakeDockerStore) add(state string, sources ...string) {
	f.t.Helper()
	type mount struct{ Type, Source string }
	mounts := []mount{}
	for _, s := range sources {
		mounts = append(mounts, mount{"bind", s})
	}
	raw, err := json.Marshal(mounts)
	if err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(state + strings.Join(sources, "\x00")))
	id := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(f.dir, state, id), []byte(id+" "+string(raw)+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeDockerStore) flag(name string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeDockerStore) unflag(name string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
		f.t.Fatal(err)
	}
}

// hookedLister runs before(call) ahead of each listing, so a test lands an event
// exactly between two steps of a pass without racing real goroutines.
type hookedLister struct {
	inner  MountLister
	calls  int
	before func(call int) error
}

func (h *hookedLister) MountSources(ctx context.Context) ([]string, error) {
	h.calls++
	if h.before != nil {
		if err := h.before(h.calls); err != nil {
			return nil, err
		}
	}
	return h.inner.MountSources(ctx)
}

type gcFixture struct {
	t     *testing.T
	m     *Manager
	src   string
	root  string
	store *fakeDockerStore
}

func newGCFixture(t *testing.T) *gcFixture {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	m, err := New(Options{Source: src, Root: root, MCPFile: filepath.Join(src, "absent.json"),
		HostHome: t.TempDir(), Prober: fakeProber{}})
	if err != nil {
		t.Fatal(err)
	}
	return &gcFixture{t: t, m: m, src: src, root: root, store: newFakeDockerStore(t)}
}

func (g *gcFixture) path(name string) string { return filepath.Join(g.root, name) }

// mk creates a directory tree as a named child of the root, dated when.
func (g *gcFixture) mk(name string, when time.Time) {
	g.t.Helper()
	dir := g.path(name)
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o700); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "x.md"), []byte("payload"), 0o600); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte(`{"hash":"x","files":{}}`), 0o600); err != nil {
		g.t.Fatal(err)
	}
	g.date(name, when)
}

func (g *gcFixture) date(name string, when time.Time) {
	g.t.Helper()
	if err := os.Chtimes(g.path(name), when, when); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gcFixture) gc() (GCReport, error) {
	g.t.Helper()
	return g.m.GC(context.Background(), g.store.client())
}

func (g *gcFixture) mustGC() GCReport {
	g.t.Helper()
	rep, err := g.gc()
	if err != nil {
		g.t.Fatal(err)
	}
	return rep
}

func (g *gcFixture) exists(name string) bool {
	_, err := os.Lstat(g.path(name))
	return err == nil
}

func (g *gcFixture) wantKept(names ...string) {
	g.t.Helper()
	for _, n := range names {
		if !g.exists(n) {
			g.t.Errorf("%s was removed, want it kept", n)
		}
	}
}

func (g *gcFixture) wantGone(names ...string) {
	g.t.Helper()
	for _, n := range names {
		if g.exists(n) {
			g.t.Errorf("%s still exists, want it removed", n)
		}
	}
}

func TestGCRemovesUnusedOldBundleWithItsImports(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", longAgo)
	rep := g.mustGC()
	g.wantGone(hashA, hashA+".imports", gcPrefix+hashA, gcPrefix+hashA+".imports")
	if len(rep.Removed) != 1 || rep.Removed[0].Name != hashA || rep.Removed[0].Bytes == 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestGCKeepsBundleMountedByRunningBox(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.store.add("running", g.path(hashA))
	rep := g.mustGC()
	g.wantKept(hashA)
	if rep.InUse != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// A stopped box can be restarted and needs its mounts back.
func TestGCKeepsBundleMountedByStoppedBox(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.store.add("stopped", g.path(hashA))
	g.mustGC()
	g.wantKept(hashA)
}

// The layout mounts files inside the bundle, not only the bundle root.
func TestGCKeepsBundleWhenOnlyAFileInsideItIsMounted(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.store.add("running", filepath.Join(g.path(hashA), "skills"))
	g.mustGC()
	g.wantKept(hashA)
}

func TestGCImportsOnlyReferenceKeepsBoth(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", longAgo)
	g.store.add("stopped", filepath.Join(g.path(hashA+".imports"), "home/agent/x.md"))
	g.mustGC()
	g.wantKept(hashA, hashA+".imports")
}

func TestGCBundleReferenceKeepsItsImports(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", longAgo)
	g.store.add("running", g.path(hashA))
	g.mustGC()
	g.wantKept(hashA, hashA+".imports")
}

// Two instances share one docker store with separate roots: the other
// instance's box on the same hash says nothing about this root's dir.
func TestGCMatchesFullPathsNotHashes(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.store.add("running", filepath.Join("/other/instance/bundle", hashA))
	g.mustGC()
	g.wantGone(hashA)
}

// A prefix of a name is another name.
func TestGCDoesNotMatchAPathThatMerelyStartsWithTheDir(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.store.add("running", g.path(hashA)+"-other")
	g.mustGC()
	g.wantGone(hashA)
}

// A box created through a differently spelled path to the same root still
// references the dir.
func TestGCMatchesASourceSpelledThroughALink(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashB, longAgo)
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(g.root, link); err != nil {
		t.Fatal(err)
	}
	g.store.add("stopped", filepath.Join(link, hashA))
	g.mustGC()
	g.wantKept(hashA)
	g.wantGone(hashB)
}

func TestGCMatchesAConfiguredRootSpelledThroughALink(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(g.root, link); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Source: g.src, Root: link, MCPFile: filepath.Join(g.src, "absent.json"),
		HostHome: t.TempDir(), Prober: fakeProber{}})
	if err != nil {
		t.Fatal(err)
	}
	g.store.add("running", g.path(hashA)) // the resolved spelling
	if _, err := m.GC(context.Background(), g.store.client()); err != nil {
		t.Fatal(err)
	}
	g.wantKept(hashA)
}

func TestGCKeepsUnusedBundleYoungerThanMinAge(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, time.Now().Add(-GCMinAge+time.Hour))
	g.mk(hashA+".imports", longAgo)
	rep := g.mustGC()
	g.wantKept(hashA, hashA+".imports")
	if rep.TooYoung != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// A pair's age is its younger half's.
func TestGCKeepsOldBundleWithYoungImports(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", time.Now())
	g.mustGC()
	g.wantKept(hashA, hashA+".imports")
}

func TestGCRemovesAVictimWithNoPermissionsAtAll(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores modes")
	}
	g := newGCFixture(t)
	g.mk(hashA, time.Now())
	if err := os.Chmod(g.path(hashA), 0o000); err != nil {
		t.Fatal(err)
	}
	g.date(hashA, longAgo)
	g.mustGC()
	g.wantGone(hashA, gcPrefix+hashA)
}

// An imports dir whose bundle is gone is judged by its own date.
func TestGCOrphanImportsFollowTheSameRules(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA+".imports", longAgo)
	g.mk(hashB+".imports", time.Now())
	g.mustGC()
	g.wantGone(hashA + ".imports")
	g.wantKept(hashB + ".imports")
}

// Reuse is what stands between a spawn and the GC: its container does not exist
// yet, so only the refreshed date protects the dir. The source changes after the
// Ensure so the dir is no longer current and survives on its age alone.
func TestGCReuseRefreshesAgeSoTheDirSurvives(t *testing.T) {
	g := newGCFixture(t)
	first, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	g.date(first.Hash, longAgo)
	reused, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused {
		t.Fatal("expected a reuse")
	}
	if err := os.WriteFile(filepath.Join(g.src, "CLAUDE.md"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.mustGC()
	g.wantKept(first.Hash)
}

// The same, with the reuse landing between the pass's scan and its rename.
func TestGCRereadsTheAgeBeforeCondemning(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 1 {
			g.date(hashA, time.Now())
		}
		return nil
	}}
	if _, err := g.m.GC(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	g.wantKept(hashA)
}

// Ensure's decide-and-touch and the GC's age-read-and-rename exclude each other:
// without that, a reuse landing between the read and the rename hands a spawn a
// directory that is about to vanish.
func TestEnsureReuseWaitsForTheGCLock(t *testing.T) {
	g := newGCFixture(t)
	if _, err := g.m.Ensure(); err != nil {
		t.Fatal(err)
	}
	g.m.gcMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := g.m.Ensure(); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
		g.m.gcMu.Unlock()
		t.Fatal("Ensure reused the dir while the GC held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	g.m.gcMu.Unlock()
	<-done
}

// A dir whose date cannot be refreshed has no protection from the GC, so it is
// not handed to a spawn.
func TestEnsureFailsWhenTheAgeCannotBeRefreshed(t *testing.T) {
	g := newGCFixture(t)
	if _, err := g.m.Ensure(); err != nil {
		t.Fatal(err)
	}
	g.m.touch = func(string, time.Time) error { return os.ErrPermission }
	if _, err := g.m.Ensure(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

// A dir that went between the lock read and the touch is rebuilt, not an error.
func TestEnsureRebuildsWhenTheDirVanishedBeforeTheTouch(t *testing.T) {
	g := newGCFixture(t)
	if _, err := g.m.Ensure(); err != nil {
		t.Fatal(err)
	}
	g.m.touch = func(string, time.Time) error { return os.ErrNotExist }
	b, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if b.Reused {
		t.Fatal("reported a reuse of a dir that is gone")
	}
}

func TestGCDockerFailureRemovesNothing(t *testing.T) {
	for _, flag := range []string{"fail-ps", "fail-inspect", "garbage-inspect"} {
		t.Run(flag, func(t *testing.T) {
			g := newGCFixture(t)
			g.mk(hashA, longAgo)
			g.mk(gcPrefix+hashB, longAgo)
			g.store.add("stopped", "/somewhere/else")
			g.store.flag(flag)
			rep, err := g.gc()
			if err == nil {
				t.Fatal("want the failure reported")
			}
			g.wantKept(hashA, gcPrefix+hashB)
			if len(rep.Removed) != 0 || rep.Swept != 0 {
				t.Fatalf("report = %+v", rep)
			}
			// The same state with docker back: the pass works.
			g.store.unflag(flag)
			g.mustGC()
			g.wantGone(hashA, gcPrefix+hashB)
		})
	}
}

// probeFails is a prober whose image cannot be resolved.
type probeFails struct{ fakeProber }

func (probeFails) ImageID() (string, error) { return "", errors.New("no such image") }
func (probeFails) Probe(string, []string) (ImageFacts, error) {
	return ImageFacts{}, errors.New("no such image")
}

// Without the image ID the current hash is unknown, so it cannot be protected.
func TestGCUnknownCurrentHashRemovesNothing(t *testing.T) {
	g := newGCFixture(t)
	g.m.opts.Prober = probeFails{}
	g.mk(hashA, longAgo)
	if _, err := g.gc(); err == nil {
		t.Fatal("want the failure reported")
	}
	g.wantKept(hashA)
}

func TestGCNeverRemovesTheCurrentHash(t *testing.T) {
	g := newGCFixture(t)
	b, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	g.date(b.Hash, longAgo)
	rep := g.mustGC()
	g.wantKept(b.Hash)
	if rep.Current != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// The window after the rename: only the marked names exist, so no Ensure can
// select the dir.
func TestGCVictimOnlyExistsUnderTheMarkedNameDuringRecheck(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", longAgo)
	saw := false
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 2 {
			saw = true
			g.wantGone(hashA, hashA+".imports")
			g.wantKept(gcPrefix+hashA, gcPrefix+hashA+".imports")
		}
		return nil
	}}
	if _, err := g.m.GC(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("no re-check ran")
	}
	g.wantGone(gcPrefix+hashA, gcPrefix+hashA+".imports")
}

// A container that appeared since the first listing gets its dir back whole.
func TestGCRecheckFindsReferenceAndRestores(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", longAgo)
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 2 {
			g.store.add("running", g.path(hashA))
		}
		return nil
	}}
	rep, err := g.m.GC(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	g.wantKept(hashA, hashA+".imports")
	g.wantGone(gcPrefix+hashA, gcPrefix+hashA+".imports")
	if _, ok := readLock(g.path(hashA)); !ok {
		t.Fatal("restored bundle lost its lock")
	}
	if len(rep.Removed) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestGCRecheckFailureRestoresEverything(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(hashA+".imports", longAgo)
	g.mk(hashB, longAgo)
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 2 {
			return errors.New("daemon went away")
		}
		return nil
	}}
	if _, err := g.m.GC(context.Background(), l); err == nil {
		t.Fatal("want the failure reported")
	}
	g.wantKept(hashA, hashA+".imports", hashB)
	g.wantGone(gcPrefix+hashA, gcPrefix+hashA+".imports", gcPrefix+hashB)
}

// A cancelled pass (shutdown) after the renames: restored, not half removed.
func TestGCCancelledBeforeRecheckRestores(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 2 {
			cancel()
		}
		return nil
	}}
	if _, err := g.m.GC(ctx, l); err == nil {
		t.Fatal("want the cancellation reported")
	}
	g.wantKept(hashA)
	g.wantGone(gcPrefix + hashA)
}

func TestGCCancelledContextRemovesNothing(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	g.mk(gcPrefix+hashB, longAgo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.m.GC(ctx, g.store.client()); err == nil {
		t.Fatal("want the cancellation reported")
	}
	g.wantKept(hashA, gcPrefix+hashB)
}

// A dir that was renamed back while its name was rebuilt is not overwritten.
func TestGCRestoreDoesNotClobberARebuiltDir(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, longAgo)
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 2 {
			g.mk(hashA, time.Now()) // an Ensure rebuilt this hash
			if err := os.WriteFile(filepath.Join(g.path(hashA), "fresh"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			g.store.add("running", g.path(hashA))
		}
		return nil
	}}
	rep, err := g.m.GC(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(g.path(hashA), "fresh")); err != nil {
		t.Fatalf("rebuilt dir was replaced: %v", err)
	}
	// The old tree may back the live container's mount: left for a later pass.
	g.wantKept(gcPrefix + hashA)
	if len(rep.Removed) != 0 || rep.Held != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// What a crash in the middle of a removal leaves: a partial tree under the marked
// name and an imports dir without its bundle. Ensure must not pick either up,
// and the next pass sweeps the marked one.
func TestGCCrashLeavesNoSelectableStateAndIsSweptNextPass(t *testing.T) {
	g := newGCFixture(t)
	b, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	cur := b.Hash
	// The crash: this hash was condemned and half removed.
	if err := os.Rename(g.path(cur), g.path(gcPrefix+cur)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(g.path(gcPrefix+cur), "settings.json")); err != nil {
		t.Fatal(err)
	}
	g.mk(cur+".imports", longAgo)

	again, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if again.Reused {
		t.Fatal("Ensure reused a condemned bundle")
	}
	if again.Dir != g.path(cur) {
		t.Fatalf("Ensure returned %s", again.Dir)
	}
	if _, err := os.Stat(filepath.Join(again.Dir, "settings.json")); err != nil {
		t.Fatalf("rebuilt bundle is partial: %v", err)
	}

	rep := g.mustGC()
	g.wantGone(gcPrefix + cur)
	g.wantKept(cur)
	if rep.Swept != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// Ensure on a hash-named dir that has no lock (a partial or unexpected dir) does
// not reuse it, and an imports dir left without a bundle does not survive a
// rebuild that has imports of its own.
func TestEnsureDoesNotSelectAPartialDir(t *testing.T) {
	g := newGCFixture(t)
	b, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Dir, lockName)); err != nil {
		t.Fatal(err)
	}
	again, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if again.Reused {
		t.Fatal("Ensure reused a dir without a lock")
	}
}

// A leftover a box still names is not swept, and never renamed back: a removal
// may have started, so it could be a partial tree.
func TestGCSweepHoldsAReferencedLeftover(t *testing.T) {
	g := newGCFixture(t)
	g.mk(gcPrefix+hashA, longAgo)
	g.store.add("stopped", g.path(hashA))
	rep := g.mustGC()
	g.wantKept(gcPrefix + hashA)
	g.wantGone(hashA)
	if rep.Held != 1 || rep.Swept != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestGCSweepsLeftoverImportsToo(t *testing.T) {
	g := newGCFixture(t)
	g.mk(gcPrefix+hashA+".imports", time.Now())
	g.mk(gcPrefix+hashA, time.Now())
	g.mustGC()
	g.wantGone(gcPrefix+hashA+".imports", gcPrefix+hashA)
}

func TestGCLeavesUnknownNamesAlone(t *testing.T) {
	g := newGCFixture(t)
	names := []string{
		"plugins-other-0123456789abcdef", "plugins-cache-0123", "plugins-cache-0123456789abcdeg", "plugins-cache-0123456789abcdef0",
		"plugins-cache-0123456789abcdef.bak", "plugins-cache-0123456789abcdef.imports", "PLUGINS-cache-0123456789abcdef", "plugins-cache-",
		sessionMCPDir, ".tmp-123", ".tmp-imp-123",
		"aaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaa", "AAAAAAAAAAAAAAAA", "gggggggggggggggg",
		hashA + ".imports.bak", hashA + ".bak", hashA + ".IMPORTS", ".imports",
		gcPrefix, gcPrefix + "nothex", gcPrefix + "plugins-other-0123456789abcdef", gcPrefix + gcPrefix + hashA,
		gcPrefix + hashA + ".bak",
	}
	for _, n := range names {
		g.mk(n, longAgo)
	}
	g.mustGC()
	g.wantKept(names...)
}

func TestGCLeavesSymlinksAlone(t *testing.T) {
	g := newGCFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outside, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	names := []string{hashA, hashB + ".imports", gcPrefix + "cccccccccccccccc", gcPrefix + "dddddddddddddddd.imports",
		snapCache, gcPrefix + snapJSON}
	for _, n := range names {
		if err := os.Symlink(outside, g.path(n)); err != nil {
			t.Fatal(err)
		}
	}
	g.mustGC()
	g.wantKept(names...)
	for _, n := range names {
		if fi, err := os.Lstat(g.path(n)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s: %v %v", n, fi, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("followed a symlink: %v", err)
	}
}

// A symlink where a bundle should be: its real imports sibling is left too, so
// one odd name cannot take half a pair.
func TestGCLeavesAPairAloneWhenEitherHalfIsNotADir(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA+".imports", longAgo)
	if err := os.WriteFile(g.path(hashA), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.mustGC()
	g.wantKept(hashA, hashA+".imports")
}

func TestGCDoesNotFollowSymlinksInsideAVictim(t *testing.T) {
	g := newGCFixture(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "dir"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(outside, "dir"), 0o700) })
	g.mk(hashA, time.Now())
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(g.path(hashA), "link")); err != nil {
		t.Fatal(err)
	}
	g.date(hashA, longAgo)
	g.mustGC()
	g.wantGone(hashA)
	fi, err := os.Stat(filepath.Join(outside, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o555 {
		t.Fatalf("chmod followed a link out of the victim: %v", fi.Mode())
	}
}

func TestGCReadOnlyTreeIsRemovedWhole(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores modes")
	}
	g := newGCFixture(t)
	g.mk(hashA, time.Now())
	sub := filepath.Join(g.path(hashA), "skills")
	if err := os.Chmod(filepath.Join(sub, "x.md"), 0o400); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(sub, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "y"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{locked, sub} {
		if err := os.Chmod(d, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	noread := filepath.Join(g.path(hashA), "noread")
	if err := os.Mkdir(noread, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(g.path(hashA), 0o500); err != nil {
		t.Fatal(err)
	}
	g.date(hashA, longAgo)
	g.mustGC()
	g.wantGone(hashA, gcPrefix+hashA)
}

// A pass with nothing to do never calls docker: no daemon traffic to log about.
func TestGCNothingToDoAsksDockerNothing(t *testing.T) {
	g := newGCFixture(t)
	g.mk(hashA, time.Now())
	l := &hookedLister{inner: g.store.client()}
	if _, err := g.m.GC(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if l.calls != 0 {
		t.Fatalf("docker listed %d times", l.calls)
	}
}

func TestGCMissingRootIsNotAnError(t *testing.T) {
	g := newGCFixture(t)
	g.m.opts.Root = filepath.Join(g.root, "never-created")
	if _, err := g.gc(); err != nil {
		t.Fatal(err)
	}
}

const (
	snapCache = "plugins-cache-cccccccccccccccc"
	snapJSON  = "plugins-json-dddddddddddddddd"
	snapMkt   = "plugins-marketplaces-eeeeeeeeeeeeeeee"
)

func TestGCRemovesUnreferencedOldSnapshots(t *testing.T) {
	g := newGCFixture(t)
	for _, n := range []string{snapCache, snapJSON, snapMkt} {
		g.mk(n, longAgo)
	}
	rep := g.mustGC()
	g.wantGone(snapCache, snapJSON, snapMkt, gcPrefix+snapCache)
	if len(rep.Removed) != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestGCKeepsYoungSnapshot(t *testing.T) {
	g := newGCFixture(t)
	g.mk(snapCache, time.Now().Add(-GCMinAge+time.Hour))
	g.mustGC()
	g.wantKept(snapCache)
}

// A live box keeps the snapshot dirs it was created with: its mount names the
// entry inside the snapshot.
func TestGCKeepsSnapshotMountedByContainer(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		t.Run(state, func(t *testing.T) {
			g := newGCFixture(t)
			g.mk(snapCache, longAgo)
			g.mk(snapJSON, longAgo)
			g.store.add(state, filepath.Join(g.path(snapCache), "cache"))
			g.mustGC()
			g.wantKept(snapCache)
			g.wantGone(snapJSON)
		})
	}
}

func TestGCRecheckRestoresASnapshot(t *testing.T) {
	g := newGCFixture(t)
	g.mk(snapCache, longAgo)
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 2 {
			g.wantGone(snapCache)
			g.wantKept(gcPrefix + snapCache)
			g.store.add("running", filepath.Join(g.path(snapCache), "cache"))
		}
		return nil
	}}
	if _, err := g.m.GC(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	g.wantKept(snapCache)
	g.wantGone(gcPrefix + snapCache)
}

func TestGCSweepsSnapshotLeftover(t *testing.T) {
	g := newGCFixture(t)
	g.mk(gcPrefix+snapCache, time.Now())
	rep := g.mustGC()
	g.wantGone(gcPrefix + snapCache)
	if rep.Swept != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// The snapshots the host plugin set fingerprints to now are what the next spawn
// takes. Removing one only costs a recopy, on the spawn path, so they are kept.
func TestGCKeepsCurrentSnapshotsHoweverOld(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	ps := pluginSnapshot(g.src, g.root, g.m.snapshotGuard())
	names := snapshotDirs(t, ps)
	if len(names) != 3 {
		t.Fatalf("snapshots = %v", names)
	}
	for _, n := range names {
		g.date(n, longAgo)
	}
	rep := g.mustGC()
	g.wantKept(names...)
	if rep.Current != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

// snapshotDirs is the distinct snapshot dirs a verdict mounts, as root children.
func snapshotDirs(t *testing.T, ps pluginSet) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for _, m := range ps.mounts {
		n := filepath.Base(filepath.Dir(m.Host))
		if !isSnapshotName(n) {
			t.Fatalf("mount %s is not in a snapshot dir", m.Host)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// A hash dir never names a snapshot: not in its lock and not in any file. The
// lock and the settings carry the verdict only, so this pins that nothing else
// grows a dependency GC would have to honour.
func TestHashDirNamesNoSnapshot(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	b, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.pluginMounts) == 0 {
		t.Fatal("want plugins mounted")
	}
	err = filepath.WalkDir(b.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "plugins-json-") || strings.Contains(string(raw), "plugins-cache-") ||
			strings.Contains(string(raw), "plugins-marketplaces-") {
			t.Errorf("%s names a snapshot", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The proof that a kept hash dir does not need a snapshot GC removed: a spawn on
// it takes the snapshots the host fingerprints to now, publishing them if they
// are gone, and every mount it gets exists.
func TestEnsureOnKeptHashDirSurvivesItsSnapshotsBeingRemoved(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	first, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	old := snapshotDirs(t, pluginSet{mounts: first.pluginMounts})
	for _, n := range old {
		g.date(n, longAgo)
	}
	// The host set changes without the verdict changing: same hash, new snapshots.
	writePluginFile(t, g.src, "cache/changed.md", "new")
	writePluginFile(t, g.src, "marketplaces/changed.md", "new")
	writePluginFile(t, g.src, "known_marketplaces.json", "{}")

	g.mustGC()
	g.wantGone(old...)
	g.wantKept(first.Hash)

	again, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reused || again.Hash != first.Hash {
		t.Fatalf("want a reuse of %s, got %+v", first.Hash, again)
	}
	if len(again.pluginMounts) != len(first.pluginMounts) {
		t.Fatalf("mounts = %v", again.pluginMounts)
	}
	for _, m := range again.pluginMounts {
		if _, err := os.Lstat(m.Host); err != nil {
			t.Errorf("mount source gone: %v", err)
		}
	}
}

// The same when the plugins have gone from the host altogether: the verdict
// flips, so the hash does, and no mount names a removed snapshot.
func TestEnsureAfterPluginsLeftTheHostMountsNoSnapshot(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	first, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	old := snapshotDirs(t, pluginSet{mounts: first.pluginMounts})
	for _, n := range old {
		g.date(n, longAgo)
	}
	if err := os.RemoveAll(filepath.Join(g.src, "plugins")); err != nil {
		t.Fatal(err)
	}
	g.mustGC()
	g.wantGone(old...)
	again, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if again.Hash == first.Hash || len(again.pluginMounts) != 0 {
		t.Fatalf("got %+v", again)
	}
}

// Without the host fingerprint the current snapshots are unknown, so that pass
// removes no snapshot, and leaves hash dirs to their own rules.
func TestGCUnreadableHostPluginSetRemovesNoSnapshot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores modes")
	}
	g := newGCFixture(t)
	pluginTree(t, g.src)
	cache := filepath.Join(g.src, "plugins", "cache")
	if err := os.Chmod(cache, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cache, 0o755) })
	g.mk(snapCache, longAgo)
	g.mk(gcPrefix+snapJSON, longAgo)
	g.mk(hashA, longAgo)
	rep, err := g.gc()
	if err == nil {
		t.Fatal("want the unknown plugin set reported")
	}
	g.wantKept(snapCache, gcPrefix+snapJSON)
	g.wantGone(hashA)
	if rep.SnapshotsUnsure != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

// A plugin set the verdict refuses could be a transient state, so the same holds.
func TestGCIncompleteHostPluginSetRemovesNoSnapshot(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	if err := os.RemoveAll(filepath.Join(g.src, "plugins", "marketplaces")); err != nil {
		t.Fatal(err)
	}
	g.mk(snapCache, longAgo)
	if _, err := g.gc(); err == nil {
		t.Fatal("want the unknown plugin set reported")
	}
	g.wantKept(snapCache)
}

// A docker failure holds for snapshots as for everything else.
func TestGCDockerFailureRemovesNoSnapshot(t *testing.T) {
	g := newGCFixture(t)
	g.mk(snapCache, longAgo)
	g.store.flag("fail-ps")
	if _, err := g.gc(); err == nil {
		t.Fatal("want the failure reported")
	}
	g.wantKept(snapCache)
}

// Reuse refreshes the marker, and the source of the dates is the shared lock.
func TestSnapshotReuseRefreshesAgeSoItSurvives(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	first, err := g.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	names := snapshotDirs(t, pluginSet{mounts: first.pluginMounts})
	for _, n := range names {
		g.date(n, longAgo)
	}
	if _, err := g.m.Ensure(); err != nil { // the reuse
		t.Fatal(err)
	}
	// No longer current, so only the refreshed date can save them.
	writePluginFile(t, g.src, "cache/changed.md", "new")
	writePluginFile(t, g.src, "marketplaces/changed.md", "new")
	writePluginFile(t, g.src, "known_marketplaces.json", "{}")
	g.mustGC()
	g.wantKept(names...)
}

func TestSnapshotReuseWaitsForTheGCLock(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	pluginSnapshot(g.src, g.root, g.m.snapshotGuard())
	g.m.gcMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pluginSnapshot(g.src, g.root, g.m.snapshotGuard())
	}()
	select {
	case <-done:
		g.m.gcMu.Unlock()
		t.Fatal("a snapshot was reused while the GC held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	g.m.gcMu.Unlock()
	<-done
}

// A snapshot whose date cannot be refreshed is not handed out: the set is
// refused, as for any other snapshot failure.
func TestSnapshotReuseFailsWhenTheAgeCannotBeRefreshed(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	guard := g.m.snapshotGuard()
	pluginSnapshot(g.src, g.root, guard)
	guard.touch = func(string, time.Time) error { return os.ErrPermission }
	ps := pluginSnapshot(g.src, g.root, guard)
	if len(ps.mounts) != 0 || len(ps.drops) == 0 {
		t.Fatalf("verdict = %+v", ps)
	}
}

// One that went between the marker read and the touch is copied again.
func TestSnapshotReuseRecopiesWhenTheDirVanished(t *testing.T) {
	g := newGCFixture(t)
	pluginTree(t, g.src)
	guard := g.m.snapshotGuard()
	first := pluginSnapshot(g.src, g.root, guard)
	names := snapshotDirs(t, first)
	guard.touch = func(p string, _ time.Time) error {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		return os.ErrNotExist
	}
	ps := pluginSnapshot(g.src, g.root, guard)
	if len(ps.mounts) == 0 {
		t.Fatalf("verdict = %+v", ps)
	}
	g.wantKept(names...)
}

func TestGCRereadsASnapshotsAgeBeforeCondemning(t *testing.T) {
	g := newGCFixture(t)
	g.mk(snapCache, longAgo)
	l := &hookedLister{inner: g.store.client(), before: func(call int) error {
		if call == 1 {
			g.date(snapCache, time.Now())
		}
		return nil
	}}
	if _, err := g.m.GC(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	g.wantKept(snapCache)
}
