package credfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/datacompat"
)

func tok(name string, v config.KeyRef) config.StaticToken {
	return config.StaticToken{
		Name:   name,
		Value:  v,
		Env:    []string{"SOME_TOKEN"},
		Egress: []config.TokenEgress{{Host: "api.example.com"}},
	}
}

func storeRef(id string) config.KeyRef { return config.KeyRef{Type: "store", ID: id} }

func awsConn(name string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{
		SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/r", Region: "us-east-1"}}
}

func tokenNames(l []config.StaticToken) []string {
	var out []string
	for _, t := range l {
		out = append(out, t.Name)
	}
	return out
}

func TestLoadMissingIsEmpty(t *testing.T) {
	f, bad, err := Load(t.TempDir())
	if err != nil || bad {
		t.Fatalf("Load missing = bad %v err %v, want clean", bad, err)
	}
	if len(f.AWSConnections)+len(f.GitHubApps)+len(f.StaticTokens) != 0 {
		t.Fatalf("Load missing = %+v, want empty", f)
	}
}

func TestLoadBadJSON(t *testing.T) {
	for _, body := range []string{"{", "", `{"static_tokens":[]} trailing`, `{"static_tokens":{}}`, `[]`} {
		dir := t.TempDir()
		if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		f, bad, err := Load(dir)
		if !bad || err == nil {
			t.Errorf("Load(%q) = bad %v err %v, want bad with an error", body, bad, err)
		}
		if len(f.StaticTokens) != 0 {
			t.Errorf("Load(%q) returned entries from a bad file", body)
		}
	}
}

func TestSaveAtomicMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data") // does not exist yet
	want := File{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	got, bad, err := Load(dir)
	if err != nil || bad || len(got.StaticTokens) != 1 || got.StaticTokens[0].Name != "a" {
		t.Fatalf("round trip = %+v bad %v err %v", got, bad, err)
	}
	if _, err := os.Stat(Path(dir) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind: %v", err)
	}
}

func TestLoadUnknownTopLevelKeyKeepsKnownEntries(t *testing.T) {
	dir := t.TempDir()
	body := `{"static_tokens":[{"name":"good","value":{"type":"store","id":"good"},"env":["B_TOKEN"],"egress":[{"host":"api.example.com"}]}],"gcp_connections":[{}]}`
	if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if bad || err != nil {
		t.Fatalf("Load = bad %v err %v, want the file to stay usable", bad, err)
	}
	if len(f.StaticTokens) != 1 {
		t.Errorf("tokens = %v, want the known entry kept", tokenNames(f.StaticTokens))
	}
	if len(f.NotUnderstood) != 1 || !strings.Contains(f.NotUnderstood[0], "gcp_connections") {
		t.Errorf("NotUnderstood = %q, want one line naming gcp_connections", f.NotUnderstood)
	}
	if err := f.CanSave(); !errors.Is(err, ErrNotUnderstood) {
		t.Errorf("CanSave = %v, want ErrNotUnderstood", err)
	}
}

func TestLoadWrongTypedKindIsBad(t *testing.T) {
	dir := t.TempDir()
	body := `{"static_tokens":{},"aws_connections":[` + awsEntryJSON + `]}`
	if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if !bad || err == nil || !strings.Contains(err.Error(), "static_tokens") {
		t.Fatalf("Load = bad %v err %v, want bad with an error naming static_tokens", bad, err)
	}
	if len(f.StaticTokens) != 0 {
		t.Errorf("tokens = %v, want none", tokenNames(f.StaticTokens))
	}
	if len(f.AWSConnections) != 1 {
		t.Errorf("aws = %v, want the other known kind to load", f.AWSConnections)
	}
	if err := f.CanSave(); !errors.Is(err, ErrNotUnderstood) {
		t.Errorf("CanSave = %v, want ErrNotUnderstood", err)
	}
}

const awsEntryJSON = `{"name":"a1","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/r","region":"us-east-1"}`

func TestLoadWholeFileFailureCannotBeSaved(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if !bad || err == nil {
		t.Fatalf("Load = bad %v err %v", bad, err)
	}
	if len(f.NotUnderstood) != 1 {
		t.Errorf("NotUnderstood = %q, want the one whole-file reason", f.NotUnderstood)
	}
	if !errors.Is(f.CanSave(), ErrNotUnderstood) {
		t.Errorf("CanSave = %v", f.CanSave())
	}
}

func TestSaveRefusesNotUnderstoodFile(t *testing.T) {
	typo := `{"static_tokens":[{"name":"typo","value":{"type":"store","id":"typo"},"env":["A_TOKEN"],"egres":[{"host":"api.example.com"}]}]}`
	for name, body := range map[string]string{
		"not json":      "{",
		"unknown key":   `{"static_tokens":[],"gcp_connections":[{}]}`,
		"unknown field": typo,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := Save(dir, File{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}})
			if !errors.Is(err, ErrNotUnderstood) {
				t.Fatalf("Save = %v, want ErrNotUnderstood", err)
			}
			if b, _ := os.ReadFile(Path(dir)); string(b) != body {
				t.Errorf("file = %q, want it byte-identical", b)
			}
			if bads, _ := filepath.Glob(Path(dir) + ".bad-*"); len(bads) != 0 {
				t.Errorf("file renamed aside: %v", bads)
			}
		})
	}
}

