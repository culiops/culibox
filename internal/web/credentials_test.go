package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
	"github.com/culiops/agent-box/internal/secretstore"
)

// fakeVals is an in-memory CredValues.
type fakeVals struct {
	mu      sync.Mutex
	m       map[string][]byte
	removed []string
	setErr  error
}

func newFakeVals() *fakeVals { return &fakeVals{m: map[string][]byte{}} }

func (f *fakeVals) Set(id string, v []byte, actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.m[id] = slices.Clone(v)
	return nil
}

func (f *fakeVals) Remove(id, actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[id]; !ok {
		return secretstore.ErrNoSuchValue
	}
	delete(f.m, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeVals) List() ([]secretstore.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []secretstore.Entry
	for id := range f.m {
		out = append(out, secretstore.Entry{ID: id})
	}
	return out, nil
}

func (f *fakeVals) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[id]
	return ok
}

// fakeCredRec is a CredRecorder; a non-nil err makes every call fail.
type fakeCredRec struct {
	mu     sync.Mutex
	events []broker.CredEvent
	err    error
}

func (f *fakeCredRec) RecordCred(e broker.CredEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeCredRec) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Action)
	}
	return out
}

// fakeChecker is a BaseRoleChecker returning a fixed result.
type fakeChecker struct {
	mu    sync.Mutex
	res   awsprobe.Result
	calls []broker.ProbeTarget
}

func (f *fakeChecker) Check(_ context.Context, t broker.ProbeTarget) awsprobe.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, t)
	return f.res
}

func (f *fakeChecker) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type credFixture struct {
	cm      *CredentialManager
	dir     string
	vals    *fakeVals
	rec     *fakeCredRec
	chk     *fakeChecker
	pm      *ProfileManager
	applied []config.Config
	mu      sync.Mutex
}

func (f *credFixture) applyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

// newCredFixture builds a manager over base with the given seed profiles. The
// apply callback records the merged config and pushes it into the profile
// manager the way credLive.Apply does.
func newCredFixture(t *testing.T, base config.Config, profiles []config.AgentConfig, accts ...account.Account) *credFixture {
	t.Helper()
	f := &credFixture{dir: t.TempDir(), vals: newFakeVals(), rec: &fakeCredRec{},
		chk: &fakeChecker{res: awsprobe.Result{State: awsprobe.NoneFound}}}
	f.pm = NewProfileManager(profiles, nil, config.RuntimeBox, nil, newFakeStore(), func([]config.AgentConfig) {})
	f.pm.SetStaticTokens(base.StaticTokens)
	f.pm.SetAWSConnections(base.AWSConnections)
	f.pm.SetGitHubApps(base.GitHubApps)
	apply := func(c config.Config) {
		f.mu.Lock()
		f.applied = append(f.applied, c)
		f.mu.Unlock()
		f.pm.SetStaticTokens(c.StaticTokens)
		f.pm.SetAWSConnections(c.AWSConnections)
		f.pm.SetGitHubApps(c.GitHubApps)
	}
	f.cm = NewCredentialManager(base, credfile.File{}, f.dir, f.pm, f.vals, f.rec, f.chk, apply,
		func() []account.Account { return accts })
	return f
}

func (f *credFixture) file(t *testing.T) credfile.File {
	t.Helper()
	got, bad, err := credfile.Load(f.dir)
	if err != nil || bad {
		t.Fatalf("load: bad=%v err=%v", bad, err)
	}
	return got
}

func (f *credFixture) fileBytes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(credfile.Path(f.dir))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tok(name, env string) config.StaticToken {
	return config.StaticToken{Name: name, Env: []string{env}, Egress: []config.TokenEgress{{Host: "api.example.com"}}}
}

func awsConn(name, profile, arn string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{SourceProfile: profile, RoleARN: arn, Region: "us-east-1"}}
}

const (
	arnRead  = "arn:aws:iam::123456789012:role/ReadOnly"
	arnWrite = "arn:aws:iam::123456789012:role/Admin"
)

