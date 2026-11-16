package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
)

// SignInState is what the host's own SSO token cache says about a source
// profile, read locally (no network call). It is the answer to "would the next
// mint find a signed-in session?", not to "is the config valid".
type SignInState string

const (
	// SignedIn: the cached access token has not expired. Until is its expiry.
	SignedIn SignInState = "signed_in"
	// SignInRenewable: the access token has expired but the cache holds what the
	// SDK needs to renew it on the next use (a session profile with a refresh
	// token and its client). Whether the refresh token itself is still good is
	// only known by asking AWS, so this is never shown as signed in.
	SignInRenewable SignInState = "renewable"
	// SignInExpired: the cached token expired and cannot be renewed.
	SignInExpired SignInState = "expired"
	// SignInNever: the profile is an SSO profile and no token cache file exists
	// for its session (never signed in, or `aws sso logout` deleted it).
	SignInNever SignInState = "never"
	// SignInUnknown: it could not be told (unreadable or malformed cache or
	// config). Never shown as ready.
	SignInUnknown SignInState = "unknown"
	// SignInNotSSO: the profile is not an SSO one (static keys, credential_process
	// and the like): there is no sign-in to be in or out of.
	SignInNotSSO SignInState = "not_sso"
	// SignInIncomplete: the profile has SSO settings but not all of them (no
	// sso_start_url, say): the SDK tries SSO and fails, so this is neither "no
	// sign-in needed" nor a sign-in state.
	SignInIncomplete SignInState = "incomplete"
	// SignInNoProfile: the profile is not in the AWS config.
	SignInNoProfile SignInState = "no_profile"
)

// SignIn is a source profile's sign-in state. It carries a state and an expiry
// only: the token cache is read for its expiry and the presence of refresh
// material, and nothing else of it leaves this file.
type SignIn struct {
	State SignInState
	Until time.Time // the access token's expiry, when one was read
}

func (s SignIn) String() string {
	if s.Until.IsZero() {
		return string(s.State)
	}
	return string(s.State) + " " + s.Until.UTC().Format(time.RFC3339)
}

// ssoCacheMax caps the cache file read: a real token file is a few KiB.
const ssoCacheMax = 256 << 10

// maxSourceDepth bounds a source_profile chain walked for its SSO profile.
const maxSourceDepth = 5

// SignInOf reads the host's AWS config (as the minter does: AWS_CONFIG_FILE
// when set, else ~/.aws/config) and the SSO token cache (~/.aws/sso/cache, the
// file the SDK itself would read) for the profile, and reports its sign-in
// state against now. A profile that assumes a role through another is judged by
// the profile it signs in with. It never touches the network.
func SignInOf(ctx context.Context, profile string, now time.Time) SignIn {
	return signInOf(ctx, profile, now, false)
}

// SignInOfDirect is SignInOf for an sso_direct connection's source profile: the
// profile must itself be a pure IAM Identity Center one (the check every
// sso_direct mint makes, notSSOProfile), so an assume-role or static-key
// profile is SignInNotSSO however it would otherwise resolve.
func SignInOfDirect(ctx context.Context, profile string, now time.Time) SignIn {
	return signInOf(ctx, profile, now, true)
}