func TestSaveKeepsGoodFileOutOfBadPath(t *testing.T) {
	dir := t.TempDir()
	f := File{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	if bads, _ := filepath.Glob(Path(dir) + ".bad-*"); len(bads) != 0 {
		t.Errorf("a valid file was renamed aside: %v", bads)
	}
}

func TestMergeAppendsWeb(t *testing.T) {
	cfg := config.Config{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}}
	m, src, errs := Merge(cfg, File{StaticTokens: []config.StaticToken{tok("b", storeRef("b"))}})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if got := strings.Join(tokenNames(m.StaticTokens), ","); got != "a,b" {
		t.Errorf("merged = %s, want a,b", got)
	}
	if src["token/a"] != "config" || src["token/b"] != "web" {
		t.Errorf("src = %v", src)
	}
	if len(cfg.StaticTokens) != 1 {
		t.Error("Merge changed the caller's config")
	}
}

func TestMergeAllKinds(t *testing.T) {
	cfg := config.Config{AWSConnections: []config.AWSConnection{awsConn("seed-aws")}}
	f := File{
		AWSConnections: []config.AWSConnection{awsConn("web-aws")},
		GitHubApps: []config.GitHubApp{{Name: "app", ClientID: "Iv1.abc",
			PrivateKey: config.KeyRef{Type: "store", ID: "gh-app-key"}}},
	}
	m, src, errs := Merge(cfg, f)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(m.AWSConnections) != 2 || len(m.GitHubApps) != 1 {
		t.Fatalf("merged = %+v", m)
	}
	if src["aws/seed-aws"] != "config" || src["aws/web-aws"] != "web" || src["github_app/app"] != "web" {
		t.Errorf("src = %v", src)
	}
}

func TestMergeRefusesSeedShadow(t *testing.T) {
	cfg := config.Config{AWSConnections: []config.AWSConnection{awsConn("prod")}}
	m, src, errs := Merge(cfg, File{AWSConnections: []config.AWSConnection{awsConn("prod")}})
	if len(errs) != 1 || errs[0].Kind != "aws" || errs[0].Name != "prod" {
		t.Fatalf("errs = %+v, want one aws/prod error", errs)
	}
	if len(m.AWSConnections) != 1 || src["aws/prod"] != "config" {
		t.Errorf("seed was displaced: %+v %v", m.AWSConnections, src)
	}
}

func TestMergeSkipsInvalidKeepsRest(t *testing.T) {
	bad := tok("bad", config.KeyRef{Type: "env", Var: "1-not-a-name"})
	good := tok("good", storeRef("good"))
	m, src, errs := Merge(config.Config{}, File{StaticTokens: []config.StaticToken{bad, good}})
	if got := strings.Join(tokenNames(m.StaticTokens), ","); got != "good" {
		t.Errorf("merged = %s, want good", got)
	}
	if len(errs) != 1 || errs[0].Kind != "token" || errs[0].Name != "bad" || errs[0].Err == "" {
		t.Errorf("errs = %+v", errs)
	}
	if _, ok := src["token/bad"]; ok {
		t.Error("skipped entry has a source")
	}
}

func TestMergeDuplicateWebNames(t *testing.T) {
	a := tok("dup", storeRef("one"))
	b := tok("dup", storeRef("two"))
	m, _, errs := Merge(config.Config{}, File{StaticTokens: []config.StaticToken{a, b}})
	if len(m.StaticTokens) != 1 || len(errs) != 1 {
		t.Fatalf("merged %d errs %v, want 1 and 1", len(m.StaticTokens), errs)
	}
}

func TestMergePasswordEnvClash(t *testing.T) {
	cfg := config.Config{PasswordEnv: "X"}
	clash := tok("clash", config.KeyRef{Type: "env", Var: "X"})
	m, _, errs := Merge(cfg, File{StaticTokens: []config.StaticToken{clash}})
	if len(m.StaticTokens) != 0 || len(errs) != 1 || errs[0].Name != "clash" {
		t.Fatalf("merged %v errs %v, want the clashing token skipped", m.StaticTokens, errs)
	}
}