func TestPutTokenCreatesValueThenEntry(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("s3cret")}, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(f.vals.m["a"]) != "s3cret" {
		t.Fatalf("store: %v", f.vals.m)
	}
	got := f.file(t)
	if len(got.StaticTokens) != 1 || got.StaticTokens[0].Value != (config.KeyRef{Type: "store", ID: "a"}) {
		t.Fatalf("file: %+v", got.StaticTokens)
	}
	if f.applyCount() != 1 {
		t.Fatalf("apply called %d times", f.applyCount())
	}
	if a := f.rec.actions(); !slices.Equal(a, []string{broker.CredCreated}) {
		t.Fatalf("audit: %v", a)
	}
	if e := f.rec.events[0]; e.Kind != credfile.KindToken || e.Name != "a" || strings.Contains(e.Detail, "s3cret") {
		t.Fatalf("audit row: %+v", e)
	}
	m, src := f.cm.Merged()
	if len(m.StaticTokens) != 1 || src["token/a"] != credfile.SourceWeb {
		t.Fatalf("merged: %+v %v", m.StaticTokens, src)
	}
	if !strings.Contains(f.fileBytes(t), `"id": "a"`) || strings.Contains(f.fileBytes(t), "s3cret") {
		t.Fatalf("file bytes: %s", f.fileBytes(t))
	}
}

func TestPutTokensRollbackOnInvalid(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	bad := tok("b", "PATH") // denied variable
	err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN"), bad},
		map[string][]byte{"a": []byte("v1"), "b": []byte("v2")}, true)
	if err == nil {
		t.Fatal("want an error")
	}
	if f.vals.has("a") || f.vals.has("b") {
		t.Fatalf("values left behind: %v", f.vals.m)
	}
	if f.fileBytes(t) != "" {
		t.Fatalf("file written: %s", f.fileBytes(t))
	}
	if f.applyCount() != 0 || len(f.rec.actions()) != 0 {
		t.Fatalf("apply=%d audit=%v", f.applyCount(), f.rec.actions())
	}
}

// A failure after the values were set (the audit row) removes the ids this
// call created, and only those.
func TestPutTokensRollbackAfterSet(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	f.vals.m["b"] = []byte("old")
	f.rec.err = errors.New("audit down")
	err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN"), tok("b", "B_TOKEN")},
		map[string][]byte{"a": []byte("v1"), "b": []byte("v2")}, true)
	if err == nil {
		t.Fatal("want an error")
	}
	if f.vals.has("a") {
		t.Fatal("created id a not removed")
	}
	if !f.vals.has("b") {
		t.Fatal("pre-existing id b removed")
	}
	if f.fileBytes(t) != "" || f.applyCount() != 0 {
		t.Fatal("write happened")
	}
}

func TestPutTokenKeepsPreexistingValueOnFailure(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	f.vals.m["a"] = []byte("old")
	err := f.cm.PutTokens([]config.StaticToken{tok("a", "PATH")}, map[string][]byte{"a": []byte("new")}, true)
	if err == nil {
		t.Fatal("want an error")
	}
	if !f.vals.has("a") || slices.Contains(f.vals.removed, "a") {
		t.Fatal("pre-existing value removed")
	}
}

func TestCreateDuplicateIs409(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v2")}, true)
	if !errors.Is(err, ErrCredExists) {
		t.Fatalf("got %v", err)
	}
	if string(f.vals.m["a"]) != "v" {
		t.Fatal("a refused create replaced the value")
	}
	// Replace of a missing entry is not found.
	err = f.cm.PutTokens([]config.StaticToken{tok("zz", "Z_TOKEN")}, nil, false)
	if !errors.Is(err, ErrCredNotFound) {
		t.Fatalf("replace missing: %v", err)
	}
}

