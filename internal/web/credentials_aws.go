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

	"github.com/culiops/culibox/internal/awshost"
	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/ssologin"
)

// RoleRef is one IAM role as the roles list returns it. SSOTrust is a hint
// read from the role's trust policy (it trusts the account root or an
// AWSReservedSSO_ role), never a promise that the profile can assume it.
type RoleRef struct {
	Name     string `json:"name"`
	ARN      string `json:"arn"`
	SSOTrust bool   `json:"sso_trust,omitempty"`
}

// RolesResult is the outcome of listing the roles a host profile can see.
// Exactly one outcome is set: Roles (possibly none), SSOExpired (the profile
// needs a sign-in), AccessDenied (the profile may not list roles: the screen
// falls back to typing the role name), or Failed (anything else). Capped says
// Roles is not every role the profile can see.
type RolesResult struct {
	Roles        []RoleRef `json:"roles,omitempty"`
	Capped       bool      `json:"capped,omitempty"`
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
	// CheckSSODirect mints the profile's SSO permission-set credentials and runs
	// the read-only gate on them. The time is the credentials' expiration (zero
	// when none were fetched). It bounds itself and never fails open: every
	// failure is an unverified or not-read-only result.
	CheckSSODirect(ctx context.Context, profile, region string) (awsprobe.GateResult, time.Time)
	Roles(ctx context.Context, profile string) RolesResult
	SSOStart(profile string) (ssologin.State, error)
	SSOStatus(profile string) (ssologin.State, bool)
}

const (
	credAWSPrefix = "/api/credentials/aws"
	// credBodyMax caps a definition body (no AWS request carries a value).
	credBodyMax = 64 << 10
	// credValueBodyMax caps a request that carries secret values. Datadog sends
	// two (64 KiB each, so 128 KiB of text plus JSON framing); a value full of
	// characters JSON must escape (a quote or newline costs 2 bytes, a control
	// character 6) can push a near-limit value past it, and then the request is
	// refused as too large.
	credValueBodyMax = 160 << 10
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
	return decodeCredJSONMax(w, r, dst, credBodyMax)
}

// decodeCredJSONMax is decodeCredJSON with its own cap: credValueBodyMax for a
// request that carries a secret value. The buffer the body was read into is
// zeroed before it returns.
func decodeCredJSONMax(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	defer zero(raw)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request too large")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid request")
		}
		return false
	}
	if !decodeBytesStrict(raw, dst) {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// decodeBytesStrict decodes raw into dst: no unknown field, nothing after the
// value. It reports only whether that worked; the decoder's error text can
// quote the input, so it is dropped.
func decodeBytesStrict(raw []byte, dst any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst) == nil && dec.Decode(&json.RawMessage{}) == io.EOF
}

