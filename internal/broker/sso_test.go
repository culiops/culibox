package broker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type keyedMinter struct {
	*fakeMinter
	keys   map[string]string
	reads  int
	keyErr error
}

func (k *keyedMinter) SourceKey(_ context.Context, profile string) (string, error) {
	k.reads++
	if k.keyErr != nil {
		return profile, k.keyErr
	}
	if s, ok := k.keys[profile]; ok {
		return s, nil
	}
	return profile, nil
}

func ssoSetup(t *testing.T) (*Broker, *keyedMinter, *memRec) {
	t.Helper()
	b, fm, rec, _ := setup(t, nil)
	two := live()
	two.AWS = binding()
	two.AWS.SourceProfile = "sandbox-ro" // another profile on the same sso_session
	other := live()
	other.AWS = binding()
	other.AWS.SourceProfile = "company"
	sessions := map[string]SessionView{"a-1": live(), "b-1": two, "c-1": other}
	b.lookup = func(id string) (SessionView, bool) { s, ok := sessions[id]; return s, ok }
	km := &keyedMinter{fakeMinter: fm, keys: map[string]string{"sandbox-admin": "example-sso", "sandbox-ro": "example-sso", "company": "corp-sso"}}
	b.minter = km
	return b, km, rec
}

func TestSSOExpiryFlagsOnlyTheFailingSessionAndClearsPerSourceKey(t *testing.T) {
	b, km, _ := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", Detail: "token expired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	if b.SSOExpired("a-1") != "example-sso" || b.SSOExpired("b-1") != "" {
		t.Fatalf("a-1=%q b-1=%q", b.SSOExpired("a-1"), b.SSOExpired("b-1"))
	}
	call(b, tokA("b-1"))
	call(b, tokA("c-1"))
	km.err = nil
	call(b, tokA("b-1")) // a success from example-sso clears every session on example-sso
	if b.SSOExpired("a-1") != "" || b.SSOExpired("b-1") != "" || b.SSOExpired("c-1") != "corp-sso" {
		t.Fatalf("a=%q b=%q c=%q", b.SSOExpired("a-1"), b.SSOExpired("b-1"), b.SSOExpired("c-1"))
	}
	if km.reads != 3 {
		t.Fatalf("each profile's key is read once, got %d reads", km.reads)
	}
}

func TestSSOLoginDoneClearsAndAudits(t *testing.T) {
	b, km, rec := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	b.SSOLoginDone("example-sso", "a-1", "", false, "exit status 1")
	if b.SSOExpired("a-1") == "" || rec.appr[len(rec.appr)-1].Action != ActionSSOLoginFailed {
		t.Fatal("a failed login clears nothing and is audited")
	}
	b.SSOLoginDone("example-sso", "a-1", "", true, "")
	if b.SSOExpired("a-1") != "" || rec.appr[len(rec.appr)-1].Action != ActionSSOLoginOK {
		t.Fatal("a completed login clears the flag and is audited")
	}
}

func TestSSOLoginDoneAuditsTheSessionAndABoundedDetail(t *testing.T) {
	b, km, rec := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	long := "line one\nline two x"
	for len(long) < 2*maxErrLen {
		long += "yy"
	}
	b.SSOLoginDone("example-sso", "a-1", "", false, long)
	r := rec.appr[len(rec.appr)-1]
	if r.SessionID != "a-1" || r.AccountID != "123456789012" || r.RoleARN == "" || r.Profile != "c" {
		t.Fatalf("row does not identify the session: %+v", r)
	}
	if len(r.Error) > maxErrLen+4 || r.Error == "" {
		t.Fatalf("detail not sanitized and bounded: %d bytes", len(r.Error))
	}
	b.SSOLoginDone("example-sso", "gone-9", "", true, "") // an unknown session is audited bare, never a panic
	if g := rec.appr[len(rec.appr)-1]; g.SessionID != "gone-9" || g.Action != ActionSSOLoginOK {
		t.Fatalf("unknown session row: %+v", g)
	}
}

func TestSSOLoginDoneForAnotherKeyLeavesTheNeed(t *testing.T) {
	b, km, _ := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	b.SSOLoginDone("corp-sso", "c-1", "", true, "")
	if b.SSOExpired("a-1") != "example-sso" {
		t.Fatal("a sign-in on another sso_session cleared this one")
	}
}

func TestForgetClearsTheSSONeed(t *testing.T) {
	b, km, _ := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	b.Forget("a-1")
	if b.SSOExpired("a-1") != "" {
		t.Fatal("Forget left the need")
	}
}

func TestSourceKeyFallsBackToTheProfileWithoutAKeyer(t *testing.T) {
	b, _, _, _ := setup(t, nil)
	if b.SourceKeyOf("sandbox-admin") != "sandbox-admin" {
		t.Fatal("a minter without SourceKey keys on the profile")
	}
}

