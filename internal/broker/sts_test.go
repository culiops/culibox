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
		"access denied":       {errors.New("AccessDenied: User is not authorized to perform: sts:AssumeRole"), false},
		"no such profile":     {errors.New("failed to get shared config profile, sandbox-admin"), false},
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
