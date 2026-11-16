package web

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
)

// The AWS table says what the SSO token cache says, not that the config parses
// (#141): "ok" is the config's status and stays "ok" for a profile nobody ever
// signed in to, so the wire carries the sign-in state next to it.
func TestViewAWSSignInState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, "aws-config")
	conf := "[profile in-p]\nsso_session = corp\nsso_account_id = 111111111111\nsso_role_name = R\n" +
		"[profile out-p]\nsso_session = fresh\nsso_account_id = 111111111111\nsso_role_name = R\n" +
		"[profile old-p]\nsso_session = stale\nsso_account_id = 111111111111\nsso_role_name = R\n" +
		"[profile bad-p]\nsso_session = broken\nsso_account_id = 111111111111\nsso_role_name = R\n" +
		"[profile keys-p]\naws_access_key_id = AKIAFAKE\naws_secret_access_key = fakefake\n" +
		"[profile ar-p]\nrole_arn = arn:aws:iam::333333333333:role/X\nsource_profile = in-p\n" +
		"[profile half-p]\nsso_region = us-east-1\nsso_account_id = 111111111111\nsso_role_name = R\n" +
		"[sso-session corp]\nsso_start_url = https://corp.awsapps.com/start\nsso_region = us-east-1\n" +
		"[sso-session fresh]\nsso_start_url = https://fresh.awsapps.com/start\nsso_region = us-east-1\n" +
		"[sso-session stale]\nsso_start_url = https://stale.awsapps.com/start\nsso_region = us-east-1\n" +
		"[sso-session broken]\nsso_start_url = https://broken.awsapps.com/start\nsso_region = us-east-1\n"
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "none"))
	cache := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(session, body string) {
		sum := sha1.Sum([]byte(session))
		if err := os.WriteFile(filepath.Join(cache, hex.EncodeToString(sum[:])+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	until := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	put("corp", `{"accessToken":"TOPSECRET-ACCESS","refreshToken":"TOPSECRET-REFRESH","clientSecret":"TOPSECRET-CLIENT","expiresAt":"`+until.Format(time.RFC3339)+`"}`)
	put("stale", `{"accessToken":"TOPSECRET-ACCESS","expiresAt":"2020-01-01T00:00:00Z"}`)
	put("broken", `{not json TOPSECRET`)

	mk := func(name, profile, mode string) config.AWSConnection {
		c := awsConn(name, profile, arnRead)
		if mode != "" {
			c.RoleARN, c.Mode = "", mode
		}
		return c
	}
	v := newViewFix(t, config.Config{AWSConnections: []config.AWSConnection{
		mk("signed-in", "in-p", ""), mk("never", "out-p", ""), mk("expired", "old-p", ""), mk("unreadable", "bad-p", ""),
		mk("static", "keys-p", ""), mk("ghost", "no-such-p", ""), mk("direct-in", "in-p", config.AWSModeSSODirect),
		mk("direct-never", "out-p", config.AWSModeSSODirect), mk("direct-assume", "ar-p", config.AWSModeSSODirect),
		mk("direct-keys", "keys-p", config.AWSModeSSODirect), mk("assume-via-sso", "ar-p", ""), mk("half", "half-p", ""),
	}}, nil)
	b, raw := v.get(t)
	got := map[string]string{}
	for _, a := range b.AWS {
		if a.Status != "ok" {
			t.Errorf("%s: config status %q", a.Name, a.Status)
		}
		if a.SignIn == nil {
			t.Fatalf("%s has no signin: %s", a.Name, raw)
		}
		got[a.Name] = a.SignIn.State
		if a.Name == "signed-in" && (a.SignIn.Until == nil || !a.SignIn.Until.Equal(until)) {
			t.Errorf("signed-in until = %v, want %v", a.SignIn.Until, until)
		}
		if a.Name == "never" && a.SignIn.Until != nil {
			t.Errorf("never signed in carries an expiry: %v", a.SignIn.Until)
		}
	}
	want := map[string]string{"signed-in": "signed_in", "never": "never", "expired": "expired", "unreadable": "unknown",
		"static": "not_sso", "ghost": "no_profile", "direct-in": "signed_in", "direct-never": "never",
		"direct-assume": "not_sso", "direct-keys": "not_sso", "assume-via-sso": "signed_in", "half": "incomplete"}
	for name, st := range want {
		if got[name] != st {
			t.Errorf("%s: signin = %q, want %q", name, got[name], st)
		}
	}
	if strings.Contains(raw, "TOPSECRET") {
		t.Errorf("the view leaks a token cache value: %s", raw)
	}
}

// An entry that did not merge has no sign-in to report: its status is invalid.
func TestViewInvalidAWSEntryHasNoSignIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	dir := t.TempDir()
	raw := `{"aws_connections": [{"name": "bad-aws", "source_profile": "h", "role_arn": "not-an-arn", "region": "us-east-1"}]}`
	if err := os.WriteFile(credfile.Path(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _, err := credfile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	fx := newCredFixture(t, config.Config{}, nil)
	cm := NewCredentialManager(config.Config{}, f, dir, fx.pm, fx.vals, fx.rec, fx.chk, func(config.Config) {}, nil)
	body := (&Server{Creds: cm}).credView()
	if len(body.AWS) != 1 || body.AWS[0].Status != credInvalid || body.AWS[0].SignIn != nil {
		t.Fatalf("%+v", body.AWS)
	}
}
