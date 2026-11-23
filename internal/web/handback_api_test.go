package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/handback"
)

func handbackAPI(t *testing.T) (*API, handback.Store) {
	t.Helper()
	st := handback.Store{Dir: filepath.Join(t.TempDir(), "handback")}
	return &API{Handback: st}, st
}

func mergeFlag(t *testing.T, st handback.Store, key string, base handback.Record, at time.Time, rep handback.Report) handback.Record {
	t.Helper()
	rec, err := st.Merge(key, base, at, rep)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func oneFlag(p string) handback.Report {
	return handback.Report{Flags: []handback.Flag{{Path: p, Classes: []handback.Class{handback.ClassMismatch}, Detail: "ELF named ." + p}}}
}

func listRecords(t *testing.T, a *API) []handback.Record {
	t.Helper()
	rec := httptest.NewRecorder()
	a.handleHandback(rec, httptest.NewRequest(http.MethodGet, "/api/handback", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Records []handback.Record `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Records == nil {
		t.Fatalf("body %s: %v (records must be an array, never null)", rec.Body, err)
	}
	return body.Records
}

func TestHandbackListsPendingOnly(t *testing.T) {
	a, st := handbackAPI(t)
	if got := listRecords(t, a); len(got) != 0 {
		t.Fatalf("empty store listed %+v", got)
	}
	at := time.Unix(9000, 0).UTC()
	flagged := mergeFlag(t, st, handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, at, oneFlag("a.pdf"))
	mergeFlag(t, st, handback.InPlaceKey("/quiet"), handback.Record{Kind: handback.KindInPlace, Workspace: "/quiet"}, at, handback.Report{Inspected: 5})
	got := listRecords(t, a)
	if len(got) != 1 || got[0].ID != flagged.ID || got[0].Workspace != "/ws" || len(got[0].Flags) != 1 {
		t.Fatalf("listed %+v, want only the flagged record", got)
	}
}

func TestHandbackListSurvivesABadRecord(t *testing.T) {
	a, st := handbackAPI(t)
	rec := mergeFlag(t, st, handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, time.Unix(1, 0), oneFlag("a.pdf"))
	if err := os.WriteFile(filepath.Join(st.Dir, strings.Repeat("b", 32)+".json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := listRecords(t, a); len(got) != 1 || got[0].ID != rec.ID {
		t.Fatalf("one bad file hid the good record: %+v", got)
	}
}

func TestHandbackReviewed(t *testing.T) {
	a, st := handbackAPI(t)
	rec := mergeFlag(t, st, handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, time.Unix(1, 0), oneFlag("a.pdf"))
	keep := mergeFlag(t, st, handback.InPlaceKey("/other"), handback.Record{Kind: handback.KindInPlace, Workspace: "/other"}, time.Unix(1, 0), oneFlag("b.pdf"))

	w := httptest.NewRecorder()
	a.handleHandback(w, postReq("/api/handback/"+rec.ID+"/reviewed", lastScanBody(rec)))
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("POST = %d %q, want 204 with no body", w.Code, w.Body)
	}
	if got := listRecords(t, a); len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("after reviewed: %+v, want only the other record", got)
	}
}

// lastScanBody is the request body the page sends: the last_scan of the record
// as it listed it.
func lastScanBody(rec handback.Record) string {
	b, _ := json.Marshal(map[string]time.Time{"last_scan": rec.LastScan})
	return string(b)
}

// R23: marking reviewed clears only what the user saw.
func TestHandbackReviewedNeedsTheLastScanTheUserSaw(t *testing.T) {
	a, st := handbackAPI(t)
	base := handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}
	t1, t2 := time.Unix(1000, 0).UTC(), time.Unix(2000, 0).UTC()
	rec := mergeFlag(t, st, handback.InPlaceKey("/ws"), base, t1, oneFlag("a.pdf"))
	mergeFlag(t, st, handback.InPlaceKey("/ws"), base, t2, oneFlag("b.pdf"))

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleHandback(w, postReq("/api/handback/"+rec.ID+"/reviewed", body))
		return w
	}
	if w := post(lastScanBody(rec)); w.Code != http.StatusConflict {
		t.Fatalf("a stale last_scan = %d %s, want 409", w.Code, w.Body)
	}
	if got := listRecords(t, a); len(got) != 1 || len(got[0].Flags) != 2 {
		t.Fatalf("a 409 must clear nothing: %+v", got)
	}
	for _, body := range []string{"", "{}", `{"last_scan":""}`, `{"last_scan":"0001-01-01T00:00:00Z"}`, `{"last_scan":"yesterday"}`, "not json", `{"last_scan":1}`} {
		if w := post(body); w.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d %s, want 400", body, w.Code, w.Body)
		}
	}
	if got := listRecords(t, a); len(got) != 1 || len(got[0].Flags) != 2 {
		t.Fatalf("a 400 must clear nothing: %+v", got)
	}
	if w := post(`{"last_scan":"` + t2.Format(time.RFC3339Nano) + `"}`); w.Code != http.StatusNoContent {
		t.Fatalf("the current last_scan = %d %s, want 204", w.Code, w.Body)
	}
	if got := listRecords(t, a); len(got) != 0 {
		t.Fatalf("after 204: %+v", got)
	}
	// A body past the bound is refused, not read to the end.
	if w := post(`{"last_scan":"` + strings.Repeat("x", 1<<20) + `"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an oversized body = %d, want 400", w.Code)
	}
}

func TestHandbackReviewedErrors(t *testing.T) {
	a, st := handbackAPI(t)
	rec := mergeFlag(t, st, handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, time.Unix(1, 0), oneFlag("a.pdf"))
	unknown := handback.RecordID("nobody")
	body := lastScanBody(rec)

	for _, c := range []struct {
		name string
		req  *http.Request
		want int
	}{
		{"cross-site", crossSiteReq("/api/handback/"+rec.ID+"/reviewed", ""), http.StatusForbidden},
		{"GET on /reviewed", httptest.NewRequest(http.MethodGet, "/api/handback/"+rec.ID+"/reviewed", nil), http.StatusMethodNotAllowed},
		{"path traversal id", postReq("/api/handback/../../etc/reviewed", body), http.StatusBadRequest},
		{"short id", postReq("/api/handback/abc/reviewed", body), http.StatusBadRequest},
		{"unknown id", postReq("/api/handback/"+unknown+"/reviewed", body), http.StatusNotFound},
		{"no /reviewed suffix", postReq("/api/handback/"+rec.ID, ""), http.StatusNotFound},
		{"extra segment", postReq("/api/handback/"+rec.ID+"/reviewed/x", ""), http.StatusNotFound},
		{"POST on the list", postReq("/api/handback", ""), http.StatusMethodNotAllowed},
	} {
		w := httptest.NewRecorder()
		a.handleHandback(w, c.req)
		if w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body, c.want)
		}
	}
	if got := listRecords(t, a); len(got) != 1 {
		t.Fatalf("a refused request changed the store: %+v", got)
	}
}

func TestHandbackRoutedBehindAuth(t *testing.T) {
	a, st := handbackAPI(t)
	rec := mergeFlag(t, st, handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, time.Unix(1, 0), oneFlag("a.pdf"))
	s, plain := routedServer(t)
	s.API = a
	h := s.Handler()
	send := func(method, path, bearer string) *httptest.ResponseRecorder {
		var rd *strings.Reader
		if method == "POST" {
			rd = strings.NewReader(lastScanBody(rec))
		} else {
			rd = strings.NewReader("")
		}
		r := httptest.NewRequest(method, path, rd)
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Host = "example.com"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := send("GET", "/api/handback", ""); w.Code == http.StatusOK {
		t.Fatal("/api/handback must not be reachable without auth")
	}
	if w := send("POST", "/api/handback/"+rec.ID+"/reviewed", ""); w.Code == http.StatusNoContent {
		t.Fatal("marking reviewed must not be reachable without auth")
	}
	if w := send("GET", "/api/handback", plain); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), rec.ID) {
		t.Fatalf("GET with a token = %d %s", w.Code, w.Body)
	}
	if w := send("POST", "/api/handback/"+rec.ID+"/reviewed", plain); w.Code != http.StatusNoContent {
		t.Fatalf("POST with a token = %d %s", w.Code, w.Body)
	}
	if w := send("GET", "/api/handback", plain); strings.Contains(w.Body.String(), rec.ID) {
		t.Fatalf("still listed after reviewed: %s", w.Body)
	}
}

