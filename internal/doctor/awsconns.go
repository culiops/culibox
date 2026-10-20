package doctor

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
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
	var out []Result
	for _, conn := range c.AWSConnections {
		src := conn.SourceProfile
		status, state, srcState, skip := Pass, "n/a", "ok", ""
		switch {
		case !cliOK:
			out = append(out, Result{Warn, "aws-connection " + conn.Name,
				"cannot check source_profile " + src + ": aws CLI did not run (" + firstLine(listErr.Error()) + ")"})
			out = append(out, baseLine("aws-connection "+conn.Name+" base role", conn.AWSBinding, ""))
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
			`); read-only must come from the role — see README "Several AWS accounts in one session"`}
	case awsprobe.NoneFound:
		return Result{Pass, check, "base role " + role + ": no broad write grant found (scoped grants are not detected)"}
	}
	d := "could not check base role " + role + ": " + awsprobe.ReasonText(res.Reason)
	if res.Reason == awsprobe.ReasonSimulateDenied {
		d += " (grant the role iam:SimulatePrincipalPolicy on itself)"
	}
	return Result{Warn, check, d}
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
