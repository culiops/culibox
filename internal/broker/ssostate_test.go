package broker

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ssoFixture points HOME and AWS_CONFIG_FILE at a temp dir, so nothing reads the
// real ~/.aws. config is the text of the config file.
func ssoFixture(t *testing.T, config string) (cacheDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "none-credentials"))
	cfg := filepath.Join(home, "aws-config")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	cacheDir = filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return cacheDir
}

func cachePath(dir, key string) string {
	sum := sha1.Sum([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

// putToken writes the cache file the AWS CLI would write for key (the sso_session
// name, or the start URL of a legacy profile): <sha1 hex>.json.
func putToken(t *testing.T, dir, key, body string) {
	t.Helper()
	if err := os.WriteFile(cachePath(dir, key), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	cfgSession = "[profile dev]\nsso_session = corp\nsso_account_id = 111111111111\nsso_role_name = R\nregion = us-east-1\n" +
		"[sso-session corp]\nsso_start_url = https://corp.awsapps.com/start\nsso_region = us-east-1\n"
	cfgLegacy = "[profile old]\nsso_start_url = https://old.awsapps.com/start\nsso_region = us-east-1\nsso_account_id = 222222222222\nsso_role_name = R\n"
)

var signInNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func tokenJSON(exp time.Time, extra string) string {
	return `{"accessToken":"SECRET-ACCESS-TOKEN","expiresAt":"` + exp.UTC().Format(time.RFC3339) + `"` + extra + `}`
}

const refreshable = `,"refreshToken":"SECRET-REFRESH","clientId":"cid","clientSecret":"SECRET-CLIENT"`

func TestSignInOf(t *testing.T) {
	soon := signInNow.Add(2 * time.Hour)
	past := signInNow.Add(-time.Hour)
	cases := []struct {
		name    string
		config  string
		profile string
		key     string // cache key to write ("" = no file)
		body    string
		want    SignInState
		until   time.Time
	}{
		{"valid session token", cfgSession, "dev", "corp", tokenJSON(soon, ""), SignedIn, soon},
		{"valid session token with refresh material", cfgSession, "dev", "corp", tokenJSON(soon, refreshable), SignedIn, soon},
		{"expired, no refresh material", cfgSession, "dev", "corp", tokenJSON(past, ""), SignInExpired, past},
		{"expired but refreshable (the SDK renews it on use)", cfgSession, "dev", "corp", tokenJSON(past, refreshable), SignInRenewable, past},
		{"expired, refresh token without client secret", cfgSession, "dev", "corp", tokenJSON(past, `,"refreshToken":"r","clientId":"c"`), SignInExpired, past},
		{"never signed in", cfgSession, "dev", "", "", SignInNever, time.Time{}},
		{"cache file for another session only", cfgSession, "dev", "other", tokenJSON(soon, ""), SignInNever, time.Time{}},
		{"malformed JSON", cfgSession, "dev", "corp", "{not json", SignInUnknown, time.Time{}},
		{"no accessToken", cfgSession, "dev", "corp", `{"expiresAt":"2999-01-01T00:00:00Z"}`, SignInUnknown, time.Time{}},
		{"no expiresAt", cfgSession, "dev", "corp", `{"accessToken":"x"}`, SignInUnknown, time.Time{}},
		{"expiresAt not RFC3339 (the SDK rejects it too)", cfgSession, "dev", "corp", `{"accessToken":"x","expiresAt":"2999-01-01T00:00:00UTC"}`, SignInUnknown, time.Time{}},
		{"legacy start-url profile, valid", cfgLegacy, "old", "https://old.awsapps.com/start", tokenJSON(soon, ""), SignedIn, soon},
		{"legacy start-url profile, expired is never renewable", cfgLegacy, "old", "https://old.awsapps.com/start", tokenJSON(past, refreshable), SignInExpired, past},
		{"legacy start-url profile keys on the URL, not the profile name", cfgLegacy, "old", "old", tokenJSON(soon, ""), SignInNever, time.Time{}},
		{"profile not in the config", cfgSession, "ghost", "", "", SignInNoProfile, time.Time{}},
		{"static-key profile", "[profile keys]\naws_access_key_id = AKIAFAKE\naws_secret_access_key = fake\n", "keys", "", "", SignInNotSSO, time.Time{}},
		{"credential_process profile", "[profile cp]\ncredential_process = /bin/true\n", "cp", "", "", SignInNotSSO, time.Time{}},
		{"sso_session section missing is not guessed at", "[profile dev]\nsso_session = corp\nsso_account_id = 1\nsso_role_name = R\n", "dev", "", "", SignInUnknown, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := ssoFixture(t, tc.config)
			if tc.key != "" {
				putToken(t, dir, tc.key, tc.body)
			}
			got := SignInOf(context.Background(), tc.profile, signInNow)
			if got.State != tc.want {
				t.Fatalf("state = %q, want %q", got.State, tc.want)
			}
			if !got.Until.Equal(tc.until) {
				t.Errorf("until = %v, want %v", got.Until, tc.until)
			}
			if strings.Contains(got.String(), "SECRET") {
				t.Errorf("the result leaks a token value: %q", got.String())
			}
		})
	}
}

// A profile that assumes a role through an SSO source profile follows the source:
// the sign-in that matters is the source's.
func TestSignInOfFollowsSourceProfile(t *testing.T) {
	dir := ssoFixture(t, cfgSession+"[profile ar]\nrole_arn = arn:aws:iam::333333333333:role/X\nsource_profile = dev\n")
	soon := signInNow.Add(time.Hour)
	putToken(t, dir, "corp", tokenJSON(soon, ""))
	if got := SignInOf(context.Background(), "ar", signInNow); got.State != SignedIn || !got.Until.Equal(soon) {
		t.Fatalf("got %+v, want signed in until %v", got, soon)
	}
}

// The expiry is compared with now: a token that expires exactly now is expired.
func TestSignInOfBoundary(t *testing.T) {
	dir := ssoFixture(t, cfgSession)
	putToken(t, dir, "corp", tokenJSON(signInNow, ""))
	if got := SignInOf(context.Background(), "dev", signInNow); got.State != SignInExpired {
		t.Fatalf("state at the instant of expiry = %q, want expired", got.State)
	}
}

// A cache file larger than any real token is not read whole.
func TestSignInOfHugeFile(t *testing.T) {
	dir := ssoFixture(t, cfgSession)
	putToken(t, dir, "corp", tokenJSON(signInNow.Add(time.Hour), `,"pad":"`+strings.Repeat("x", 2<<20)+`"`))
	if got := SignInOf(context.Background(), "dev", signInNow); got.State != SignInUnknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}

// An unreadable cache file is unknown, never "never signed in" and never green.
func TestSignInOfUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	dir := ssoFixture(t, cfgSession)
	putToken(t, dir, "corp", tokenJSON(signInNow.Add(time.Hour), ""))
	if err := os.Chmod(cachePath(dir, "corp"), 0); err != nil {
		t.Fatal(err)
	}
	if got := SignInOf(context.Background(), "dev", signInNow); got.State != SignInUnknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}
