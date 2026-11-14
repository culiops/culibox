package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// scratchFixture lays out a home with a denied data dir and, BESIDE it (not
// inside), a 0700 scratch root named like Claude Code's: claude-<uid>.
func scratchFixture(t *testing.T) (home, scratch, data string, pol fsPolicy) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(tmp, "home")
	data = filepath.Join(home, "data")
	scratch = filepath.Join(tmp, fmt.Sprintf("claude-%d", os.Getuid()))
	mustMkdirAll(t, data)
	mustWrite(t, filepath.Join(data, "cookie.secret"), "c")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	pol, err = newFsPolicy(home, BoxTrees{Denied: []string{data}}, scratch)
	if err != nil {
		t.Fatal(err)
	}
	return home, scratch, data, pol
}

func TestScratchCandidates(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	got := ScratchCandidates(env(map[string]string{"CLAUDE_CODE_TMPDIR": "/x"}), 1001, "/tmp")
	if want := []string{"/x/claude-1001", "/tmp/claude-1001"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got = ScratchCandidates(env(nil), 1001, "/var/folders/T")
	if want := []string{"/tmp/claude-1001", "/var/folders/T/claude-1001"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestScratch_ReadsFileReadOnly(t *testing.T) {
	_, scratch, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(scratch, "a", "f.log"))
	got, err := pol.resolve(filepath.Join(scratch, "a", "f.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := fsTarget{Root: scratch, Rel: "a/f.log", Abs: filepath.Join(scratch, "a/f.log"), ReadOnly: true}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestScratch_HomeStillNotReadOnly(t *testing.T) {
	home, _, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(home, "p", "a.txt"))
	got, err := pol.resolve(filepath.Join(home, "p", "a.txt"))
	if err != nil || got.ReadOnly || got.Root != home {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestScratch_TasksOutputLinkIntoHomeOpensReadOnly(t *testing.T) {
	home, scratch, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(home, "p", "agent.jsonl"))
	mustMkdirAll(t, filepath.Join(scratch, "tasks"))
	mustSymlink(t, filepath.Join(home, "p", "agent.jsonl"), filepath.Join(scratch, "tasks", "x.output"))
	got, err := pol.resolve(filepath.Join(scratch, "tasks", "x.output"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != home || got.Rel != "p/agent.jsonl" || !got.ReadOnly {
		t.Fatalf("got %+v", got)
	}
}

func TestScratch_HomeLinkIntoScratchIsReadOnly(t *testing.T) {
	home, scratch, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(scratch, "a", "f.log"))
	mustMkdirAll(t, home)
	mustSymlink(t, filepath.Join(scratch, "a"), filepath.Join(home, "s"))
	got, err := pol.resolve(filepath.Join(home, "s", "f.log"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != scratch || !got.ReadOnly {
		t.Fatalf("got %+v", got)
	}
}

func TestScratch_LinkOutRefused(t *testing.T) {
	_, scratch, _, pol := scratchFixture(t)
	mustSymlink(t, outsideFile(t), filepath.Join(scratch, "out"))
	if _, err := pol.resolve(filepath.Join(scratch, "out")); !errors.Is(err, errOutsideHome) {
		t.Fatalf("err = %v, want errOutsideHome", err)
	}
}

func TestScratch_LinkToDeniedRefused(t *testing.T) {
	_, scratch, data, pol := scratchFixture(t)
	mustSymlink(t, data, filepath.Join(scratch, "d"))
	if _, err := pol.resolve(filepath.Join(scratch, "d", "cookie.secret")); !errors.Is(err, errDenied) {
		t.Fatalf("err = %v, want errDenied", err)
	}
}

func TestScratch_DeniedInHomeIsErrDenied(t *testing.T) {
	_, _, data, pol := scratchFixture(t)
	if _, err := pol.resolve(filepath.Join(data, "cookie.secret")); !errors.Is(err, errDenied) {
		t.Fatalf("err = %v, want errDenied", err)
	}
}

func TestScratch_LookAlikeAndUncleanPaths(t *testing.T) {
	_, scratch, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(scratch, "a", "f.log"))
	mustWrite(t, scratch+"-x/f")
	if _, err := pol.resolve(scratch + "-x/f"); !errors.Is(err, errOutsideHome) {
		t.Fatalf("look-alike: err = %v", err)
	}
	if _, err := pol.resolve(scratch + "/../" + filepath.Base(scratch) + "-x/f"); !errors.Is(err, errOutsideHome) {
		t.Fatalf("climb to look-alike: err = %v", err)
	}
	got, err := pol.resolve(scratch + "//a/./f.log")
	if err != nil || !got.ReadOnly || got.Rel != "a/f.log" {
		t.Fatalf("unclean: got %+v, %v", got, err)
	}
}

func TestScratch_RootRejected(t *testing.T) {
	defer func(f func() int) { scratchUID = f }(scratchUID)
	cases := []struct {
		name  string
		setup func(t *testing.T, tmp, home string) (cand string)
	}{
		{"missing", func(t *testing.T, tmp, home string) string { return filepath.Join(tmp, "claude-x") }},
		{"world writable", func(t *testing.T, tmp, home string) string {
			c := filepath.Join(tmp, "claude-x")
			mustMkdirAll(t, c)
			mustChmod(t, c, 0o777)
			return c
		}},
		{"group writable", func(t *testing.T, tmp, home string) string {
			c := filepath.Join(tmp, "claude-x")
			mustMkdirAll(t, c)
			mustChmod(t, c, 0o770)
			return c
		}},
		{"other owner", func(t *testing.T, tmp, home string) string {
			c := filepath.Join(tmp, "claude-x")
			mustMkdirAll(t, c)
			mustChmod(t, c, 0o700)
			scratchUID = func() int { return os.Getuid() + 1 }
			return c
		}},
		{"is home", func(t *testing.T, tmp, home string) string { mustChmod(t, home, 0o700); return home }},
		{"ancestor of home", func(t *testing.T, tmp, home string) string { mustChmod(t, tmp, 0o700); return tmp }},
		{"ancestor of a denied tree", func(t *testing.T, tmp, home string) string {
			c := filepath.Join(tmp, "other")
			mustChmod(t, c, 0o700)
			return c
		}},
		{"symlink leaf", func(t *testing.T, tmp, home string) string {
			real := filepath.Join(tmp, "real")
			mustMkdirAll(t, real)
			mustChmod(t, real, 0o700)
			c := filepath.Join(tmp, "claude-x")
			mustSymlink(t, real, c)
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scratchUID = os.Getuid
			tmp, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(tmp, "home")
			other := filepath.Join(tmp, "other", "data")
			mustMkdirAll(t, home)
			mustMkdirAll(t, other)
			cand := tc.setup(t, tmp, home)
			pol, err := newFsPolicy(home, BoxTrees{Denied: []string{other}}, cand)
			if err != nil {
				t.Fatal(err)
			}
			if len(pol.scratch) != 0 {
				t.Fatalf("scratch roots = %v, want none", pol.scratch)
			}
			// Restore modes so t.TempDir cleanup can remove everything.
			_ = os.Chmod(tmp, 0o755)
		})
	}
}

func TestScratch_CandidateInsideHomeStaysHome(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	cand := filepath.Join(home, "claude-x")
	mustWrite(t, filepath.Join(cand, "f"))
	mustChmod(t, cand, 0o700)
	pol, err := newFsPolicy(home, BoxTrees{}, cand)
	if err != nil {
		t.Fatal(err)
	}
	if len(pol.scratch) != 0 {
		t.Fatalf("scratch roots = %v, want none", pol.scratch)
	}
	got, err := pol.resolve(filepath.Join(cand, "f"))
	if err != nil || got.ReadOnly || got.Root != home {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestScratch_SymlinkedParentAccepted(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	mustMkdirAll(t, home)
	realParent := filepath.Join(tmp, "private-tmp")
	real := filepath.Join(realParent, "claude-1")
	mustWrite(t, filepath.Join(real, "f.log"))
	mustChmod(t, real, 0o700)
	mustSymlink(t, realParent, filepath.Join(tmp, "tmplink"))
	cand := filepath.Join(tmp, "tmplink", "claude-1")
	pol, err := newFsPolicy(home, BoxTrees{}, cand)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pol.resolve(filepath.Join(cand, "f.log"))
	if err != nil || got.Root != real || !got.ReadOnly {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestScratch_RootAppearsAfterServerStart(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	mustMkdirAll(t, home)
	scratch := filepath.Join(tmp, "claude-1")
	s := &Server{Home: home, ScratchDirs: []string{scratch}}
	if _, err := s.resolveFs(filepath.Join(scratch, "f.log")); !errors.Is(err, errOutsideHome) && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("before: err = %v", err)
	}
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(scratch, "f.log"))
	got, err := s.resolveFs(filepath.Join(scratch, "f.log"))
	if err != nil || !got.ReadOnly {
		t.Fatalf("after: got %+v, %v", got, err)
	}
}

func TestScratch_SwapAfterResolveRefusedByRoot(t *testing.T) {
	_, scratch, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(scratch, "a", "hostname"))
	tg, err := pol.resolve(filepath.Join(scratch, "a", "hostname"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(scratch, "a")); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "/etc", filepath.Join(scratch, "a"))
	if f, err := openFsTarget(tg, 0); err == nil {
		f.Close()
		t.Fatal("open after swap succeeded; os.Root must refuse the escape")
	}
}

// The policy holds the scratch root by identity. A different directory moved
// into the same path after the snapshot is not that root, so a path string
// that still matches must not be served.
func TestScratch_SwappedRootDirIsNotTrusted(t *testing.T) {
	_, scratch, _, pol := scratchFixture(t)
	if err := os.Rename(scratch, scratch+".old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch + ".old") })
	mustWrite(t, filepath.Join(scratch, "planted.txt"))
	mustChmod(t, scratch, 0o700)
	if _, err := pol.resolve(filepath.Join(scratch, "planted.txt")); !errors.Is(err, errOutsideHome) {
		t.Fatalf("err = %v, want errOutsideHome", err)
	}
}

func TestScratch_HardlinkIsSameUserFile(t *testing.T) {
	home, scratch, _, pol := scratchFixture(t)
	mustWrite(t, filepath.Join(home, "x"), "same-user")
	if err := os.Link(filepath.Join(home, "x"), filepath.Join(scratch, "h")); err != nil {
		t.Skipf("hardlink across dirs not possible here: %v", err)
	}
	tg, err := pol.resolve(filepath.Join(scratch, "h"))
	if err != nil || !tg.ReadOnly {
		t.Fatalf("got %+v, %v", tg, err)
	}
	f, err := openFsTarget(tg, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestScratch_ContainedWorkspaceUnderScratchIsStrictReadOnly(t *testing.T) {
	home, scratch, data, _ := scratchFixture(t)
	ws := filepath.Join(scratch, "ws")
	mustWrite(t, filepath.Join(ws, "f"))
	pol, err := newFsPolicy(home, BoxTrees{Denied: []string{data}, Contained: []string{ws}}, scratch)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pol.resolve(filepath.Join(ws, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Strict || got.Root != ws || !got.ReadOnly {
		t.Fatalf("got %+v", got)
	}
}

func mustChmod(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}

// roState lists every path under dir with its content (files) or "/" (dirs),
// so a refused mutation can be checked to have changed nothing at all.
func roState(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() {
			m[p] = "/"
		} else if b, err := os.ReadFile(p); err == nil {
			m[p] = string(b)
		} else {
			m[p] = "?"
		}
		return nil
	})
	return m
}

func roFixture(t *testing.T) (s *Server, home, scratch string) {
	t.Helper()
	s, home, scratch = scratchServer(t)
	mustWrite(t, filepath.Join(scratch, "f.txt"), "orig")
	mustWrite(t, filepath.Join(scratch, "a", "b.txt"), "b")
	mustWrite(t, filepath.Join(home, "p", "a.txt"), "home")
	return s, home, scratch
}

func wantReadOnly(t *testing.T, rec *httptest.ResponseRecorder, before, after map[string]string) {
	t.Helper()
	if rec.Code != http.StatusForbidden || errBody(t, rec) != msgReadOnly {
		t.Fatalf("got %d %s, want 403 %q", rec.Code, rec.Body, msgReadOnly)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("file system changed:\nbefore %v\nafter  %v", before, after)
	}
}

func opJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestReadOnly_Write(t *testing.T) {
	s, _, scratch := roFixture(t)
	before := roState(t, scratch)
	rec := writeReq(t, s, opJSON(map[string]any{"path": filepath.Join(scratch, "f.txt"), "content": "x", "overwrite": true}))
	wantReadOnly(t, rec, before, roState(t, scratch))
}

func TestReadOnly_WriteThroughHomeLinkIntoScratch(t *testing.T) {
	s, home, scratch := roFixture(t)
	mustSymlink(t, scratch, filepath.Join(home, "s"))
	before := roState(t, scratch)
	rec := writeReq(t, s, opJSON(map[string]any{"path": filepath.Join(home, "s", "f.txt"), "content": "x", "overwrite": true}))
	wantReadOnly(t, rec, before, roState(t, scratch))
}

func TestReadOnly_WriteThroughScratchLinkIntoHome(t *testing.T) {
	s, home, scratch := roFixture(t)
	mustSymlink(t, filepath.Join(home, "p"), filepath.Join(scratch, "h"))
	before := roState(t, home)
	rec := writeReq(t, s, opJSON(map[string]any{"path": filepath.Join(scratch, "h", "a.txt"), "content": "x", "overwrite": true}))
	wantReadOnly(t, rec, before, roState(t, home))
}

func TestReadOnly_Mkdir(t *testing.T) {
	s, _, scratch := roFixture(t)
	before := roState(t, scratch)
	rec := httptest.NewRecorder()
	s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", opJSON(map[string]string{"dir": scratch, "name": "n"})))
	wantReadOnly(t, rec, before, roState(t, scratch))
}

func TestReadOnly_Rename(t *testing.T) {
	s, _, scratch := roFixture(t)
	before := roState(t, scratch)
	rec := httptest.NewRecorder()
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", opJSON(map[string]string{"path": filepath.Join(scratch, "f.txt"), "name": "g.txt"})))
	wantReadOnly(t, rec, before, roState(t, scratch))
}

func TestReadOnly_Delete(t *testing.T) {
	s, _, scratch := roFixture(t)
	before := roState(t, scratch)
	rec := httptest.NewRecorder()
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", opJSON(map[string]any{"path": filepath.Join(scratch, "f.txt")})))
	wantReadOnly(t, rec, before, roState(t, scratch))
	rec = httptest.NewRecorder()
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", opJSON(map[string]any{"path": filepath.Join(scratch, "a"), "recursive": true, "entries": 1})))
	wantReadOnly(t, rec, before, roState(t, scratch))
}

func TestReadOnly_Upload(t *testing.T) {
	s, _, scratch := roFixture(t)
	s.MaxUploadBytes = 1 << 20
	before := roState(t, scratch)
	req := uploadReq(t, scratch, [][2]string{{"u.txt", "u"}})
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	s.handleFsUpload(rec, req)
	wantReadOnly(t, rec, before, roState(t, scratch))
}

func TestReadOnly_HomeMutationsStillWork(t *testing.T) {
	s, home, _ := roFixture(t)
	rec := httptest.NewRecorder()
	s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", opJSON(map[string]string{"dir": home, "name": "n"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("mkdir in home: %d %s", rec.Code, rec.Body)
	}
}

// listJSON lists dir through handleFsList and returns the decoded body, so a test
// can tell an absent "readonly" key from a false one.
func listJSON(t *testing.T, s *Server, dir string) map[string]any {
	t.Helper()
	rec := fsGet(s.handleFsList, "/api/fs/list", dir)
	if rec.Code != http.StatusOK {
		t.Fatalf("list %s: %d %s", dir, rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// wantListReadOnly: the listing says readonly:true (the Files tree hides its
// mutation buttons from it; the server refuses the mutations regardless).
func wantListReadOnly(t *testing.T, s *Server, dir string) {
	t.Helper()
	m := listJSON(t, s, dir)
	if m["readonly"] != true {
		t.Fatalf("list %s: readonly = %v, want true (body %v)", dir, m["readonly"], m)
	}
}

func TestScratch_ListCarriesReadOnly(t *testing.T) {
	s, _, scratch := roFixture(t)
	wantListReadOnly(t, s, scratch)
	wantListReadOnly(t, s, filepath.Join(scratch, "a"))
}

func TestScratch_ListHomeHasNoReadOnly(t *testing.T) {
	s, home, _ := roFixture(t)
	m := listJSON(t, s, filepath.Join(home, "p"))
	if _, has := m["readonly"]; has {
		t.Fatalf("a home listing must omit readonly, got %v", m)
	}
	if m["path"] != filepath.Join(home, "p") {
		t.Fatalf("path = %v", m["path"])
	}
}

func TestScratch_ListThroughHomeLinkIntoScratchIsReadOnly(t *testing.T) {
	s, home, scratch := roFixture(t)
	mustSymlink(t, scratch, filepath.Join(home, "s"))
	wantListReadOnly(t, s, filepath.Join(home, "s"))
}

// Sticky, as fsresolve.go says: once the walk touched a scratch root it stays
// read-only even when a link in it leads back into $HOME.
func TestScratch_ListScratchLinkIntoHomeIsReadOnly(t *testing.T) {
	s, home, scratch := roFixture(t)
	mustSymlink(t, filepath.Join(home, "p"), filepath.Join(scratch, "h"))
	wantListReadOnly(t, s, filepath.Join(scratch, "h"))
}
