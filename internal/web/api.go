// Package web serves the REST API, the static dashboard, and the terminal
// WebSocket route.
package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/culiops/culiops-agent-console/internal/box"
	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/session"
)

// SessionService is the subset of session.Manager the API needs (Info-based so
// handlers never touch a live *Session).
type SessionService interface {
	List() []session.Info
	Spawn(launcher.SpawnSpec) (session.Info, error)
	Kill(id string) error
	Rename(id, name string) error
	Get(id string) (session.Info, bool)
}

// API holds the HTTP handlers for sessions.
type API struct {
	Sessions   SessionService
	DefaultCwd string // used when a spawn request omits cwd (e.g. $HOME)
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
		writeJSON(w, http.StatusOK, a.Sessions.List())
	case http.MethodPost:
		var req struct {
			Agent   string         `json:"agent"`
			Name    string         `json:"name"`
			Cwd     string         `json:"cwd"`
			Options map[string]any `json:"options"`
			Runtime string         `json:"runtime"` // "" = the agent's default (box)
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Agent == "" {
			writeErr(w, http.StatusBadRequest, "agent required")
			return
		}
		// Reject anything but the two known tiers. An unrecognised value must not
		// fall through to a default — dropping to the host tier by typo is exactly
		// the silent de-containment this design exists to prevent.
		switch req.Runtime {
		case "", config.RuntimeBox, config.RuntimeHost:
		default:
			writeErr(w, http.StatusBadRequest, "runtime must be box or host")
			return
		}
		cwd := strings.TrimSpace(req.Cwd)
		if cwd == "" {
			cwd = a.DefaultCwd // default working dir ($HOME unless configured)
		}
		// Name is a free-form display label (blank → working-dir default), so a
		// spawn no longer fails on the name.
		info, err := a.Sessions.Spawn(launcher.SpawnSpec{
			Agent: req.Agent, Name: req.Name, Cwd: cwd, Options: req.Options, Runtime: req.Runtime,
		})
		if err != nil {
			log.Printf("spawn agent=%q: %v", req.Agent, err) // detail server-side only
			// One exception to the generic message: a refusal to contain the
			// chosen workspace is actionable and names only the path the caller
			// just supplied. Hiding it behind "could not start session" is the
			// silent failure that makes people give up on containment.
			if errors.Is(err, box.ErrUnboxableWorkspace) {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeErr(w, http.StatusBadRequest, "could not start session")
			return
		}
		writeJSON(w, http.StatusCreated, info)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSession serves DELETE (kill) on /api/sessions/{id}.
func (a *API) handleSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if _, ok := a.Sessions.Get(id); !ok {
			writeErr(w, http.StatusNotFound, "no such session")
			return
		}
		if err := a.Sessions.Kill(id); err != nil {
			if errors.Is(err, session.ErrNotFound) { // pruned between Get and Kill
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
			writeErr(w, http.StatusInternalServerError, "kill failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request")
			return
		}
		if err := a.Sessions.Rename(id, req.Name); err != nil {
			switch {
			case errors.Is(err, session.ErrNotFound):
				writeErr(w, http.StatusNotFound, "no such session")
			case errors.Is(err, session.ErrInvalidName):
				writeErr(w, http.StatusBadRequest, err.Error())
			default:
				writeErr(w, http.StatusInternalServerError, "rename failed")
			}
			return
		}
		if info, ok := a.Sessions.Get(id); ok {
			writeJSON(w, http.StatusOK, info)
		} else { // pruned between Rename and Get — renamed, but already gone
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
