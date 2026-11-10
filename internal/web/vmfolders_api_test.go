package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/vmshares"
)

type fakeVMFolders struct {
	addErr   error
	addRes   VMAddResult
	applyErr error
	job      *VMApply
	gotDry   bool
	adds     int
}

func (f *fakeVMFolders) View() (VMFoldersView, error) {
	return VMFoldersView{State: "applied", Rows: []VMFolderRow{}, RunningBoxed: []string{}}, nil
}
func (f *fakeVMFolders) Add(p, m string, dry bool) (VMFoldersView, VMAddResult, error) {
	f.adds++
	f.gotDry = dry
	v, _ := f.View()
	return v, f.addRes, f.addErr
}
func (f *fakeVMFolders) SetMode(p, m string) (VMFoldersView, error) { return f.View() }
func (f *fakeVMFolders) Remove(p string) (VMFoldersView, error)     { return f.View() }
func (f *fakeVMFolders) Apply() (VMApply, error)                    { return VMApply{ID: 1, State: "running"}, f.applyErr }
func (f *fakeVMFolders) ApplyStatus() (VMApply, bool) {
	if f.job == nil {
		return VMApply{}, false
	}
	return *f.job, true
}

func vmReq(method, path, body, origin string, kind credKind) *http.Request {
	return storeReq(method, path, body, origin, kind)
}

func vmSrv() (*Server, *fakeVMFolders) {
	f := &fakeVMFolders{}
	return &Server{VMFolders: f}, f
}

