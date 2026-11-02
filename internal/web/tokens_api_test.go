package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/auth"
)

func tokenServer(t *testing.T) *Server {
	t.Helper()
	ts, err := auth.OpenTokenStore(t.TempDir() + "/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Tokens: ts}
}

func asCookie(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), credKindKey, credCookie))
}

func TestTokens_CreateThenList(t *testing.T) {
	s := tokenServer(t)
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"macbook"}`)
	s.handleTokens(w, asCookie(httptest.NewRequest("POST", "/api/tokens", body)))
	if w.Code != http.StatusOK {
		t.Fatalf("create code=%d want 200", w.Code)
	}
	var created struct {
		Token string `json:"token"`
	}
	json.NewDecoder(w.Body).Decode(&created)
	if !strings.HasPrefix(created.Token, "abx_") {
		t.Fatalf("create did not return plaintext token: %q", created.Token)
	}

	w2 := httptest.NewRecorder()
	s.handleTokens(w2, asCookie(httptest.NewRequest("GET", "/api/tokens", nil)))
	var list []auth.TokenInfo
	json.NewDecoder(w2.Body).Decode(&list)
	if len(list) != 1 || list[0].Name != "macbook" {
		t.Fatalf("list=%+v", list)
	}
}

func TestTokens_CreateRequiresSessionNotBearer(t *testing.T) {
	s := tokenServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/tokens", strings.NewReader(`{"name":"x"}`))
	r = r.WithContext(context.WithValue(r.Context(), credKindKey, credBearer))
	s.handleTokens(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bearer create code=%d want 403", w.Code)
	}
}

func TestTokens_Revoke(t *testing.T) {
	s := tokenServer(t)
	_, info, _ := s.Tokens.Create("cli")
	w := httptest.NewRecorder()
	s.handleToken(w, asCookie(httptest.NewRequest("DELETE", "/api/tokens/"+info.ID, nil)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke code=%d want 204", w.Code)
	}
	if len(s.Tokens.List()) != 0 {
		t.Fatal("token not revoked")
	}
}
