package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"

	"github.com/culiops/agent-box/internal/awshost"
	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/credfile"
	"github.com/culiops/agent-box/internal/ssologin"
	"github.com/culiops/agent-box/internal/web"
)

const (
	// maxListedRoles caps the roles one listing returns.
	maxListedRoles = 1000
	// maxScannedRoles caps the roles one listing reads before it sorts and
	// caps them, so an agentbox-* role past the first thousand still shows.
	maxScannedRoles = 5000
	// checkTimeout bounds one base-role check: a mint, then a simulation.
	checkTimeout = 15 * time.Second
	// rolesTimeout bounds a whole role listing, every page of it.
	rolesTimeout = 20 * time.Second
	// ssoKeyTimeout bounds reading the host config for a profile's SSO session.
	ssoKeyTimeout = 5 * time.Second
	// fallbackIAMRegion is used when the profile names none: IAM is global, the
	// client only needs a region to pick an endpoint.
	fallbackIAMRegion = "us-east-1"
)

// iamListRoles is the one IAM call the roles listing makes (*iam.Client).
type iamListRoles interface {
	ListRoles(ctx context.Context, in *iam.ListRolesInput, opts ...func(*iam.Options)) (*iam.ListRolesOutput, error)
}

// awsCredService is the credentials screen's use of the host's AWS signer. It
// exists only while the broker runs, and is built from the broker's own minter
// and simulator so a check is the same mint a spawn makes. Every method takes a
// profile name the web layer has already validated; none returns AWS error
// text (it can name the SSO principal and host paths).
type awsCredService struct {
	minter broker.Minter
	sim    awsprobe.Simulator
	sso    ssoRunner // nil = no aws CLI on the daemon's PATH
	newIAM func(aws.Config) iamListRoles
	rec    web.CredRecorder // the sign-in result row; nil = none

	// Test seams; zero = the constants above.
	checkTimeout time.Duration
	rolesCap     int
	scanCap      int
}

var _ web.AWSCredService = (*awsCredService)(nil)

// awsServiceFor builds the AWS service for a running broker. With no broker it
// returns untyped nils: a nil *awsCredService stored in an interface would not
// compare equal to nil and would panic on its first Check, and the credentials
// screen reads nil as "AWS connections need the boxed runtime".
func awsServiceFor(b *brokerService, rec web.CredRecorder) (web.BaseRoleChecker, web.AWSCredService) {
	if b == nil {
		return nil, nil
	}
	svc := &awsCredService{minter: b.Minter, sim: b.Sim, rec: rec}
	if b.SSO != nil { // a nil *Runner must stay a nil interface
		svc.sso = b.SSO
	}
	return svc, svc
}

// Check mints the base role the way a spawn does (the same session policy,
// under a throwaway "check-" session name) and has the role simulate its own
// write access: broker.ProbeRole, behind a timeout.
func (s *awsCredService) Check(ctx context.Context, t broker.ProbeTarget) awsprobe.Result {
	d := s.checkTimeout
	if d <= 0 {
		d = checkTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return awsprobe.Fail(awsprobe.ReasonError, time.Now())
	}
	return broker.ProbeRole(ctx, s.minter, s.sim, t, awsscope.SessionName("check-"+hex.EncodeToString(b[:])), time.Now)
}

// Roles lists the IAM roles visible to the host profile, read-only and capped.
// The shared config is loaded the way STSMinter loads it (awshost.LoadOpts), so
// both mean the same profile.
func (s *awsCredService) Roles(ctx context.Context, profile string) web.RolesResult {
	ctx, cancel := context.WithTimeout(ctx, rolesTimeout)
	defer cancel()
	opts := append(awshost.LoadOpts(), awsconfig.WithSharedConfigProfile(profile))
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return rolesFailure(err)
	}
	if cfg.Region == "" {
		cfg.Region = fallbackIAMRegion
	}
	var client iamListRoles
	if s.newIAM != nil {
		client = s.newIAM(cfg)
	} else {
		client = iam.NewFromConfig(cfg)
	}
	limit, scan := s.rolesCap, s.scanCap
	if limit <= 0 {
		limit = maxListedRoles
	}
	if scan <= 0 {
		scan = maxScannedRoles
	}
	var res web.RolesResult
	var marker *string
	for {
		out, err := client.ListRoles(ctx, &iam.ListRolesInput{Marker: marker, MaxItems: aws.Int32(1000)})
		if err != nil {
			if len(res.Roles) == 0 {
				return rolesFailure(err)
			}
			// A later page failed (throttled, timed out): keep what was read.
			rolesFailure(err) // logs the code
			res.Capped = true
			break
		}
		for _, r := range out.Roles {
			arn := aws.ToString(r.Arn)
			res.Roles = append(res.Roles, web.RoleRef{Name: aws.ToString(r.RoleName), ARN: arn,
				SSOTrust: ssoTrust(aws.ToString(r.AssumeRolePolicyDocument), awsscope.AccountOf(arn))})
		}
		more := out.IsTruncated && out.Marker != nil && *out.Marker != ""
		if len(res.Roles) >= scan {
			res.Roles, res.Capped = res.Roles[:scan], res.Capped || more || len(res.Roles) > scan
			break
		}
		if !more {
			break
		}
		marker = out.Marker
	}
	sortRoles(res.Roles)
	if len(res.Roles) > limit {
		res.Roles, res.Capped = res.Roles[:limit], true
	}
	return res
}

