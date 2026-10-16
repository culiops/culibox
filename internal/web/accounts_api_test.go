package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
)

func acctServer(t *testing.T) (*Server, *mockSvc, string) {
	t.Helper()
	data := t.TempDir()
	def := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(def, 0o700)
	st, err := account.Open(filepath.Join(data, "accounts.json"), data, def)
	if err != nil {
		t.Fatal(err)
	}
	svc := &mockSvc{}
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetAccounts(st)
	s := &Server{Accounts: st, Profiles: pm, API: &API{Sessions: svc}, SecretOK: func(string) bool { return false }}
	return s, svc, data
}

func doJSON(t *testing.T, h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestAccountsCRUD(t *testing.T) {
	s, _, data := acctServer(t)
	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`); rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"../x","provider":"claude"}`); rec.Code != 400 {
		t.Fatalf("traversal name accepted: %d", rec.Code)
	}
	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"w2","provider":"claude","config_dir":"/etc"}`); rec.Code != 400 {
		t.Fatalf("config_dir from the request must be rejected (DisallowUnknownFields): %d", rec.Code)
	}
	rec := doJSON(t, s.handleAccounts, "GET", "/api/accounts", "")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 || list[0]["name"] != "default" || list[1]["name"] != "work" || list[1]["login_state"] != "unknown" {
		t.Fatalf("list = %v", list)
	}
	if strings.Contains(rec.Body.String(), data) {
		t.Fatal("config_dir must be shown home-relative or relative, never leaking the absolute data dir")
	}
	if rec := doJSON(t, s.handleAccount, "PUT", "/api/accounts/work", `{"token_secret":"WORK_TOK","inherit_personal":true}`); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.handleAccount, "PUT", "/api/accounts/default", `{}`); rec.Code != 400 {
		t.Fatalf("default editable: %d", rec.Code)
	}
}

func TestAccountDeleteRefusedWhileReferenced(t *testing.T) {
	s, svc, data := acctServer(t)
	doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`)
	if err := s.Profiles.Save(config.AgentConfig{Name: "p", Bin: "claude", Account: "work"}); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, s.handleAccount, "DELETE", "/api/accounts/work", ""); rec.Code != 409 {
		t.Fatalf("delete with a referencing profile = %d, want 409", rec.Code)
	}
	s.Profiles.Delete("p")
	svc.list = []session.Info{{ID: "x", Account: "work"}} // live session; use mockSvc's real list field name
	if rec := doJSON(t, s.handleAccount, "DELETE", "/api/accounts/work", ""); rec.Code != 409 {
		t.Fatalf("delete with a live session = %d, want 409", rec.Code)
	}
	svc.list = []session.Info{{ID: "x", Account: "work", Exited: true}}
	rec := doJSON(t, s.handleAccount, "DELETE", "/api/accounts/work", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "left_on_disk") {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(data, "accounts", "work")); err != nil {
		t.Fatal("dir must stay on disk")
	}
}

func TestMetaRateLimitsPerAccount(t *testing.T) {
	s, _, data := acctServer(t)
	defFile := filepath.Join(t.TempDir(), "abtop.json")
	os.WriteFile(defFile, []byte(`{"updated_at":1,"five_hour":{"used_percentage":10,"resets_at":2}}`), 0o600)
	s.RateLimitsFile = defFile
	doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`)
	doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"idle","provider":"claude"}`)
	os.WriteFile(filepath.Join(data, "accounts", "work", "claude", account.RateLimitsName),
		[]byte(`{"updated_at":3,"seven_day":{"used_percentage":50,"resets_at":4}}`), 0o600)
	m := s.rateLimitsByAccount()
	if m["default"].FiveHour == nil || m["work"].SevenDay == nil {
		t.Fatalf("map = %+v", m)
	}
	if _, ok := m["idle"]; ok {
		t.Fatal("an account with no file must be absent, not zero")
	}
}

func TestAccountsLoginStateNeverLeaksSecretValue(t *testing.T) {
	s, _, _ := acctServer(t)
	s.SecretOK = func(n string) bool { return n == "WORK_TOK" }
	doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude","token_secret":"WORK_TOK"}`)
	rec := doJSON(t, s.handleAccounts, "GET", "/api/accounts", "")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if list[1]["login_state"] != "token" || list[1]["token_secret"] != "WORK_TOK" {
		t.Fatalf("view = %v", list[1])
	}
	// a name that does not resolve is not "token"
	s.SecretOK = func(string) bool { return false }
	rec = doJSON(t, s.handleAccounts, "GET", "/api/accounts", "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	if list[1]["login_state"] != "unknown" {
		t.Fatalf("unresolved secret must not read as token: %v", list[1])
	}
}

func TestAccountDuplicateIs409AndUnknownIs404(t *testing.T) {
	s, _, _ := acctServer(t)
	doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`)
	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`); rec.Code != 409 {
		t.Fatalf("duplicate = %d", rec.Code)
	}
	if rec := doJSON(t, s.handleAccount, "DELETE", "/api/accounts/ghost", ""); rec.Code != 404 {
		t.Fatalf("delete unknown = %d", rec.Code)
	}
	if rec := doJSON(t, s.handleAccount, "PUT", "/api/accounts/ghost", `{}`); rec.Code != 404 {
		t.Fatalf("update unknown = %d", rec.Code)
	}
}

// Review Focus 4: an account created while the server runs shows its quota in
// /api/meta (through the real routes, behind auth) with no restart.
func TestAccountsRoutesAreGuardedAndMetaSeesNewAccount(t *testing.T) {
	s, _, data := acctServer(t)
	s.AllowedHosts = []string{"example.com"}
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/accounts", nil))
	if rec.Code == 200 {
		t.Fatal("/api/accounts must not be reachable without auth")
	}

	ck := issueCookie(t, s)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(ck)
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		return r
	}
	if m := metaGet(t, s); m["rate_limits"] != nil {
		t.Fatalf("no data yet, rate_limits must be absent: %v", m["rate_limits"])
	}
	doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"late","provider":"claude"}`)
	os.WriteFile(filepath.Join(data, "accounts", "late", "claude", account.RateLimitsName),
		[]byte(`{"updated_at":3,"five_hour":{"used_percentage":7,"resets_at":4}}`), 0o600)
	var meta struct {
		RL map[string]RateLimits `json:"rate_limits"`
	}
	json.Unmarshal(get("/api/meta").Body.Bytes(), &meta)
	if meta.RL["late"].FiveHour == nil || meta.RL["late"].FiveHour.UsedPercentage != 7 {
		t.Fatalf("meta = %+v", meta)
	}
	if r := get("/api/accounts"); r.Code != 200 {
		t.Fatalf("authed list = %d", r.Code)
	}
}