func TestHandbackNilStore(t *testing.T) {
	a := &API{}
	w := httptest.NewRecorder()
	a.handleHandback(w, httptest.NewRequest(http.MethodGet, "/api/handback", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"records":[]}` {
		t.Fatalf("unwired store: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	a.handleHandback(w, postReq("/api/handback/"+handback.RecordID("x")+"/reviewed", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST with no store = %d, want 404", w.Code)
	}
}

func TestFlaggedOnProfileViews(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	seeds := []config.AgentConfig{
		{Name: "cl", Bin: "claude", Runtime: config.RuntimeBox, CloneOf: "/home/u/repo", DefaultCwd: filepath.Join(t.TempDir(), "clone")},
		{Name: "inplace", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: ws},
		{Name: "host", Bin: "claude", Runtime: config.RuntimeHost, DefaultCwd: ws},
		{Name: "other", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: t.TempDir()},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	var asked [][2]string
	pm.SetFlagged(func(of, workspace string) int {
		asked = append(asked, [2]string{of, workspace})
		switch {
		case of == "/home/u/repo":
			return 3
		case workspace == ws:
			return 2
		}
		return 0
	})
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	if got["cl"].Flagged != 3 || got["inplace"].Flagged != 2 || got["other"].Flagged != 0 {
		t.Errorf("flagged: cl=%d inplace=%d other=%d", got["cl"].Flagged, got["inplace"].Flagged, got["other"].Flagged)
	}
	if got["host"].Flagged != 0 {
		t.Errorf("a host-tier profile wrote nothing through a box, flagged = %d", got["host"].Flagged)
	}
	b, _ := json.Marshal(got["other"])
	if strings.Contains(string(b), `"flagged"`) {
		t.Errorf("a zero count must be omitted: %s", b)
	}
	// An in-place profile is asked about its workspace, a clone profile about its repo.
	for _, q := range asked {
		if q[0] != "" && q[1] != "" {
			t.Errorf("asked with both a repo and a workspace: %v", q)
		}
	}
}

func TestFlaggedUnwiredIsZero(t *testing.T) {
	pm := NewProfileManager([]config.AgentConfig{{Name: "a", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: t.TempDir()}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	if v := pm.Views(); len(v) != 1 || v[0].Flagged != 0 {
		t.Fatalf("views = %+v", v)
	}
}