// credAudit appends one AWS-setup row. A row that cannot be written stops the
// call it describes.
func (s *Server) credAudit(w http.ResponseWriter, action, detail string) bool {
	var err error = errors.New("no audit recorder is wired")
	if s.CredAudit != nil {
		err = s.CredAudit.RecordCred(broker.CredEvent{Action: action, Kind: credfile.KindAWS, Actor: storeActor, Detail: detail})
	}
	if err != nil {
		log.Printf("credentials: audit %s: %v", action, err)
		writeErr(w, http.StatusInternalServerError, ErrCredPersist.Error())
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
	writeRateLimited(w, wait)
	return true
}

// writeRateLimited answers 429 with Retry-After rounded up to whole seconds.
func writeRateLimited(w http.ResponseWriter, wait time.Duration) {
	secs := max(int((wait+time.Second-1)/time.Second), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("too many AWS calls: try again in %d seconds", secs))
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
		Mode          string `json:"mode"`
		SourceProfile string `json:"source_profile"`
		RoleARN       string `json:"role_arn"`
		Region        string `json:"region"`
	}
	if !decodeCredJSON(w, r, &req) {
		return
	}
	switch req.Mode {
	case "", "assume_role":
	case config.AWSModeSSODirect:
		s.credAWSCheckSSODirect(w, r, req.SourceProfile, req.RoleARN, req.Region)
		return
	default:
		writeErr(w, http.StatusBadRequest, "mode must be assume_role or sso_direct")
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
	// The signer is about to be used: that row is written first, and without it
	// the call does not happen. The state row follows.
	if !s.credAudit(w, broker.CredChecked, req.RoleARN+" started") {
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

// ssoCheckTimeout bounds one sso_direct check: the mint (20 s) and the gate
// (15 s) it runs, with a margin. The result is never made better by the
// deadline: a gate that runs out of time answers unverified.
const ssoCheckTimeout = 40 * time.Second

// credAWSCheckSSODirect is the sso_direct branch of POST .../check: it mints
// the profile's permission-set credentials and runs the read-only gate, exactly
// as a save and a spawn do, and answers {"gate": ..., "expires": ...}. No
// credential is returned, and no AssumeRole verdict is read or written.
func (s *Server) credAWSCheckSSODirect(w http.ResponseWriter, r *http.Request, profile, roleARN, region string) {
	switch {
	case !credProfileRe.MatchString(profile):
		writeErr(w, http.StatusBadRequest, "source_profile must be letters, digits, . _ - (at most 64)")
		return
	case roleARN != "":
		writeErr(w, http.StatusBadRequest, "role_arn is not used in sso_direct mode; leave it out")
		return
	case awsscope.ValidRegion(region) != nil:
		writeErr(w, http.StatusBadRequest, "region must be an AWS region name (e.g. ap-southeast-1)")
		return
	}
	if s.credLimited(w) {
		return
	}
	detail := "sso_direct " + profile
	if !s.credAudit(w, broker.CredChecked, detail+" started") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ssoCheckTimeout)
	defer cancel()
	res, exp := s.CredAWS.CheckSSODirect(ctx, profile, region)
	res, exp = normalizeGate(res, exp)
	detail += " " + string(res.State)
	if res.Reason != "" {
		detail += " " + string(res.Reason)
	}
	if !s.credAudit(w, broker.CredChecked, detail) {
		return
	}
	writeJSON(w, http.StatusOK, gateBody(res, exp))
}

// normalizeGate makes the fail-closed reading of a gate result: only a
// read_only result that names its role stands as read_only, and any state it
// does not know is unverified. A refusal keeps its own state.
func normalizeGate(res awsprobe.GateResult, exp time.Time) (awsprobe.GateResult, time.Time) {
	switch {
	case res.State == awsprobe.GateReadOnly && res.RoleARN != "":
		return res, exp
	case res.State == awsprobe.GateNotReadOnly:
		return res, time.Time{}
	}
	res.State = awsprobe.GateUnverified
	if res.Reason == "" {
		res.Reason = awsprobe.ReasonError
	}
	return res, time.Time{}
}

// gateBody is the body of an sso_direct check or save: the gate result and the
// credentials' expiration (RFC 3339, "" when none), from which the form derives
// the N hours of its warning.
func gateBody(res awsprobe.GateResult, exp time.Time) map[string]any {
	e := ""
	if !exp.IsZero() {
		e = exp.UTC().Format(time.RFC3339)
	}
	return map[string]any{"gate": res, "expires": e}
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
	if conn.SSODirect() {
		// No acknowledgement exists for this mode: ack_write is not read.
		ctx, cancel := context.WithTimeout(r.Context(), ssoCheckTimeout)
		defer cancel()
		gate, exp, err := s.Creds.PutSSODirect(ctx, conn, create)
		if err != nil {
			writeCredErr(w, err)
			return
		}
		code := http.StatusOK
		if create {
			code = http.StatusCreated
		}
		body := gateBody(gate, exp)
		body["name"] = conn.Name
		writeJSON(w, code, body)
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
	var limited ErrCheckLimited
	var gate ErrSSOGate
	switch {
	case errors.As(err, &gate):
		g, _ := normalizeGate(gate.Gate, time.Time{})
		writeJSON(w, http.StatusConflict, map[string]any{"error": gate.Error(), "code": gate.Code(), "gate": g})
	case errors.Is(err, secretstore.ErrTooLarge):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "the value is larger than 64 KiB", "too_large")
	case errors.Is(err, secretstore.ErrNoSuchValue):
		writeErrCode(w, http.StatusNotFound, "no such value in the store", "not_found")
	case errors.Is(err, secretstore.ErrNotInitialized):
		writeErrCode(w, http.StatusConflict, "the encrypted store is not created yet", "store_missing")
	case errors.Is(err, secretstore.ErrKeeperUnavailable):
		writeStoreUnavailable(w, err)
	case errors.Is(err, secretstore.ErrBadID):
		writeErr(w, http.StatusBadRequest, secretstore.ErrBadID.Error())
	case errors.Is(err, secretstore.ErrCaseCollision):
		writeErrCode(w, http.StatusConflict, err.Error(), "id_collision")
	case errors.As(err, &limited):
		writeRateLimited(w, limited.Wait)
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
