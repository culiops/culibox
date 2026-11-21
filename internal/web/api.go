// Package web serves the REST API, the static dashboard, and the terminal
// WebSocket route.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/session"
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
	// TimelineFor is a page of the session's transcript timeline (see
	// session.Manager.TimelineFor); ok is false for an unknown session.
	TimelineFor(id string, beforeMS int64, limit int) (session.Timeline, bool)
	// BoxOutputDir is the host dir a boxed session's Playwright output is
	// mounted from; ok only for a known boxed session.
	BoxOutputDir(id string) (string, bool)
	SetReplyable(id string, v bool) error
	SendMessage(id, text string) error
	Interrupt(id string) error
	Answer(id, choice, askSHA256 string) error
	Restart(id string) (session.Info, error)
	// BringHome starts a "now" return of a clone session's work; it returns at
	// once (see session.Manager.BringHome).
	BringHome(id string) error
}

// API holds the HTTP handlers for sessions.
type API struct {
	Sessions   SessionService
	DefaultCwd string // used when a spawn request omits cwd (e.g. $HOME)
	// ProfileCwd returns the named profile's default_cwd ("" = none); a spawn
	// that omits cwd uses it before DefaultCwd. nil = no profile defaults.
	ProfileCwd func(agent string) string
	// VMState reports the macOS box VM for GET /api/vm-state: "running",
	// "stopped" (only when confirmed) or "" (unknown, no VM). nil = no box layer. It
	// may take 30 s (colima status), so the endpoint caches it and runs one
	// call at a time.
	VMState func(ctx context.Context) string
	vmCache vmStateCache
	// QuickShell returns the verified quick-shell profile name ("" = none); a
	// companion shell (POST /api/sessions/{id}/shell) runs it. nil = none.
	QuickShell func() string
	// OptionsDropped reports the spawn option overrides the profile refused,
	// for the 201 body only (never persisted). nil = nothing is reported.
	OptionsDropped func(launcher.SpawnSpec) []launcher.OptionDrop
	// Roots is the audited workspace-root list. A browser Start with add_root
	// adds the session's folder to it (#343); nil = no roots service, and the
	// spawn is unchanged.
	Roots WorkspaceRoots
	AWS   AWSStatusService // nil = the credential broker is not running; /aws is a 404
	// Elevation serves the write-window and AWS sign-in endpoints; nil = the
	// credential broker is not running, and they are 404.
	Elevation ElevationService
	// Egress serves GET /api/sessions/{id}/egress, the rail's egress line for a
	// box that has no AWS binding; nil = egress control is not running, and the
	// route is a 404.
	Egress EgressStatusService
	// Activity supplies the credential and approval rows of GET
	// /api/sessions/{id}/activity; nil = the broker is not running, and the
	// timeline has transcript rows only.
	Activity ActivityAuditService
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

// writeErrCode is writeErr plus a stable machine-readable code the UI can act on.
func writeErrCode(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
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
	case errors.Is(err, launcher.ErrStoreLocked):
		// The fixed text only: the wrapped error names the secret, which stays in the log above.
		writeErrCode(w, http.StatusLocked, launcher.ErrStoreLocked.Error(), "store_locked")
	case errors.Is(err, launcher.ErrStoreUnavailable):
		// 503, never 423: no unlock form for a Keychain failure. The message is the
		// UnavailableError's own (reason + fix); the wrapped secret name stays in the log.
		var ue *secretstore.UnavailableError
		if errors.As(err, &ue) {
			writeErrCode(w, http.StatusServiceUnavailable, ue.Error(), "store_unavailable")
		} else {
			writeErrCode(w, http.StatusServiceUnavailable, launcher.ErrStoreUnavailable.Error(), "store_unavailable")
		}
	case errors.As(err, new(*box.NotSharedError)):
		var ns *box.NotSharedError
		errors.As(err, &ns)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "not_shared",
			"path": vmDisplay(ns.Dir, ns.Home), "pending": ns.Pending, "read_only": ns.ReadOnly, "need_write": ns.NeedWrite})
	case errors.Is(err, launcher.ErrProfilesNewer):
		// A fixed text: the wrapped error names the host path of profiles.json,
		// which stays in the log above (as writeSaveErr does for a save).
		writeErr(w, http.StatusBadRequest, profilesNewerSpawnMsg)
	case errors.Is(err, box.ErrVMRestarting):
		writeErrCode(w, http.StatusServiceUnavailable, err.Error(), "vm_restarting")
	case errors.Is(err, box.ErrBoxUnavailable):
		// The containment refusal: its sentence names the remedy and is shown as it is. The code is what lets
		// the New session dialog keep it on its own error line (#142); the text and the 400 are unchanged.
		// After vm_restarting and not_shared, which wrap this error and have a code of their own.
		writeErrCode(w, http.StatusBadRequest, err.Error(), "box_unavailable")
	case errors.Is(err, box.ErrUnboxableWorkspace), errors.Is(err, box.ErrWorkspaceHeld),
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