// sortRoles puts the roles a binding should pick first: agentbox-* (the agent
// role from docs/aws/agent-role; IAM names ignore case), then roles whose trust names the account or
// an SSO role, then the rest, each group by name.
func sortRoles(roles []web.RoleRef) {
	rank := func(r web.RoleRef) int {
		switch {
		case strings.HasPrefix(strings.ToLower(r.Name), "agentbox-"):
			return 0
		case r.SSOTrust:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(roles, func(a, b web.RoleRef) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

// ssoTrust reads a role's trust policy (URL-encoded, as ListRoles returns it)
// and says whether an Allow statement trusts this account's root, the account
// id, or an AWSReservedSSO_ role, in its principal or its conditions. It is a
// hint for ordering the list: anything unreadable is false.
func ssoTrust(doc, account string) bool {
	if dec, err := url.QueryUnescape(doc); err == nil {
		doc = dec
	}
	var pol struct {
		Statement json.RawMessage
	}
	if json.Unmarshal([]byte(doc), &pol) != nil || len(pol.Statement) == 0 {
		return false
	}
	type statement struct {
		Effect    string
		Principal json.RawMessage
		Condition json.RawMessage
	}
	var stmts []statement
	if pol.Statement[0] == '[' {
		if json.Unmarshal(pol.Statement, &stmts) != nil {
			return false
		}
	} else {
		var one statement
		if json.Unmarshal(pol.Statement, &one) != nil {
			return false
		}
		stmts = []statement{one}
	}
	for _, st := range stmts {
		if st.Effect != "Allow" {
			continue
		}
		// Only a positive operator names who may assume: StringNotLike and
		// friends name who may not.
		var conds map[string]json.RawMessage
		_ = json.Unmarshal(st.Condition, &conds)
		for op, v := range conds {
			if !strings.Contains(op, "Not") && strings.Contains(string(v), "AWSReservedSSO_") {
				return true
			}
		}
		var p struct{ AWS json.RawMessage }
		if json.Unmarshal(st.Principal, &p) != nil || len(p.AWS) == 0 {
			continue
		}
		var ids []string
		if json.Unmarshal(p.AWS, &ids) != nil {
			var one string
			if json.Unmarshal(p.AWS, &one) != nil {
				continue
			}
			ids = []string{one}
		}
		for _, id := range ids {
			if strings.Contains(id, "AWSReservedSSO_") || (account != "" && (id == account || id == "arn:aws:iam::"+account+":root")) {
				return true
			}
		}
	}
	return false
}

// rolesFailure maps a failed listing to one of the outcomes the screen acts
// on. Only the error code or class is logged, never the text.
func rolesFailure(err error) web.RolesResult {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "AccessDenied", "AccessDeniedException":
			return web.RolesResult{AccessDenied: true}
		}
	}
	if broker.IsSSOExpired(err) {
		return web.RolesResult{SSOExpired: true}
	}
	code := "none"
	if api != nil {
		code = api.ErrorCode()
	}
	log.Printf("credentials: list roles failed (aws code: %s)", code)
	return web.RolesResult{Failed: true}
}

// ssoKey is the sign-in flow key of a source profile: its sso_session, else
// its own name, as the broker keys it, so the screen's sign-in and a session's
// share one flow.
func (s *awsCredService) ssoKey(profile string) string {
	if kr, ok := s.minter.(broker.SourceKeyer); ok {
		ctx, cancel := context.WithTimeout(context.Background(), ssoKeyTimeout)
		defer cancel()
		if k, err := kr.SourceKey(ctx, profile); err == nil && k != "" {
			return k
		}
	}
	return profile
}

// SSOStart begins (or joins) the device sign-in for the profile. A failure, or
// no aws CLI, is a web.SSOLoginError carrying the command the human can run.
func (s *awsCredService) SSOStart(profile string) (ssologin.State, error) {
	command := hostLoginCommand(profile)
	if command == "" {
		return ssologin.State{}, &web.SSOLoginError{Err: errors.New("the profile name is not valid")}
	}
	if s.sso == nil {
		return ssologin.State{}, &web.SSOLoginError{Command: command, Err: errors.New("the aws CLI is not on the daemon's PATH")}
	}
	st, err := s.sso.Start(s.ssoKey(profile), profile, func(ok bool, detail string) {
		if s.rec == nil {
			return
		}
		word := "failed"
		if ok {
			word = "ok"
		}
		// detail is a fixed phrase or "exit status N" (ssologin.Done).
		if err := s.rec.RecordCred(broker.CredEvent{Action: broker.CredSSOResult, Kind: credfile.KindAWS, Actor: "web",
			Detail: profile + " " + word + ": " + detail}); err != nil {
			log.Printf("credentials: audit %s: %v", broker.CredSSOResult, err)
		}
	})
	if err != nil {
		return ssologin.State{}, &web.SSOLoginError{Command: command, Err: err}
	}
	return st, nil
}

// SSOStatus is the state of the sign-in flow for the profile, if there is one.
func (s *awsCredService) SSOStatus(profile string) (ssologin.State, bool) {
	if s.sso == nil || !ssologin.ValidProfile(profile) {
		return ssologin.State{}, false
	}
	return s.sso.Status(s.ssoKey(profile))
}
