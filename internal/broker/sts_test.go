package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
)

func TestClassifySSOExpiry(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"typed invalid token": {fmt.Errorf("get credentials: %w", &ssocreds.InvalidTokenError{Err: errors.New("x")}), true},
		"refresh failed text": {errors.New("failed to refresh cached SSO token: InvalidGrantException"), true},
		"sso expired text":    {errors.New("the SSO session associated with this profile has expired or is otherwise invalid"), true},
		"logged out (no token file)": {errors.New("get credentials: failed to refresh cached credentials, failed to read cached SSO token file, " +
			"open /home/u/.aws/sso/cache/0123.json: no such file or directory"), true},
		"access denied":   {errors.New("AccessDenied: User is not authorized to perform: sts:AssumeRole"), false},
		"no such profile": {errors.New("failed to get shared config profile, sandbox-admin"), false},
	}
	for name, c := range cases {
		if got := errors.Is(classify(c.err), ErrSSOExpired); got != c.want {
			t.Errorf("%s: sso-expired=%v, want %v", name, got, c.want)
		}
	}
}

func TestClassifyKeepsTheAWSErrorCodeApartFromTheDetail(t *testing.T) {
	api := &smithy.GenericAPIError{Code: "AccessDenied",
		Message: "User: arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin/alice@example.com is not authorized"}
	err := classify(fmt.Errorf("operation error STS: AssumeRole, https response error StatusCode: 403: %w", api))
	var me *MintError
	if !errors.As(err, &me) {
		t.Fatalf("classify returned %T, want *MintError", err)
	}
	if me.Code != "AccessDenied" {
		t.Fatalf("code %q", me.Code)
	}
	if !strings.Contains(me.Detail, "alice@example.com") {
		t.Fatalf("detail should keep the full text for the host-side audit: %q", me.Detail)
	}
	if strings.Contains(me.Code, "alice") {
		t.Fatalf("code leaks the principal: %q", me.Code)
	}
}

func TestClassifyWithoutAnAPIErrorHasAGenericCode(t *testing.T) {
	var me *MintError
	if !errors.As(classify(errors.New("dial tcp: lookup sts.ap-southeast-1.amazonaws.com: no such host")), &me) || me.Code != "MintFailed" {
		t.Fatalf("got %+v", me)
	}
}

func TestClassifySSOExpiredIsAMintErrorToo(t *testing.T) {
	err := classify(&ssocreds.InvalidTokenError{Err: errors.New("expired")})
	var me *MintError
	if !errors.Is(err, ErrSSOExpired) || !errors.As(err, &me) || me.Code != "SSOExpired" {
		t.Fatalf("got %v", err)
	}
}

func TestAssumeRoleInputBindsTheSessionToTheAgent(t *testing.T) {
	in := assumeRoleInput(MintInput{RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent",
		SessionName: "agentbox-claude-3", Policy: `{"Version":"2012-10-17"}`, Duration: 15 * time.Minute})
	if *in.RoleArn != "arn:aws:iam::123456789012:role/agentbox-agent" || *in.RoleSessionName != "agentbox-claude-3" ||
		*in.SourceIdentity != "agentbox-claude-3" || *in.Policy != `{"Version":"2012-10-17"}` || *in.DurationSeconds != 900 {
		t.Fatalf("%+v", in)
	}
}

func TestMintWithAMissingProfileFailsLocallyWithAConfigError(t *testing.T) {
	// Point the SDK at empty config files: LoadDefaultConfig fails on the missing
	// profile before any network call, and nothing is written.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("HOME", dir)
	_, err := STSMinter{}.Mint(context.Background(), MintInput{SourceProfile: "no-such-profile", Region: "ap-southeast-1",
		RoleARN: "arn:aws:iam::123456789012:role/r", SessionName: "agentbox-x", Policy: "{}", Duration: 15 * time.Minute})
	var me *MintError
	if !errors.As(err, &me) || me.Code != "ConfigError" {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("Mint wrote into the config dir: %v", entries)
	}
}

