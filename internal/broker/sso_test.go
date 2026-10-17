package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type keyedMinter struct {
	*fakeMinter
	keys  map[string]string
	reads int
}

func (k *keyedMinter) SourceKey(_ context.Context, profile string) string {
	k.reads++
	if s, ok := k.keys[profile]; ok {
		return s
	}
	return profile
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
	km := &keyedMinter{fakeMinter: fm, keys: map[string]string{"sandbox-admin": "culi-sso", "sandbox-ro": "culi-sso", "company": "corp-sso"}}
	b.minter = km
	return b, km, rec
}

func TestSSOExpiryFlagsOnlyTheFailingSessionAndClearsPerSourceKey(t *testing.T) {
	b, km, _ := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", Detail: "token expired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	if b.SSOExpired("a-1") != "culi-sso" || b.SSOExpired("b-1") != "" {
		t.Fatalf("a-1=%q b-1=%q", b.SSOExpired("a-1"), b.SSOExpired("b-1"))
	}
	call(b, tokA("b-1"))
	call(b, tokA("c-1"))
	km.err = nil
	call(b, tokA("b-1")) // a success from culi-sso clears every session on culi-sso
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
	b.SSOLoginDone("culi-sso", "a-1", false, "exit status 1")
	if b.SSOExpired("a-1") == "" || rec.appr[len(rec.appr)-1].Action != ActionSSOLoginFailed {
		t.Fatal("a failed login clears nothing and is audited")
	}
	b.SSOLoginDone("culi-sso", "a-1", true, "")
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
	b.SSOLoginDone("culi-sso", "a-1", false, long)
	r := rec.appr[len(rec.appr)-1]
	if r.SessionID != "a-1" || r.AccountID != "123456789012" || r.RoleARN == "" || r.Profile != "c" {
		t.Fatalf("row does not identify the session: %+v", r)
	}
	if len(r.Error) > maxErrLen+4 || r.Error == "" {
		t.Fatalf("detail not sanitized and bounded: %d bytes", len(r.Error))
	}
	b.SSOLoginDone("culi-sso", "gone-9", true, "") // an unknown session is audited bare, never a panic
	if g := rec.appr[len(rec.appr)-1]; g.SessionID != "gone-9" || g.Action != ActionSSOLoginOK {
		t.Fatalf("unknown session row: %+v", g)
	}
}

func TestSSOLoginDoneForAnotherKeyLeavesTheNeed(t *testing.T) {
	b, km, _ := ssoSetup(t)
	km.err = &MintError{Code: "SSOExpired", cause: ErrSSOExpired}
	call(b, tokA("a-1"))
	b.SSOLoginDone("corp-sso", "c-1", true, "")
	if b.SSOExpired("a-1") != "culi-sso" {
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
	os.WriteFile(cfg, []byte("[profile p1]\nsso_session = culi-sso\nsso_account_id = 123456789012\nsso_role_name = X\nregion = ap-southeast-1\n\n"+
		"[sso-session culi-sso]\nsso_region = ap-southeast-1\nsso_start_url = https://d-0000000000.awsapps.com/start\n\n"+
		"[profile legacy]\nregion = ap-southeast-1\n"), 0o600)
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	m := STSMinter{}
	if got := m.SourceKey(context.Background(), "p1"); got != "culi-sso" {
		t.Fatalf("p1 → %q", got)
	}
	if got := m.SourceKey(context.Background(), "legacy"); got != "legacy" {
		t.Fatalf("legacy → %q", got)
	}
	if got := m.SourceKey(context.Background(), "missing"); got != "missing" {
		t.Fatalf("missing → %q", got)
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
