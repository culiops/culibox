package awsprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/smithy-go"

	"github.com/culiops/culibox/internal/awsscope"
)

// GateState is the outcome of the sso_direct read-only gate. Only GateReadOnly
// lets a permission set's credentials reach an agent.
type GateState string

const (
	GateReadOnly    GateState = "read_only"
	GateNotReadOnly GateState = "not_read_only"
	GateUnverified  GateState = "unverified"
)

const (
	// ReasonMissingPerms: the permission set cannot read its own policies;
	// GateResult.MissingPerms names the IAM actions to grant.
	ReasonMissingPerms Reason = "missing_permissions"
	// ReasonMissingContext: the simulation needs condition context values it
	// cannot know, so it proves nothing for those actions.
	ReasonMissingContext Reason = "missing_context"
)

// ErrSSOExpired is wrapped by whoever fetched the credentials when the host's
// SSO login has lapsed. The broker's ErrSSOExpired is this same value.
var ErrSSOExpired = errors.New("SSO session expired")

// DataReadActions is the gate's descriptive data-read set (spec step 6): what
// the AssumeRole investigate scope denies, plus S3 object reads and KMS
// decryption.
var DataReadActions = append(awsscope.DataReadDenies(), "s3:GetObject", "kms:Decrypt")

// gateRoleRead and gatePolicyReads are the IAM reads the gate needs from the
// permission set itself (spec steps 2 and 3), sorted.
var (
	gateRoleRead    = "iam:GetRole"
	gatePolicyReads = []string{"iam:GetPolicy", "iam:GetPolicyVersion", "iam:GetRolePolicy",
		"iam:ListAttachedRolePolicies", "iam:ListRolePolicies"}
)

// DataRead is one step-6 action and whether the permission set denies it.
type DataRead struct {
	Action string `json:"action"`
	Denied bool   `json:"denied"`
}

// GateResult is one gate outcome. Refused lists every refusing pattern or
// action found, sorted, not just the first, so an admin can fix them at once.
type GateResult struct {
	State        GateState  `json:"state"`
	Reason       Reason     `json:"reason,omitempty"`        // Unverified only
	RoleARN      string     `json:"role_arn,omitempty"`      // the IAM role ARN, aws-reserved path included
	Caller       string     `json:"caller,omitempty"`        // the assumed-role ARN without its session name, set once step 1 passed
	Refused      []Verdict  `json:"refused,omitempty"`       // NotReadOnly; for Unverified, the actions that proved nothing
	MissingPerms []string   `json:"missing_perms,omitempty"` // Reason == missing_permissions
	DataReads    []DataRead `json:"data_reads,omitempty"`    // descriptive, never blocks
	// PolicyDigest is the sha256 (hex) of the permission set's policy
	// documents, set once step 3 read them. It pins an acknowledgement of a
	// structural refusal (NotAction, missing Action), whose pattern names no
	// content.
	PolicyDigest string    `json:"policy_digest,omitempty"`
	CheckedAt    time.Time `json:"checked_at,omitzero"`
}

// SimDecision is one simulated action: its EvalDecision and whether the
// simulation reported missing context values.
type SimDecision struct {
	Decision       string
	MissingContext bool
}

// GateAPI is the AWS surface the gate reads, all with the permission set's
// own credentials. Errors are returned unchanged so the gate can classify them.
type GateAPI interface {
	CallerARN(ctx context.Context) (string, error)                     // sts:GetCallerIdentity
	RoleARN(ctx context.Context, roleName string) (string, error)      // iam:GetRole
	PolicyDocs(ctx context.Context, roleName string) ([]string, error) // inline + attached default versions, all pages
	Simulate(ctx context.Context, roleARN string, actions []string) (map[string]SimDecision, error)
}

const ssoRolePrefix = "AWSReservedSSO_"