func TestMergeExtraResolvesAcrossFileOrder(t *testing.T) {
	a := tok("a", storeRef("a"))
	a.MCP = &config.TokenMCP{Server: "srv", Command: "run", Extra: []string{"b"}}
	b := tok("b", storeRef("b"))
	b.Env = []string{"OTHER_TOKEN"}
	m, _, errs := Merge(config.Config{}, File{StaticTokens: []config.StaticToken{a, b}})
	if len(errs) != 0 || len(m.StaticTokens) != 2 {
		t.Fatalf("merged %v errs %v, want both kept regardless of file order", tokenNames(m.StaticTokens), errs)
	}
}

func TestMergedListsPassValidateMerged(t *testing.T) {
	cfg := config.Config{PasswordEnv: "X"}
	m, _, errs := Merge(cfg, File{StaticTokens: []config.StaticToken{
		tok("ok", storeRef("ok")), tok("clash", config.KeyRef{Type: "env", Var: "X"})}})
	if len(errs) != 1 {
		t.Fatalf("errs = %v", errs)
	}
	if err := ValidateMerged(m); err != nil {
		t.Errorf("ValidateMerged(merged) = %v", err)
	}
	bad := config.Config{PasswordEnv: "X", StaticTokens: []config.StaticToken{tok("clash", config.KeyRef{Type: "env", Var: "X"})}}
	if err := ValidateMerged(bad); err == nil {
		t.Error("ValidateMerged accepted a password_env clash")
	}
}

func TestTemplateFieldNotInDefHash(t *testing.T) {
	a := tok("a", storeRef("a"))
	b := a
	b.Template = "sentry"
	if a.DefHash() != b.DefHash() {
		t.Error("Template changed DefHash")
	}
	if got := b.Clone().Template; got != "sentry" {
		t.Errorf("Clone dropped Template: %q", got)
	}
}

func TestLoadUnknownEntryFieldKeepsOtherEntries(t *testing.T) {
	dir := t.TempDir()
	body := `{"static_tokens":[
	  {"name":"typo","value":{"type":"store","id":"typo"},"env":["A_TOKEN"],"egres":[{"host":"api.example.com"}]},
	  {"name":"good","value":{"type":"store","id":"good"},"env":["B_TOKEN"],"egress":[{"host":"api.example.com"}]}]}`
	if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if bad || err != nil {
		t.Fatalf("Load = bad %v err %v, want the file to stay usable", bad, err)
	}
	m, src, errs := Merge(config.Config{}, f)
	if got := strings.Join(tokenNames(m.StaticTokens), ","); got != "good" || src["token/good"] != "web" {
		t.Errorf("merged = %s %v, want only good", got, src)
	}
	if len(errs) != 1 || errs[0].Kind != "token" || errs[0].Name != "typo" || !strings.Contains(errs[0].Err, "egres") {
		t.Errorf("errs = %+v, want one error naming typo and the unknown field", errs)
	}
	// The undecodable entry keeps the file from being saved over: a save would
	// drop it, so Save refuses and the file is left as it is.
	if err := f.CanSave(); !errors.Is(err, ErrNotUnderstood) {
		t.Errorf("CanSave = %v, want ErrNotUnderstood", err)
	}
	if err := Save(dir, f); !errors.Is(err, ErrNotUnderstood) {
		t.Errorf("Save = %v, want ErrNotUnderstood", err)
	}
}

const oneToken = `"static_tokens":[{"name":"good","value":{"type":"store","id":"good"},"env":["B_TOKEN"],"egress":[{"host":"api.example.com"}]}]`

// #291: a version higher than this build knows fails closed: no entry loads, so
// none reaches the broker, and the error names the file, the version and the way out.
func TestLoadHigherVersionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":` + "99" + `,"revoked":["good"],` + oneToken + `}`
	if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if !bad || !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("Load = bad %v err %v, want bad and ErrNewer", bad, err)
	}
	for _, want := range []string{"credentials.json", "99", datacompat.Hint} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if len(f.StaticTokens)+len(f.AWSConnections)+len(f.GitHubApps) != 0 {
		t.Errorf("entries loaded from a newer-format file: %+v", f)
	}
	merged, _, _ := Merge(config.Config{}, f)
	if len(merged.StaticTokens) != 0 {
		t.Errorf("Merge activated %v", tokenNames(merged.StaticTokens))
	}
	if err := f.CanSave(); !errors.Is(err, ErrNotUnderstood) {
		t.Errorf("CanSave = %v, want ErrNotUnderstood", err)
	}
	if err := Save(dir, File{}); !errors.Is(err, ErrNotUnderstood) {
		t.Errorf("Save over a newer file = %v, want ErrNotUnderstood", err)
	}
	if b, _ := os.ReadFile(Path(dir)); string(b) != body {
		t.Errorf("the newer file was changed: %q", b)
	}
}