func TestSeedNameRefused(t *testing.T) {
	seed := tok("a", "A_TOKEN")
	seed.Value = config.KeyRef{Type: "file", Path: "/etc/a"}
	base := config.Config{StaticTokens: []config.StaticToken{seed},
		AWSConnections: []config.AWSConnection{awsConn("prod", "p", arnRead)},
		GitHubApps:     []config.GitHubApp{{Name: "gh", ClientID: "Iv1", PrivateKey: config.KeyRef{Type: "file", Path: "/k.pem"}}}}
	f := newCredFixture(t, base, nil)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "B_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); !errors.Is(err, ErrCredSeed) {
		t.Fatalf("token: %v", err)
	}
	if f.vals.has("a") {
		t.Fatal("value set for a refused seed name")
	}
	if _, err := f.cm.PutAWS(context.Background(), awsConn("prod", "p", arnRead), true, false); !errors.Is(err, ErrCredSeed) {
		t.Fatalf("aws: %v", err)
	}
	if err := f.cm.PutGitHubApp(config.GitHubApp{Name: "gh", ClientID: "Iv2"}, []byte("pem"), true); !errors.Is(err, ErrCredSeed) {
		t.Fatalf("gh: %v", err)
	}
}

func TestSeedNotDeletable(t *testing.T) {
	seed := tok("a", "A_TOKEN")
	seed.Value = config.KeyRef{Type: "file", Path: "/etc/a"}
	f := newCredFixture(t, config.Config{StaticTokens: []config.StaticToken{seed}}, nil)
	if err := f.cm.Delete(credfile.KindToken, "a"); !errors.Is(err, ErrCredSeed) {
		t.Fatalf("got %v", err)
	}
	if err := f.cm.Delete(credfile.KindToken, "nope"); !errors.Is(err, ErrCredNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := f.cm.Delete("bogus", "a"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestDeleteInUse(t *testing.T) {
	profiles := []config.AgentConfig{
		{Name: "p1", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"a"}},
		{Name: "p2", Bin: "claude", Runtime: config.RuntimeBox},
	}
	f := newCredFixture(t, config.Config{}, profiles)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	err := f.cm.Delete(credfile.KindToken, "a")
	var inUse ErrCredInUse
	if !errors.As(err, &inUse) || !slices.Equal(inUse.By, []string{"p1"}) {
		t.Fatalf("got %v", err)
	}
	if len(f.file(t).StaticTokens) != 1 {
		t.Fatal("deleted anyway")
	}
}

func TestDeleteRemovesEntry(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	if err := f.cm.Delete(credfile.KindToken, "a"); err != nil {
		t.Fatal(err)
	}
	if len(f.file(t).StaticTokens) != 0 || f.applyCount() != 2 {
		t.Fatalf("file=%+v apply=%d", f.file(t), f.applyCount())
	}
	if a := f.rec.actions(); !slices.Equal(a, []string{broker.CredCreated, broker.CredDeleted}) {
		t.Fatalf("audit %v", a)
	}
}

func TestRefsToCoversEveryBinding(t *testing.T) {
	profiles := []config.AgentConfig{
		{Name: "seed-aws", AWSConnections: []string{"c"}},
		{Name: "seed-gh", GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "o", App: "app"}}}},
		{Name: "seed-pat", GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "o", Token: "t"}}}},
		{Name: "seed-tok", Tokens: []string{"t"}},
	}
	fs := newFakeStore()
	fs.m["run-tok"] = config.AgentConfig{Name: "run-tok", Tokens: []string{"t", "u"}}
	pm := NewProfileManager(profiles, nil, "", nil, fs, func([]config.AgentConfig) {})
	if got := pm.RefsTo(credfile.KindToken, "t"); !slices.Equal(got, []string{"run-tok", "seed-pat", "seed-tok"}) {
		t.Fatalf("token: %v", got)
	}
	if got := pm.RefsTo(credfile.KindAWS, "c"); !slices.Equal(got, []string{"seed-aws"}) {
		t.Fatalf("aws: %v", got)
	}
	if got := pm.RefsTo(credfile.KindGitHubApp, "app"); !slices.Equal(got, []string{"seed-gh"}) {
		t.Fatalf("gh: %v", got)
	}
	if got := pm.RefsTo(credfile.KindToken, "none"); len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
	if n := len(pm.All()); n != 5 {
		t.Fatalf("All: %d", n)
	}
}

