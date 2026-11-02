package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
)

const awsCheckTimeout = 20 * time.Second

// awsProbeTimeout bounds one doctor base-role probe (mint plus simulation). It
// is longer than the daemon's 5s: a human is waiting and would rather see a
// result than a timeout.
const awsProbeTimeout = 15 * time.Second

// awsProber probes one base role; the result is never an error (a probe that
// cannot establish the answer is Unverified).
type awsProber func(ctx context.Context, t broker.ProbeTarget) awsprobe.Result

// newAWSProber is the real prober: it mints as the session name
// agentbox-doctor (what CloudTrail shows) and calls broker.ProbeRole directly,
// not Broker.ProbeBase, so doctor writes no audit row and keeps no cache.
func newAWSProber(m broker.Minter, sim awsprobe.Simulator) awsProber {
	return func(ctx context.Context, t broker.ProbeTarget) awsprobe.Result {
		ctx, cancel := context.WithTimeout(ctx, awsProbeTimeout)
		defer cancel()
		return broker.ProbeRole(ctx, m, sim, t, awsscope.SessionName("doctor"), time.Now)
	}
}

// awsGateTimeout bounds one sso_direct read-only check in doctor: the mint
// (20 s) and the gate (15 s) of the broker, with a margin.
const awsGateTimeout = 40 * time.Second

// ssoGate gates one sso_direct binding: conn is the connection's name, "" for
// an inline profile binding. It never fails: every failure is an Unverified or
// NotReadOnly result.
type ssoGate func(ctx context.Context, bnd config.AWSBinding, conn string) awsprobe.GateResult

// discardRec drops the broker's audit rows: doctor, like its base-role probe,
// writes no audit row.
type discardRec struct{}

func (discardRec) Record(broker.Row) error { return nil }

// newSSOGate is the real gate: the broker's own mint and read-only check
// (CheckSSODirect), with its audit rows discarded.
func newSSOGate(m broker.Minter) ssoGate {
	b := broker.New(nil, nil, m, discardRec{})
	b.SetGate(broker.IAMGate)
	return func(ctx context.Context, bnd config.AWSBinding, conn string) awsprobe.GateResult {
		ctx, cancel := context.WithTimeout(ctx, awsGateTimeout)
		defer cancel()
		var res awsprobe.GateResult
		if conn == "" {
			res, _ = b.CheckSSODirect(ctx, bnd)
		} else {
			res, _ = b.CheckSSODirectConn(ctx, config.AWSConnection{Name: conn, AWSBinding: bnd})
		}
		return res
	}
}

// checkAWSConns reports, per host aws_connections entry, whether its
// source_profile exists in this user's AWS config, whether the SSO login behind
// it is valid, and whether the connection can be elevated; then a second line
// for its base role (can it write?). Legacy inline AWS bindings on profiles
// (the config's agents, overridden by the profile store's) get one
// aws-binding line each. SSO is checked once per sourceKey (the profile's
// sso_session, else the profile name), as the broker keys it, so connections on
// one login cost one call; a base role is probed once per (source_profile,
// role_arn). Like the other host-side checks it reads the shell's own AWS
// config; a profile name may appear in the output, a credential never does. A
// stale SSO login is a WARN (routine, a human signs in); a missing
// source_profile is a FAIL for a connection (every mint on it will fail). The
// base-role probe is advice: it never produces a FAIL.
func checkAWSConns(ctx context.Context, c config.Config, stored []config.AgentConfig, storeErr error,
	r boxvm.Runner, probe awsProber) []Result {
	return checkAWSConnsGate(ctx, c, stored, storeErr, r, probe, nil)
}

