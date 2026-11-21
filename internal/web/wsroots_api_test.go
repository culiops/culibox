package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/wsroots"
)

// rootsAPIRig is a Server over a real wsroots.Service in temp dirs.
type rootsAPIRig struct {
	s         *Server
	home, dir string
	rec       *fakeCredRec
}

func newRootsAPIRig(t *testing.T) *rootsAPIRig {
	t.Helper()
	home, _, _ := rootsFixture(t)
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := &fakeCredRec{}
	svc := wsroots.NewService(data, home, RootsAudit(rec), nil)
	svc.SetScanBases(func() []string { return nil }) // never scan this machine's real /srv, /opt ...
	return &rootsAPIRig{s: &Server{Home: home, DataDir: data, Roots: svc}, home: home, dir: data, rec: rec}
}

func (g *rootsAPIRig) roots(method, body string) *httptest.ResponseRecorder {
	return do(g.s.handleWorkspaceRoots, storeReq(method, "/api/workspace-roots", body, "http://"+host, credCookie))
}

func (g *rootsAPIRig) check(path string) *httptest.ResponseRecorder {
	return do(g.s.handleWorkspaceCheck, storeReq("GET", "/api/workspace/check?path="+url.QueryEscape(path), "", "", credCookie))
}

type rootsView struct {
	Roots []struct {
		Path      string `json:"path"`
		Added     string `json:"added"`
		LastUsed  string `json:"last_used"`
		Available bool   `json:"available"`
		Always    bool   `json:"always"`
	} `json:"roots"`
	Elsewhere []struct {
		Path  string `json:"path"`
		Label string `json:"label"`
	} `json:"elsewhere"`
	Corrupt bool `json:"corrupt"`
}

