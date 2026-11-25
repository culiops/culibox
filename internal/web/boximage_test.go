package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/culiops/culibox/internal/boximagecheck"
	"github.com/culiops/culibox/internal/config"
)

func boxImageServer(hook func(context.Context) boximagecheck.Report) *Server {
	return &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {}),
		BoxImage: hook}
}

func TestBoxImageAPI(t *testing.T) {
	calls := 0
	s := boxImageServer(func(context.Context) boximagecheck.Report {
		calls++
		return boximagecheck.Report{Configured: "culiops/culibox:base", Verdict: boximagecheck.Update, Want: "ghcr.io/culiops/culibox@sha256:" + hex64, CheckOn: true}
	})
	ck := issueCookie(t, s)
	h := s.Handler()
	do := func(method string, withCookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/box-image", nil)
		if withCookie {
			r.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := do(http.MethodGet, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got struct {
		Verdict string `json:"verdict"`
		Want    string `json:"want"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "update" || got.Want == "" {
		t.Fatalf("body = %s", rec.Body)
	}

	if rec := do(http.MethodPost, true); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code = %d, want 405", rec.Code)
	}
	before := calls
	if rec := do(http.MethodGet, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET without a session: code = %d, want 401", rec.Code)
	}
	if calls != before {
		t.Fatal("the hook ran for an unauthenticated request")
	}
}

func TestBoxImageAPINilHook(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleBoxImage(w, httptest.NewRequest(http.MethodGet, "/api/box-image", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
