package web

import (
	"context"
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

func (f fakeRoots) Paths() []string                           { return f }
func (f fakeRoots) Covering(string) string                    { return "" }
func (f fakeRoots) List() ([]wsroots.Root, error)             { return nil, nil }
func (f fakeRoots) Add(string, string) (wsroots.Root, error)  { return wsroots.Root{}, nil }
func (f fakeRoots) Remove(string, string) error               { return nil }
func (f fakeRoots) Touch(string)                              {}
func (f fakeRoots) Elsewhere(context.Context) []wsroots.Place { return nil }

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
	// A relative link inside the root climbs out through the walk's own ".."
	// handling, which the lexical Clean above never reaches.
	if err := os.Symlink("../x", filepath.Join(extra, "up")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolve(filepath.Join(extra, "up")); !errors.Is(err, errOutsideHome) {
		t.Fatalf("link ../x: err = %v, want errOutsideHome", err)
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
	inHome := filepath.Join(home, "sub")
	mustMkdirAll(t, inHome)
	p, err := newFsPolicy(home, []string{gone, notDir, home, inHome}, BoxTrees{})
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

// A box workspace that holds an extra root stays contained: a link the box
// planted inside it must never be followed in host mode into $HOME.
func TestPolicyContainedAncestorOfExtraRootStaysStrict(t *testing.T) {
	home, base, _ := rootsFixture(t)
	w := filepath.Join(base, "proj")
	api := filepath.Join(w, "api")
	mustMkdirAll(t, api)
	mustWrite(t, filepath.Join(api, "a.txt"), "x")
	mustWrite(t, filepath.Join(home, ".bashrc"), "secret")
	if err := os.Symlink(filepath.Join(home, ".bashrc"), filepath.Join(api, "evil")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(w, "evildir")); err != nil {
		t.Fatal(err)
	}
	for _, roots := range [][]string{{api}, {w, api}} {
		p, err := newFsPolicy(home, roots, BoxTrees{Contained: []string{w}})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.contained) != 1 {
			t.Fatalf("roots %v: contained = %d, want 1 (the workspace must stay contained)", roots, len(p.contained))
		}
		for _, link := range []string{filepath.Join(api, "evil"), filepath.Join(w, "evildir", ".bashrc")} {
			got, err := p.resolve(link)
			if err == nil && (!got.Strict || got.Root != w) {
				t.Fatalf("roots %v: resolve(%s) = %+v, want Strict under %s or errOutsideHome", roots, link, got, w)
			}
			if err != nil && !errors.Is(err, errOutsideHome) {
				t.Fatalf("roots %v: resolve(%s) err = %v", roots, link, err)
			}
			if err == nil {
				if f, oerr := openFsTarget(got, 0); oerr == nil {
					f.Close()
					t.Fatalf("roots %v: opened %s, a link out of the workspace", roots, link)
				}
			}
		}
		got, err := p.resolve(filepath.Join(api, "a.txt"))
		if err != nil {
			t.Fatalf("roots %v: a.txt: %v", roots, err)
		}
		if !got.Strict || got.Root != w || got.Rel != "api/a.txt" {
			t.Fatalf("roots %v: a.txt = %+v, want Strict Root=%s Rel=api/a.txt", roots, got, w)
		}
	}
	// W is only an ancestor of the root: the workspace itself and its other
	// content are not served.
	p, _ := newFsPolicy(home, []string{api}, BoxTrees{Contained: []string{w}})
	mustWrite(t, filepath.Join(w, "other.txt"), "x")
	for _, req := range []string{w, filepath.Join(w, "other.txt")} {
		if _, err := p.resolve(req); !errors.Is(err, errOutsideHome) {
			t.Fatalf("resolve(%s) err = %v, want errOutsideHome", req, err)
		}
	}
}

// A box can replace a folder inside its workspace with a link. An extra root
// stored as W/api that now resolves elsewhere has been redirected and is
// dropped, so the link's target is never served in host mode.
func TestPolicyDropsRedirectedRoot(t *testing.T) {
	home, base, victim := rootsFixture(t)
	w := filepath.Join(base, "proj")
	api := filepath.Join(w, "api")
	mustMkdirAll(t, api)
	mustWrite(t, filepath.Join(victim, "v.txt"), "secret")
	if err := os.RemoveAll(api); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, api); err != nil {
		t.Fatal(err)
	}
	p, err := newFsPolicy(home, []string{api}, BoxTrees{Contained: []string{w}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.roots) != 1 || p.roots[0] != home {
		t.Fatalf("roots = %v, want only home (the redirected root dropped)", p.roots)
	}
	for _, req := range []string{filepath.Join(victim, "v.txt"), filepath.Join(api, "v.txt")} {
		got, err := p.resolve(req)
		if err == nil && (!got.Strict || got.Root != w) {
			t.Fatalf("resolve(%s) = %+v, want errOutsideHome or Strict under %s", req, got, w)
		}
		if err != nil && !errors.Is(err, errOutsideHome) {
			t.Fatalf("resolve(%s) err = %v", req, err)
		}
	}
	if _, err := p.resolve(filepath.Join(victim, "v.txt")); !errors.Is(err, errOutsideHome) {
		t.Fatalf("victim/v.txt err = %v, want errOutsideHome", err)
	}
}

// An extra root that is, sits in, or holds a denied tree (CuliBox's data dir, the box state root) is dropped
// when the policy is built: spec 5 calls those "CuliBox's own", and a hand-edited list must not widen the
// Files reach over them. A control root in the same list is kept.
func TestPolicyDropsRootsTouchingDenied(t *testing.T) {
	home, holder, ctrl := rootsFixture(t)
	denied := filepath.Join(holder, "data")
	inside := filepath.Join(denied, "sub")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, root string }{
		{"equal to the denied dir", denied},
		{"inside the denied dir", inside},
		{"holding the denied dir", holder},
	} {
		p, err := newFsPolicy(home, []string{c.root, ctrl}, BoxTrees{Denied: []string{denied}})
		if err != nil {
			t.Fatal(err)
		}
		if p.hasRoot(c.root) {
			t.Errorf("a root %s was kept: roots = %v", c.name, p.roots)
		}
		if !p.hasRoot(ctrl) {
			t.Errorf("%s: the control root was dropped: roots = %v", c.name, p.roots)
		}
	}
	// A sibling that only shares a name prefix is not "inside" (component-wise compare).
	sib := filepath.Join(holder, "data2")
	if err := os.Mkdir(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := newFsPolicy(home, []string{sib}, BoxTrees{Denied: []string{denied}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.hasRoot(sib) {
		t.Errorf("a sibling sharing a name prefix was dropped: roots = %v", p.roots)
	}
}