// Gate decides whether the SSO permission set behind api's credentials is
// read-only (spec "Gate algorithm", steps 1-7). It fails closed: only a
// definite proof returns GateReadOnly. A definite refusal wins over an
// unverifiable step, since nothing later can make a refused set read-only.
// account is the profile's sso_account_id. The time budget is ctx's.
func Gate(ctx context.Context, api GateAPI, account string, now time.Time) GateResult {
	g := gateRun{res: GateResult{CheckedAt: now}}

	// Step 1: the caller is an Identity Center permission-set role session in
	// the profile's account.
	caller, err := api.CallerARN(ctx)
	if err != nil {
		return g.unverified(gateReason(ctx, err, ReasonError))
	}
	partition, callerAcct, roleName, ok := ssoCaller(caller)
	if !ok {
		return g.refuse(Verdict{Pattern: PatternCaller, Why: "is not an IAM Identity Center permission-set role session (assumed-role/" + ssoRolePrefix + "*)"})
	}
	if callerAcct != account {
		return g.refuse(Verdict{Pattern: PatternAccount, Why: fmt.Sprintf("the credentials are for account %s, not the profile's account %q", callerAcct, account)})
	}

	g.res.Caller = "arn:" + partition + ":sts::" + callerAcct + ":assumed-role/" + roleName

	// Step 2: the role's real ARN, with its aws-reserved path.
	roleARN, err := api.RoleARN(ctx, roleName)
	if err != nil {
		return g.failed(ctx, err, append([]string{gateRoleRead}, gatePolicyReads...))
	}
	if !ssoRoleARN(roleARN, partition, account, roleName) {
		return g.refuse(Verdict{Pattern: PatternRole, Why: "iam:GetRole did not return this permission set's aws-reserved/sso.amazonaws.com role"})
	}
	g.res.RoleARN = roleARN

	// Step 3: every inline and attached policy document. A permissions
	// boundary only restricts, so it is not read.
	docs, err := api.PolicyDocs(ctx, roleName)
	if err != nil {
		return g.failed(ctx, err, gatePolicyReads)
	}

	g.res.PolicyDigest = policyDigest(docs)

	// Step 4: deny-aware residual allows.
	residual, err := Residual(docs)
	switch {
	case errors.Is(err, errAllowNotAction):
		return g.refuse(Verdict{Pattern: "NotAction", Why: err.Error()})
	case errors.Is(err, errAllowNoAction):
		return g.refuse(Verdict{Pattern: "Action", Why: err.Error()})
	case err != nil: // unreadable document: nothing is proven either way
		return g.unverified(ReasonError)
	}
	denies, err := UnconditionalDenies(docs)
	if err != nil { // Residual parsed the same documents; kept fail-closed
		return g.unverified(ReasonError)
	}

	// Step 5: classify, then screen what passed for credential minting. A
	// refused pattern already names everything it covers, so it is not
	// expanded into actions to simulate.
	cls := ClassifyResidual(residual)
	g.res.Refused = append(g.res.Refused, cls.Refused...)
	refusedPat := map[string]bool{}
	for _, v := range cls.Refused {
		refusedPat[strings.ToLower(v.Pattern)] = true
	}
	var passing []string
	for _, p := range residual {
		if !refusedPat[strings.ToLower(p)] {
			passing = append(passing, p)
		}
	}
	mustDeny, unknown := MintingScreen(passing)
	for _, p := range unknown {
		g.res.Refused = append(g.res.Refused, Verdict{Pattern: p, Why: "is not in the AWS action catalog snapshot (" + CatalogDate +
			"); update it with `go run ./internal/awsprobe/gencatalog`"})
	}

	// Steps 6 and 7: one batched simulation of everything that must be denied
	// plus the descriptive data-read set.
	why := map[string]string{} // lowercased action -> why it must be denied
	var actions []string
	add := func(a, w string) {
		k := strings.ToLower(a)
		if _, dup := why[k]; !dup {
			why[k] = w
			actions = append(actions, a)
		}
	}
	for _, a := range WriteActions {
		add(a, "is a write action")
	}
	sePatterns := make([]string, 0, len(cls.SideEffect))
	for p := range cls.SideEffect {
		sePatterns = append(sePatterns, p)
	}
	slices.SortFunc(sePatterns, cmpFold)
	for _, p := range sePatterns {
		for _, a := range cls.SideEffect[p] {
			add(a, "is the side-effect of "+p)
		}
	}
	for _, a := range mustDeny {
		add(a, "can mint or return credentials")
	}
	dataRead := map[string]bool{}
	for _, a := range DataReadActions {
		dataRead[strings.ToLower(a)] = true
		if _, dup := why[strings.ToLower(a)]; !dup {
			actions = append(actions, a)
		}
	}

	reply, err := api.Simulate(ctx, roleARN, actions)
	if err != nil {
		return g.unverified(gateReason(ctx, err, ReasonSimulateDenied))
	}
	decisions := make(map[string]SimDecision, len(reply))
	for a, d := range reply {
		decisions[strings.ToLower(a)] = d
	}

	var missingCtx []Verdict
	incomplete := false
	for _, a := range actions {
		k := strings.ToLower(a)
		d, ok := decisions[k]
		if !ok || (d.Decision != "allowed" && d.Decision != "explicitDeny" && d.Decision != "implicitDeny") {
			incomplete = true
			continue
		}
		// What denied means (spec, amended by R17): explicitDeny backed by
		// an unconditional Resource "*" Deny in the permission set's own
		// documents (the simulator reports a conditional or NotResource Deny
		// as explicitDeny with no missing context, and an SCP or boundary
		// Deny is invisible here), or implicitDeny with no residual Allow
		// covering the action.
		cover := coveringAllow(residual, a)
		var denied bool
		switch d.Decision {
		case "explicitDeny":
			denied = denies.Denies(a)
		case "implicitDeny":
			denied = cover == ""
		}
		if dataRead[k] {
			g.res.DataReads = append(g.res.DataReads, DataRead{Action: a, Denied: denied})
		}
		w, must := why[k]
		if !must || denied {
			continue
		}
		switch {
		case d.Decision == "allowed" && d.MissingContext:
			g.res.Refused = append(g.res.Refused, Verdict{Pattern: a, Why: w + " and is allowed under a condition the simulation could not evaluate"})
		case d.Decision == "allowed":
			g.res.Refused = append(g.res.Refused, Verdict{Pattern: a, Why: w + " and is allowed"})
		case d.Decision == "explicitDeny":
			g.res.Refused = append(g.res.Refused, Verdict{Pattern: a, Why: w + "; the simulation denies it, but no unconditional Deny (no Condition, no NotResource, " +
				`Resource "*") in the permission set's own policies covers it: the deny is conditional, resource-scoped, or in an SCP or boundary. Make it unconditional`})
		case d.MissingContext: // implicitDeny under a conditional covering Allow
			missingCtx = append(missingCtx, Verdict{Pattern: a, Why: w + "; the Allow " + cover +
				" covers it and the simulation needs condition context values it cannot know"})
		default: // implicitDeny under a covering Allow
			g.res.Refused = append(g.res.Refused, Verdict{Pattern: a, Why: w + "; implicitly denied, but the Allow " + cover +
				" covers it (a resource- or condition-scoped grant the simulation cannot see)"})
		}
	}

	switch {
	case len(g.res.Refused) > 0:
		return g.done(GateNotReadOnly)
	case incomplete:
		return g.unverified(ReasonError)
	case len(missingCtx) > 0:
		g.res.Refused, g.res.Reason = missingCtx, ReasonMissingContext
		return g.done(GateUnverified)
	}
	return g.done(GateReadOnly)
}