func TestLoadMissingOrCurrentVersionLoads(t *testing.T) {
	for _, body := range []string{`{` + oneToken + `}`, `{"version":1,` + oneToken + `}`} {
		dir := t.TempDir()
		if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		f, bad, err := Load(dir)
		if bad || err != nil || len(f.StaticTokens) != 1 || f.CanSave() != nil {
			t.Errorf("%s: Load = %+v bad %v err %v", body, f, bad, err)
		}
	}
}

func TestLoadBadVersionFailsClosed(t *testing.T) {
	for _, v := range []string{`"two"`, `0`, `-1`, `1.5`, `null`} {
		dir := t.TempDir()
		if err := os.WriteFile(Path(dir), []byte(`{"version":`+v+`,`+oneToken+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		f, bad, err := Load(dir)
		if !bad || err == nil || len(f.StaticTokens) != 0 {
			t.Errorf("version %s: Load = %+v bad %v err %v, want fail closed", v, f, bad, err)
		}
	}
}

// At the current version an unknown key stays tolerated: benign additions need no bump.
func TestLoadCurrentVersionUnknownKeyStillTolerated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte(`{"version":1,"gcp_connections":[{}],`+oneToken+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if bad || err != nil || len(f.StaticTokens) != 1 || len(f.NotUnderstood) != 1 {
		t.Fatalf("Load = %+v bad %v err %v", f, bad, err)
	}
}

// Writers: version 1 is written as no version key, so a build from before the
// version existed can still read the file after a rollback; any later version
// is written out, and a rewrite keeps it.
func TestWriteVersion(t *testing.T) {
	if got := writeVersion(1); got != 0 {
		t.Errorf("writeVersion(1) = %d, want 0 (omitted)", got)
	}
	if got := writeVersion(3); got != 3 {
		t.Errorf("writeVersion(3) = %d, want 3", got)
	}
	dir := t.TempDir()
	if err := Save(dir, File{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(Path(dir)); strings.Contains(string(b), "version") {
		t.Errorf("version 1 file carries a version key: %s", b)
	}
}

func TestRewriteKeepsFutureVersion(t *testing.T) {
	old := FormatVersion
	FormatVersion = 3
	defer func() { FormatVersion = old }()
	dir := t.TempDir()
	if err := Save(dir, File{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}}); err != nil {
		t.Fatal(err)
	}
	f, bad, err := Load(dir)
	if bad || err != nil || f.Version != 3 {
		t.Fatalf("Load = %+v bad %v err %v, want version 3", f, bad, err)
	}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	if f, _, _ = Load(dir); f.Version != 3 {
		t.Errorf("rewrite changed the version to %d", f.Version)
	}
	FormatVersion = 1 // an older build now reads the version-3 file
	if f, bad, err = Load(dir); !bad || !errors.Is(err, datacompat.ErrNewer) || len(f.StaticTokens) != 0 {
		t.Errorf("older build: Load = %+v bad %v err %v", f, bad, err)
	}
}

func ssoAckConn(name string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{
		SourceProfile: "p", Region: "us-east-1", Mode: config.AWSModeSSODirect},
		FullAccessAck: &awsprobe.Ack{State: awsprobe.GateNotReadOnly,
			Caller:  "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Example_0000000000000000",
			Refused: []string{"iam:*"}, At: time.Unix(1700000000, 0).UTC(), Actor: "web"}}
}

func TestMergeStripsSeedAck(t *testing.T) {
	cfg := config.Config{AWSConnections: []config.AWSConnection{ssoAckConn("seed")}}
	m, _, errs := Merge(cfg, File{AWSConnections: []config.AWSConnection{ssoAckConn("web")}})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	got := map[string]*awsprobe.Ack{}
	for _, c := range m.AWSConnections {
		got[c.Name] = c.FullAccessAck
	}
	if got["seed"] != nil {
		t.Error("a config.json seed kept its acknowledgement")
	}
	if got["web"] == nil {
		t.Error("a web entry lost its acknowledgement")
	}
	if cfg.AWSConnections[0].FullAccessAck == nil {
		t.Error("Merge changed the caller's config")
	}
}
