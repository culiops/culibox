package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// The device tokens page (UI plan 4, #174) draws a Created and a Last used column from this very list, and says
// "Never used" for a token that has not connected, so the JSON is the contract: created_at is always there (unix
// seconds), last_used_at is absent until the token has authenticated once and a number from then on. Decoded into
// a map, not auth.TokenInfo, so an omitted key and a key sent as 0 are told apart.
func TestTokensListCarriesCreatedAndLastUsed(t *testing.T) {
	s := tokenServer(t)
	plain, info, err := s.Tokens.Create("macbook")
	if err != nil {
		t.Fatal(err)
	}
	list := func() []map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleTokens(w, asCookie(httptest.NewRequest("GET", "/api/tokens", nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("list code=%d want 200", w.Code)
		}
		var rows []map[string]any
		if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0]["id"] != info.ID {
			t.Fatalf("list=%v want just %q", rows, info.ID)
		}
		return rows
	}

	// Seconds, not milliseconds: the page hands the value to new Date(unix * 1000), so a value in ms would draw the year 56000.
	// A window around now tells the two apart (ms is a thousand times larger).
	inWindow := func(v any) bool {
		f, ok := v.(float64)
		diff := int64(f) - time.Now().Unix()
		return ok && diff > -300 && diff < 300
	}

	fresh := list()[0]
	if !inWindow(fresh["created_at"]) {
		t.Fatalf("a new token's created_at=%v, want unix seconds within 300 s of now (%d)", fresh["created_at"], time.Now().Unix())
	}
	if v, present := fresh["last_used_at"]; present {
		t.Fatalf("a token that never connected carries last_used_at=%v, want the key omitted", v)
	}

	if _, ok := s.Tokens.Verify(plain); !ok {
		t.Fatal("the token does not verify")
	}
	used := list()[0]
	if !inWindow(used["last_used_at"]) {
		t.Fatalf("after Verify, last_used_at=%v, want unix seconds within 300 s of now (%d)", used["last_used_at"], time.Now().Unix())
	}
	if used["created_at"] != fresh["created_at"] {
		t.Fatalf("created_at moved from %v to %v when the token was used", fresh["created_at"], used["created_at"])
	}
}
