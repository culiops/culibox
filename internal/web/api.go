// Package web serves the REST API, the static dashboard, and the terminal
// WebSocket route.
package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/culiops/agent-box/internal/account"
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
	Interrupt(id string) error
	Answer(id, choice, askSHA256 string) error
	Restart(id string) (session.Info, error)
}

// API holds the HTTP handlers for sessions.
type API struct {
	Sessions   SessionService
	DefaultCwd string // used when a spawn request omits cwd (e.g. $HOME)
	// ProfileCwd returns the named profile's default_cwd ("" = none); a spawn
	// that omits cwd uses it before DefaultCwd. nil = no profile defaults.
	ProfileCwd func(agent string) string
	// OptionsDropped reports the spawn option overrides the profile refused,
	// for the 201 body only (never persisted). nil = nothing is reported.
	OptionsDropped func(launcher.SpawnSpec) []launcher.OptionDrop
	AWS            AWSStatusService // nil = the credential broker is not running; /aws is a 404
	// Elevation serves the write-window and AWS sign-in endpoints; nil = the
	// credential broker is not running, and they are 404.
	Elevation ElevationService
	// Egress serves GET /api/sessions/{id}/egress, the rail's egress line for a
	// box that has no AWS binding; nil = egress control is not running, and the
	// route is a 404.
	Egress EgressStatusService
}

// EgressStatusService reports a boxed session's egress mode and proxy counters.
// ok is false for a session that is unknown or has no egress control (a host
// session). It carries the mode and two counts, never the session's token.
type EgressStatusService interface {
	EgressStatus(sessionID string) (status any, ok bool)
}

// AWSStatusService reports a session's AWS binding, broker counters and recent
// audit rows. ok is false for a session that is unknown or has no binding.
type AWSStatusService interface {
	AWSStatus(sessionID string) (status any, ok bool)
}

// credAwareAWSStatus is an AWSStatusService that can tell a signed-in browser
// session from any other caller. The device sign-in code and URL are returned
// only to the cookie session (a bearer token must not read them), so the
// status of the /aws route goes through this when the service has it.
type credAwareAWSStatus interface {
	AWSStatusFor(sessionID string, cookie bool) (status any, ok bool)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// sha256HexRe is the only ask_sha256 shape /answer accepts (what the web
// client's sha256Hex produces).
var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// writeSpawnErr maps a Spawn or Restart failure. A refusal that names only
// what the caller just sent (a workspace that cannot be contained, a directory
// that belongs to a clone-mode box, a first message that was refused, an
// account that cannot run the profile) or a containment remedy
// (ErrBoxUnavailable: a command or path, never an env value) is shown;
// anything else stays generic
// and the detail goes to the server log only. None of these messages carries
// the prompt text.
func writeSpawnErr(w http.ResponseWriter, agent string, err error) {
	log.Printf("spawn agent=%q: %v", agent, err)
	switch {
	case errors.Is(err, box.ErrUnboxableWorkspace), errors.Is(err, box.ErrBoxUnavailable), errors.Is(err, box.ErrWorkspaceHeld),
		errors.Is(err, box.ErrBoxOwned),
		errors.Is(err, launcher.ErrBadPrompt),
		errors.Is(err, launcher.ErrPromptUnsupported),
		errors.Is(err, launcher.ErrCodexAccount), errors.Is(err, account.ErrUnknown),
		errors.Is(err, launcher.ErrAccountConflict):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, "could not start session")
	}
}

