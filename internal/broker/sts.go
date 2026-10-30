package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/culiops/agent-box/internal/awshost"
	"github.com/culiops/agent-box/internal/config"
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
	if in.Mode == config.AWSModeSSODirect {
		return mintSSODirect(ctx, in)
	}
	opts := append(awshost.LoadOpts(), awsconfig.WithSharedConfigProfile(in.SourceProfile), awsconfig.WithRegion(in.Region))
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
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

// SourceKey names the SSO session a source profile signs in with: profiles on
// one sso_session expire together and one sign-in fixes them all. A legacy
// profile (no sso_session) keys on its own name; so does an unreadable one, but
// that comes back with the read error so the caller does not cache it.
func (STSMinter) SourceKey(ctx context.Context, profile string) (string, error) {
	sc, err := loadProfile(ctx, profile)
	if err != nil {
		return profile, err
	}
	if sc.SSOSessionName != "" {
		return sc.SSOSessionName, nil
	}
	return profile, nil
}

// Codes of a refused sso_direct mint.
const (
	codeNotSSOProfile     = "NotSSOProfile"     // the source profile is not a pure IAM Identity Center profile
	codeNotSSOCredentials = "NotSSOCredentials" // what it returned is not short-lived SSO role credentials
)

// mintSSODirect hands back the source profile's own SSO permission-set
// credentials (no AssumeRole, no session policy). It refuses, before any
// credential is read, a profile that is not purely an IAM Identity Center
// profile: static keys, credential_process, credential_source, web identity,
// login sessions or an assume-role chain would each resolve to something else
// (static keys first of all, ahead of the SSO settings). What comes back must
// still be SSO role credentials that expire (see ssoDirectCreds), so a profile
// edited between the check and the load is caught too. Never a long-lived key.
func mintSSODirect(ctx context.Context, in MintInput) (Creds, error) {
	sc, err := loadProfile(ctx, in.SourceProfile)
	if err != nil {
		return Creds{}, withCode(classify(err), "ConfigError")
	}
	if why := notSSOProfile(sc); why != "" {
		return Creds{}, &MintError{Code: codeNotSSOProfile, Detail: fmt.Sprintf("profile %q %s", in.SourceProfile, why)}
	}
	opts := append(awshost.LoadOpts(), awsconfig.WithSharedConfigProfile(in.SourceProfile), awsconfig.WithRegion(in.Region))
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return Creds{}, withCode(classify(err), "ConfigError")
	}
	if cfg.Credentials == nil {
		return Creds{}, &MintError{Code: codeNotSSOCredentials, Detail: "the profile resolved to no credential provider"}
	}
	return ssoDirectCreds(ctx, cfg.Credentials)
}

// notSSOProfile says why sc is not a pure IAM Identity Center profile ("" when
// it is one).
func notSSOProfile(sc awsconfig.SharedConfig) string {
	switch {
	case sc.SSOSessionName == "" && sc.SSOAccountID == "":
		return "is not an IAM Identity Center (SSO) profile (no sso_session or sso_account_id)"
	case sc.Credentials.HasKeys():
		return "has static access keys"
	case sc.CredentialProcess != "":
		return "has a credential_process"
	case sc.CredentialSource != "":
		return "has a credential_source"
	case sc.WebIdentityTokenFile != "":
		return "has a web_identity_token_file"
	case sc.LoginSession != "":
		return "has a login_session"
	case sc.RoleARN != "" || sc.SourceProfileName != "" || sc.Source != nil:
		return "assumes a role (role_arn/source_profile)"
	}
	return ""
}

// ssoDirectCreds retrieves from p and accepts only short-lived SSO role
// credentials: from the SSO provider, with a session token and an expiry.
func ssoDirectCreds(ctx context.Context, p aws.CredentialsProvider) (Creds, error) {
	c, err := p.Retrieve(ctx)
	if err != nil {
		return Creds{}, classify(err)
	}
	var why string
	switch {
	case c.Source != ssocreds.ProviderName:
		why = "the credentials did not come from IAM Identity Center (source " + c.Source + ")"
	case !c.CanExpire || c.Expires.IsZero():
		why = "the credentials do not expire"
	case c.SessionToken == "":
		why = "the credentials have no session token"
	case c.AccessKeyID == "" || c.SecretAccessKey == "":
		why = "the credentials are incomplete"
	}
	if why != "" {
		return Creds{}, &MintError{Code: codeNotSSOCredentials, Detail: why}
	}
	return Creds{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken,
		Expiration: c.Expires}, nil
}

// SSOAccountOf is the source profile's sso_account_id, the account the
// sso_direct gate requires the credentials to be for. A profile without one is
// an error.
func SSOAccountOf(ctx context.Context, profile string) (string, error) {
	sc, err := loadProfile(ctx, profile)
	if err != nil {
		return "", err
	}
	if sc.SSOAccountID == "" {
		return "", fmt.Errorf("profile %q has no sso_account_id", profile)
	}
	return sc.SSOAccountID, nil
}

// loadProfile reads one profile from the host's shared config files, as the
// SDK resolves it, without touching the network.
func loadProfile(ctx context.Context, profile string) (awsconfig.SharedConfig, error) {
	cfgs, creds := awshost.SharedFiles()
	return awsconfig.LoadSharedConfigProfile(ctx, profile, func(o *awsconfig.LoadSharedConfigOptions) {
		o.ConfigFiles, o.CredentialsFiles = cfgs, creds
	})
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

// IsSSOExpired reports whether err, from an SDK call made with a host profile,
// means that profile's SSO login has lapsed or was ended (the same test a mint
// failure goes through).
func IsSSOExpired(err error) bool { return errors.Is(classify(err), ErrSSOExpired) }

// withCode replaces a generic code with a more specific one, leaving SSO expiry
// and real AWS codes alone.
func withCode(err error, code string) error {
	var me *MintError
	if errors.As(err, &me) && me.Code == "MintFailed" {
		me.Code = code
	}
	return err
}