// checkAWSConnsGate is checkAWSConns with the gate for sso_direct bindings. An
// sso_direct binding has no agent role to probe, so instead of the base-role
// line it gets a read-only gate line: PASS when the permission set is
// read-only, FAIL when it is not or cannot be verified (a spawn would be
// refused). A nil gate leaves such a binding unchecked (WARN).
func checkAWSConnsGate(ctx context.Context, c config.Config, stored []config.AgentConfig, storeErr error,
	r boxvm.Runner, probe awsProber, gate ssoGate) []Result {
	legacy := legacyBindings(c, stored, storeErr)
	var storeLine *Result
	if storeErr != nil {
		storeLine = &Result{Warn, "aws-binding profiles",
			"could not read the profile store (" + firstLine(storeErr.Error()) + "); its profiles were not checked"}
	}
	if len(c.AWSConnections) == 0 && len(legacy) == 0 {
		if storeLine != nil {
			return []Result{*storeLine}
		}
		return nil
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, awsCheckTimeout)
		defer cancel()
		return r.Run(ctx, nil, "aws", args...)
	}
	listing, listErr := run("configure", "list-profiles")
	cliOK := listErr == nil
	have := map[string]bool{}
	for _, p := range strings.Fields(listing) {
		have[p] = true
	}
	sso := map[string]string{} // sourceKey -> "valid" | "expired"
	// ssoState is "n/a", "valid" or "expired" for a source profile that exists.
	ssoState := func(src string) string {
		key, ok := awsSourceKey(run, src)
		if !ok {
			return "n/a"
		}
		st, seen := sso[key]
		if !seen {
			st = "valid"
			if _, err := run("sts", "get-caller-identity", "--profile", src, "--query", "Account", "--output", "text"); err != nil {
				st = "expired"
			}
			sso[key] = st
		}
		return st
	}
	probed := map[[2]string]awsprobe.Result{}
	baseLine := func(check string, b config.AWSBinding, skip string) Result {
		if skip != "" {
			return Result{Warn, check, "base role not checked: " + skip}
		}
		t := broker.TargetOf(b)
		k := [2]string{t.SourceProfile, t.RoleARN}
		res, ok := probed[k]
		if !ok {
			res = probe(ctx, t)
			probed[k] = res
		}
		return baseRoleResult(check, t.RoleARN, res)
	}
	// gateLine is the read-only gate line of an sso_direct binding, run once
	// per (binding, connection) and skipped when the sign-in cannot work.
	gateLine := func(check string, b config.AWSBinding, connName, skip string) Result {
		switch {
		case skip != "":
			return Result{Warn, check, "read-only gate not run: " + skip}
		case gate == nil:
			return Result{Warn, check, "read-only gate not run: no gate available"}
		}
		return ssoGateResult(check, gate(ctx, b, connName))
	}
	var out []Result
	for _, conn := range c.AWSConnections {
		src := conn.SourceProfile
		status, state, srcState, skip := Pass, "n/a", "ok", ""
		switch {
		case !cliOK:
			out = append(out, Result{Warn, "aws-connection " + conn.Name,
				"cannot check source_profile " + src + ": aws CLI did not run (" + firstLine(listErr.Error()) + ")"})
			if conn.SSODirect() {
				out = append(out, Result{Warn, "aws-connection " + conn.Name + " read-only gate",
					"read-only gate not run: aws CLI did not run"})
			} else {
				out = append(out, baseLine("aws-connection "+conn.Name+" base role", conn.AWSBinding, ""))
			}
			continue
		case !have[src]:
			status, srcState, skip = Fail, "missing", "source_profile "+src+" missing"
		default:
			if state = ssoState(src); state == "expired" {
				status, skip = Warn, "sso expired"
			}
		}
		elevate := "no"
		if conn.Elevate != nil {
			elevate = "yes"
		}
		out = append(out, Result{status, "aws-connection " + conn.Name,
			"source_profile " + src + " " + srcState + "; sso " + state + "; elevate " + elevate})
		if conn.SSODirect() {
			out = append(out, gateLine("aws-connection "+conn.Name+" read-only gate", conn.AWSBinding, conn.Name, skip))
			continue
		}
		out = append(out, baseLine("aws-connection "+conn.Name+" base role", conn.AWSBinding, skip))
	}
	for _, p := range legacy {
		skip := ""
		if cliOK {
			if !have[p.AWS.SourceProfile] {
				skip = "source_profile " + p.AWS.SourceProfile + " missing"
			} else if ssoState(p.AWS.SourceProfile) == "expired" {
				skip = "sso expired"
			}
		}
		if p.AWS.SSODirect() {
			out = append(out, gateLine("aws-binding "+p.Name+" read-only gate", *p.AWS, "", skip))
			continue
		}
		out = append(out, baseLine("aws-binding "+p.Name, *p.AWS, skip))
	}
	if storeLine != nil {
		out = append(out, *storeLine)
	}
	return out
}

