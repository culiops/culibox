package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/culiops/culibox/internal/wsroots"
)

// fakeRoots is a WorkspaceRoots that only answers Paths; the fs policy reads
// nothing else.
type fakeRoots []string

func (f fakeRoots) Paths() []string                          { return f }
func (f fakeRoots) Covering(string) string                   { return "" }
func (f fakeRoots) List() ([]wsroots.Root, error)            { return nil, nil }
func (f fakeRoots) Add(string, string) (wsroots.Root, error) { return wsroots.Root{}, nil }
func (f fakeRoots) Remove(string, string) error              { return nil }
func (f fakeRoots) Touch(string)                             {}

// rootsFixture is a resolved home, an extra root outside it and an unrelated
// third directory, each a fresh temp dir.
func rootsFixture(t *testing.T) (home, extra, other string) {
	t.Helper()
	res := func(d string) string {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return res(t.TempDir()), res(t.TempDir()), res(t.TempDir())
}

func TestPolicyServesExtraRoot(t *testing.T) {
	home, extra, _ := rootsFixture(t)
	mustWrite(t, filepath.Join(extra, "a.txt"), "x")
	p, err := newFsPolicy(home, []string{extra}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.resolve(filepath.Join(extra, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != extra || got.Rel != "a.txt" || got.ReadOnly || got.Strict {
		t.Fatalf("got %+v, want Root=%s Rel=a.txt host-mode, not read-only", got, extra)
	}
}

func TestPolicyRefusesOutsideAllRoots(t *testing.T) {
	home, extra, other := rootsFixture(t)
	mustWrite(t, filepath.Join(other, "f"), "x")
	p, err := newFsPolicy(home, []string{extra}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolve(filepath.Join(other, "f")); !errors.Is(err, errOutsideHome) {
		t.Fatalf("err = %v, want errOutsideHome", err)
	}
}

func TestPolicySymlinkOutOfExtraRootRefused(t *testing.T) {
	home, extra, other := rootsFixture(t)
	mustWrite(t, filepath.Join(other, "f"), "x")
	if err := os.Symlink(other, filepath.Join(extra, "l")); err != nil {
		t.Fatal(err)
	}
	p, err := newFsPolicy(home, []string{extra}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []string{filepath.Join(extra, "l"), filepath.Join(extra, "l", "f")} {
		if _, err := p.resolve(req); !errors.Is(err, errOutsideHome) {
			t.Fatalf("resolve(%s) err = %v, want errOutsideHome", req, err)
		}
	}
}

func TestPolicyDotDotOutOfExtraRoot(t *testing.T) {
	home, base, _ := rootsFixture(t)
	// The request is cleaned first, so extra/../x names a sibling of extra.
	extra := filepath.Join(base, "ws")
	mustMkdirAll(t, extra)
	mustWrite(t, filepath.Join(base, "x"), "x")
	p, err := newFsPolicy(home, []string{extra}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolve(extra + "/../x"); !errors.Is(err, errOutsideHome) {
		t.Fatalf("err = %v, want errOutsideHome", err)
	}
}

func TestPolicyContainedTreeUnderExtraRoot(t *testing.T) {
	home, extra, other := rootsFixture(t)
	boxed := filepath.Join(extra, "boxed")
	mustMkdirAll(t, boxed)
	mustWrite(t, filepath.Join(extra, "secret"), "host file")
	mustWrite(t, filepath.Join(other, "secret"), "other")
	if err := os.Symlink(filepath.Join(extra, "secret"), filepath.Join(boxed, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(boxed, "out")); err != nil {
		t.Fatal(err)
	}
	p, err := newFsPolicy(home, []string{extra}, BoxTrees{Contained: []string{boxed}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"up", "out/secret"} {
		got, err := p.resolve(filepath.Join(boxed, rel))
		if err != nil {
			t.Fatalf("resolve(%s): %v", rel, err)
		}
		if !got.Strict || got.Root != boxed {
			t.Fatalf("resolve(%s) = %+v, want Strict under %s", rel, got, boxed)
		}
		if f, err := openFsTarget(got, 0); err == nil {
			f.Close()
			t.Fatalf("openFsTarget(%s) opened a link escaping the contained tree", rel)
		}
	}
}

// A box workspace that is itself a served root (the usual case) stays strict;
// it must not fall back to host-mode link following.
func TestPolicyContainedRootIsStrict(t *testing.T) {
	home, extra, other := rootsFixture(t)
	mustWrite(t, filepath.Join(other, "secret"), "other")
	if err := os.Symlink(other, filepath.Join(extra, "out")); err != nil {
		t.Fatal(err)
	}
	p, err := newFsPolicy(home, []string{extra}, BoxTrees{Contained: []string{extra}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.resolve(filepath.Join(extra, "out", "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Strict || got.Root != extra {
		t.Fatalf("got %+v, want Strict under %s", got, extra)
	}
	if f, err := openFsTarget(got, 0); err == nil {
		f.Close()
		t.Fatal("opened a link escaping the contained root")
	}
}

func TestPolicyDropsForbiddenRoot(t *testing.T) {
	home, _, _ := rootsFixture(t)
	if _, err := os.Stat("/etc/hosts"); err != nil {
		t.Skip("no /etc/hosts")
	}
	p, err := newFsPolicy(home, []string{"/etc"}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolve("/etc/hosts"); !errors.Is(err, errOutsideHome) {
		t.Fatalf("err = %v, want errOutsideHome", err)
	}
	if len(p.roots) != 1 {
		t.Fatalf("roots = %v, want home only", p.roots)
	}
}

func TestPolicyDropsMissingRoot(t *testing.T) {
	home, extra, _ := rootsFixture(t)
	gone := filepath.Join(extra, "gone")
	notDir := filepath.Join(extra, "file")
	mustWrite(t, notDir, "x")
	p, err := newFsPolicy(home, []string{gone, notDir, home}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.roots) != 1 || p.roots[0] != home {
		t.Fatalf("roots = %v, want only home", p.roots)
	}
}

func TestRootOfLongestMatch(t *testing.T) {
	home, extra, _ := rootsFixture(t)
	api := filepath.Join(extra, "api")
	mustMkdirAll(t, filepath.Join(api, "x"))
	p, err := newFsPolicy(home, []string{extra, api}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := p.rootOf(filepath.Join(api, "x")); !ok || r != api {
		t.Fatalf("rootOf(api/x) = %q, %v, want %q", r, ok, api)
	}
	if r, ok := p.rootOf(extra + "-sibling"); ok {
		t.Fatalf("rootOf(%s-sibling) = %q, want no match (component-wise)", extra, r)
	}
	if r, ok := p.rootOf(home); !ok || r != home {
		t.Fatalf("rootOf(home) = %q, %v", r, ok)
	}
}

func TestServerFsListExtraRoot(t *testing.T) {
	home, extra, other := rootsFixture(t)
	mustWrite(t, filepath.Join(extra, "a.txt"), "x")
	s := &Server{Home: home, Roots: fakeRoots{extra}}
	for path, want := range map[string]int{extra: http.StatusOK, other: http.StatusForbidden} {
		rr := httptest.NewRecorder()
		s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path="+path, nil))
		if rr.Code != want {
			t.Fatalf("list %s = %d (%s), want %d", path, rr.Code, rr.Body, want)
		}
	}
}

func TestResolvePreferReturnsRoot(t *testing.T) {
	home, extra, _ := rootsFixture(t)
	mustMkdirAll(t, filepath.Join(home, "proj"))
	mustMkdirAll(t, filepath.Join(extra, "api", "src"))
	pol, err := newFsPolicy(home, []string{extra}, BoxTrees{})
	if err != nil {
		t.Fatal(err)
	}
	if _, rel, root, ok := resolvePrefer(pol, filepath.Join(home, "proj")); !ok || root != home || rel != "proj" {
		t.Fatalf("home prefer = rel %q root %q ok %v", rel, root, ok)
	}
	if _, rel, root, ok := resolvePrefer(pol, filepath.Join(extra, "api", "src")); !ok || root != extra || rel != filepath.Join("api", "src") {
		t.Fatalf("extra prefer = rel %q root %q ok %v", rel, root, ok)
	}
	if _, _, _, ok := resolvePrefer(pol, extra); ok {
		t.Fatal("a root itself must not be a prefer root")
	}
}
