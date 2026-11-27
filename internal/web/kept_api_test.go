package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// keptDocker is the docker a kept-folder test's box manager sees: no
// container but the running ones.
type keptDocker struct {
	mu      sync.Mutex
	running map[string]bool
	stopped []string
	stopErr error // every Stop fails with it, the box left running
}

func (d *keptDocker) Run(context.Context, []string) (string, error)    { return "", nil }
func (d *keptDocker) Stdout(context.Context, []string) (string, error) { return "", nil }
func (d *keptDocker) Inspect(_ context.Context, name string) (box.State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running[name] {
		return box.StateRunning, nil
	}
	return box.StateMissing, nil
}

// ListBoxes lists the running boxes, as docker ps -a would.
func (d *keptDocker) ListBoxes(context.Context, string) ([]box.BoxInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []box.BoxInfo
	for n := range d.running {
		out = append(out, box.BoxInfo{Name: n, State: "running"})
	}
	return out, nil
}
func (d *keptDocker) ListByLabel(context.Context, string) ([]string, error) { return nil, nil }
func (d *keptDocker) Stop(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopErr != nil {
		return d.stopErr
	}
	d.stopped = append(d.stopped, name)
	delete(d.running, name)
	return nil
}
func (d *keptDocker) HasRuntime(context.Context, string) (bool, error) { return false, nil }
func (d *keptDocker) InspectMounts(ctx context.Context, name string) (box.State, []string, box.BoxLabels, error) {
	st, err := d.Inspect(ctx, name)
	return st, nil, box.BoxLabels{}, err
}
func (d *keptDocker) BindSource(context.Context, string, string) (string, error) { return "", nil }
func (d *keptDocker) BindSources(context.Context, string) (map[string]string, error) {
	return nil, nil
}

const keptAPIBox = "ab-box-0123456789ab"

// keptAPI is a Server with profiles p and q over a real box manager, and the
// store of profile p for workspace /w, whose .culiops holds a.txt. It returns
// the server, the store's key, its directory, and the docker fake.
type keptAPIFixture struct {
	s      *Server
	m      *box.Manager
	key    string
	store  string
	docker *keptDocker
	root   string
	audit  *keptAudit
}

// keptAudit is what the box manager hands its audit hook.
type keptAudit struct {
	mu   sync.Mutex
	rows [][4]string // actor, action, name, detail
}

func (a *keptAudit) record(actor, action, name, detail string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, [4]string{actor, action, name, detail})
	return nil
}

func (a *keptAudit) all() [][4]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][4]string(nil), a.rows...)
}

func keptAPI(t *testing.T) keptAPIFixture {
	t.Helper()
	root := t.TempDir()
	docker := &keptDocker{running: map[string]bool{}}
	audit := &keptAudit{}
	m, err := box.New(box.Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: "TEST", StateRoot: root, Client: docker,
		Audit: audit.record})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.WaitKeptPasses) // the keep pass of a box a forget removed
	pm := NewProfileManager([]config.AgentConfig{{Name: "p", Bin: "claude"}, {Name: "q", Bin: "claude"}}, nil, "", nil,
		newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: pm, Kept: m}
	key := box.ConversationsKey("TEST", "/w", "", "p")
	store := keptWriteStore(t, root, key, box.KeptState{Profile: "p", Workspace: "/w", Kept: []box.KeptEntry{
		{Path: ".culiops", KeptAt: time.Now(), Bytes: 5, Files: 1, Status: "kept"}}})
	keptFile(t, store, "home/.culiops/a.txt", "hello")
	return keptAPIFixture{s: s, m: m, key: key, store: store, docker: docker, root: root, audit: audit}
}

