package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/wsroots"
)

type spawnRootRig struct {
	a    *API
	m    *mockSvc
	svc  *wsroots.Service
	rec  *fakeCredRec
	home string
	data string
}

func newSpawnRootRig(t *testing.T, info session.Info) *spawnRootRig {
	t.Helper()
	home := realTemp(t)
	rec := &fakeCredRec{}
	data := realTemp(t)
	svc := wsroots.NewService(data, home, RootsAudit(rec), nil)
	m := &mockSvc{spawnInfo: info}
	return &spawnRootRig{a: &API{Sessions: m, Roots: svc}, m: m, svc: svc, rec: rec, home: home, data: data}
}

// browserSpawn is the New session form's request: a cookie session, same origin.
func browserSpawn(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(body))
	r.Host = host
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r.WithContext(context.WithValue(r.Context(), credKindKey, credCookie))
}

type spawnResp struct {
	ID   string `json:"id"`
	Root *struct {
		Added  bool   `json:"added"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"root"`
}

func (g *spawnRootRig) post(t *testing.T, r *http.Request) (int, spawnResp, string) {
	t.Helper()
	w := httptest.NewRecorder()
	g.a.handleSessions(w, r)
	var got spawnResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", w.Body, err)
	}
	return w.Code, got, w.Body.String()
}

func (g *spawnRootRig) rootList(t *testing.T) []string {
	t.Helper()
	return g.svc.Paths()
}

func TestSpawnAddRootFromBrowser(t *testing.T) {
	dir := realTemp(t)
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true}`))
	if code != http.StatusCreated {
		t.Fatalf("code %d %s", code, body)
	}
	if got.Root == nil || !got.Root.Added || got.Root.Path != dir || got.Root.Reason != "" {
		t.Fatalf("root = %+v (%s)", got.Root, body)
	}
	if g.svc.Covering(dir) != dir {
		t.Fatalf("folder is not a root: %v", g.rootList(t))
	}
	if len(g.rec.events) != 1 || g.rec.events[0].Action != "created" || g.rec.events[0].Kind != "workspace_root" ||
		g.rec.events[0].Name != dir || g.rec.events[0].Actor != storeActor {
		t.Fatalf("audit = %+v", g.rec.events)
	}
}

func TestSpawnAddRootUsesRepoTop(t *testing.T) {
	top := realTemp(t)
	sub := filepath.Join(top, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: sub, WorkspaceRoot: top})
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+sub+`","add_root":true}`))
	if code != http.StatusCreated || got.Root == nil || !got.Root.Added || got.Root.Path != top {
		t.Fatalf("code %d root %+v (%s)", code, got.Root, body)
	}
	if g.svc.Covering(top) != top {
		t.Fatalf("repo top is not a root: %v", g.rootList(t))
	}
}

func TestSpawnAddRootIgnoredForBearer(t *testing.T) {
	dir := realTemp(t)
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	r := browserSpawn(`{"agent":"claude","cwd":"` + dir + `","add_root":true}`)
	r = r.WithContext(context.WithValue(r.Context(), credKindKey, credBearer))
	code, got, body := g.post(t, r)
	if code != http.StatusCreated || got.Root != nil || strings.Contains(body, `"root"`) {
		t.Fatalf("code %d body %s", code, body)
	}
	if len(g.rootList(t)) != 0 || len(g.rec.events) != 0 {
		t.Fatalf("a bearer request added a root: %v %+v", g.rootList(t), g.rec.events)
	}
}

func TestSpawnAddRootIgnoredCrossOrigin(t *testing.T) {
	dir := realTemp(t)
	for name, mod := range map[string]func(*http.Request){
		"cross-site":   func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"other origin": func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"no origin":    func(r *http.Request) { r.Header.Del("Origin") },
	} {
		g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
		r := browserSpawn(`{"agent":"claude","cwd":"` + dir + `","add_root":true}`)
		mod(r)
		code, got, body := g.post(t, r)
		if code != http.StatusCreated || got.Root != nil {
			t.Fatalf("%s: code %d body %s", name, code, body)
		}
		if len(g.rootList(t)) != 0 || len(g.rec.events) != 0 {
			t.Fatalf("%s: a root was added: %v", name, g.rootList(t))
		}
	}
}

func TestSpawnAddRootAbsentOrFalseAddsNothing(t *testing.T) {
	dir := realTemp(t)
	for _, body := range []string{`{"agent":"claude","cwd":"` + dir + `"}`, `{"agent":"claude","cwd":"` + dir + `","add_root":false}`} {
		g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
		code, got, _ := g.post(t, browserSpawn(body))
		if code != http.StatusCreated || got.Root != nil || len(g.rootList(t)) != 0 {
			t.Fatalf("%s: code %d root %+v roots %v", body, code, got.Root, g.rootList(t))
		}
	}
}