func decodeRoots(t *testing.T, w *httptest.ResponseRecorder) rootsView {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var v rootsView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var e struct{ Error, Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e.Code, e.Error
}

func TestRootsAPIListAddRemove(t *testing.T) {
	g := newRootsAPIRig(t)
	extra := realTemp(t)
	v := decodeRoots(t, g.roots("GET", ""))
	if len(v.Roots) != 1 || v.Roots[0].Path != g.home || !v.Roots[0].Always || !v.Roots[0].Available {
		t.Fatalf("initial list = %+v, want home alone, always, available", v.Roots)
	}
	if v.Elsewhere == nil || len(v.Elsewhere) != 0 || v.Corrupt {
		t.Fatalf("elsewhere/corrupt = %v/%v, want [] and false", v.Elsewhere, v.Corrupt)
	}
	body, _ := json.Marshal(map[string]string{"path": extra})
	v = decodeRoots(t, g.roots("POST", string(body)))
	if len(v.Roots) != 2 || v.Roots[1].Path != extra || v.Roots[1].Always || !v.Roots[1].Available || v.Roots[1].Added == "" {
		t.Fatalf("after add = %+v", v.Roots)
	}
	v = decodeRoots(t, g.roots("GET", ""))
	if len(v.Roots) != 2 {
		t.Fatalf("a fresh GET lost the root: %+v", v.Roots)
	}
	if w := g.roots("DELETE", string(body)); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if v = decodeRoots(t, g.roots("GET", "")); len(v.Roots) != 1 {
		t.Fatalf("after delete = %+v", v.Roots)
	}
	w := g.roots("DELETE", string(body))
	if w.Code != 404 {
		t.Fatalf("delete of a missing root: %d %s", w.Code, w.Body)
	}
	if w := g.roots("PUT", ""); w.Code != 405 {
		t.Fatalf("PUT: %d", w.Code)
	}
}

func TestRootsAPIBearerRefused(t *testing.T) {
	g := newRootsAPIRig(t)
	extra := realTemp(t)
	for _, m := range []string{"POST", "DELETE"} {
		w := do(g.s.handleWorkspaceRoots, storeReq(m, "/api/workspace-roots", `{"path":"`+extra+`"}`, "http://"+host, credBearer))
		if w.Code != 403 {
			t.Errorf("%s with a bearer: %d %s", m, w.Code, w.Body)
		}
		if _, msg := errCode(t, w); msg != "changing workspace roots needs this page's signed-in browser session" {
			t.Errorf("%s message = %q", m, msg)
		}
	}
	if len(g.s.Roots.Paths()) != 0 || len(g.rec.events) != 0 {
		t.Fatal("a refused write reached the service")
	}
	if w := do(g.s.handleWorkspaceRoots, storeReq("GET", "/api/workspace-roots", "", "", credBearer)); w.Code != 200 {
		t.Fatalf("a bearer may read: %d", w.Code)
	}
}

func TestRootsAPICrossOriginRefused(t *testing.T) {
	g := newRootsAPIRig(t)
	extra := realTemp(t)
	for _, origin := range []string{"https://evil.example", ""} {
		w := do(g.s.handleWorkspaceRoots, storeReq("POST", "/api/workspace-roots", `{"path":"`+extra+`"}`, origin, credCookie))
		if w.Code != 403 {
			t.Errorf("origin %q: %d %s", origin, w.Code, w.Body)
		}
	}
	if len(g.s.Roots.Paths()) != 0 {
		t.Fatal("a cross-origin write reached the service")
	}
}

func TestRootsAPIBodyStrict(t *testing.T) {
	g := newRootsAPIRig(t)
	for _, b := range []string{`{"path":"/x","extra":1}`, `{"path":"/x"} {}`, `nope`, `{"path":"` + strings.Repeat("a", 17<<10) + `"}`} {
		for _, m := range []string{"POST", "DELETE"} {
			if w := g.roots(m, b); w.Code != 400 {
				t.Errorf("%s body %.30q: %d", m, b, w.Code)
			}
		}
	}
}

func TestRootsAPIRefusedEtc(t *testing.T) {
	g := newRootsAPIRig(t)
	w := g.roots("POST", `{"path":"/etc"}`)
	if w.Code != 400 {
		t.Fatalf("/etc: %d %s", w.Code, w.Body)
	}
	if code, msg := errCode(t, w); code != "refused" || msg == "" {
		t.Fatalf("code %q error %q, want refused with a reason", code, msg)
	}
}

func TestRootsAPIErrorCodes(t *testing.T) {
	g := newRootsAPIRig(t)
	extra := realTemp(t)
	post := func(p string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"path": p})
		return g.roots("POST", string(b))
	}
	if w := post(extra); w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if w := post(filepath.Join(extra, "sub")); w.Code != 400 {
		t.Fatalf("missing folder: %d %s", w.Code, w.Body)
	} else if code, _ := errCode(t, w); code != "not_dir" {
		t.Fatalf("missing folder code %q", code)
	}
	if err := os.Mkdir(filepath.Join(extra, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if w := post(filepath.Join(extra, "sub")); w.Code != 409 {
		t.Fatalf("covered: %d %s", w.Code, w.Body)
	} else if code, _ := errCode(t, w); code != "covered" {
		t.Fatalf("covered code %q", code)
	}
	if w := post(g.home); w.Code != 409 {
		t.Fatalf("home: %d %s", w.Code, w.Body)
	} else if code, _ := errCode(t, w); code != "covered" {
		t.Fatalf("home code %q", code)
	}
	for i := 0; i < wsroots.MaxRoots-1; i++ {
		if w := post(realTemp(t)); w.Code != 200 {
			t.Fatalf("add %d: %d %s", i, w.Code, w.Body)
		}
	}
	w := post(realTemp(t))
	if code, _ := errCode(t, w); w.Code != 409 || code != "cap" {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
}

func TestRootsAPIAuditFailureNoRoot(t *testing.T) {
	g := newRootsAPIRig(t)
	g.rec.err = errors.New("audit store: disk full")
	w := g.roots("POST", `{"path":"`+realTemp(t)+`"}`)
	if w.Code < 500 {
		t.Fatalf("a failed audit must fail the add: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "disk full") {
		t.Fatalf("raw error text reached the page: %s", w.Body)
	}
	if len(g.s.Roots.Paths()) != 0 {
		t.Fatal("a root was saved without its audit row")
	}
}

func TestRootsAPICorrupt(t *testing.T) {
	g := newRootsAPIRig(t)
	if err := os.WriteFile(filepath.Join(g.dir, wsroots.FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := decodeRoots(t, g.roots("GET", ""))
	if !v.Corrupt || len(v.Roots) != 1 || !v.Roots[0].Always {
		t.Fatalf("corrupt view = %+v", v)
	}
	w := g.roots("POST", `{"path":"`+realTemp(t)+`"}`)
	if code, _ := errCode(t, w); w.Code != 409 || code != "list_corrupt" {
		t.Fatalf("add on a corrupt file: %d %s", w.Code, w.Body)
	}
	w = g.roots("DELETE", `{"path":"/x"}`)
	if code, _ := errCode(t, w); w.Code != 409 || code != "list_corrupt" {
		t.Fatalf("remove on a corrupt file: %d %s", w.Code, w.Body)
	}
}

func TestRootsListMarksUnavailable(t *testing.T) {
	g := newRootsAPIRig(t)
	g.s.rootHealth.ttl = 20 * time.Millisecond
	gone := realTemp(t)
	if w := g.roots("POST", `{"path":"`+gone+`"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	// The POST reply checked the root; the next check runs once that answer is
	// older than the health TTL, and a root last seen live keeps its answer
	// until that re-check answers.
	time.Sleep(30 * time.Millisecond)
	var v rootsView
	for i := 0; i < 100; i++ {
		if v = decodeRoots(t, g.roots("GET", "")); len(v.Roots) == 2 && !v.Roots[1].Available {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(v.Roots) != 2 || v.Roots[1].Available || !v.Roots[0].Available {
		t.Fatalf("a removed folder must show not available: %+v", v.Roots)
	}
	// Still removable.
	if w := g.roots("DELETE", `{"path":"`+gone+`"}`); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
}

func TestRootsListMarksRedirectedUnavailable(t *testing.T) {
	g := newRootsAPIRig(t)
	link := filepath.Join(realTemp(t), "link")
	target := realTemp(t)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	writeRootsFile(t, g.dir, link)
	v := decodeRoots(t, g.roots("GET", ""))
	if len(v.Roots) != 2 || v.Roots[1].Available {
		t.Fatalf("a root now behind a symlink must show not available: %+v", v.Roots)
	}
}

func writeRootsFile(t *testing.T, dir string, paths ...string) {
	t.Helper()
	rows := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		rows = append(rows, map[string]any{"path": p, "added": "2026-11-21T00:00:00Z", "added_by": "web"})
	}
	b, err := json.Marshal(map[string]any{"version": 1, "roots": rows})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, wsroots.FileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRootsAuditRow(t *testing.T) {
	g := newRootsAPIRig(t)
	extra := realTemp(t)
	g.roots("POST", `{"path":"`+extra+`"}`)
	g.roots("DELETE", `{"path":"`+extra+`"}`)
	want := []broker.CredEvent{
		{Action: broker.CredCreated, Kind: "workspace_root", Name: extra, Actor: storeActor},
		{Action: broker.CredDeleted, Kind: "workspace_root", Name: extra, Actor: storeActor},
	}
	if len(g.rec.events) != 2 || g.rec.events[0] != want[0] || g.rec.events[1] != want[1] {
		t.Fatalf("audit rows = %+v, want %+v", g.rec.events, want)
	}
}

// A forbidden root written by hand into the file is dropped by the policy and
// shown as not available (spec §9, end to end through a real Service).
func TestRootsHandWrittenForbiddenRootEndToEnd(t *testing.T) {
	g := newRootsAPIRig(t)
	ok := realTemp(t)
	writeRootsFile(t, g.dir, "/etc", ok)
	// Control: the same file's harmless root is served, so the 403 below is the
	// forbidden-root drop and not "outside every root".
	rr := httptest.NewRecorder()
	g.s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path="+url.QueryEscape(ok), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("control root = %d (%s), want 200", rr.Code, rr.Body)
	}
	rr = httptest.NewRecorder()
	g.s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path=/etc", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("GET /api/fs/list?path=/etc = %d (%s), want 403", rr.Code, rr.Body)
	}
	v := decodeRoots(t, g.roots("GET", ""))
	if len(v.Roots) != 3 || v.Roots[1].Path != "/etc" || v.Roots[1].Available || !v.Roots[2].Available {
		t.Fatalf("roots = %+v, want /etc listed and not available, the control available", v.Roots)
	}
}

// A root that is, sits in or holds CuliBox's data folder reads "not available": the Files policy drops it
// (TestPolicyDropsRootsTouchingDenied), so the list says what Files shows.
func TestRootsListMarksDataDirUnavailable(t *testing.T) {
	g := newRootsAPIRig(t)
	inside := filepath.Join(g.dir, "sub")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := realTemp(t)
	writeRootsFile(t, g.dir, g.dir, inside, ok)
	v := decodeRoots(t, g.roots("GET", ""))
	if len(v.Roots) != 4 || v.Roots[1].Available || v.Roots[2].Available || !v.Roots[3].Available {
		t.Fatalf("roots = %+v, want the data folder and a folder in it not available, the control available", v.Roots)
	}
}

type checkResp struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	IsDir       bool   `json:"is_dir"`
	Root        string `json:"root"`
	WouldAdd    bool   `json:"would_add"`
	AddRootPath string `json:"add_root_path"`
	Refused     string `json:"refused"`
	Box         struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
		Code   string `json:"code"`
	} `json:"box"`
	VMShared bool `json:"vm_shared"`
}

func decodeCheck(t *testing.T, w *httptest.ResponseRecorder) checkResp {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var c checkResp
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkspaceCheckWouldAdd(t *testing.T) {
	g := newRootsAPIRig(t)
	out := realTemp(t)
	c := decodeCheck(t, g.check(out))
	if c.Path != out || !c.Exists || !c.IsDir || c.Root != "" || !c.WouldAdd || c.AddRootPath != out || c.Refused != "" || !c.Box.OK || !c.VMShared {
		t.Fatalf("outside every root: %+v", c)
	}
	// Under home: covered, nothing new, and (R2) no refusal line.
	sub := filepath.Join(g.home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	c = decodeCheck(t, g.check(sub))
	if c.Root != g.home || c.WouldAdd || c.Refused != "" {
		t.Fatalf("under home: %+v", c)
	}
	// Under an added root.
	if _, err := g.s.Roots.Add(out, "web"); err != nil {
		t.Fatal(err)
	}
	c = decodeCheck(t, g.check(out))
	if c.Root != out || c.WouldAdd || c.Refused != "" {
		t.Fatalf("under an added root: %+v", c)
	}
	// Missing and not-a-directory.
	c = decodeCheck(t, g.check(filepath.Join(realTemp(t), "nope")))
	if c.Exists || c.IsDir || c.WouldAdd {
		t.Fatalf("missing: %+v", c)
	}
	f := filepath.Join(realTemp(t), "f")
	mustWrite(t, f, "x")
	c = decodeCheck(t, g.check(f))
	if !c.Exists || c.IsDir || c.WouldAdd {
		t.Fatalf("a file: %+v", c)
	}
}

func TestWorkspaceCheckRepoTop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	g := newRootsAPIRig(t)
	top := realTemp(t)
	if out, err := exec.Command("git", "-C", top, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	sub := filepath.Join(top, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	c := decodeCheck(t, g.check(sub))
	if c.Path != sub || c.AddRootPath != top || !c.WouldAdd {
		t.Fatalf("picked subfolder of a repo: %+v, want add_root_path %s", c, top)
	}
}

func TestWorkspaceCheckNormalises(t *testing.T) {
	g := newRootsAPIRig(t)
	base := realTemp(t)
	for _, d := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(base, "b")
	for _, in := range []string{want + "/", filepath.Join(base, "a") + "/../b"} {
		if c := decodeCheck(t, g.check(in)); c.Path != want || !c.IsDir {
			t.Errorf("check(%q).path = %q, want %q", in, c.Path, want)
		}
	}
	link := filepath.Join(realTemp(t), "ln")
	if err := os.Symlink(want, link); err != nil {
		t.Fatal(err)
	}
	if c := decodeCheck(t, g.check(link)); c.Path != want {
		t.Errorf("a symlink resolves: path %q, want %q", c.Path, want)
	}
	for _, bad := range []string{"relative/dir", "", "~/x", "/tmp/a\x00b"} {
		if w := g.check(bad); w.Code != 400 {
			t.Errorf("check(%q) = %d, want 400", bad, w.Code)
		}
	}
	if w := do(g.s.handleWorkspaceCheck, storeReq("POST", "/api/workspace/check?path=/tmp", "", "", credCookie)); w.Code != 405 {
		t.Errorf("POST = %d, want 405", w.Code)
	}
}

func TestWorkspaceCheckForbidden(t *testing.T) {
	g := newRootsAPIRig(t)
	c := decodeCheck(t, g.check("/etc"))
	if c.Refused == "" || c.WouldAdd || c.Root != "" {
		t.Fatalf("/etc: %+v", c)
	}
	if c.Box.OK || c.Box.Reason == "" || c.Box.Code == "not_shared" {
		t.Fatalf("/etc box = %+v, want refused and not a share problem", c.Box)
	}
	// The data directory is refused too.
	c = decodeCheck(t, g.check(g.dir))
	if c.Refused == "" || c.WouldAdd {
		t.Fatalf("data dir: %+v", c)
	}
	// A credential folder under home is covered by home: R2, no refusal line.
	if err := os.Mkdir(filepath.Join(g.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	c = decodeCheck(t, g.check(filepath.Join(g.home, ".ssh")))
	if c.Root != g.home || c.Refused != "" || c.WouldAdd {
		t.Fatalf("~/.ssh: %+v", c)
	}
}

func TestWorkspaceCheckBoxShareCode(t *testing.T) {
	g := newRootsAPIRig(t)
	out := realTemp(t)
	shared := realTemp(t)
	g.s.Shares = func() (box.ShareView, error) {
		return box.ShareView{Applied: []box.VMShare{{Path: shared, Writable: true}}}, nil
	}
	c := decodeCheck(t, g.check(out))
	if c.Box.OK || c.Box.Code != "not_shared" || c.VMShared {
		t.Fatalf("an unshared folder: %+v", c)
	}
	c = decodeCheck(t, g.check(shared))
	if !c.Box.OK || c.Box.Code != "" || !c.VMShared {
		t.Fatalf("a shared folder: %+v", c)
	}
}

func TestRootsRoutesOnlyWithService(t *testing.T) {
	get := func(s *Server, tok, path string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "example.com"
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	with, tok := routedServer(t)
	with.Roots = fakeRoots{}
	for _, p := range []string{"/api/workspace-roots", "/api/workspace/check?path=/tmp"} {
		if c := get(with, tok, p); c != 200 {
			t.Errorf("with a service %s: %d", p, c)
		}
	}
	without, tok := routedServer(t)
	for _, p := range []string{"/api/workspace-roots", "/api/workspace/check?path=/tmp"} {
		if c := get(without, tok, p); c != 404 {
			t.Errorf("without a service %s: %d", p, c)
		}
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The home row is the resolved home, the spelling Covering and the check use,
// even when the configured home is reached through a symlink.
func TestRootsListShowsResolvedHome(t *testing.T) {
	real := realTemp(t)
	link := filepath.Join(realTemp(t), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	data := realTemp(t)
	svc := wsroots.NewService(data, real, RootsAudit(&fakeCredRec{}), nil)
	svc.SetScanBases(func() []string { return nil })
	s := &Server{Home: link, DataDir: data, Roots: svc}
	v := decodeRoots(t, do(s.handleWorkspaceRoots, storeReq("GET", "/api/workspace-roots", "", "", credCookie)))
	if len(v.Roots) != 1 || v.Roots[0].Path != real || !v.Roots[0].Always || !v.Roots[0].Available {
		t.Fatalf("list = %+v, want the resolved home %s", v.Roots, real)
	}
}

// scanSpy counts the scans a request asks for and answers with a fixed list.
type scanSpy struct {
	WorkspaceRoots
	n      int
	places []wsroots.Place
}

func (s *scanSpy) Elsewhere(context.Context) []wsroots.Place { s.n++; return s.places }

// GET lists the scan's places, minus any home or a root already reaches; it
// is the only request that scans, and a reply with none is [] and not null.
func TestRootsAPIElsewhere(t *testing.T) {
	g := newRootsAPIRig(t)
	root := realTemp(t)
	if _, err := g.s.Roots.Add(root, storeActor); err != nil {
		t.Fatal(err)
	}
	spy := &scanSpy{WorkspaceRoots: g.s.Roots, places: []wsroots.Place{
		{Path: "/srv/api", Label: "api"},
		{Path: filepath.Join(g.home, "inside-home"), Label: "inside-home"},
		{Path: filepath.Join(root, "inside-root"), Label: "inside-root"},
		{Path: root, Label: "root-itself"},
		{Path: "/opt/web", Label: "web"},
	}}
	g.s.Roots = spy
	v := decodeRoots(t, g.roots("GET", ""))
	if len(v.Elsewhere) != 2 || v.Elsewhere[0].Path != "/srv/api" || v.Elsewhere[0].Label != "api" || v.Elsewhere[1].Path != "/opt/web" {
		t.Fatalf("elsewhere = %+v, want /srv/api and /opt/web only", v.Elsewhere)
	}
	if spy.n != 1 {
		t.Fatalf("a GET scanned %d times, want 1", spy.n)
	}
	// A write reply does not scan.
	extra := realTemp(t)
	body, _ := json.Marshal(map[string]string{"path": extra})
	w := g.roots("POST", string(body))
	v = decodeRoots(t, w)
	if spy.n != 1 || v.Elsewhere == nil || len(v.Elsewhere) != 0 || !strings.Contains(w.Body.String(), `"elsewhere":[]`) {
		t.Fatalf("a POST reply scanned (%d) or carried %s", spy.n, w.Body)
	}
	spy.places = nil
	if w := g.roots("GET", ""); !strings.Contains(w.Body.String(), `"elsewhere":[]`) {
		t.Fatalf("no places must be [], got %s", w.Body)
	}
}

// A device token may read the roots list but never triggers or receives the scan.
func TestRootsAPIBearerGetDoesNotScan(t *testing.T) {
	g := newRootsAPIRig(t)
	spy := &scanSpy{WorkspaceRoots: g.s.Roots, places: []wsroots.Place{{Path: "/srv/api", Label: "api"}}}
	g.s.Roots = spy
	w := do(g.s.handleWorkspaceRoots, storeReq("GET", "/api/workspace-roots", "", "", credBearer))
	v := decodeRoots(t, w)
	if spy.n != 0 || v.Elsewhere == nil || len(v.Elsewhere) != 0 || !strings.Contains(w.Body.String(), `"elsewhere":[]`) {
		t.Fatalf("bearer GET: %d scans, body %s", spy.n, w.Body)
	}
	if len(v.Roots) != 1 || !v.Roots[0].Always {
		t.Fatalf("the roots list must still be readable: %+v", v.Roots)
	}
	if v := decodeRoots(t, g.roots("GET", "")); len(v.Elsewhere) != 1 || spy.n != 1 {
		t.Fatalf("a cookie GET must still scan: %+v, %d", v.Elsewhere, spy.n)
	}
}
