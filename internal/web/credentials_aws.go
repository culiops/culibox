package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/awshost"
	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
	"github.com/culiops/agent-box/internal/ssologin"
)

// RoleRef is one IAM role as the roles list returns it.
type RoleRef struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

// RolesResult is the outcome of listing the roles a host profile can see.
// Exactly one outcome is set: Roles (possibly none), SSOExpired (the profile
// needs a sign-in), AccessDenied (the profile may not list roles: the screen
// falls back to typing the role name), or Failed (anything else).
type RolesResult struct {
	Roles        []RoleRef `json:"roles,omitempty"`
	SSOExpired   bool      `json:"sso_expired,omitempty"`
	AccessDenied bool      `json:"access_denied,omitempty"`
	Failed       bool      `json:"failed,omitempty"`
}

// AWSCredService is what the credentials screen needs from the host's AWS
// signer: it is the broker's own mint-and-simulate behind a base-role check, a
// role listing signed by a host profile, and the in-UI SSO sign-in. nil on the
// Server means no broker is running.
type AWSCredService interface {
	BaseRoleChecker
	Roles(ctx context.Context, profile string) RolesResult
	SSOStart(profile string) (ssologin.State, error)
	SSOStatus(profile string) (ssologin.State, bool)
}

const (
	credAWSPrefix = "/api/credentials/aws"
	// credBodyMax caps a definition body (no AWS request carries a value).
	credBodyMax = 64 << 10
)

// credProfileRe is the source_profile shape every AWS call accepts, checked
// before a name reaches the SDK or the aws CLI.
var (
	credProfileRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	credAccountRe = regexp.MustCompile(`^[0-9]{12}$`)
	credNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// awsRouteWords are the path words under /api/credentials/aws; a connection
// named like one could not be edited or deleted by name.
var awsRouteWords = []string{"host-profiles", "roles", "check", "sso-login"}

// callLimiter is a fixed-window limit on the calls that use the host's AWS
// signer. The zero value allows 10 calls a minute; it is for the whole daemon,
// not per client: there is one operator, and a stolen cookie must not multiply
// the calls. now is injectable for tests.
type callLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	start  time.Time
	n      int
}

// allow takes one call from the window. When it cannot, it returns how long
// until the window ends.
func (l *callLimiter) allow() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	max, window, now := l.max, l.window, time.Now
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = time.Minute
	}
	if l.now != nil {
		now = l.now
	}
	t := now()
	if l.start.IsZero() || t.Sub(l.start) >= window {
		l.start, l.n = t, 0
	}
	if l.n >= max {
		return l.start.Add(window).Sub(t), false
	}
	l.n++
	return 0, true
}

// verdictBody is a base-role result on the wire.
type verdictBody struct {
	State      string     `json:"state"`
	Actions    []string   `json:"actions,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	ReasonText string     `json:"reason_text,omitempty"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
}

func verdictOf(r awsprobe.Result) verdictBody {
	n := r.Normalize()
	v := verdictBody{State: string(n.State), Actions: n.Actions, Reason: string(n.Reason)}
	if n.Reason != "" {
		v.ReasonText = awsprobe.ReasonText(n.Reason)
	}
	if !n.CheckedAt.IsZero() {
		at := n.CheckedAt
		v.CheckedAt = &at
	}
	return v
}

// assumeOf says whether the base role could be assumed, from a probe result.
// A simulation that was refused still means the assume worked.
func assumeOf(r awsprobe.Result) string {
	if r.State != awsprobe.Unverified {
		return "ok"
	}
	switch r.Reason {
	case awsprobe.ReasonAssumeDenied:
		return "denied"
	case awsprobe.ReasonSSOExpired:
		return "sso_expired"
	case awsprobe.ReasonSimulateDenied:
		return "ok"
	}
	return "error"
}

// credGate is the one rule for a credentials request: this page's own
// signed-in browser session (never a bearer token). A route that changes
// something or uses the host's AWS signer also needs a matching Origin
// (strict); a plain GET carries none from the same origin, so it is held to
// the lenient same-origin check.
func credGate(w http.ResponseWriter, r *http.Request, strict bool) bool {
	ok := credKindOf(r) == credCookie
	if ok && strict {
		ok = strictSameOrigin(r)
	} else if ok {
		ok = sameOrigin(r)
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "credentials need this page's signed-in browser session")
	}
	return ok
}

// decodeCredJSON reads a request body into dst: at most credBodyMax bytes, no
// unknown field, nothing after the value. No error text echoes the body.
func decodeCredJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, credBodyMax))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request too large")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid request")
		}
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.Decode(&json.RawMessage{}) != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// credAudit appends one AWS-setup row. A row that cannot be written stops the
// call it describes.
func (s *Server) credAudit(w http.ResponseWriter, action, detail string) bool {
	if s.CredAudit == nil {
		return true
	}
	if err := s.CredAudit.RecordCred(broker.CredEvent{Action: action, Kind: credfile.KindAWS, Actor: storeActor, Detail: detail}); err != nil {
		log.Printf("credentials: audit %s: %v", action, err)
		writeErr(w, http.StatusInternalServerError, "could not record the audit row; nothing was done")
		return false
	}
	return true
}