// handleSessions serves GET (list) and POST (spawn) on /api/sessions.
func (a *API) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.Sessions.List())
	case http.MethodPost:
		var req struct {
			Agent     string         `json:"agent"`
			Name      string         `json:"name"`
			Cwd       string         `json:"cwd"`
			Options   map[string]any `json:"options"`
			Runtime   string         `json:"runtime"`   // "" = the agent's default (box)
			Prompt    string         `json:"prompt"`    // first message (claude only; the launcher validates)
			Replyable *bool          `json:"replyable"` // nil = the profile's default
			Account   string         `json:"account"`   // "" = the profile's own account, else default
			Login     bool           `json:"login"`     // sign-in session for a named claude account (host tier)
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
		// An omitted cwd means "the profile's own": the UI pre-fills it, but an API
		// client (or a clone profile whose default_cwd is the clone) would otherwise
		// land in DefaultCwd. No existence check — a clone's first spawn creates it.
		if cwd == "" && a.ProfileCwd != nil {
			cwd = strings.TrimSpace(a.ProfileCwd(req.Agent))
		}
		if cwd == "" {
			cwd = a.DefaultCwd // default working dir ($HOME unless configured)
		}
		// Name is a free-form display label (blank → working-dir default), so a
		// spawn no longer fails on the name.
		// The generic message hides most refusals; a refusal that is actionable
		// and names only what the caller just supplied (an unboxable workspace,
		// a rejected first message) is shown instead — see writeSpawnErr.
		spec := launcher.SpawnSpec{
			Agent: req.Agent, Name: req.Name, Cwd: cwd, Options: req.Options, Runtime: req.Runtime,
			Prompt: req.Prompt, Replyable: req.Replyable, Account: req.Account, Login: req.Login,
		}
		info, err := a.Sessions.Spawn(spec)
		if err != nil {
			writeSpawnErr(w, req.Agent, err)
			return
		}
		// A rejected override must not look accepted: say which were dropped.
		// Response only; session.Info and the sidecar do not carry it.
		resp := struct {
			session.Info
			OptionsDropped []launcher.OptionDrop `json:"options_dropped,omitempty"`
		}{Info: info}
		if a.OptionsDropped != nil {
			resp.OptionsDropped = a.OptionsDropped(spec)
		}
		writeJSON(w, http.StatusCreated, resp)
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
	// POST|DELETE /api/sessions/{id}/aws/elevate and POST .../aws/sso-login:
	// before the /aws suffix check below, which would not match them anyway.
	if sid, ok := strings.CutSuffix(id, elevateSuffix); ok {
		a.handleElevate(w, r, sid)
		return
	}
	if sid, ok := strings.CutSuffix(id, ssoLoginSuffix); ok {
		a.handleSSOLogin(w, r, sid)
		return
	}
	// GET /api/sessions/{id}/aws — the session's AWS (inline or named
	// connections), GitHub and/or token binding, broker counters and recent
	// audit rows. 204 when the session is unknown or has no binding; 404
	// when the broker is not running.
	if strings.HasSuffix(id, "/aws") {
		if a.AWS == nil {
			writeErr(w, http.StatusNotFound, "credential broker not enabled")
			return
		}
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		sid := strings.TrimSuffix(id, "/aws")
		var st any
		var ok bool
		if cs, isCS := a.AWS.(credAwareAWSStatus); isCS {
			st, ok = cs.AWSStatusFor(sid, credKindOf(r) == credCookie)
		} else {
			st, ok = a.AWS.AWSStatus(sid)
		}
		if ok {
			writeJSON(w, http.StatusOK, st)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	// GET /api/sessions/{id}/egress — the egress line for a box without AWS
	// access (the /aws route is 204 for it). 204 when the session is unknown or
	// has no egress control; 404 when egress control is not running.
	if strings.HasSuffix(id, "/egress") {
		if a.Egress == nil {
			writeErr(w, http.StatusNotFound, "egress control not enabled")
			return
		}
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if st, ok := a.Egress.EgressStatus(strings.TrimSuffix(id, "/egress")); ok {
			writeJSON(w, http.StatusOK, st)
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
		case errors.Is(err, session.ErrNotReplyable), errors.Is(err, session.ErrNotClaude),
			errors.Is(err, session.ErrRepliesDisabled):
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
	// POST /api/sessions/{id}/interrupt — stop what the session is doing: Esc
	// for an agent (its turn ends, it keeps running), Ctrl+C for a shell. The
	// key is chosen in the session layer. No body and no reply opt-in: it can
	// only stop work, never start it.
	if strings.HasSuffix(id, "/interrupt") {
		if !checkMutationGuard(w, r) {
			return
		}
		switch err := a.Sessions.Interrupt(strings.TrimSuffix(id, "/interrupt")); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, session.ErrNoSession):
			writeErr(w, http.StatusNotFound, "no such session")
		case errors.Is(err, session.ErrNotLive):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrBinUnresolved):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrTooSoon):
			writeErr(w, http.StatusTooManyRequests, err.Error())
		default:
			writeErr(w, http.StatusGone, "session is gone")
		}
		return
	}
	// POST /api/sessions/{id}/answer — one key into a permission dialog, only
	// for the exact command the operator reviewed (ask_sha256). Every gate is
	// in the session layer; this validates the shape and names the refusal.
	if strings.HasSuffix(id, "/answer") {
		if !checkMutationGuard(w, r) {
			return
		}
		var body struct {
			Choice    string `json:"choice"`
			AskSHA256 string `json:"ask_sha256"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !sha256HexRe.MatchString(body.AskSHA256) {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		switch err := a.Sessions.Answer(strings.TrimSuffix(id, "/answer"), body.Choice, body.AskSHA256); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, session.ErrBadChoice):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrNoSession):
			writeErr(w, http.StatusNotFound, "no such session")
		case errors.Is(err, session.ErrNotClaude), errors.Is(err, session.ErrNotReplyable),
			errors.Is(err, session.ErrRepliesDisabled):
			writeErr(w, http.StatusForbidden, err.Error())
		case errors.Is(err, session.ErrNotLive):
			// Mirrors /interrupt: a died-between-the-guard-and-the-write race is
			// a 410 (below), but a session that simply isn't live right now
			// (exited/detached) is a normal, retryable conflict, not "gone".
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrNotBlocked), errors.Is(err, session.ErrPromptChanged):
			// ErrPromptChanged also covers a repeat answer on an unchanged
			// prompt (control.go's lastAnswered memo) — the session layer
			// cannot and does not distinguish the two, so the body stays the
			// sentinel's own text ("the prompt changed"); the web client (Task
			// 11/12's answerErrorText) is what turns a 409 here into "Already
			// answered — if the dialog is still open, answer in the terminal."
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrTooSoon):
			writeErr(w, http.StatusTooManyRequests, err.Error())
		default:
			writeErr(w, http.StatusGone, "session is gone")
		}
		return
	}
	// POST /api/sessions/{id}/restart — replace an exited session (still in
	// retention) with a new one from the same spec; 201 + the new session.
	if strings.HasSuffix(id, "/restart") {
		if !checkMutationGuard(w, r) {
			return
		}
		info, err := a.Sessions.Restart(strings.TrimSuffix(id, "/restart"))
		switch {
		case err == nil:
			writeJSON(w, http.StatusCreated, info)
		case errors.Is(err, session.ErrNoSession):
			writeErr(w, http.StatusNotFound, "no such session")
		case errors.Is(err, session.ErrNotExited), errors.Is(err, session.ErrRestarting):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeSpawnErr(w, "(restart "+strings.TrimSuffix(id, "/restart")+")", err)
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
