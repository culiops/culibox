package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/ssologin"
)

// ElevationService is what the elevation and sign-in endpoints need from the
// daemon. cred is the credential kind the request arrived with (broker.CredCookie,
// broker.CredBearer or "none"); only a signed-in browser may grant.
type ElevationService interface {
	// Grant asks for a write window; req.Name is the AWS connection it is for
	// ("" for the legacy binding).
	Grant(id string, req broker.ElevateRequest) (broker.ElevationView, error)
	Drop(id, cred string) (broker.ElevationView, error)
	// Refused audits a refusal the web layer decided itself.
	Refused(id, cred, reason string)
	// SSOLogin signs in for the source profile recorded on the session's
	// binding, or on its connection name ("" for the legacy binding).
	SSOLogin(id, name string) (ssologin.State, error)
}

// ErrElevationUnknown is what the service answers for an id that is not a live
// boxed session with an AWS binding.
var ErrElevationUnknown = errors.New("no live boxed session with an AWS binding has this id")

// ErrConnectionName is what the service wraps when a request names no AWS
// connection the session has (or names one where the session has none): the
// caller's mistake, answered 400.
var ErrConnectionName = errors.New("name one of this session's AWS connections")

// SSOLoginError is a sign-in that could not start or whose output could not be
// read. Command is the host command the human can run instead; it names the
// session's recorded source profile and nothing else.
type SSOLoginError struct {
	Command string
	Err     error
}

func (e *SSOLoginError) Error() string {
	if e.Err == nil {
		return "aws sso login unavailable"
	}
	return "aws sso login unavailable: " + e.Err.Error()
}

func (e *SSOLoginError) Unwrap() error { return e.Err }

// SSOStatus is the sso object of GET .../aws. For a session bound by named
// connections, Sources has one entry per sign-in source in play, and the
// top-level fields summarise them (Expired: any is; Source: the first).
type SSOStatus struct {
	Expired bool            `json:"expired"`
	Source  string          `json:"source,omitempty"`
	Command string          `json:"command,omitempty"`
	Login   *ssologin.State `json:"login,omitempty"`
	Sources []SSOSource     `json:"sources,omitempty"`
}

// SSOSource is one sign-in source of a session with connections: the
// connections that sign in through it (sorted), whether their mints wait on
// it, the host command and any flow in progress.
type SSOSource struct {
	Source  string          `json:"source"`
	Names   []string        `json:"names"`
	Expired bool            `json:"expired"`
	Command string          `json:"command,omitempty"`
	Login   *ssologin.State `json:"login,omitempty"`
}

// HostSession names a live host-tier session that is not contained.
type HostSession struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GrantContext is what the grant panel needs besides the binding itself.
type GrantContext struct {
	HostSessions []HostSession `json:"host_sessions"`
	Blocked      string        `json:"blocked,omitempty"`
	// ConnBlocked is, for a session with connections, why a grant for a
	// write-capable connection would be refused now because of another
	// connection's window or leftover write credentials; absent when none is.
	ConnBlocked map[string]string `json:"conn_blocked,omitempty"`
}

var sessionIDPathRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)

const (
	elevateSuffix  = "/aws/elevate"
	ssoLoginSuffix = "/aws/sso-login"
	refusedPrefix  = "elevation refused: "
)

// strictSameOrigin: the Origin header must be present and name this host, and
// Sec-Fetch-Site (when sent) must not say otherwise. csrfGuard lets an absent
// Origin through for older clients; a grant of write access and a host
// sign-in process do not (M3b spec §9, correction 13).
func strictSameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return (o == "http://"+r.Host || o == "https://"+r.Host) && sameOrigin(r)
}

// credName is the fixed label of the credential kind a request arrived with.
func credName(r *http.Request) string {
	switch credKindOf(r) {
	case credCookie:
		return broker.CredCookie
	case credBearer:
		return broker.CredBearer
	}
	return "none"
}

// elevateSID is the session id of a POST .../aws/elevate path, if the path is
// one and the id is well formed.
func elevateSID(r *http.Request) (string, bool) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/sessions/")
	if !ok {
		return "", false
	}
	sid, ok := strings.CutSuffix(rest, elevateSuffix)
	if !ok || !sessionIDPathRe.MatchString(sid) {
		return "", false
	}
	return sid, true
}