type gateRun struct{ res GateResult }

func (g *gateRun) done(s GateState) GateResult {
	g.res.State = s
	slices.SortFunc(g.res.Refused, func(a, b Verdict) int {
		if c := cmpFold(a.Pattern, b.Pattern); c != 0 {
			return c
		}
		return strings.Compare(a.Why, b.Why)
	})
	return g.res
}

func (g *gateRun) refuse(v ...Verdict) GateResult {
	g.res.Refused = append(g.res.Refused, v...)
	return g.done(GateNotReadOnly)
}

// unverified ends the run Unverified, unless a definite refusal is already
// known, which wins.
func (g *gateRun) unverified(r Reason) GateResult {
	if len(g.res.Refused) > 0 {
		return g.done(GateNotReadOnly)
	}
	g.res.Reason = r
	return g.done(GateUnverified)
}

// failed ends the run on an IAM read error; AccessDenied names the reads to
// grant.
func (g *gateRun) failed(ctx context.Context, err error, perms []string) GateResult {
	r := gateReason(ctx, err, ReasonMissingPerms)
	if r == ReasonMissingPerms {
		g.res.MissingPerms = slices.Clone(perms)
	}
	return g.unverified(r)
}

// gateReason maps an AWS call failure to a Reason; denied is the reason for
// AccessDenied at this step. Expired role-session credentials (ExpiredToken)
// read as SSO expired: signing in again is the fix.
func gateReason(ctx context.Context, err error, denied Reason) Reason {
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return ReasonTimeout
	}
	if errors.Is(err, ErrSSOExpired) {
		return ReasonSSOExpired
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AccessDenied", "AccessDeniedException":
			return denied
		case "ExpiredToken", "ExpiredTokenException":
			return ReasonSSOExpired
		}
	}
	return ReasonError
}