func TestRemoveValueInUse(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	err := f.cm.RemoveValue("a")
	var inUse ErrCredInUse
	if !errors.As(err, &inUse) || !slices.Equal(inUse.By, []string{"static_tokens/a"}) {
		t.Fatalf("got %v", err)
	}
	if !f.vals.has("a") {
		t.Fatal("removed anyway")
	}
	if err := f.cm.SetValue("loose", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := f.cm.RemoveValue("loose"); err != nil || f.vals.has("loose") {
		t.Fatalf("remove unreferenced: %v", err)
	}
}

// An agent's secrets name and an account's token_secret count when a store
// secret_source exists.
func TestRemoveValueAgentAndAccountRefs(t *testing.T) {
	base := config.Config{SecretSources: []config.SecretSource{{Type: "store"}}}
	profiles := []config.AgentConfig{{Name: "p1", Bin: "claude", Secrets: []string{"GH_TOKEN"}}}
	f := newCredFixture(t, base, profiles, account.Account{Name: "work", Provider: "claude", TokenSecret: "WORK_TOKEN"})
	f.vals.m["GH_TOKEN"] = []byte("x")
	f.vals.m["WORK_TOKEN"] = []byte("y")
	var inUse ErrCredInUse
	if err := f.cm.RemoveValue("GH_TOKEN"); !errors.As(err, &inUse) || !slices.Equal(inUse.By, []string{"agents/p1.secrets"}) {
		t.Fatalf("agent: %v", err)
	}
	if err := f.cm.RemoveValue("WORK_TOKEN"); !errors.As(err, &inUse) || !slices.Equal(inUse.By, []string{"accounts/work.token_secret"}) {
		t.Fatalf("account: %v", err)
	}
}

func TestPutAWSNeedsAck(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	f.chk.res = awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject"}}
	_, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnWrite), true, false)
	var na ErrNeedsAck
	if !errors.As(err, &na) || na.Result.State != awsprobe.Writes {
		t.Fatalf("got %v", err)
	}
	if f.fileBytes(t) != "" || f.applyCount() != 0 {
		t.Fatal("saved without ack")
	}
	if v := f.cm.Verdict("p", arnWrite); v.State != awsprobe.Writes {
		t.Fatalf("verdict not kept: %+v", v)
	}
	res, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnWrite), true, true)
	if err != nil || res.State != awsprobe.Writes {
		t.Fatalf("ack save: %v %+v", err, res)
	}
	if len(f.file(t).AWSConnections) != 1 || f.applyCount() != 1 {
		t.Fatal("not saved")
	}
	if a := f.rec.actions(); !slices.Contains(a, broker.CredWriteAck) || a[len(a)-1] != broker.CredCreated {
		t.Fatalf("audit %v", a)
	}
}

func TestPutAWSEditLabelNoRecheck(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnRead), true, false); err != nil {
		t.Fatal(err)
	}
	n := f.chk.n()
	edit := awsConn("c", "p", arnRead)
	edit.Label = "Prod read"
	edit.Prod = true
	edit.Region = "eu-west-1"
	if _, err := f.cm.PutAWS(context.Background(), edit, false, false); err != nil {
		t.Fatal(err)
	}
	if f.chk.n() != n {
		t.Fatal("checker called on a label edit")
	}
	if got := f.file(t).AWSConnections[0]; got.Label != "Prod read" {
		t.Fatalf("not replaced: %+v", got)
	}
	if a := f.rec.actions(); a[len(a)-1] != broker.CredUpdated {
		t.Fatalf("audit %v", a)
	}
}

func TestPutAWSEditRoleRechecks(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnRead), true, false); err != nil {
		t.Fatal(err)
	}
	n := f.chk.n()
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnWrite), false, true); err != nil {
		t.Fatal(err)
	}
	if f.chk.n() != n+1 {
		t.Fatal("role change did not re-check")
	}
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "q", arnWrite), false, true); err != nil {
		t.Fatal(err)
	}
	if f.chk.n() != n+2 {
		t.Fatal("source_profile change did not re-check")
	}
}

func TestPutAWSNoBroker(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	f.cm.chk = nil
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnRead), true, false); !errors.Is(err, ErrNoBroker) {
		t.Fatalf("got %v", err)
	}
	if f.fileBytes(t) != "" {
		t.Fatal("saved")
	}
}

