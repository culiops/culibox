// Package web serves the REST API, the static dashboard, and the terminal
// WebSocket route.
package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/tmux"
)

// SessionService is the subset of session.Manager the API needs.
type SessionService interface {
	List() ([]tmux.Session, error)
	Spawn(launcher.SpawnSpec) (tmux.Session, error)
	Kill(name string) error
	Get(name string) (tmux.Session, bool, error)
}

// API holds the HTTP handlers for sessions.
type API struct{ Sessions SessionService }

type sessionDTO struct {
	Name    string `json:"name"`
	Created int64  `json:"created,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// handleSessions serves GET (list) and POST (spawn) on /api/sessions.
func (a *API) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessions, err := a.Sessions.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "list failed")
			return
		}
		out := make([]sessionDTO, 0, len(sessions))
		for _, s := range sessions {
			d := sessionDTO{Name: s.Name}
			if !s.Created.IsZero() {
				d.Created = s.Created.Unix()
			}
			out = append(out, d)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req struct {
			Agent string `json:"agent"`
			Cwd   string `json:"cwd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Agent == "" {
			writeErr(w, http.StatusBadRequest, "agent required")
			return
		}
		sess, err := a.Sessions.Spawn(launcher.SpawnSpec{Agent: req.Agent, Cwd: req.Cwd})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sessionDTO{Name: sess.Name})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSession serves DELETE (kill) on /api/sessions/{name}.
func (a *API) handleSession(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, ok, err := a.Sessions.Get(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	if err := a.Sessions.Kill(name); err != nil {
		writeErr(w, http.StatusInternalServerError, "kill failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
