package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/ssologin"
)

// ElevationService is what the elevation and sign-in endpoints need from the
// daemon. cred is the credential kind the request arrived with (broker.CredCookie,
// broker.CredBearer or "none"); only a signed-in browser may grant.
type ElevationService interface {
	Grant(id string, minutes int, reason, confirmLabel, cred string) (broker.ElevationView, error)
	Drop(id, cred string) (broker.ElevationView, error)
	// Refused audits a refusal the web layer decided itself.
	Refused(id, cred, reason string)
	SSOLogin(id string) (ssologin.State, error)
}

// ErrElevationUnknown is what the service answers for an id that is not a live
// boxed session with an AWS binding.
var ErrElevationUnknown = errors.New("no live boxed session with an AWS binding has this id")

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

// SSOStatus is the sso object of GET .../aws.
type SSOStatus struct {
	Expired bool            `json:"expired"`
	Source  string          `json:"source,omitempty"`
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
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		v, err := a.Elevation.Grant(sid, req.Minutes, req.Reason, req.ConfirmLabel, credName(r))
		if err != nil {
			writeElevErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"elevation": v})
	case http.MethodDelete:
		// Lowering power: any authenticated caller may (D7). The auth layer
		// admits only a cookie or a bearer token; "none" here is a wiring bug.
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
	// service takes it from the session's recorded binding.
	if credKindOf(r) != credCookie || !strictSameOrigin(r) {
		writeErr(w, http.StatusForbidden, "signing in needs this page's signed-in browser session")
		return
	}
	st, err := a.Elevation.SSOLogin(sid)
	if err != nil {
		writeElevErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": st})
}

// writeElevErr maps a service error onto a response. None of the texts carries
// a credential, a token or process output: the broker's refusals are fixed
// sentences, and anything unrecognised is logged here and shown generically.
func writeElevErr(w http.ResponseWriter, err error) {
	var le *SSOLoginError
	switch {
	case errors.Is(err, ErrElevationUnknown):
		writeErr(w, http.StatusNotFound, "no such session")
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
