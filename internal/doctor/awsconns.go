package doctor

import (
	"context"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/config"
)

const awsCheckTimeout = 20 * time.Second

// checkAWSConns reports one line per host aws_connections entry: whether its
// source_profile exists in this user's AWS config, whether the SSO login behind
// it is valid, and whether the connection can be elevated. SSO is checked once
// per sourceKey (the profile's sso_session, else the profile name), as the
// broker keys it, so connections on one login cost one call. Like the other
// host-side checks it reads the shell's own AWS config; a profile name may
// appear in the output, a credential never does. A stale SSO login is a WARN
// (routine, a human signs in); a missing source_profile is a FAIL (every mint
// on that connection will fail).
func checkAWSConns(ctx context.Context, c config.Config, r boxvm.Runner) []Result {
	if len(c.AWSConnections) == 0 {
		return nil
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, awsCheckTimeout)
		defer cancel()
		return r.Run(ctx, nil, "aws", args...)
	}
	listing, err := run("configure", "list-profiles")
	if err != nil {
		out := make([]Result, 0, len(c.AWSConnections))
		for _, conn := range c.AWSConnections {
			out = append(out, Result{Warn, "aws-connection " + conn.Name,
				"cannot check source_profile " + conn.SourceProfile + ": aws CLI did not run (" + firstLine(err.Error()) + ")"})
		}
		return out
	}
	have := map[string]bool{}
	for _, p := range strings.Fields(listing) {
		have[p] = true
	}
	sso := map[string]string{} // sourceKey -> "valid" | "expired"
	var out []Result
	for _, conn := range c.AWSConnections {
		src := conn.SourceProfile
		status, ssoState := Pass, "n/a"
		srcState := "ok"
		if !have[src] {
			status, srcState = Fail, "missing"
		} else if key, ok := awsSourceKey(run, src); ok {
			st, seen := sso[key]
			if !seen {
				st = "valid"
				if _, err := run("sts", "get-caller-identity", "--profile", src, "--query", "Account", "--output", "text"); err != nil {
					st = "expired"
				}
				sso[key] = st
			}
			ssoState = st
			if st == "expired" {
				status = Warn
			}
		}
		elevate := "no"
		if conn.Elevate != nil {
			elevate = "yes"
		}
		out = append(out, Result{status, "aws-connection " + conn.Name,
			"source_profile " + src + " " + srcState + "; sso " + ssoState + "; elevate " + elevate})
	}
	return out
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