// `aws sso logout` deletes the cached SSO token file. The SDK then fails before
// any network call with "failed to read cached SSO token file"; that is the
// same human action as an expired login (run aws sso login), not an STS error.
// Found by the Linux integration run (check 7), where it surfaced as a 502
// MintFailed with no login hint.
// The name avoids "sso"/"expired": t.TempDir embeds it in the cache path the
// SDK error quotes, and the text heuristic would then match on the path alone.
func TestMintAfterALogoutAsksForALogin(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	ini := "[sso-session s]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-southeast-1\n" +
		"[profile src]\nsso_session = s\nsso_account_id = 123456789012\nsso_role_name = AdministratorAccess\nregion = ap-southeast-1\n"
	if err := os.WriteFile(cfg, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("HOME", dir) // no ~/.aws/sso/cache: the state right after a logout
	_, err := STSMinter{}.Mint(context.Background(), MintInput{SourceProfile: "src", Region: "ap-southeast-1",
		RoleARN: "arn:aws:iam::123456789012:role/r", SessionName: "agentbox-x", Policy: "{}", Duration: 15 * time.Minute})
	if !errors.Is(err, ErrSSOExpired) {
		t.Fatalf("got %v, want ErrSSOExpired", err)
	}
}

// ssoDirectFiles points the SDK at a config with one pure SSO profile and
// every non-SSO shape sso_direct must refuse, and returns the directory.
func ssoDirectFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	ini := "[sso-session s]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-southeast-1\n" +
		"[profile sso-ro]\nsso_session = s\nsso_account_id = 123456789012\nsso_role_name = ReadOnly\nregion = ap-southeast-1\n" +
		"[profile legacy-sso]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-southeast-1\n" +
		"sso_account_id = 210987654321\nsso_role_name = ReadOnly\n" +
		"[profile static]\nregion = ap-southeast-1\n" +
		"[profile process]\ncredential_process = /bin/echo {}\n" +
		"[profile chain]\nrole_arn = arn:aws:iam::123456789012:role/r\nsource_profile = sso-ro\n" +
		"[profile sso-chain]\nsso_session = s\nsso_account_id = 123456789012\nsso_role_name = ReadOnly\n" +
		"role_arn = arn:aws:iam::123456789012:role/r\nsource_profile = sso-ro\n" +
		"[profile sso-static]\nsso_session = s\nsso_account_id = 123456789012\nsso_role_name = ReadOnly\n" +
		"[profile sso-process]\nsso_session = s\nsso_account_id = 123456789012\nsso_role_name = ReadOnly\ncredential_process = /bin/echo {}\n"
	creds := "[static]\naws_access_key_id = AKIAEXAMPLEEXAMPLE00\naws_secret_access_key = secret\n" +
		"[sso-static]\naws_access_key_id = AKIAEXAMPLEEXAMPLE00\naws_secret_access_key = secret\n"
	if err := os.WriteFile(cfg, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("HOME", dir) // no ~/.aws/sso/cache
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	return dir
}

// Review Focus 1: in sso_direct a static-key, credential_process or
// assume-role profile is refused before any credential is read, so the mode
// never vends a long-lived key. Each would otherwise resolve locally (static
// keys need no network at all).
func TestSTSMintSSODirectRefusesNonSSOProfile(t *testing.T) {
	ssoDirectFiles(t)
	for _, p := range []string{"static", "process", "chain", "sso-chain", "sso-static", "sso-process"} {
		c, err := STSMinter{}.Mint(context.Background(), MintInput{SourceProfile: p, Region: "ap-southeast-1", Mode: "sso_direct"})
		var me *MintError
		if !errors.As(err, &me) || me.Code != "NotSSOProfile" {
			t.Errorf("%s: got %v, want NotSSOProfile", p, err)
		}
		if c != (Creds{}) {
			t.Errorf("%s: credentials handed back: %+v", p, c)
		}
	}
}

// A pure SSO profile goes on to the SSO cache; with none (just after `aws sso
// logout`) the mint asks for a sign-in exactly as AssumeRole does.
func TestSTSMintSSODirectAfterLogoutAsksForALogin(t *testing.T) {
	ssoDirectFiles(t)
	for _, p := range []string{"sso-ro", "legacy-sso"} {
		_, err := STSMinter{}.Mint(context.Background(), MintInput{SourceProfile: p, Region: "ap-southeast-1", Mode: "sso_direct"})
		if !errors.Is(err, ErrSSOExpired) {
			t.Errorf("%s: got %v, want ErrSSOExpired", p, err)
		}
	}
}

func TestSTSMintSSODirectNeedsExpiringCreds(t *testing.T) {
	exp := time.Unix(1_800_003_600, 0)
	good := aws.Credentials{AccessKeyID: "ASIAX", SecretAccessKey: "s", SessionToken: "t", Source: ssocreds.ProviderName,
		CanExpire: true, Expires: exp}
	cases := map[string]func(*aws.Credentials){
		"cannot expire":    func(c *aws.Credentials) { c.CanExpire = false },
		"no session token": func(c *aws.Credentials) { c.SessionToken = "" },
		"no expiry":        func(c *aws.Credentials) { c.Expires = time.Time{} },
		"not from SSO":     func(c *aws.Credentials) { c.Source = "EnvConfigCredentials" },
		"no key":           func(c *aws.Credentials) { c.AccessKeyID = "" },
	}
	for name, mut := range cases {
		c := good
		mut(&c)
		got, err := ssoDirectCreds(context.Background(), aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return c, nil }))
		var me *MintError
		if !errors.As(err, &me) || me.Code != "NotSSOCredentials" || got != (Creds{}) {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
	}
	got, err := ssoDirectCreds(context.Background(), aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return good, nil }))
	if err != nil || got != (Creds{AccessKeyID: "ASIAX", SecretAccessKey: "s", SessionToken: "t", Expiration: exp}) {
		t.Fatalf("good: %+v %v", got, err)
	}
	_, err = ssoDirectCreds(context.Background(), aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, &ssocreds.InvalidTokenError{Err: errors.New("expired")}
	}))
	if !errors.Is(err, ErrSSOExpired) {
		t.Fatalf("an expired login: %v", err)
	}
}

func TestSSOAccountOf(t *testing.T) {
	ssoDirectFiles(t)
	for p, want := range map[string]string{"sso-ro": "123456789012", "legacy-sso": "210987654321"} {
		if got, err := SSOAccountOf(context.Background(), p); err != nil || got != want {
			t.Errorf("%s: %q %v", p, got, err)
		}
	}
	for _, p := range []string{"static", "chain", "no-such-profile"} {
		if got, err := SSOAccountOf(context.Background(), p); err == nil {
			t.Errorf("%s: %q, want an error", p, got)
		}
	}
}

// AssumeRole never sets the mode.
func TestAssumeRoleMintInputHasNoMode(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": live()})
	if w := call(b, tokA("claude-3")); w.Code != 200 || fm.calls[0].Mode != "" {
		t.Fatalf("status %d input %+v", w.Code, fm.calls[0])
	}
}
