package broker

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// The SDK picks a profile's credentials in a fixed order (static keys before
// SSO), so the sign-in state follows it (#141 review M2).
func TestSignInOfFollowsSDKCredentialOrder(t *testing.T) {
	soon := signInNow.Add(time.Hour)
	const sess = "[sso-session corp]\nsso_start_url = https://corp.awsapps.com/start\nsso_region = us-east-1\n"
	cases := []struct {
		name, config, profile string
		want                  SignInState
	}{
		{"static keys beat an sso_session: the keys sign in, not SSO",
			"[profile k]\naws_access_key_id = AKIAFAKE\naws_secret_access_key = fake\nsso_session = corp\nsso_account_id = 1\nsso_role_name = R\n" + sess, "k", SignInNotSSO},
		{"credential_source beats SSO", "[profile c]\ncredential_source = Environment\nrole_arn = arn:aws:iam::1:role/x\nsso_session = corp\nsso_account_id = 1\nsso_role_name = R\n" + sess, "c", SignInNotSSO},
		{"web identity beats SSO", "[profile w]\nweb_identity_token_file = /tmp/x\nrole_arn = arn:aws:iam::1:role/x\nsso_session = corp\nsso_account_id = 1\nsso_role_name = R\n" + sess, "w", SignInNotSSO},
		{"legacy account and region but no start URL: the SDK tries SSO and fails",
			"[profile l]\nsso_region = us-east-1\nsso_account_id = 1\nsso_role_name = R\n", "l", SignInIncomplete},
		{"legacy start URL but no role name", "[profile l]\nsso_start_url = https://x.awsapps.com/start\nsso_region = us-east-1\nsso_account_id = 1\n", "l", SignInIncomplete},
		{"sso-session section without a start URL", "[profile s]\nsso_session = corp\nsso_account_id = 1\nsso_role_name = R\n[sso-session corp]\nsso_region = us-east-1\n", "s", SignInIncomplete},
		{"profile start URL that disagrees with its sso-session",
			"[profile s]\nsso_session = corp\nsso_start_url = https://other.awsapps.com/start\nsso_account_id = 1\nsso_role_name = R\n" + sess, "s", SignInIncomplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := ssoFixture(t, tc.config)
			putToken(t, dir, "corp", tokenJSON(soon, "")) // a live cache must not make any of these "signed in"
			if got := SignInOf(context.Background(), tc.profile, signInNow); got.State != tc.want {
				t.Fatalf("state = %q, want %q", got.State, tc.want)
			}
		})
	}
}

// An sso_direct mint refuses a source profile that is not itself a pure SSO one
// (notSSOProfile), so it is never shown as signed in (#141 review M3).
func TestSignInOfDirectNeedsAPureSSOProfile(t *testing.T) {
	soon := signInNow.Add(time.Hour)
	dir := ssoFixture(t, cfgSession+
		"[profile ar]\nrole_arn = arn:aws:iam::333333333333:role/X\nsource_profile = dev\n"+
		"[profile keys]\naws_access_key_id = AKIAFAKE\naws_secret_access_key = fake\nsso_session = corp\nsso_account_id = 1\nsso_role_name = R\n")
	putToken(t, dir, "corp", tokenJSON(soon, ""))
	for _, p := range []string{"ar", "keys"} {
		if got := SignInOfDirect(context.Background(), p, signInNow); got.State != SignInNotSSO {
			t.Errorf("%s: direct state = %q, want not_sso", p, got.State)
		}
	}
	// The same assume-role profile is fine for an assume_role connection.
	if got := SignInOf(context.Background(), "ar", signInNow); got.State != SignedIn {
		t.Errorf("assume-role through SSO: state = %q, want signed_in", got.State)
	}
	// A pure SSO profile is judged by its cache, either way.
	if got := SignInOfDirect(context.Background(), "dev", signInNow); got.State != SignedIn || !got.Until.Equal(soon) {
		t.Errorf("pure SSO profile: %+v", got)
	}
	if got := SignInOfDirect(context.Background(), "ghost", signInNow); got.State != SignInNoProfile {
		t.Errorf("missing profile: %q", got.State)
	}
}

// An expired session token is renewable only while the client registration the
// refresh needs is still live (#141 review M4).
func TestSignInOfRenewableNeedsALiveRegistration(t *testing.T) {
	past := signInNow.Add(-time.Hour)
	for _, tc := range []struct {
		name, reg string
		want      SignInState
	}{
		{"no registrationExpiresAt", "", SignInRenewable},
		{"registration in the future", `,"registrationExpiresAt":"` + signInNow.Add(24*time.Hour).Format(time.RFC3339) + `"`, SignInRenewable},
		{"registration lapsed", `,"registrationExpiresAt":"` + signInNow.Add(-time.Minute).Format(time.RFC3339) + `"`, SignInExpired},
		{"registration lapses right now", `,"registrationExpiresAt":"` + signInNow.Format(time.RFC3339) + `"`, SignInExpired},
		{"registration not a timestamp", `,"registrationExpiresAt":"soon"`, SignInExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := ssoFixture(t, cfgSession)
			putToken(t, dir, "corp", tokenJSON(past, refreshable+tc.reg))
			if got := SignInOf(context.Background(), "dev", signInNow); got.State != tc.want {
				t.Fatalf("state = %q, want %q", got.State, tc.want)
			}
		})
	}
}

// A FIFO at the cache path must not hang the credentials page: open(2) on it
// blocks until a writer appears (#141 review M1).
func TestSignInOfFIFOCacheDoesNotHang(t *testing.T) {
	dir := ssoFixture(t, cfgSession)
	if err := syscall.Mkfifo(cachePath(dir, "corp"), 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	done := make(chan SignIn, 1)
	go func() { done <- SignInOf(context.Background(), "dev", signInNow) }()
	select {
	case got := <-done:
		if got.State != SignInUnknown {
			t.Fatalf("state = %q, want unknown", got.State)
		}
	case <-time.After(5 * time.Second):
		// Unblock the stuck reader so the goroutine does not outlive the test.
		if f, err := os.OpenFile(cachePath(dir, "corp"), os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("SignInOf blocked on a FIFO")
	}
}

// A directory or other non-regular file at the path is not a token either.
func TestSignInOfDirectoryAtCachePath(t *testing.T) {
	dir := ssoFixture(t, cfgSession)
	if err := os.Mkdir(cachePath(dir, "corp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := SignInOf(context.Background(), "dev", signInNow); got.State != SignInUnknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}