func keptWriteStore(t *testing.T, root, key string, st box.KeptState) string {
	t.Helper()
	dir := box.KeptStoreDir(root, key)
	for _, sub := range []string{"home", "clash"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func keptFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keptLink(t *testing.T, target, dir, rel string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func keptGet(s *Server, profile, sub string, q url.Values) *httptest.ResponseRecorder {
	target := "/api/profiles/" + profile + "/kept" + sub
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func keptPost(s *Server, profile, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("POST", "/api/profiles/"+profile+"/kept/forget", strings.NewReader(body)))
	return rec
}

func TestKeptAPIStores(t *testing.T) {
	f := keptAPI(t)
	keptWriteStore(t, f.root, box.ConversationsKey("TEST", "/w", "", "q"), box.KeptState{Profile: "q", Workspace: "/w"})
	rec := keptGet(f.s, "p", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET kept: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Stores []struct {
			Key        string `json:"key"`
			Profile    string `json:"profile"`
			Workspace  string `json:"workspace"`
			TotalBytes int64  `json:"total_bytes"`
			Kept       []struct {
				Path   string `json:"path"`
				Status string `json:"status"`
			} `json:"kept"`
			NotKept []any    `json:"not_kept"`
			Clashes []string `json:"clashes"`
		} `json:"stores"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Stores) != 1 {
		t.Fatalf("stores %+v, want only p's", body.Stores)
	}
	st := body.Stores[0]
	if st.Key != f.key || st.Profile != "p" || st.Workspace != "/w" || st.TotalBytes != 5 ||
		len(st.Kept) != 1 || st.Kept[0].Path != ".culiops" || st.Kept[0].Status != "kept" || st.NotKept == nil || st.Clashes == nil {
		t.Fatalf("store %+v", st)
	}
	if !strings.Contains(rec.Body.String(), `"not_kept":[]`) || !strings.Contains(rec.Body.String(), `"clashes":[]`) {
		t.Fatalf("empty lists must be [], not null: %s", rec.Body)
	}
}

func TestKeptAPIList(t *testing.T) {
	f := keptAPI(t)
	keptFile(t, f.store, "home/.culiops/sub/b.txt", "bb")
	keptLink(t, t.TempDir(), f.store, "home/.culiops/out")
	q := keptWriteStore(t, f.root, box.ConversationsKey("TEST", "/w", "", "q"), box.KeptState{Profile: "q", Workspace: "/w"})
	keptFile(t, q, "home/.q/x", "q")

	rec := keptGet(f.s, "p", "/list", url.Values{"key": {f.key}, "path": {".culiops"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Path     string    `json:"path"`
		ReadOnly bool      `json:"readonly"`
		Entries  []fsEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []fsEntry{{Name: "sub", IsDir: true}, {Name: "a.txt", Size: 5}, {Name: "out", Link: true}}
	if body.Path != ".culiops" || !body.ReadOnly || len(body.Entries) != 3 {
		t.Fatalf("list %+v", body)
	}
	for i, w := range want {
		g := body.Entries[i]
		if g.Name != w.Name || g.IsDir != w.IsDir || g.Link != w.Link || (w.Size != 0 && g.Size != w.Size) {
			t.Errorf("entry %d = %+v, want %+v", i, g, w)
		}
	}
	if rec := keptGet(f.s, "p", "/list", url.Values{"key": {f.key}, "path": {".culiops/sub"}}); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"b.txt"`) {
		t.Fatalf("list sub: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []url.Values{
		{"key": {f.key}, "path": {".culiops/out"}},
		{"key": {f.key}, "path": {".culiops/a.txt"}},
		{"key": {f.key}, "path": {".config"}},
		{"key": {f.key}, "path": {"../x"}},
		{"key": {f.key}, "path": {""}},
		{"key": {f.key}, "path": {".missing"}},
		{"key": {"0123456789ab"}, "path": {".culiops"}},
		{"key": {box.ConversationsKey("TEST", "/w", "", "q")}, "path": {".q"}}, // q's store, asked under p
		{"path": {".culiops"}},
	} {
		if rec := keptGet(f.s, "p", "/list", c); rec.Code != http.StatusNotFound {
			t.Errorf("list %v: %d %s, want 404", c, rec.Code, rec.Body)
		}
	}
}

func TestKeptAPIFileNoFollow(t *testing.T) {
	f := keptAPI(t)
	keptFile(t, f.store, "home/.culiops/sub/b.txt", "bb")
	keptLink(t, "a.txt", f.store, "home/.culiops/l.txt")
	keptLink(t, "sub", f.store, "home/.culiops/ldir")
	if err := syscall.Mkfifo(filepath.Join(f.store, "home/.culiops/f.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	keptFile(t, f.store, "home/.culiops/x\"y\nz.txt", "odd")

	rec := keptGet(f.s, "p", "/file", url.Values{"key": {f.key}, "path": {".culiops/a.txt"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("file: %d %s", rec.Code, rec.Body)
	}
	var fr fsRead
	if err := json.Unmarshal(rec.Body.Bytes(), &fr); err != nil {
		t.Fatal(err)
	}
	if fr.Kind != "text" || fr.Content != "hello" || !fr.ReadOnly {
		t.Fatalf("file read %+v, want text hello readonly", fr)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, p := range []string{".culiops/l.txt", ".culiops/ldir/b.txt", ".culiops/f.txt", ".culiops/sub", ".culiops", ".culiops/nope"} {
			if rec := keptGet(f.s, "p", "/file", url.Values{"key": {f.key}, "path": {p}}); rec.Code != http.StatusNotFound {
				t.Errorf("file %s: %d %s, want 404", p, rec.Code, rec.Body)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a file read blocked (FIFO?)")
	}
	rec = keptGet(f.s, "p", "/file", url.Values{"key": {f.key}, "path": {".culiops/x\"y\nz.txt"}, "download": {"1"}})
	if rec.Code != http.StatusOK || rec.Body.String() != "odd" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	cd := rec.Header().Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n") || strings.Contains(cd, `x"y`) {
		t.Fatalf("Content-Disposition carries the box's name raw: %q", cd)
	}
}

// The forget is a POST behind csrfGuard: a cookie POST from another origin
// never reaches it.
func TestKeptAPIForgetCSRF(t *testing.T) {
	f := keptAPI(t)
	ck := issueCookie(t, f.s)
	h := f.s.Handler()
	do := func(method string, hdr [2]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/profiles/p/kept/forget", strings.NewReader(`{"key":"`+f.key+`","path":".culiops"}`))
		r.AddCookie(ck)
		if hdr[0] != "" {
			r.Header.Set(hdr[0], hdr[1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	folder := filepath.Join(f.store, "home/.culiops")
	if rec := do("POST", [2]string{"Origin", "http://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin cookie POST: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := do("POST", [2]string{"Sec-Fetch-Site", "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site cookie POST: %d %s, want 403", rec.Code, rec.Body)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Fatalf("a refused POST removed the folder: %v", err)
	}
	if rec := do("GET", [2]string{}); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET forget: %d, want 405", rec.Code)
	}
	if rec := do("POST", [2]string{"Sec-Fetch-Site", "same-origin"}); rec.Code != http.StatusOK {
		t.Fatalf("same-origin POST: %d %s, want 200", rec.Code, rec.Body)
	}
	if _, err := os.Lstat(folder); !os.IsNotExist(err) {
		t.Fatalf("the folder is still there after forget: %v", err)
	}
	want := [4]string{"web", "box_kept_forget", "p", "path=.culiops"}
	if rows := f.audit.all(); len(rows) != 1 || rows[0] != want {
		t.Fatalf("audit rows %v, want one %v: a forget is the person's, not CuliBox's", rows, want)
	}
}

// A bearer client's forget is audited as the bearer's.
func TestKeptAPIForgetActorBearer(t *testing.T) {
	f := keptAPI(t)
	auth, plain := testServerWithAuth(t)
	f.s.Session, f.s.Tokens = auth.Session, auth.Tokens
	r := httptest.NewRequest("POST", "/api/profiles/p/kept/forget", strings.NewReader(`{"key":"`+f.key+`","path":".culiops"}`))
	r.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer forget: %d %s", rec.Code, rec.Body)
	}
	if rows := f.audit.all(); len(rows) != 1 || rows[0][0] != "bearer" {
		t.Fatalf("audit rows %v, want one by bearer", rows)
	}
}

// keptAPIRecord writes the record of keptAPIBox, a box of store key.
func keptAPIRecord(t *testing.T, f keptAPIFixture) {
	t.Helper()
	keptFile(t, filepath.Join(f.root, "boxstate", ".kept", ".boxes"), keptAPIBox, `{"key":"`+f.key+`"}`)
	f.docker.mu.Lock()
	f.docker.running[keptAPIBox] = true
	f.docker.mu.Unlock()
}

// Only a live session refuses a forget, with a sentence the user can act on.
func TestKeptAPIForgetSessions409(t *testing.T) {
	f := keptAPI(t)
	keptAPIRecord(t, f)
	f.m.AdoptBox(keptAPIBox) // a live session in the store's box
	rec := keptPost(f.s, "p", `{"key":"`+f.key+`","path":".culiops"}`)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"end this profile's boxed sessions first"}` {
		t.Fatalf("forget with a session: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(f.store, "home/.culiops/a.txt")); err != nil {
		t.Fatalf("a refused forget removed the folder: %v", err)
	}
	for _, body := range []string{`{"key":"` + f.key + `","path":".config"}`, `{"key":"0123456789ab","path":".culiops"}`, `not json`,
		`{"key":"` + f.key + `","path":".culiops","x":1}`} {
		if rec := keptPost(f.s, "p", body); rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("forget %s: %d %s, want 404 or 400", body, rec.Code, rec.Body)
		}
	}
}

// A box with no session (it outlives its last one by the idle TTL) is removed
// and the forget goes on.
func TestKeptAPIForgetIdleBoxRemoved(t *testing.T) {
	f := keptAPI(t)
	keptAPIRecord(t, f)
	rec := keptPost(f.s, "p", `{"key":"`+f.key+`","path":".culiops"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("forget with an idle box: %d %s", rec.Code, rec.Body)
	}
	f.docker.mu.Lock()
	stopped := append([]string(nil), f.docker.stopped...)
	f.docker.mu.Unlock()
	if len(stopped) != 1 || stopped[0] != keptAPIBox {
		t.Fatalf("stopped %v, want the idle box %s", stopped, keptAPIBox)
	}
	if _, err := os.Lstat(filepath.Join(f.store, "home/.culiops")); !os.IsNotExist(err) {
		t.Fatalf("the folder is still there: %v", err)
	}
}

// An idle box that will not go: the user is told to try again.
func TestKeptAPIForgetIdleBoxStopping409(t *testing.T) {
	f := keptAPI(t)
	keptAPIRecord(t, f)
	f.docker.mu.Lock()
	f.docker.stopErr = errors.New("docker rm: busy")
	f.docker.mu.Unlock()
	rec := keptPost(f.s, "p", `{"key":"`+f.key+`","path":".culiops"}`)
	if rec.Code != http.StatusConflict ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"a box of this profile is still stopping; try again in a minute"}` {
		t.Fatalf("forget when the idle box will not stop: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(f.store, "home/.culiops/a.txt")); err != nil {
		t.Fatalf("a refused forget removed the folder: %v", err)
	}
}

func TestKeptAPIUnknownProfile404(t *testing.T) {
	f := keptAPI(t)
	q := url.Values{"key": {f.key}, "path": {".culiops/a.txt"}}
	for _, sub := range []string{"", "/list", "/file"} {
		if rec := keptGet(f.s, "nope", sub, q); rec.Code != http.StatusNotFound {
			t.Errorf("GET nope/kept%s: %d %s, want 404", sub, rec.Code, rec.Body)
		}
	}
	if rec := keptPost(f.s, "nope", `{"key":"`+f.key+`","path":".culiops"}`); rec.Code != http.StatusNotFound {
		t.Errorf("POST nope/kept/forget: %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(f.store, "home/.culiops/a.txt")); err != nil {
		t.Fatalf("a forget under an unknown profile removed the folder: %v", err)
	}

	// No box layer: no store to show, nothing to read.
	f.s.Kept = nil
	if rec := keptGet(f.s, "p", "", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"stores":[]}` {
		t.Fatalf("GET kept without a box layer: %d %s", rec.Code, rec.Body)
	}
	if rec := keptGet(f.s, "p", "/file", q); rec.Code != http.StatusNotFound {
		t.Fatalf("GET file without a box layer: %d", rec.Code)
	}
}

// GET /kept answers from state.json alone: a store whose state says 1 GiB is
// 1 GiB, and its kept row stays even with the folder gone from disk, which a
// walk or a stat of the folder would have shown. That no directory under the
// store's home/ is opened or listed is pinned in the box package, where the
// hooks are (TestKeptStoresNoWalk).
func TestKeptAPIGetDoesNoWalk(t *testing.T) {
	f := keptAPI(t)
	keptWriteStore(t, f.root, f.key, box.KeptState{Profile: "p", Workspace: "/w", Kept: []box.KeptEntry{
		{Path: ".culiops", KeptAt: time.Now(), Bytes: 1 << 30, Files: 1, Status: "kept"}}})
	if err := os.RemoveAll(filepath.Join(f.store, "home", ".culiops")); err != nil {
		t.Fatal(err)
	}
	rec := keptGet(f.s, "p", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total_bytes":1073741824`) ||
		!strings.Contains(rec.Body.String(), `"path":".culiops"`) {
		t.Fatalf("GET kept: %d %s, want the row and total_bytes from the state", rec.Code, rec.Body)
	}
}
