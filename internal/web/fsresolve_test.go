package web

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestResolveFs(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(home, "ws")
	data := filepath.Join(home, "data")
	proj := filepath.Join(home, "proj")
	creds := filepath.Join(home, ".claude", ".credentials.json")
	mustMkdirAll(t, filepath.Join(ws, "sub"))
	mustMkdirAll(t, filepath.Join(ws, "inner"))
	mustMkdirAll(t, data)
	mustMkdirAll(t, proj)
	mustMkdirAll(t, filepath.Join(home, "dparent", "data"))
	mustWrite(t, creds, "secret")
	mustWrite(t, filepath.Join(ws, "sub", "f.txt"), "f")
	mustWrite(t, filepath.Join(proj, "a.txt"), "a")
	mustWrite(t, filepath.Join(data, "cookie.secret"), "c")
	mustSymlink(t, creds, filepath.Join(ws, "evil"))
	mustSymlink(t, ws, filepath.Join(home, "alias"))
	mustSymlink(t, filepath.Join(ws, "evil"), filepath.Join(proj, "hl"))
	mustSymlink(t, data, filepath.Join(proj, "dl"))
	mustSymlink(t, "/etc", filepath.Join(proj, "out"))
	mustSymlink(t, "l2", filepath.Join(proj, "l1"))
	mustSymlink(t, "l1", filepath.Join(proj, "l2"))
	mustSymlink(t, "../ws/sub", filepath.Join(proj, "relw"))
	mustSymlink(t, "../data/x", filepath.Join(proj, "reld"))
	mustSymlink(t, "../ws/sub/../../proj", filepath.Join(proj, "climb"))
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(other, "f"), "o")

	std := BoxTrees{Denied: []string{data}, Contained: []string{ws}}
	host := func(rel string) fsTarget {
		return fsTarget{Root: home, Rel: rel, Abs: filepath.Join(home, rel)}
	}
	strict := func(root, rel string) fsTarget {
		return fsTarget{Root: root, Rel: rel, Abs: filepath.Join(root, rel), Strict: true}
	}

	cases := []struct {
		name    string
		trees   BoxTrees
		req     string
		want    fsTarget
		wantErr error
	}{
		{"host file", std, filepath.Join(proj, "a.txt"), host("proj/a.txt"), nil},
		{"strict file", std, filepath.Join(ws, "sub", "f.txt"), strict(ws, "sub/f.txt"), nil},
		{"strict root", std, ws, strict(ws, "."), nil},
		{"box link left unresolved", std, filepath.Join(ws, "evil"), strict(ws, "evil"), nil},
		{"host alias to W", std, filepath.Join(home, "alias", "sub", "f.txt"), strict(ws, "sub/f.txt"), nil},
		{"host link into W", std, filepath.Join(proj, "hl"), strict(ws, "evil"), nil},
		{"denied file", std, filepath.Join(data, "cookie.secret"), fsTarget{}, errDenied},
		{"host link into denied", std, filepath.Join(proj, "dl", "x"), fsTarget{}, errDenied},
		{"host link outside home", std, filepath.Join(proj, "out", "hosts"), fsTarget{}, errOutsideHome},
		{"missing", std, filepath.Join(proj, "nope"), fsTarget{}, os.ErrNotExist},
		{"under a file", std, filepath.Join(proj, "a.txt", "x"), fsTarget{}, os.ErrNotExist},
		{"dotdot traversal out of home", std,
			filepath.Join(proj, "..", "..", filepath.Base(other), "f"), fsTarget{}, errOutsideHome},
		{"empty", std, "", fsTarget{}, errBadPath},
		{"relative", std, "rel/x", fsTarget{}, errBadPath},
		{"link loop", std, filepath.Join(proj, "l1"), fsTarget{}, errBadPath},
		{"host relative link into W", std, filepath.Join(proj, "relw"), strict(ws, "sub"), nil},
		{"host relative link into denied", std, filepath.Join(proj, "reld"), fsTarget{}, errDenied},
		{"host link whose remainder climbs out of W", std, filepath.Join(proj, "climb"), fsTarget{}, errOutsideHome},
		{"nested: outermost wins",
			BoxTrees{Denied: []string{data}, Contained: []string{filepath.Join(ws, "inner"), ws}},
			filepath.Join(ws, "inner", "x"), strict(ws, "inner/x"), nil},
		{"contained outside home",
			BoxTrees{Contained: []string{other}},
			filepath.Join(other, "f"), fsTarget{}, errOutsideHome},
		{"contained home/ancestor ignored",
			BoxTrees{Denied: []string{data}, Contained: []string{home, filepath.Dir(home), filepath.Join(data, "..")}},
			filepath.Join(proj, "a.txt"), host("proj/a.txt"), nil},
		{"contained holding denied ignored: host",
			BoxTrees{Denied: []string{filepath.Join(home, "dparent", "data")}, Contained: []string{filepath.Join(home, "dparent")}},
			filepath.Join(proj, "a.txt"), host("proj/a.txt"), nil},
		{"contained holding denied ignored: denied",
			BoxTrees{Denied: []string{filepath.Join(home, "dparent", "data")}, Contained: []string{filepath.Join(home, "dparent")}},
			filepath.Join(home, "dparent", "data", "x"), fsTarget{}, errDenied},
		{"nonexistent tree entries ignored",
			BoxTrees{Denied: []string{filepath.Join(home, "gone1")}, Contained: []string{filepath.Join(home, "gone2")}},
			filepath.Join(proj, "a.txt"), host("proj/a.txt"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := newFsPolicy(home, nil, tc.trees)
			if err != nil {
				t.Fatalf("newFsPolicy: %v", err)
			}
			got, err := p.resolve(tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("resolve(%q) err = %v (got %+v), want %v", tc.req, err, got, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q) err = %v", tc.req, err)
			}
			if got != tc.want {
				t.Fatalf("resolve(%q) = %+v, want %+v", tc.req, got, tc.want)
			}
		})
	}
}

// resolveFs is fsPolicy()+resolve: a nil BoxTrees is host behaviour, and the
// server's trees reach the resolver.
func TestServerResolveFs(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(home, "ws")
	mustWrite(t, filepath.Join(ws, "f"), "f")

	s := &Server{Home: home}
	got, err := s.resolveFs(filepath.Join(ws, "f"))
	if err != nil || got.Strict || got.Root != home || got.Rel != "ws/f" {
		t.Fatalf("nil BoxTrees: got (%+v, %v), want host ws/f", got, err)
	}
	s.BoxTrees = func() BoxTrees { return BoxTrees{Contained: []string{ws}} }
	got, err = s.resolveFs(filepath.Join(ws, "f"))
	if err != nil || !got.Strict || got.Root != ws || got.Rel != "f" {
		t.Fatalf("with BoxTrees: got (%+v, %v), want strict ws f", got, err)
	}
}