func TestSpawnAddRootForbiddenStillSpawns(t *testing.T) {
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: "/etc"})
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","runtime":"host","cwd":"/etc","add_root":true}`))
	if code != http.StatusCreated || got.ID != "s1" {
		t.Fatalf("code %d %s", code, body)
	}
	if got.Root == nil || got.Root.Added || got.Root.Reason == "" {
		t.Fatalf("root = %+v (%s)", got.Root, body)
	}
	if len(g.rootList(t)) != 0 || len(g.rec.events) != 0 {
		t.Fatalf("a forbidden folder became a root: %v", g.rootList(t))
	}
}

func TestSpawnCoveredNoRootField(t *testing.T) {
	extra := realTemp(t)
	sub := filepath.Join(extra, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: sub})
	if _, err := g.svc.Add(extra, storeActor); err != nil {
		t.Fatal(err)
	}
	g.rec.events = nil
	cases := map[string]session.Info{
		"under a root": {ID: "s1", Cwd: sub},
		"home itself":  {ID: "s1", Cwd: g.home},
		"under home":   {ID: "s1", Cwd: g.home, WorkspaceRoot: g.home},
	}
	for name, info := range cases {
		g.m.spawnInfo = info
		code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+info.Cwd+`","add_root":true}`))
		if code != http.StatusCreated || got.Root != nil || strings.Contains(body, `"root"`) {
			t.Fatalf("%s: code %d body %s", name, code, body)
		}
	}
	if len(g.rec.events) != 0 || len(g.rootList(t)) != 1 {
		t.Fatalf("a covered folder changed the list: %v %+v", g.rootList(t), g.rec.events)
	}
}

// A folder inside a root whose repo top lies above that root must not widen the
// list to the repo top: the spec adds only when the cwd is outside every root.
func TestSpawnCoveredCwdDoesNotPromoteRepoTop(t *testing.T) {
	top := realTemp(t)
	inner := filepath.Join(top, "a", "b")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: inner, WorkspaceRoot: top})
	if _, err := g.svc.Add(filepath.Join(top, "a"), storeActor); err != nil {
		t.Fatal(err)
	}
	g.rec.events = nil
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+inner+`","add_root":true}`))
	if code != http.StatusCreated || got.Root != nil || g.svc.Covering(top) == top || len(g.rec.events) != 0 {
		t.Fatalf("code %d body %s roots %v", code, body, g.rootList(t))
	}
}

func TestSpawnFailedNoAdd(t *testing.T) {
	dir := realTemp(t)
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	g.m.spawnErr = errors.New("boom")
	code, _, body := func() (int, spawnResp, string) {
		w := httptest.NewRecorder()
		g.a.handleSessions(w, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true}`))
		return w.Code, spawnResp{}, w.Body.String()
	}()
	if code != http.StatusBadRequest || strings.Contains(body, `"root"`) {
		t.Fatalf("code %d body %s", code, body)
	}
	if len(g.rootList(t)) != 0 || len(g.rec.events) != 0 {
		t.Fatalf("a failed spawn added a root: %v", g.rootList(t))
	}
}

// R3: a clone-mode spawn (the request, or a session that is one) never adds.
func TestSpawnAddRootSkippedForClone(t *testing.T) {
	dir := realTemp(t)
	for name, c := range map[string]struct {
		body string
		info session.Info
	}{
		"clone requested": {`{"agent":"claude","cwd":"` + dir + `","add_root":true,"clone":true}`, session.Info{ID: "s1", Cwd: dir}},
		"clone session":   {`{"agent":"claude","cwd":"` + dir + `","add_root":true}`, session.Info{ID: "s1", Cwd: dir, CloneOf: "/some/repo"}},
	} {
		g := newSpawnRootRig(t, c.info)
		code, got, body := g.post(t, browserSpawn(c.body))
		if code != http.StatusCreated || got.Root != nil || strings.Contains(body, `"root"`) {
			t.Fatalf("%s: code %d body %s", name, code, body)
		}
		if len(g.rootList(t)) != 0 || len(g.rec.events) != 0 {
			t.Fatalf("%s: a root was added: %v", name, g.rootList(t))
		}
	}
	// clone:false is an ordinary spawn.
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	_, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true,"clone":false}`))
	if got.Root == nil || !got.Root.Added {
		t.Fatalf("clone:false should add: %s", body)
	}
}