func signInOf(ctx context.Context, profile string, now time.Time, direct bool) SignIn {
	sc, err := loadProfile(ctx, profile)
	if err != nil {
		var missing awsconfig.SharedConfigProfileNotExistError
		if errors.As(err, &missing) {
			return SignIn{State: SignInNoProfile}
		}
		return SignIn{State: SignInUnknown}
	}
	if direct && notSSOProfile(sc) != "" {
		return SignIn{State: SignInNotSSO}
	}
	// The SDK's own order (config resolve_credentials.go, resolveCredsFromProfile):
	// a source profile, then static keys, credential_source and web identity,
	// and only then SSO. A profile with static keys and an sso_session signs in
	// with the keys, so it has no SSO sign-in to be in or out of.
	for depth := 0; sc.Source != nil && depth < maxSourceDepth; depth++ {
		sc = *sc.Source
	}
	switch {
	case sc.Source != nil:
		return SignIn{State: SignInUnknown} // a chain longer than any real one
	case sc.Credentials.HasKeys(), sc.CredentialSource != "", sc.WebIdentityTokenFile != "":
		return SignIn{State: SignInNotSSO}
	case sc.SSOSessionName == "" && sc.SSORegion == "" && sc.SSOAccountID == "" && sc.SSOStartURL == "" && sc.SSORoleName == "":
		return SignIn{State: SignInNotSSO}
	}
	// SSO settings are present: the SDK tries SSO and fails on an incomplete
	// profile, so that is not "no sign-in needed".
	var key string
	switch {
	case sc.SSOSessionName != "":
		// A session profile still needs the account and role GetRoleCredentials
		// is asked for: without them every mint fails whatever the token says.
		if sc.SSOAccountID == "" || sc.SSORoleName == "" ||
			sc.SSOSession == nil || sc.SSOSession.SSORegion == "" || sc.SSOSession.SSOStartURL == "" ||
			(sc.SSORegion != "" && sc.SSORegion != sc.SSOSession.SSORegion) ||
			(sc.SSOStartURL != "" && sc.SSOStartURL != sc.SSOSession.SSOStartURL) {
			return SignIn{State: SignInIncomplete}
		}
		key = sc.SSOSession.Name
	default:
		if sc.SSORegion == "" || sc.SSOStartURL == "" || sc.SSOAccountID == "" || sc.SSORoleName == "" {
			return SignIn{State: SignInIncomplete}
		}
		key = sc.SSOStartURL
	}
	return tokenState(key, sc.SSOSessionName != "", now)
}

// ssoToken is the part of an SSO token cache file the state needs. The access
// token is only checked for presence, as the SDK does, and dropped.
type ssoToken struct {
	AccessToken  string `json:"accessToken"`
	ExpiresAt    string `json:"expiresAt"`
	RefreshToken string `json:"refreshToken"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	// RegistrationExpiresAt is when the OIDC client registration the refresh
	// needs lapses (the AWS CLI writes it; the SDK does not look at it).
	RegistrationExpiresAt string `json:"registrationExpiresAt"`
}

// registrationLive: no registrationExpiresAt means nothing says the client is
// gone; one that is present must parse and be in the future, else the refresh
// cannot work and the token is simply expired.
func registrationLive(at string, now time.Time) bool {
	if at == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, at)
	return err == nil && now.Before(t)
}

// tokenState reads the cache file for key (the sso_session name, or a legacy
// profile's start URL) the way the SDK's own loader does, and judges it. A
// session profile's expired token is renewable when the SDK has what it needs
// to refresh it (a legacy one never is).
func tokenState(key string, session bool, now time.Time) SignIn {
	path, err := ssocreds.StandardCachedTokenFilepath(key)
	if err != nil {
		return SignIn{State: SignInUnknown}
	}
	// O_NONBLOCK, because open(2) on a FIFO blocks until a writer shows up and
	// that would hang the credentials page (the SDK's own loader uses
	// os.ReadFile and would block on such a path too; this read must not).
	// The fstat after the open is the real guard: only a regular file is read.
	// O_NONBLOCK has no effect on reads of a regular file.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SignIn{State: SignInNever}
		}
		return SignIn{State: SignInUnknown}
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return SignIn{State: SignInUnknown}
	}
	raw, err := io.ReadAll(io.LimitReader(f, ssoCacheMax+1))
	if err != nil || len(raw) > ssoCacheMax {
		return SignIn{State: SignInUnknown}
	}
	var t ssoToken
	if json.Unmarshal(raw, &t) != nil || t.AccessToken == "" {
		return SignIn{State: SignInUnknown}
	}
	// The SDK accepts RFC 3339 only: any other form is a cache it cannot use.
	exp, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil || exp.IsZero() {
		return SignIn{State: SignInUnknown}
	}
	switch {
	case now.Before(exp):
		return SignIn{State: SignedIn, Until: exp}
	case session && t.RefreshToken != "" && t.ClientID != "" && t.ClientSecret != "" && registrationLive(t.RegistrationExpiresAt, now):
		return SignIn{State: SignInRenewable, Until: exp}
	}
	return SignIn{State: SignInExpired, Until: exp}
}
