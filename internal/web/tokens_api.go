package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/culiops/agent-box/internal/auth"
)

// handleTokens lists (GET) or creates (POST) access tokens. Both require a
// session credential: token management is the master surface, so a bearer token
// can neither mint more tokens nor enumerate the others' metadata.
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	if credKindOf(r) != credCookie {
		writeErr(w, http.StatusForbidden, "token management requires a session")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Tokens.List())
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxOpBody)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
			writeErr(w, http.StatusBadRequest, "name required")
			return
		}
		plain, info, err := s.Tokens.Create(strings.TrimSpace(body.Name))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not create token")
			return
		}
		// The plaintext is returned exactly once, here.
		writeJSON(w, http.StatusOK, map[string]any{"token": plain, "info": info})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleToken revokes a token by id (DELETE /api/tokens/{id}). Session only.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if credKindOf(r) != credCookie {
		writeErr(w, http.StatusForbidden, "token revocation requires a session")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/tokens/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	if err := s.Tokens.Revoke(id); err != nil {
		if errors.Is(err, auth.ErrTokenNotFound) {
			writeErr(w, http.StatusNotFound, "token not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not revoke token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
