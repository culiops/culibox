package broker

import (
	"context"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/culiops/agent-box/internal/awsprobe"
)

// IAMGate is the real sso_direct read-only gate (a GateFunc): the IAM and STS
// reads, made with the permission set's own freshly fetched credentials. The
// daemon installs it with SetGate, and doctor uses it the same way.
func IAMGate(ctx context.Context, c Creds, region, account, sourceProfile string) awsprobe.GateResult {
	creds := aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken,
		Source: "agentbox-gate", CanExpire: true, Expires: c.Expiration}
	api, err := awsprobe.NewIAMGate(ctx, creds, region, sourceProfile)
	if err != nil { // the host's AWS config did not load: nothing was proved
		log.Printf("broker: sso_direct gate: %v", err)
		return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonError, CheckedAt: time.Now()}
	}
	return awsprobe.Gate(ctx, api, account, time.Now())
}
