package broker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

const mintTimeout = 20 * time.Second

// STSMinter mints with the host user's own AWS config: the binding's
// source_profile (normally an SSO profile) signs an AssumeRole into the agent
// role with the session policy attached. It reads the host's shared config and
// SSO cache and writes nothing of its own to ~/.aws (see the note on token
// refresh in the task report: the SDK itself rewrites a refreshed SSO token).
type STSMinter struct{}

func (STSMinter) Mint(ctx context.Context, in MintInput) (Creds, error) {
	ctx, cancel := context.WithTimeout(ctx, mintTimeout)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithSharedConfigProfile(in.SourceProfile), awsconfig.WithRegion(in.Region))
	if err != nil {
		return Creds{}, withCode(classify(err), "ConfigError")
	}
	out, err := sts.NewFromConfig(cfg).AssumeRole(ctx, assumeRoleInput(in))
	if err != nil {
		return Creds{}, classify(err)
	}
	c := out.Credentials
	if c == nil {
		return Creds{}, &MintError{Code: "MintFailed", Detail: "AssumeRole returned no credentials"}
	}
	return Creds{AccessKeyID: aws.ToString(c.AccessKeyId), SecretAccessKey: aws.ToString(c.SecretAccessKey),
		SessionToken: aws.ToString(c.SessionToken), Expiration: aws.ToTime(c.Expiration)}, nil
}

// assumeRoleInput carries the session name as both RoleSessionName and
// SourceIdentity, so CloudTrail names the agent session on every call it makes.
func assumeRoleInput(in MintInput) *sts.AssumeRoleInput {
	return &sts.AssumeRoleInput{
		RoleArn:         aws.String(in.RoleARN),
		RoleSessionName: aws.String(in.SessionName),
		SourceIdentity:  aws.String(in.SessionName),
		Policy:          aws.String(in.Policy),
		DurationSeconds: aws.Int32(int32(in.Duration / time.Second)),
	}
}

// classify turns an SDK failure into a *MintError: ErrSSOExpired when the host's
// SSO login has lapsed or was ended (`aws sso logout` deletes the cached token
// file, which the SDK reports as "failed to read cached SSO token file"), else
// the AWS error code when there is one.
func classify(err error) error {
	var inv *ssocreds.InvalidTokenError
	s := strings.ToLower(err.Error())
	if errors.As(err, &inv) || (strings.Contains(s, "sso") && (strings.Contains(s, "expired") ||
		strings.Contains(s, "invalidgrant") || strings.Contains(s, "refresh cached sso token") ||
		strings.Contains(s, "read cached sso token file"))) {
		return &MintError{Code: "SSOExpired", Detail: err.Error(), cause: ErrSSOExpired}
	}
	code := "MintFailed"
	var api smithy.APIError
	switch {
	case errors.As(err, &api) && api.ErrorCode() != "":
		code = api.ErrorCode()
	case errors.Is(err, context.DeadlineExceeded):
		code = "Timeout"
	}
	return &MintError{Code: code, Detail: err.Error(), cause: err}
}

// withCode replaces a generic code with a more specific one, leaving SSO expiry
// and real AWS codes alone.
func withCode(err error, code string) error {
	var me *MintError
	if errors.As(err, &me) && me.Code == "MintFailed" {
		me.Code = code
	}
	return err
}
