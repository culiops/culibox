package broker

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/config"
)

// ProbeTarget is what a base-role probe needs from a binding.
type ProbeTarget struct {
	SourceProfile, Region, RoleARN string
	S3Read                         []string
}

// TargetOf is the probe target of a binding.
func TargetOf(b config.AWSBinding) ProbeTarget {
	return ProbeTarget{SourceProfile: b.SourceProfile, Region: b.Region, RoleARN: b.RoleARN, S3Read: slices.Clone(b.S3Read)}
}

// ProbeRole mints the base role the way a session would (same session policy,
// the given session name) and has the role simulate its own write access. It
// is the single mint+simulate implementation shared by the daemon and doctor.
// Every failure is an Unverified result with an enum reason; it never returns
// AWS error text. The probe credential never leaves this function.
func ProbeRole(ctx context.Context, m Minter, sim awsprobe.Simulator, t ProbeTarget, sessionName string, now func() time.Time) awsprobe.Result {
	policy, err := awsscope.SessionPolicy(awsscope.PartitionOf(t.RoleARN), t.S3Read)
	if err != nil {
		return awsprobe.Fail(awsprobe.ReasonError, now())
	}
	c, err := m.Mint(ctx, MintInput{SourceProfile: t.SourceProfile, Region: t.Region, RoleARN: t.RoleARN,
		SessionName: sessionName, Policy: policy, Duration: credTTL})
	if err != nil {
		return awsprobe.Fail(mintReason(ctx, err), now())
	}
	creds := aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey,
		SessionToken: c.SessionToken, Source: "agentbox-probe"}
	dec, err := sim.Simulate(ctx, creds, t.Region, t.RoleARN, awsprobe.WriteActions)
	if err != nil {
		if ctx.Err() != nil {
			return awsprobe.Fail(awsprobe.ReasonTimeout, now())
		}
		return awsprobe.Fail(awsprobe.SimulateReason(err), now())
	}
	res := awsprobe.Classify(dec)
	res.CheckedAt = now()
	return res
}

// mintReason maps a mint failure to a Reason.
func mintReason(ctx context.Context, err error) awsprobe.Reason {
	var me *MintError
	code := ""
	if errors.As(err, &me) {
		code = me.Code
	}
	switch {
	case ctx.Err() != nil || code == "Timeout":
		return awsprobe.ReasonTimeout
	case errors.Is(err, ErrSSOExpired):
		return awsprobe.ReasonSSOExpired
	case code == "AccessDenied":
		return awsprobe.ReasonAssumeDenied
	}
	return awsprobe.ReasonError
}
