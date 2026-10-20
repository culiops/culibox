package credfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
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
	for _, body := range []string{"{", "", `{"static_tokens":[]} trailing`, `{"typo_field":1}`} {
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

func TestSaveRenamesBadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, File{StaticTokens: []config.StaticToken{tok("a", storeRef("a"))}}); err != nil {
		t.Fatal(err)
	}
	bads, _ := filepath.Glob(Path(dir) + ".bad-*")
	if len(bads) != 1 {
		t.Fatalf("bad files = %v, want one", bads)
	}
	if b, _ := os.ReadFile(bads[0]); string(b) != "{" {
		t.Errorf("bad file content = %q, want the original", b)
	}
	if _, bad, err := Load(dir); bad || err != nil {
		t.Errorf("new file not valid: bad %v err %v", bad, err)
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