func TestSTSMinterSourceKeyReadsTheSSOSession(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config")
	os.WriteFile(cfg, []byte("[profile p1]\nsso_session = example-sso\nsso_account_id = 123456789012\nsso_role_name = X\nregion = ap-southeast-1\n\n"+
		"[sso-session example-sso]\nsso_region = ap-southeast-1\nsso_start_url = https://d-0000000000.awsapps.com/start\n\n"+
		"[profile legacy]\nregion = ap-southeast-1\n"), 0o600)
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	m := STSMinter{}
	if got, err := m.SourceKey(context.Background(), "p1"); got != "example-sso" || err != nil {
		t.Fatalf("p1 → %q, %v", got, err)
	}
	if got, err := m.SourceKey(context.Background(), "legacy"); got != "legacy" || err != nil {
		t.Fatalf("legacy → %q, %v", got, err)
	}
	if got, err := m.SourceKey(context.Background(), "missing"); got != "missing" || err == nil {
		t.Fatalf("missing → %q, %v (an unreadable profile keys on its name but reports the error)", got, err)
	}
}

func TestSharedFilesFollowTheEnvironmentLikeMint(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", "/tmp/x/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/tmp/x/creds")
	cfgs, creds := sharedFiles()
	if len(cfgs) != 1 || cfgs[0] != "/tmp/x/config" || len(creds) != 1 || creds[0] != "/tmp/x/creds" {
		t.Fatalf("cfgs=%v creds=%v", cfgs, creds)
	}
	t.Setenv("AWS_CONFIG_FILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	if cfgs, creds := sharedFiles(); cfgs != nil || creds != nil {
		t.Fatalf("unset env must defer to the SDK defaults, got %v %v", cfgs, creds)
	}
}

// A SourceKey read that failed is not cached: the profile name serves that call
// only, and the next call reads again and keeps the real key.
func TestSourceKeyOfDoesNotCacheAFailedRead(t *testing.T) {
	b, km, _ := ssoSetup(t)
	km.keyErr = errors.New("config unreadable")
	if got := b.SourceKeyOf("sandbox-admin"); got != "sandbox-admin" {
		t.Fatalf("a failed read keys on the profile for this call: %q", got)
	}
	km.keyErr = nil
	if got := b.SourceKeyOf("sandbox-admin"); got != "example-sso" {
		t.Fatalf("the failure was cached: %q", got)
	}
	reads := km.reads
	if got := b.SourceKeyOf("sandbox-admin"); got != "example-sso" || km.reads != reads {
		t.Fatalf("a good read must be cached: %q after %d extra reads", got, km.reads-reads)
	}
}

// ssoOnly fails the mint with an expired SSO login for one source profile only.
type ssoOnly struct {
	*keyedMinter
	bad string
}

func (s ssoOnly) Mint(ctx context.Context, in MintInput) (Creds, error) {
	if in.SourceProfile == s.bad {
		return Creds{}, &MintError{Code: "SSOExpired", Detail: "token expired", cause: ErrSSOExpired}
	}
	return s.keyedMinter.Mint(ctx, in)
}

// Review Focus 4: two connections on different source profiles, one of whose
// sign-in has lapsed. Only that connection fails; the other keeps minting.
func TestSSOExpiryIsPerConnection(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"c-1": liveConns()})
	km := &keyedMinter{fakeMinter: fm, keys: map[string]string{"prof-a": "sso-a", "prof-b": "sso-b"}}
	b.minter = ssoOnly{keyedMinter: km, bad: "prof-a"}
	if w := callBody(b, tokA("c-1"), `{"name":"a"}`); w.Code != 503 {
		t.Fatalf("a: %d %s", w.Code, w.Body)
	}
	if w := callBody(b, tokA("c-1"), `{"name":"b"}`); w.Code != 200 {
		t.Fatalf("b must still mint: %d %s", w.Code, w.Body)
	}
	got := b.SSOExpiredConns("c-1")
	if len(got) != 1 || got["a"] != "sso-a" {
		t.Fatalf("SSOExpiredConns = %v", got)
	}
	h := Hooks{B: b}
	if _, _, sso := h.AWSState("c-1"); sso != "sso-a" {
		t.Fatalf("legacy ssoExpired = %q", sso)
	}
	if cs := h.AWSConnState("c-1"); len(cs) != 1 || cs["a"] != "sso-a" {
		t.Fatalf("AWSConnState = %v", cs)
	}
	b.SSOLoginDone("sso-a", "c-1", "a", true, "")
	if len(b.SSOExpiredConns("c-1")) != 0 || len(h.AWSConnState("c-1")) != 0 {
		t.Fatalf("a completed sign-in clears the connection: %v", b.SSOExpiredConns("c-1"))
	}
}

func TestSSOLoginDoneNamesTheConnection(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"c-1": liveConns()})
	b.SSOLoginDone("sso-b", "c-1", "b", true, "")
	r := lastAppr(rec)
	if r.Entry != "b" || r.RoleARN != connRoleB || r.AccountID != "222222222222" || r.Action != ActionSSOLoginOK {
		t.Fatalf("row %+v", r)
	}
	b.SSOLoginDone("sso-b", "c-1", "zzz", false, "x") // not bound: audited bare, no entry
	if r := lastAppr(rec); r.Entry != "" || r.RoleARN != "" {
		t.Fatalf("unbound connection row %+v", r)
	}
}