// profilesNewerSpawnMsg is the spawn refusal shown while profiles.json was
// written by a newer CuliBox (#291); `culibox doctor` shows the versions.
const profilesNewerSpawnMsg = "no session can start: profiles.json was written by a newer CuliBox. Run that CuliBox again, or restore the pre-upgrade backup (docs/UPGRADING.md); culibox doctor shows the versions"

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
			Clone     *bool          `json:"clone"`     // nil = the profile decides; false = in place; true = its clone (box only)
			AddRoot   bool           `json:"add_root"`  // the New session form only: make the folder a workspace root (#343)
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
		// A clone is a box's own copy of the repo: the host tier never gets one.
		if req.Clone != nil && *req.Clone && req.Runtime == config.RuntimeHost {
			writeErr(w, http.StatusBadRequest, "a clone runs only in a box")
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
			Prompt: req.Prompt, Replyable: req.Replyable, Account: req.Account, Login: req.Login, Clone: req.Clone,
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
			Root           *rootAdd              `json:"root,omitempty"`
		}{Info: info}
		if a.OptionsDropped != nil {
			resp.OptionsDropped = a.OptionsDropped(spec)
		}
		// The root add comes last and never fails the spawn: the session is
		// already running, and a refusal is only reported.
		resp.Root = a.rootAfterSpawn(r, info, req.AddRoot, req.Clone != nil && *req.Clone)
		writeJSON(w, http.StatusCreated, resp)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSpawnShell serves POST /api/sessions/{id}/shell: a quick shell in the
// parent's working directory and tier, recorded as the parent's companion. The
// body is ignored, so cwd, tier and parent can only come from the server's own
// session list. This is the only place a session's ParentID is set; it is
// reachable from the signed-in browser only (never a bearer token, never an
// agent, which has no cookie), so an agent cannot hide a session under another
// or have one ended with a parent it chose. Nesting is one level: a shell's
// target is replaced by its own parent, but only by one created no later than
// the shell (a newer one reused a freed id after a restart). When nothing alive
// is left to belong to (the parent exited or is gone), the shell opens
// unlinked in the target's own cwd and tier, as "+ Shell" always did.
func (a *API) handleSpawnShell(w http.ResponseWriter, r *http.Request, targetID string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if credKindOf(r) != credCookie || !strictSameOrigin(r) {
		writeErr(w, http.StatusForbidden, "shells are started from the web UI")
		return
	}
	byID := make(map[string]session.Info)
	for _, in := range a.Sessions.List() {
		byID[in.ID] = in
	}
	target, ok := byID[targetID]
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	parent, linked := target, true
	if target.ParentID != "" {
		p, ok := byID[target.ParentID]
		// The client's nesting rule, server-side: a parent must be top-level
		// and no newer than its shell, or the shell is listed as top-level.
		if ok && p.ParentID == "" && !p.Created.After(target.Created) {
			parent = p
		} else {
			linked = false
		}
	}
	if parent.Exited {
		linked = false
	}
	spec := launcher.SpawnSpec{Cwd: target.Cwd, Runtime: target.Runtime}
	if linked {
		spec = launcher.SpawnSpec{Cwd: parent.Cwd, Runtime: parent.Runtime, ParentID: parent.ID}
	}
	shell := ""
	if a.QuickShell != nil {
		shell = a.QuickShell()
	}
	if shell == "" {
		writeErr(w, http.StatusConflict, "no shell profile")
		return
	}
	spec.Agent, spec.Name = shell, "sh:"+filepath.Base(spec.Cwd)
	info, err := a.Sessions.Spawn(spec)
	if err != nil {
		writeSpawnErr(w, shell, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
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
	// GET /api/sessions/{id}/activity[?before=<ms>&limit=<n>] — the transcript
	// timeline merged with the credential and approval audit rows. Cookie
	// session only.
	if sid, ok := strings.CutSuffix(id, "/activity"); ok {
		a.handleActivity(w, r, sid)
		return
	}
	// GET /api/sessions/{id}/boxfile?path=<abs box path>[&download=1] — a file
	// the boxed session's Playwright MCP saved, served read-only. Everything in
	// that directory is written by the box, so the path must sit under the one
	// allowlisted box dir and the open goes through boxread. Nothing about the
	// request or the file is logged.
	if sid, ok := strings.CutSuffix(id, "/boxfile"); ok {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.handleBoxFile(w, r, sid)
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
	// POST /api/sessions/{id}/shell — start a companion shell nested under the
	// session; the only writer of ParentID.
	if sid, ok := strings.CutSuffix(id, "/shell"); ok {
		a.handleSpawnShell(w, r, sid)
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
	// POST /api/sessions/{id}/bring-home — "Bring work home now": start the
	// return and answer 202 at once; the result lands on the session's return state.
	if sid, ok := strings.CutSuffix(id, "/bring-home"); ok {
		if !checkMutationGuard(w, r) {
			return
		}
		switch err := a.Sessions.BringHome(sid); {
		case err == nil:
			writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
		case errors.Is(err, session.ErrNoSession):
			writeErr(w, http.StatusNotFound, "no such session")
		case errors.Is(err, session.ErrNotClone), errors.Is(err, session.ErrReturning):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, "bring home failed")
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