func TestSpawnAddRootFailureReasons(t *testing.T) {
	// Audit failure: no root, a fixed phrase, and the raw error stays out of the body.
	dir := realTemp(t)
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	g.rec.err = errors.New("disk /secret/path is full")
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true}`))
	if code != http.StatusCreated || got.Root == nil || got.Root.Added ||
		got.Root.Reason != "the audit log could not record it" || strings.Contains(body, "secret") {
		t.Fatalf("audit failure: code %d body %s", code, body)
	}
	if len(g.rootList(t)) != 0 {
		t.Fatalf("a failed audit saved a root: %v", g.rootList(t))
	}

	// Cap.
	g = newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	for i := 0; i < wsroots.MaxRoots; i++ {
		if _, err := g.svc.Add(realTemp(t), storeActor); err != nil {
			t.Fatal(err)
		}
	}
	_, got, body = g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true}`))
	if got.Root == nil || got.Root.Added || got.Root.Reason != "the 32-root limit is reached" {
		t.Fatalf("cap: %s", body)
	}

	// A damaged file.
	g = newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	if _, err := g.svc.Add(realTemp(t), storeActor); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.data, wsroots.FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, got, body = g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true}`))
	if got.Root == nil || got.Root.Added || got.Root.Reason != "workspace-roots.json is damaged" {
		t.Fatalf("corrupt: %s", body)
	}
}

// Every Start under a root is a use of it, even one that adds nothing (a
// bearer request with add_root): the root's LastUsed is set.
func TestSpawnTouchesCwdAlways(t *testing.T) {
	dir := realTemp(t)
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	if _, err := g.svc.Add(dir, storeActor); err != nil {
		t.Fatal(err)
	}
	r := browserSpawn(`{"agent":"claude","cwd":"` + dir + `","add_root":true}`)
	r = r.WithContext(context.WithValue(r.Context(), credKindKey, credBearer))
	g.post(t, r)
	list, err := g.svc.List()
	if err != nil || len(list) != 1 || list[0].LastUsed.IsZero() {
		t.Fatalf("a bearer Start under a root did not set its LastUsed: %+v, %v", list, err)
	}
}

// I1: a cwd typed through a symlink names the same folder as its resolved
// spelling. A folder a root already covers must not widen the list to its repo
// top, and the covering root's LastUsed must be set.
func TestSpawnSymlinkedCwdCoveredNoWiden(t *testing.T) {
	repo := realTemp(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(realTemp(t), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(link, "sub")
	// The session reports the cwd as typed (filepath.Abs, not resolved) and the
	// repo top resolved, as session.Spawn does.
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: typed, WorkspaceRoot: repo})
	if _, err := g.svc.Add(sub, storeActor); err != nil {
		t.Fatal(err)
	}
	g.rec.events = nil
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+typed+`","add_root":true}`))
	if code != http.StatusCreated || got.Root != nil {
		t.Fatalf("code %d body %s", code, body)
	}
	list, err := g.svc.List()
	if err != nil || len(list) != 1 || list[0].Path != sub || len(g.rec.events) != 0 {
		t.Fatalf("a covered cwd typed through a link changed the list: %+v %v %+v", list, err, g.rec.events)
	}
	if list[0].LastUsed.IsZero() {
		t.Fatalf("the covering root %q has no LastUsed after a Start through a link", sub)
	}
}

func TestSpawnWithoutRootsServiceUnchanged(t *testing.T) {
	m := &mockSvc{spawnInfo: session.Info{ID: "s1", Cwd: "/w"}}
	a := &API{Sessions: m}
	w := httptest.NewRecorder()
	a.handleSessions(w, browserSpawn(`{"agent":"claude","cwd":"/w","add_root":true}`))
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"root"`) {
		t.Fatalf("code %d %s", w.Code, w.Body)
	}
}

// A root a browser Start adds is also that Start's use: its LastUsed is set,
// not left at "never" while its first session runs.
func TestSpawnAddedRootIsTouched(t *testing.T) {
	dir := realTemp(t)
	g := newSpawnRootRig(t, session.Info{ID: "s1", Cwd: dir})
	code, got, body := g.post(t, browserSpawn(`{"agent":"claude","cwd":"`+dir+`","add_root":true}`))
	if code != http.StatusCreated || got.Root == nil || !got.Root.Added {
		t.Fatalf("code %d root %+v (%s)", code, got.Root, body)
	}
	list, err := g.svc.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if list[0].LastUsed.IsZero() {
		t.Fatalf("added root %q has no LastUsed after its first Start", list[0].Path)
	}
}