// credLimited answers 429 when the shared AWS-call budget is spent.
func (s *Server) credLimited(w http.ResponseWriter) bool {
	wait, ok := s.credLimit.allow()
	if ok {
		return false
	}
	secs := int((wait + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("too many AWS calls: try again in %d seconds", secs))
	return true
}

func writeNoBroker(w http.ResponseWriter) {
	writeErrCode(w, http.StatusConflict, ErrNoBroker.Error(), "no_broker")
}

// handleCredAWS serves /api/credentials/aws and everything under it.
func (s *Server) handleCredAWS(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, credAWSPrefix)
	var allow []string
	var h func(http.ResponseWriter, *http.Request, string)
	switch rest {
	case "":
		allow, h = []string{http.MethodPost}, s.credAWSCreate
	case "/host-profiles":
		allow, h = []string{http.MethodGet}, s.credAWSHostProfiles
	case "/roles":
		allow, h = []string{http.MethodPost}, s.credAWSRoles
	case "/check":
		allow, h = []string{http.MethodPost}, s.credAWSCheck
	case "/sso-login":
		allow, h = []string{http.MethodPost, http.MethodGet}, s.credAWSSSO
	default:
		name := strings.TrimPrefix(rest, "/")
		if !strings.HasPrefix(rest, "/") || strings.Contains(name, "/") || name == "" {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		allow, h = []string{http.MethodPut, http.MethodDelete}, func(w http.ResponseWriter, r *http.Request, _ string) {
			s.credAWSEntry(w, r, name)
		}
	}
	if !slices.Contains(allow, r.Method) {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !credGate(w, r, r.Method != http.MethodGet) {
		return
	}
	h(w, r, rest)
}

// credAWSHostProfiles is GET .../host-profiles: the profiles of the host's AWS
// config file, without any key material.
func (s *Server) credAWSHostProfiles(w http.ResponseWriter, _ *http.Request, _ string) {
	profiles, err := awshost.HostProfiles()
	if err != nil {
		log.Printf("credentials: host profiles: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not read the AWS config file")
		return
	}
	if profiles == nil {
		profiles = []awshost.Profile{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

// credAWSRoles is POST .../roles.
func (s *Server) credAWSRoles(w http.ResponseWriter, r *http.Request, _ string) {
	if s.CredAWS == nil {
		writeNoBroker(w)
		return
	}
	var req struct {
		SourceProfile string `json:"source_profile"`
		AccountID     string `json:"account_id"`
	}
	if !decodeCredJSON(w, r, &req) {
		return
	}
	if !credProfileRe.MatchString(req.SourceProfile) {
		writeErr(w, http.StatusBadRequest, "source_profile must be letters, digits, . _ - (at most 64)")
		return
	}
	if req.AccountID != "" && !credAccountRe.MatchString(req.AccountID) {
		writeErr(w, http.StatusBadRequest, "account_id must be 12 digits")
		return
	}
	if s.credLimited(w) || !s.credAudit(w, broker.CredRolesListed, req.SourceProfile) {
		return
	}
	res := s.CredAWS.Roles(r.Context(), req.SourceProfile)
	if req.AccountID != "" {
		res.Roles = slices.DeleteFunc(slices.Clone(res.Roles), func(x RoleRef) bool { return awsscope.AccountOf(x.ARN) != req.AccountID })
	}
	writeJSON(w, http.StatusOK, res)
}

// credAWSCheck is POST .../check: the broker's base-role probe, exactly as a
// spawn would make it.
func (s *Server) credAWSCheck(w http.ResponseWriter, r *http.Request, _ string) {
	if s.CredAWS == nil {
		writeNoBroker(w)
		return
	}
	var req struct {
		SourceProfile string `json:"source_profile"`
		RoleARN       string `json:"role_arn"`
		Region        string `json:"region"`
	}
	if !decodeCredJSON(w, r, &req) {
		return
	}
	switch {
	case !credProfileRe.MatchString(req.SourceProfile):
		writeErr(w, http.StatusBadRequest, "source_profile must be letters, digits, . _ - (at most 64)")
		return
	case awsscope.ValidRoleARN(req.RoleARN) != nil:
		writeErr(w, http.StatusBadRequest, "role_arn must be an IAM role ARN (arn:aws:iam::<12-digit account>:role/<name>)")
		return
	case awsscope.ValidRegion(req.Region) != nil:
		writeErr(w, http.StatusBadRequest, "region must be an AWS region name (e.g. ap-southeast-1)")
		return
	}
	if s.credLimited(w) {
		return
	}
	res := s.CredAWS.Check(r.Context(), broker.ProbeTarget{SourceProfile: req.SourceProfile, Region: req.Region, RoleARN: req.RoleARN}).Normalize()
	detail := req.RoleARN + " " + string(res.State)
	if res.Reason != "" {
		detail += " " + string(res.Reason)
	}
	if !s.credAudit(w, broker.CredChecked, detail) {
		return
	}
	if s.Creds != nil {
		s.Creds.RecordVerdict(req.SourceProfile, req.RoleARN, res)
	}
	v := verdictOf(res)
	writeJSON(w, http.StatusOK, map[string]any{
		"assume": assumeOf(res), "verdict": v.State, "actions": v.Actions,
		"reason": v.Reason, "reason_text": v.ReasonText, "checked_at": v.CheckedAt,
	})
}

// credAWSSSO is POST (start) and GET (status) .../sso-login.
func (s *Server) credAWSSSO(w http.ResponseWriter, r *http.Request, _ string) {
	if s.CredAWS == nil {
		writeNoBroker(w)
		return
	}
	if r.Method == http.MethodGet {
		p := r.URL.Query().Get("profile")
		if !credProfileRe.MatchString(p) || !ssologin.ValidProfile(p) {
			writeErr(w, http.StatusBadRequest, "profile must be letters, digits, . _ - (at most 64)")
			return
		}
		st, ok := s.CredAWS.SSOStatus(p)
		if !ok {
			writeErr(w, http.StatusNotFound, "no sign-in in progress")
			return
		}
		writeJSON(w, http.StatusOK, st)
		return
	}
	var req struct {
		SourceProfile string `json:"source_profile"`
	}
	if !decodeCredJSON(w, r, &req) {
		return
	}
	if !credProfileRe.MatchString(req.SourceProfile) || !ssologin.ValidProfile(req.SourceProfile) {
		writeErr(w, http.StatusBadRequest, "source_profile must be letters, digits, . _ - (at most 64, not starting with -)")
		return
	}
	if s.credLimited(w) || !s.credAudit(w, broker.CredSSOStarted, req.SourceProfile) {
		return
	}
	st, err := s.CredAWS.SSOStart(req.SourceProfile)
	if err != nil {
		var le *SSOLoginError
		if errors.As(err, &le) {
			log.Printf("credentials: sso login: %v", le)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":   "could not start the AWS sign-in here; run the command on the host",
				"command": le.Command,
			})
			return
		}
		log.Printf("credentials: sso login: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not start the AWS sign-in")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// awsSaveReq is the body of an AWS connection create or replace.
type awsSaveReq struct {
	Connection config.AWSConnection `json:"connection"`
	AckWrite   bool                 `json:"ack_write"`
}

// credAWSCreate is POST /api/credentials/aws.
func (s *Server) credAWSCreate(w http.ResponseWriter, r *http.Request, _ string) {
	s.credAWSSave(w, r, "", true)
}

func (s *Server) credAWSSave(w http.ResponseWriter, r *http.Request, name string, create bool) {
	if create && s.CredAWS == nil {
		writeNoBroker(w)
		return
	}
	var req awsSaveReq
	if !decodeCredJSON(w, r, &req) {
		return
	}
	conn := req.Connection
	if !create {
		if conn.Name == "" {
			conn.Name = name
		}
		if conn.Name != name {
			writeErr(w, http.StatusBadRequest, "the name in the body must match the URL")
			return
		}
	}
	if slices.Contains(awsRouteWords, conn.Name) {
		writeErr(w, http.StatusBadRequest, "that name is reserved")
		return
	}
	res, err := s.Creds.PutAWS(r.Context(), conn, create, req.AckWrite)
	if err != nil {
		writeCredErr(w, err)
		return
	}
	code := http.StatusOK
	if create {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"name": conn.Name, "verdict": verdictOf(res)})
}

// credAWSEntry is PUT and DELETE /api/credentials/aws/{name}.
func (s *Server) credAWSEntry(w http.ResponseWriter, r *http.Request, name string) {
	if !credNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "invalid name")
		return
	}
	if r.Method == http.MethodPut {
		s.credAWSSave(w, r, name, false)
		return
	}
	if err := s.Creds.Delete(credfile.KindAWS, name); err != nil {
		writeCredErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeCredErr maps a CredentialManager error. The sentences of the manager's
// own rules are shown (they name the entry the caller just sent, never a
// value); a failure to audit or save is logged and shown as a fixed sentence,
// because its text can name a path.
func writeCredErr(w http.ResponseWriter, err error) {
	var inUse ErrCredInUse
	var ack ErrNeedsAck
	switch {
	case errors.As(err, &ack):
		writeJSON(w, http.StatusConflict, map[string]any{"error": ack.Error(), "code": "needs_ack", "verdict": verdictOf(ack.Result)})
	case errors.Is(err, ErrNoBroker):
		writeNoBroker(w)
	case errors.Is(err, ErrCredExists):
		writeErrCode(w, http.StatusConflict, err.Error(), "exists")
	case errors.Is(err, ErrCredSeed):
		writeErrCode(w, http.StatusConflict, err.Error(), "seed")
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "in_use", "used_by": inUse.By})
	case errors.Is(err, ErrCredNotFound):
		writeErrCode(w, http.StatusNotFound, err.Error(), "not_found")
	case errors.Is(err, ErrCredPersist):
		log.Printf("credentials: %v", err)
		writeErr(w, http.StatusInternalServerError, ErrCredPersist.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}