func TestVMFoldersRoutesOnlyWithService(t *testing.T) {
	get := func(s *Server, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/vm-folders", nil)
		r.Host = "example.com"
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	with, tok := routedServer(t)
	with.VMFolders = &fakeVMFolders{}
	if w := get(with, tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"applied"`) {
		t.Fatalf("with a service: %d %s", w.Code, w.Body)
	}
	without, tok := routedServer(t)
	if w := get(without, tok); w.Code != 404 {
		t.Fatalf("without a service the route must not exist: %d %s", w.Code, w.Body)
	}
}

func TestVMFoldersBearerWriteRefused(t *testing.T) {
	s, f := vmSrv()
	for _, c := range []struct{ m, p, b string }{
		{"POST", "/api/vm-folders", `{"path":"~/x","mode":"rw","dry_run":false}`},
		{"PATCH", "/api/vm-folders", `{"path":"~/x","mode":"ro"}`},
		{"DELETE", "/api/vm-folders?path=~/x", ""},
	} {
		w := do(s.handleVMFolders, vmReq(c.m, c.p, c.b, "", credBearer))
		if w.Code != 403 {
			t.Errorf("%s with a bearer: %d %s", c.m, w.Code, w.Body)
		}
	}
	if w := do(s.handleVMFoldersApply, vmReq("POST", "/api/vm-folders/apply", "", "", credBearer)); w.Code != 403 {
		t.Errorf("apply with a bearer: %d", w.Code)
	}
	if f.adds != 0 {
		t.Error("a refused write reached the service")
	}
	if w := do(s.handleVMFolders, vmReq("GET", "/api/vm-folders", "", "", credBearer)); w.Code != 200 {
		t.Errorf("a bearer may read: %d", w.Code)
	}
}

func TestVMFoldersCrossOriginRefused(t *testing.T) {
	s, f := vmSrv()
	w := do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", `{"path":"~/x","mode":"rw"}`, "https://evil.example", credCookie))
	if w.Code != 403 || f.adds != 0 {
		t.Fatalf("cross-origin: %d %s", w.Code, w.Body)
	}
	w = do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", `{"path":"~/x","mode":"rw"}`, "http://"+host, credCookie))
	if w.Code != 200 || f.adds != 1 {
		t.Fatalf("same-origin cookie: %d %s", w.Code, w.Body)
	}
}

func TestVMFoldersBodyStrict(t *testing.T) {
	s, _ := vmSrv()
	for _, b := range []string{`{"path":"~/x","mode":"rw","extra":1}`, `{"path":"~/x"} {}`, `nope`,
		`{"path":"` + strings.Repeat("a", 17<<10) + `"}`} {
		w := do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", b, "http://"+host, credCookie))
		if w.Code != 400 {
			t.Errorf("body %.30q: %d", b, w.Code)
		}
	}
}

func TestVMFoldersAddRefusedShape(t *testing.T) {
	s, f := vmSrv()
	f.addErr = vmshares.Check("/home/u/.ssh", vmshares.Env{Home: "/home/u", DataRoot: "/home/u/.local/share/agent-box"})
	if f.addErr == nil {
		t.Skip("Check did not refuse .ssh")
	}
	w := do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", `{"path":"~/.ssh","mode":"rw"}`, "http://"+host, credCookie))
	var b map[string]string
	json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 400 || b["code"] != "refused" || b["field"] != "path" || !strings.Contains(b["error"], "holds keys and sockets") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestVMFoldersAddConflictShape(t *testing.T) {
	s, f := vmSrv()
	f.addErr = fmt.Errorf("wrapped: %w", &vmshares.Conflict{Child: "/h/a/b", Parent: "/h/a", Home: "/h"})
	w := do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", `{"path":"~/a/b","mode":"rw"}`, "http://"+host, credCookie))
	var b map[string]string
	json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 400 || b["code"] != "rw_in_ro" || b["field"] != "path" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestVMFoldersErrorCodes(t *testing.T) {
	s, f := vmSrv()
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{{ErrApplyRunning, 409, "apply_running"}, {ErrListCorrupt, 423, "list_corrupt"}} {
		f.addErr = c.err
		w := do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", `{"path":"~/a","mode":"rw"}`, "http://"+host, credCookie))
		if w.Code != c.status || !strings.Contains(w.Body.String(), `"code":"`+c.code+`"`) {
			t.Errorf("%v: %d %s", c.err, w.Code, w.Body)
		}
	}
	f.applyErr = ErrApplyRunning
	w := do(s.handleVMFoldersApply, vmReq("POST", "/api/vm-folders/apply", "", "http://"+host, credCookie))
	if w.Code != 409 {
		t.Errorf("apply running: %d", w.Code)
	}
	f.applyErr = ErrVMNotSetUp
	w = do(s.handleVMFoldersApply, vmReq("POST", "/api/vm-folders/apply", "", "http://"+host, credCookie))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "vm_not_set_up") {
		t.Errorf("not set up: %d %s", w.Code, w.Body)
	}
	f.applyErr = nil
	w = do(s.handleVMFoldersApply, vmReq("POST", "/api/vm-folders/apply", "", "http://"+host, credCookie))
	if w.Code != 202 {
		t.Errorf("apply: %d", w.Code)
	}
	if w := do(s.handleVMFoldersApply, vmReq("GET", "/api/vm-folders/apply", "", "", credBearer)); w.Code != 404 {
		t.Errorf("no job: %d", w.Code)
	}
	f.job = &VMApply{ID: 2, State: "done"}
	if w := do(s.handleVMFoldersApply, vmReq("GET", "/api/vm-folders/apply", "", "", credBearer)); w.Code != 200 {
		t.Errorf("job: %d", w.Code)
	}
}

func TestVMFoldersDryRunCovered(t *testing.T) {
	s, f := vmSrv()
	f.addRes = VMAddResult{CoveredBy: "/h/a", Absorbed: []string{}}
	w := do(s.handleVMFolders, vmReq("POST", "/api/vm-folders", `{"path":"~/a/b","mode":"rw","dry_run":true}`, "http://"+host, credCookie))
	var b struct {
		Result VMAddResult `json:"result"`
		View   VMFoldersView
	}
	json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || !f.gotDry || b.Result.CoveredBy != "/h/a" || b.View.State != "applied" {
		t.Fatalf("%d %s dry=%v", w.Code, w.Body, f.gotDry)
	}
}

func metaOf(t *testing.T, s *Server, tok string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/meta", nil)
	r.Host = "example.com"
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return m
}

func TestMetaVMFoldersFlag(t *testing.T) {
	s, tok := routedServer(t)
	m := metaOf(t, s, tok)
	if m["vm_folders"] != false || m["vm_apply"] != nil {
		t.Fatalf("nil service: %v %v", m["vm_folders"], m["vm_apply"])
	}
	f := &fakeVMFolders{}
	s.VMFolders = f
	if m := metaOf(t, s, tok); m["vm_folders"] != true || m["vm_apply"] != nil {
		t.Fatalf("no job: %v %v", m["vm_folders"], m["vm_apply"])
	}
	f.job = &VMApply{ID: 3, State: "done"}
	if m := metaOf(t, s, tok); m["vm_apply"] != nil {
		t.Fatalf("done job must be null: %v", m["vm_apply"])
	}
	for _, st := range []string{"running", "failed"} {
		f.job = &VMApply{ID: 3, State: st}
		if m := metaOf(t, s, tok); m["vm_apply"] == nil {
			t.Fatalf("%s job must show", st)
		}
	}
}

func TestSpawnErrNotSharedShape(t *testing.T) {
	rec := httptest.NewRecorder()
	err := fmt.Errorf("spawn: %w", &box.NotSharedError{Dir: "/Users/u/code/x", Pending: true, Home: "/Users/u"})
	writeSpawnErr(rec, "claude", err)
	var b map[string]any
	json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != 400 || b["code"] != "not_shared" || b["path"] != "~/code/x" || b["pending"] != true || b["read_only"] != false {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatal("precondition")
	}
}

func TestSpawnErrVMRestarting(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSpawnErr(rec, "claude", fmt.Errorf("x: %w", box.ErrVMRestarting))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"vm_restarting"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// C2: the spawn refusal says whether the folder needs write access, so the
// page's "Share this folder…" picks the mode the hint names.
func TestSpawnErrNotSharedNeedWrite(t *testing.T) {
	for _, need := range []bool{false, true} {
		rec := httptest.NewRecorder()
		writeSpawnErr(rec, "claude", &box.NotSharedError{Dir: "/Users/u/orig", NeedWrite: need, Home: "/Users/u"})
		var b map[string]any
		json.Unmarshal(rec.Body.Bytes(), &b)
		if b["need_write"] != need {
			t.Fatalf("need %v: body %s", need, rec.Body)
		}
	}
}
