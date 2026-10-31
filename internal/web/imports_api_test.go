package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
)

func importsServer(f func() bundle.ImportSet) *Server {
	return &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {}),
		Imports:  f}
}

func TestBundleImportsRoute(t *testing.T) {
	calls := 0
	s := importsServer(func() bundle.ImportSet {
		calls++
		return bundle.ImportSet{Files: []bundle.ImportFile{{
			Import: "@notes.md", Name: "notes.md", Box: "/b/notes.md", Host: "/h/notes.md",
			Size: 12, SHA256: "abc", Warnings: []string{"imports itself"},
		}}}
	})
	ck := issueCookie(t, s)
	h := s.Handler()
	do := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/bundle/imports", nil)
		r.AddCookie(ck)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := do("GET")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code=%d want 200: %s", rec.Code, rec.Body)
	}
	var got struct {
		Files []struct {
			Name     string   `json:"name"`
			Warnings []string `json:"warnings"`
		} `json:"files"`
		Refused []any `json:"refused"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body)
	}
	if len(got.Files) != 1 || got.Files[0].Name != "notes.md" || len(got.Files[0].Warnings) != 1 {
		t.Fatalf("files=%+v", got.Files)
	}

	n := calls
	rec = do("POST")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code=%d want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Errorf("Allow=%q want GET", rec.Header().Get("Allow"))
	}
	if calls != n {
		t.Errorf("resolver ran for a refused method")
	}

	// Unauthenticated: behind the same auth as the other /api routes.
	r := httptest.NewRequest("GET", "/api/bundle/imports", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie code=%d want 401", rec.Code)
	}
}

// An empty set encodes as [] (not null) so the UI can iterate it.
func TestBundleImportsEmptyIsArray(t *testing.T) {
	s := importsServer(func() bundle.ImportSet { return bundle.ImportSet{} })
	w := httptest.NewRecorder()
	s.handleBundleImports(w, httptest.NewRequest("GET", "/api/bundle/imports", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["files"]) != "[]" {
		t.Errorf("files=%s want []", raw["files"])
	}
}

func TestBundleImportsNilIsNotRegistered(t *testing.T) {
	s := importsServer(nil)
	ck := issueCookie(t, s)
	r := httptest.NewRequest("GET", "/api/bundle/imports", nil)
	r.AddCookie(ck)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}
