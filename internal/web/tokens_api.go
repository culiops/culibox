package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleTokens lists (GET) or creates (POST) access tokens. Creation requires a
// session credential: a bearer token must not be able to mint more tokens.
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Tokens.List())
	case http.MethodPost:
		if credKindOf(r) != credCookie {
			writeErr(w, http.StatusForbidden, "token creation requires a session")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
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
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