func TestEnvRefRefused(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	e := tok("a", "A_TOKEN")
	e.Value = config.KeyRef{Type: "env", Var: "A_SRC"}
	if err := f.cm.PutTokens([]config.StaticToken{e}, nil, true); !errors.Is(err, ErrEnvRefOnWeb) {
		t.Fatalf("token: %v", err)
	}
	app := config.GitHubApp{Name: "gh", ClientID: "Iv1", PrivateKey: config.KeyRef{Type: "env", Var: "GH_KEY"}}
	if err := f.cm.PutGitHubApp(app, nil, true); !errors.Is(err, ErrEnvRefOnWeb) {
		t.Fatalf("gh: %v", err)
	}
	if f.fileBytes(t) != "" {
		t.Fatal("saved")
	}
}

func TestPrivateEgressRefused(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	e := tok("a", "A_TOKEN")
	e.Egress = []config.TokenEgress{{Host: "jira.corp.example", Private: true, CIDR: "10.0.0.0/24"}}
	if err := f.cm.PutTokens([]config.StaticToken{e}, map[string][]byte{"a": []byte("v")}, true); !errors.Is(err, ErrPrivateEgressOnWeb) {
		t.Fatalf("got %v", err)
	}
	if f.vals.has("a") {
		t.Fatal("value set")
	}
}

func TestTemplateBounded(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	e := tok("a", "A_TOKEN")
	e.Template = "Not A Catalog Id!"
	if err := f.cm.PutTokens([]config.StaticToken{e}, map[string][]byte{"a": []byte("v")}, true); err == nil {
		t.Fatal("bad template accepted")
	}
	e.Template = "datadog"
	if err := f.cm.PutTokens([]config.StaticToken{e}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
}

func TestMCPRefusedOnWeb(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	e := tok("a", "A_TOKEN")
	e.MCP = &config.TokenMCP{Server: "s", Command: "npx"}
	if err := f.cm.PutTokens([]config.StaticToken{e}, map[string][]byte{"a": []byte("v")}, true); !errors.Is(err, ErrMCPOnWeb) {
		t.Fatalf("got %v", err)
	}
}

func TestPutGitHubAppStoresKey(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if err := f.cm.PutGitHubApp(config.GitHubApp{Name: "gh", ClientID: "Iv1"}, []byte("-----BEGIN"), true); err != nil {
		t.Fatal(err)
	}
	if !f.vals.has("gh-gh-key") {
		t.Fatalf("store: %v", f.vals.m)
	}
	got := f.file(t).GitHubApps
	if len(got) != 1 || got[0].PrivateKey != (config.KeyRef{Type: "store", ID: "gh-gh-key"}) {
		t.Fatalf("file: %+v", got)
	}
	// Edit without a new key keeps the reference.
	if err := f.cm.PutGitHubApp(config.GitHubApp{Name: "gh", ClientID: "Iv2", PrivateKey: got[0].PrivateKey}, nil, false); err != nil {
		t.Fatal(err)
	}
	if f.file(t).GitHubApps[0].ClientID != "Iv2" {
		t.Fatal("not replaced")
	}
}

// A new value may not overwrite a stored value another definition reads.
func TestValueOfAnotherEntryNotOverwritten(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	b := tok("b", "B_TOKEN")
	b.Value = config.KeyRef{Type: "store", ID: "a"}
	err := f.cm.PutTokens([]config.StaticToken{b}, map[string][]byte{"b": []byte("other")}, true)
	var inUse ErrCredInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("got %v", err)
	}
	if string(f.vals.m["a"]) != "v" {
		t.Fatal("value of a overwritten")
	}
}

func TestAuditFailureBlocksWrite(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	f.rec.err = errors.New("db locked")
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnRead), true, false); err == nil {
		t.Fatal("want an error")
	}
	if err := f.cm.PutGitHubApp(config.GitHubApp{Name: "gh", ClientID: "Iv1"}, []byte("pem"), true); err == nil {
		t.Fatal("want an error")
	}
	if f.vals.has("gh-gh-key") {
		t.Fatal("key left behind")
	}
	if f.fileBytes(t) != "" || f.applyCount() != 0 {
		t.Fatal("write happened")
	}
	if m, _ := f.cm.Merged(); len(m.AWSConnections)+len(m.GitHubApps) != 0 {
		t.Fatal("in-memory state changed")
	}
}