// legacyBindings is every profile with an inline AWS binding, sorted by name:
// the config's agents, with the profile store's entries replacing same-named
// ones (the store wins at runtime). An unreadable store contributes nothing.
func legacyBindings(c config.Config, stored []config.AgentConfig, storeErr error) []config.AgentConfig {
	byName := map[string]config.AgentConfig{}
	for _, a := range c.Agents {
		byName[a.Name] = a
	}
	if storeErr == nil {
		for _, a := range stored {
			byName[a.Name] = a
		}
	}
	var out []config.AgentConfig
	for _, a := range byName {
		if a.AWS != nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// baseRoleResult renders a probe result as a doctor line. Never a FAIL, and
// only names from awsprobe.WriteActions and the role's own name are printed.
func baseRoleResult(check, roleARN string, res awsprobe.Result) Result {
	role := roleARN[strings.LastIndexByte(roleARN, '/')+1:]
	res = res.Normalize()
	switch res.State {
	case awsprobe.Writes:
		return Result{Warn, check, "base role " + role + " can write (" + strings.Join(res.Actions, ", ") +
			`); read-only must come from the role — see docs/AWS.md "Several AWS accounts in one session"`}
	case awsprobe.NoneFound:
		return Result{Pass, check, "base role " + role + ": no broad write grant found (scoped grants are not detected)"}
	}
	d := "could not check base role " + role + ": " + awsprobe.ReasonText(res.Reason)
	if res.Reason == awsprobe.ReasonSimulateDenied {
		d += " (grant the role iam:SimulatePrincipalPolicy on itself)"
	}
	return Result{Warn, check, d}
}

// maxGateItems is how many refused items a doctor line names.
const maxGateItems = 6

// ssoGateResult renders a gate result as a doctor line. Unlike the base-role
// line it can FAIL: a spawn of an sso_direct binding that is not read-only, or
// cannot be verified, is refused.
func ssoGateResult(check string, res awsprobe.GateResult) Result {
	var items []string
	for _, v := range res.Refused {
		items = append(items, firstLine(v.Pattern+": "+v.Why))
	}
	more := ""
	if len(items) > maxGateItems {
		more = fmt.Sprintf("; … and %d more", len(items)-maxGateItems)
		items = items[:maxGateItems]
	}
	list := strings.Join(items, "; ") + more
	switch {
	case res.State == awsprobe.GateReadOnly && res.RoleARN != "":
		return Result{Pass, check, "SSO permission set is read-only (sso_direct)"}
	case res.State == awsprobe.GateNotReadOnly:
		d := "SSO permission set is not read-only (sso_direct); spawns are refused"
		if list != "" {
			d += ": " + list
		}
		return Result{Fail, check, d}
	}
	reason := awsprobe.ReasonText(res.Reason)
	if res.Reason == "" {
		reason = "no verdict"
	}
	d := "could not verify the SSO permission set is read-only (" + reason + "); spawns are refused"
	if len(res.MissingPerms) > 0 {
		d += "; grant " + strings.Join(res.MissingPerms, ", ")
	}
	if list != "" {
		d += ": " + list
	}
	return Result{Fail, check, d}
}

// awsSourceKey says whether src signs in through SSO and, if so, its sourceKey:
// its sso_session, else (a legacy sso_start_url profile) its own name.
func awsSourceKey(run func(args ...string) (string, error), src string) (string, bool) {
	if s, err := run("configure", "get", "sso_session", "--profile", src); err == nil {
		if s = strings.TrimSpace(s); s != "" {
			return s, true
		}
	}
	if s, err := run("configure", "get", "sso_start_url", "--profile", src); err == nil && strings.TrimSpace(s) != "" {
		return src, true
	}
	return "", false
}