// auditCSRFRefusal is csrfGuard's refusal hook. csrfGuard turns a cross-origin
// cookie POST away before any handler runs, which would leave an attempted
// grant out of the audit; this records it, only for POST .../aws/elevate. The
// broker throttles the rows.
func (a *API) auditCSRFRefusal(r *http.Request) {
	if a == nil || a.Elevation == nil || r.Method != http.MethodPost {
		return
	}
	if sid, ok := elevateSID(r); ok {
		a.Elevation.Refused(sid, credName(r), "grant attempted from another origin")
	}
}

func (a *API) handleElevate(w http.ResponseWriter, r *http.Request, sid string) {
	if a.Elevation == nil || !sessionIDPathRe.MatchString(sid) {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	switch r.Method {
	case http.MethodPost:
		if credKindOf(r) != credCookie || !strictSameOrigin(r) {
			a.Elevation.Refused(sid, credName(r), "grant attempted without a signed-in browser session on this origin")
			writeErr(w, http.StatusForbidden, "granting write access needs this page's signed-in browser session")
			return
		}
		var req struct {
			Minutes      int    `json:"minutes"`
			Reason       string `json:"reason"`
			ConfirmLabel string `json:"confirm_label"`
			Name         string `json:"name"` // the AWS connection; absent for the legacy binding
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || dec.Decode(&json.RawMessage{}) != io.EOF { // one value, nothing after it
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		v, err := a.Elevation.Grant(sid, broker.ElevateRequest{Minutes: req.Minutes, Name: req.Name, Reason: req.Reason,
			ConfirmLabel: req.ConfirmLabel, Cred: credName(r)})
		if err != nil {
			writeElevErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"elevation": v})
	case http.MethodDelete:
		// Lowering power: any authenticated caller may (D7). The auth layer
		// admits only a cookie or a bearer token; "none" here is a wiring bug.
		// The body is never read: a drop names no connection and fails over none.
		if credKindOf(r) == credNone {
			writeErr(w, http.StatusForbidden, "sign in first")
			return
		}
		v, err := a.Elevation.Drop(sid, credName(r))
		if err != nil {
			writeElevErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"elevation": v})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleSSOLogin(w http.ResponseWriter, r *http.Request, sid string) {
	if a.Elevation == nil || !sessionIDPathRe.MatchString(sid) {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Not audited when refused: it grants nothing. It does start a host
	// process and returns a device code, so only this page's own browser
	// session may ask. The profile is never read from the request: the
	// service takes it from the session's recorded binding, or from the
	// recorded connection the optional body names. Nothing else is accepted.
	if credKindOf(r) != credCookie || !strictSameOrigin(r) {
		writeErr(w, http.StatusForbidden, "signing in needs this page's signed-in browser session")
		return
	}
	name, ok := readSSOName(w, r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	st, err := a.Elevation.SSOLogin(sid, name)
	if err != nil {
		writeElevErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": st})
}

// readSSOName reads the optional sign-in body {"name": "..."}: an empty body
// (what a legacy client sends) is no name. ok is false for anything else than
// one such object of at most 4 KiB.
func readSSOName(w http.ResponseWriter, r *http.Request) (name string, ok bool) {
	var req struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	switch err := dec.Decode(&req); {
	case err == io.EOF:
		return "", true
	case err != nil:
		return "", false
	}
	if dec.Decode(&json.RawMessage{}) != io.EOF { // one value, nothing after it
		return "", false
	}
	return req.Name, true
}

// writeElevErr maps a service error onto a response. None of the texts carries
// a credential, a token or process output: the broker's refusals are fixed
// sentences, and anything unrecognised is logged here and shown generically.
func writeElevErr(w http.ResponseWriter, err error) {
	var le *SSOLoginError
	switch {
	case errors.Is(err, ErrElevationUnknown):
		writeErr(w, http.StatusNotFound, "no such session")
	case errors.Is(err, ErrConnectionName):
		writeErr(w, http.StatusBadRequest, ErrConnectionName.Error()) // fixed text: the request's name is never echoed
	case errors.Is(err, broker.ErrElevateRefused):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), refusedPrefix))
	case errors.As(err, &le):
		log.Printf("sso login: %v", le)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "could not start the AWS sign-in here; run the command on the host",
			"command": le.Command,
		})
	default:
		log.Printf("elevation: %v", err)
		writeErr(w, http.StatusInternalServerError, "elevation failed")
	}
}