func TestErrorsNeverCarryValue(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	const secret = "VALUE-DO-NOT-ECHO"
	for _, e := range []config.StaticToken{tok("a", "PATH"), tok("Bad Name", "A_TOKEN")} {
		err := f.cm.PutTokens([]config.StaticToken{e}, map[string][]byte{e.Name: []byte(secret)}, true)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("err %v", err)
		}
	}
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"other": []byte(secret)}, true); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("stray value: %v", err)
	}
}

// Review Focus 1: two tabs create the same name at once.
func TestConcurrentCreatesSameName(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = f.cm.PutTokens([]config.StaticToken{tok("x", "X_TOKEN")}, map[string][]byte{"x": []byte("v")}, true)
		}(i)
	}
	wg.Wait()
	ok, exists := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrCredExists):
			exists++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || exists != 1 {
		t.Fatalf("ok=%d exists=%d", ok, exists)
	}
	if !f.vals.has("x") || len(f.file(t).StaticTokens) != 1 {
		t.Fatal("state after race")
	}
}

// Review Focus 1: a profile saved against a just-deleted entry is refused with
// the existing "not defined" error, and one already stored keeps working.
func TestProfileAgainstDeletedEntry(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if err := f.cm.PutTokens([]config.StaticToken{tok("a", "A_TOKEN")}, map[string][]byte{"a": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	if err := f.cm.Delete(credfile.KindToken, "a"); err != nil {
		t.Fatal(err)
	}
	err := f.pm.Save(config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("got %v", err)
	}
	// A stale profile already naming the entry (the save raced the delete).
	f.pm.store.(*fakeStore).m["stale"] = config.AgentConfig{Name: "stale", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"a"}}
	if got := f.pm.RefsTo(credfile.KindToken, "a"); !slices.Equal(got, []string{"stale"}) {
		t.Fatalf("refs %v", got)
	}
	if _, err := f.cm.PutAWS(context.Background(), awsConn("c", "p", arnRead), true, false); err != nil {
		t.Fatalf("unrelated save blocked by a stale profile: %v", err)
	}
}

func TestDeleteRefusedWhenAnotherEntryDependsOnIt(t *testing.T) {
	// A file written by hand: web token b's mcp.extra names web token a.
	f := newCredFixture(t, config.Config{}, nil)
	a := tok("a", "A_TOKEN")
	a.Value = config.KeyRef{Type: "file", Path: "/a"}
	b := tok("b", "B_TOKEN")
	b.Value = config.KeyRef{Type: "file", Path: "/b"}
	b.MCP = &config.TokenMCP{Server: "s", Command: "npx", Extra: []string{"a"}}
	f.cm = NewCredentialManager(config.Config{}, credfile.File{StaticTokens: []config.StaticToken{a, b}}, f.dir, f.pm,
		f.vals, f.rec, f.chk, func(config.Config) {}, nil)
	if err := f.cm.Delete(credfile.KindToken, "a"); err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("got %v", err)
	}
}

func TestVerdictMapBounded(t *testing.T) {
	f := newCredFixture(t, config.Config{}, nil)
	if v := f.cm.Verdict("p", arnRead); v.Reason != awsprobe.ReasonNotChecked {
		t.Fatalf("absent: %+v", v)
	}
	for i := range maxVerdicts + 10 {
		f.cm.RecordVerdict(fmt.Sprintf("p%d", i), arnRead, awsprobe.Result{State: awsprobe.NoneFound})
	}
	if n := len(f.cm.verdicts); n != maxVerdicts {
		t.Fatalf("verdicts: %d", n)
	}
	if v := f.cm.Verdict(fmt.Sprintf("p%d", maxVerdicts+9), arnRead); v.State != awsprobe.NoneFound {
		t.Fatalf("latest lost: %+v", v)
	}
}
