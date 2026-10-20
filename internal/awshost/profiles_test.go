package awshost

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, s string) []Profile {
	t.Helper()
	got, err := ParseProfiles(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSharedFilesFollowTheEnvironmentLikeMint(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", "/tmp/x/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/tmp/x/creds")
	cfgs, creds := SharedFiles()
	if len(cfgs) != 1 || cfgs[0] != "/tmp/x/config" || len(creds) != 1 || creds[0] != "/tmp/x/creds" {
		t.Fatalf("cfgs=%v creds=%v", cfgs, creds)
	}
	if n := len(LoadOpts()); n != 2 {
		t.Fatalf("both files set must give 2 load options, got %d", n)
	}
	t.Setenv("AWS_CONFIG_FILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	if cfgs, creds := SharedFiles(); cfgs != nil || creds != nil {
		t.Fatalf("unset env must defer to the SDK defaults, got %v %v", cfgs, creds)
	}
	if n := len(LoadOpts()); n != 0 {
		t.Fatalf("unset env must add no load options, got %d", n)
	}
}

func TestParseSSODirect(t *testing.T) {
	got := parse(t, "[profile a]\nsso_account_id = 111111111111\nsso_role_name = ReadOnly\nregion = eu-west-1\n")
	want := []Profile{{Name: "a", Kind: "sso", SSOAccountID: "111111111111", SSORoleName: "ReadOnly", Region: "eu-west-1", Selectable: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseSSOSessionResolved(t *testing.T) {
	got := parse(t, "[sso-session corp]\nsso_region = us-east-1\nsso_start_url = https://x.awsapps.com/start\n\n"+
		"[profile b]\nsso_session = corp\nsso_account_id = 222222222222\nsso_role_name = X\n")
	want := []Profile{{Name: "b", Kind: "sso", SSOAccountID: "222222222222", SSORoleName: "X", SSOSession: "corp", Selectable: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseAssumeRole(t *testing.T) {
	got := parse(t, "[profile c]\nrole_arn = arn:aws:iam::333333333333:role/Admin\nsource_profile = a\n")
	want := []Profile{{Name: "c", Kind: "assume-role", RoleARN: "arn:aws:iam::333333333333:role/Admin", SourceProfile: "a", Selectable: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseOtherKinds(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	in := "[profile cp]\ncredential_process = /usr/bin/true\n" +
		"[profile web]\nweb_identity_token_file = /tmp/tok\nrole_arn = arn:aws:iam::1:role/x\n" +
		"[profile keys]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = " + secret + "\n"
	got := parse(t, in)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, p := range got {
		if p.Kind != "other" || !p.Selectable {
			t.Fatalf("%s: want other/selectable, got %+v", p.Name, p)
		}
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"aws_secret_access_key", "aws_access_key_id", secret, "AKIAEXAMPLE"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("marshalled profiles leak %q: %s", bad, b)
		}
	}
}

func TestParseMFANotSelectable(t *testing.T) {
	got := parse(t, "[profile m]\nrole_arn = arn:aws:iam::1:role/x\nsource_profile = a\nmfa_serial = arn:aws:iam::1:mfa/me\n")
	if len(got) != 1 || got[0].Selectable || got[0].Note != "needs an MFA code; not supported" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseDefaultAndOddInput(t *testing.T) {
	in := "# comment\r\n; another\r\n[default]\r\nregion = us-east-1 ; inline\r\ns3 =\r\n  max_concurrent_requests = 5\r\n" +
		"\r\n[profile has space]\r\nregion = eu-west-1\r\n[services foo]\r\ndynamodb =\r\n  endpoint_url = http://x\r\n"
	got := parse(t, in)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Name != "default" || got[0].Kind != "other" || !got[0].Selectable || got[0].Region != "us-east-1" {
		t.Fatalf("default: %+v", got[0])
	}
	if got[1].Name != "has space" || got[1].Selectable || got[1].Note == "" {
		t.Fatalf("spaced name must be listed unselectable with a note: %+v", got[1])
	}
}

func TestParseRepeatedSectionMergesAndOrderKept(t *testing.T) {
	got := parse(t, "[profile z]\nregion = a\n[profile y]\n[profile z]\nsso_account_id = 1\nsso_role_name = R\n")
	if len(got) != 2 || got[0].Name != "z" || got[1].Name != "y" || got[0].Kind != "sso" || got[0].Region != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestHostProfilesMissingFile(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	got, err := HostProfiles()
	if got != nil || err != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestHostProfilesReadsConfigNotCredentials(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config"
	creds := dir + "/credentials"
	writeFile(t, cfg, "[profile a]\nregion = eu-west-1\n")
	writeFile(t, creds, "[leaked]\naws_access_key_id = AKIA\n")
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", creds)
	got, err := HostProfiles()
	if err != nil || len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The SDK treats an indented "k = v" as a sub-property only after an empty-valued
// key; otherwise it is an ordinary property of the profile.
func TestParseIndentedTopLevelProperties(t *testing.T) {
	got := parse(t, "[profile x]\n    sso_account_id = 111111111111\n    sso_role_name = ReadOnly\n"+
		"[profile y]\nrole_arn = arn:aws:iam::1:role/x\n\tsource_profile = x\n\tmfa_serial = arn:aws:iam::1:mfa/me\n")
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if x := got[0]; x.Kind != "sso" || x.SSOAccountID != "111111111111" || x.SSORoleName != "ReadOnly" || !x.Selectable {
		t.Fatalf("indented sso keys lost: %+v", x)
	}
	if y := got[1]; y.Kind != "assume-role" || y.SourceProfile != "x" || y.Selectable || y.Note != "needs an MFA code; not supported" {
		t.Fatalf("indented source_profile / mfa_serial lost: %+v", y)
	}
}

func TestParseSubBlockStaysSkippedAndEndsAtNextKey(t *testing.T) {
	// the indented entries after "s3 =" belong to s3, even when named like real keys
	got := parse(t, "[profile a]\ns3 =\n  region = ignored\n  mfa_serial = ignored\nregion = eu-west-1\n  sso_role_name = R\n")
	if len(got) != 1 || got[0].Region != "eu-west-1" || !got[0].Selectable {
		t.Fatalf("sub-block entries leaked: %+v", got)
	}
	// ...but after a non-empty key an indented line is a normal key again
	if got[0].Kind != "other" || got[0].SSORoleName != "" {
		t.Fatalf("got %+v", got[0])
	}
}
