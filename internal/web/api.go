// Package web serves the REST API, the static dashboard, and the terminal
// WebSocket route.
package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/session"
)

// SessionService is the subset of session.Manager the API needs (Info-based so
// handlers never touch a live *Session).
type SessionService interface {
	List() []session.Info
	Spawn(launcher.SpawnSpec) (session.Info, error)
	Kill(id string) error
	Rename(id, name string) error
	Get(id string) (session.Info, bool)
	UsageFor(id string) (session.Usage, bool)
	AttributionFor(id string) (session.Attribution, bool)
	PeekFor(id string) (session.Peek, bool)
	SetReplyable(id string, v bool) error
	SendMessage(id, text string) error
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
	// GET /api/sessions/{id}/usage — per-window context/model/effort. 204 when the
	// session isn't claude or has no locatable transcript (a normal, quiet miss).
	if strings.HasSuffix(id, "/usage") {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		sid := strings.TrimSuffix(id, "/usage")
		if u, ok := a.Sessions.UsageFor(sid); ok {
			writeJSON(w, http.StatusOK, u)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	// GET /api/sessions/{id}/peek — the agent's last message plus the mid-tool
	// flag. 204 when the session isn't claude or has no readable transcript;
	// the client distinguishes those two using Info.agent.
	if strings.HasSuffix(id, "/peek") {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		sid := strings.TrimSuffix(id, "/peek")
		if p, ok := a.Sessions.PeekFor(sid); ok {
			writeJSON(w, http.StatusOK, p)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	// GET /api/sessions/{id}/attribution — context-by-tool breakdown (full-transcript
	// scan, on demand). 204 for non-claude / no transcript.
	if strings.HasSuffix(id, "/attribution") {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		sid := strings.TrimSuffix(id, "/attribution")
		if at, ok := a.Sessions.AttributionFor(sid); ok {
			writeJSON(w, http.StatusOK, at)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	// POST /api/sessions/{id}/replyable — operator opt-in for accepting messages.
	if strings.HasSuffix(id, "/replyable") {
		if !checkMutationGuard(w, r) {
			return
		}
		var body struct {
			Replyable bool `json:"replyable"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		sid := strings.TrimSuffix(id, "/replyable")
		// The claude-only scope is enforced in the session layer, not here —
		// this just names which refusal happened. 403, not 404: the session
		// exists, it simply cannot be granted this.
		switch err := a.Sessions.SetReplyable(sid, body.Replyable); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, session.ErrNoSession):
			writeErr(w, http.StatusNotFound, "no such session")
		default:
			// ErrNotClaude and anything a future revision adds: a refusal to
			// grant, on a session that exists. 403 fails closed for a reason
			// this handler does not recognise.
			writeErr(w, http.StatusForbidden, err.Error())
		}
		return
	}
	// POST /api/sessions/{id}/message — type one line into a waiting session.
	// Every guard is re-checked in the session layer; the client's disabled
	// button is UX, not the control.
	if strings.HasSuffix(id, "/message") {
		if !checkMutationGuard(w, r) {
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		sid := strings.TrimSuffix(id, "/message")
		err := a.Sessions.SendMessage(sid, body.Text)
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, session.ErrBadText):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrNoSession):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, session.ErrNotReplyable), errors.Is(err, session.ErrNotClaude):
			writeErr(w, http.StatusForbidden, err.Error())
		// ErrNoTranscript joins the two "not right now" reasons rather than the
		// "never" ones: the transcript may well become readable on the next poll.
		case errors.Is(err, session.ErrNotWaiting), errors.Is(err, session.ErrPending),
			errors.Is(err, session.ErrNoTranscript):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			// The session died between the guard and the write.
			writeErr(w, http.StatusGone, "session is gone")
		}
		return
	}
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