// ssoCaller parses a GetCallerIdentity ARN of an Identity Center permission
// set session: arn:<partition>:sts::<account>:assumed-role/AWSReservedSSO_<ps>_<id>/<user>.
func ssoCaller(arn string) (partition, account, role string, ok bool) {
	f := strings.SplitN(arn, ":", 6)
	if len(f) != 6 || f[0] != "arn" || f[1] == "" || f[2] != "sts" || f[3] != "" || f[4] == "" {
		return "", "", "", false
	}
	r := strings.Split(f[5], "/")
	if len(r) != 3 || r[0] != "assumed-role" || r[2] == "" || !strings.HasPrefix(r[1], ssoRolePrefix) || len(r[1]) == len(ssoRolePrefix) {
		return "", "", "", false
	}
	return f[1], f[4], r[1], true
}

// ssoRoleARN reports whether arn is role name in account under the Identity
// Center path arn:<partition>:iam::<account>:role/aws-reserved/sso.amazonaws.com/[<region>/]<name>.
func ssoRoleARN(arn, partition, account, name string) bool {
	f := strings.SplitN(arn, ":", 6)
	if len(f) != 6 || f[0] != "arn" || f[1] != partition || f[2] != "iam" || f[3] != "" || f[4] != account {
		return false
	}
	path, ok := strings.CutPrefix(f[5], "role/aws-reserved/sso.amazonaws.com/")
	return ok && (path == name || strings.HasSuffix(path, "/"+name))
}

// policyDigest is the lowercase hex sha256 of docs, sorted so that order does
// not matter, each document prefixed by its decimal length so that no two
// different lists share an encoding.
func policyDigest(docs []string) string {
	sorted := slices.Clone(docs)
	slices.Sort(sorted)
	h := sha256.New()
	for _, d := range sorted {
		fmt.Fprintf(h, "%d:", len(d))
		h.Write([]byte(d))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// coveringAllow returns the first residual Allow pattern that matches action,
// or "".
func coveringAllow(residual []string, action string) string {
	for _, p := range residual {
		if MatchAction(p, action) {
			return p
		}
	}
	return ""
}

func cmpFold(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
