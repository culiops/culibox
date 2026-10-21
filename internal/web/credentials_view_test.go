package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
	"github.com/culiops/agent-box/internal/secretchain"
	"github.com/culiops/agent-box/internal/secretstore"
)

const pathCreds = "/api/credentials"

// viewBody is the GET /api/credentials answer, decoded.
type viewBody struct {
	Store struct {
		Initialized    bool       `json:"initialized"`
		Locked         bool       `json:"locked"`
		ExpiresAt      *time.Time `json:"expires_at"`
		Platform       string     `json:"platform"`
		PassphraseMode bool       `json:"passphrase_mode"`
	} `json:"store"`
	AWS []struct {
		Name          string   `json:"name"`
		Label         string   `json:"label"`
		Description   string   `json:"description"`
		AccountID     string   `json:"account_id"`
		Region        string   `json:"region"`
		Prod          bool     `json:"prod"`
		Source        string   `json:"source"`
		SourceProfile string   `json:"source_profile"`
		RoleARN       string   `json:"role_arn"`
		Elevate       any      `json:"elevate"`
		Status        string   `json:"status"`
		Error         string   `json:"error"`
		UsedBy        []string `json:"used_by"`
		Base          struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"base"`
	} `json:"aws"`
	GitHubApps []struct {
		Name       string   `json:"name"`
		ClientID   string   `json:"client_id"`
		KeyRefKind string   `json:"key_ref_kind"`
		Source     string   `json:"source"`
		Status     string   `json:"status"`
		Error      string   `json:"error"`
		UsedBy     []string `json:"used_by"`
	} `json:"github_apps"`
	Tokens []struct {
		Name        string   `json:"name"`
		Provider    string   `json:"provider"`
		Template    string   `json:"template"`
		Label       string   `json:"label"`
		Description string   `json:"description"`
		Prod        bool     `json:"prod"`
		Env         []string `json:"env"`
		Hosts       []string `json:"hosts"`
		ValueKind   string   `json:"value_kind"`
		Source      string   `json:"source"`
		Status      string   `json:"status"`
		Error       string   `json:"error"`
		UsedBy      []string `json:"used_by"`
	} `json:"tokens"`
	Values []struct {
		ID           string   `json:"id"`
		Updated      any      `json:"updated"`
		ReferencedBy []string `json:"referenced_by"`
	} `json:"values"`
	LegacySecrets []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"legacy_secrets"`
	FileBad bool `json:"file_bad"`
	Invalid []struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		Error string `json:"error"`
	} `json:"invalid"`
}

type viewFix struct {
	*credFixture
	srv *Server
	st  *fakeSecretStore
}

// newViewFix is a fixture whose server shows base (config.json seeds) and the
// profiles; the store is locked and passphrase-protected like a fresh daemon.
func newViewFix(t *testing.T, base config.Config, profiles []config.AgentConfig) *viewFix {
	t.Helper()
	fx := newCredFixture(t, base, profiles)
	st := &fakeSecretStore{st: secretstore.Status{Initialized: true, Platform: "linux", PassphraseMode: true}}
	return &viewFix{credFixture: fx, st: st,
		srv: &Server{Creds: fx.cm, CredAudit: fx.rec, Store: st}}
}

func (v *viewFix) get(t *testing.T) (viewBody, string) {
	t.Helper()
	w := do(v.srv.handleCredentials, storeReq("GET", pathCreds, "", "", credCookie))
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", pathCreds, w.Code, w.Body)
	}
	var b viewBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body)
	}
	return b, w.Body.String()
}

// withStoreRef gives t a store reference (tok leaves the value unset).
func withStoreRef(t config.StaticToken, id string) config.StaticToken {
	t.Value = config.KeyRef{Type: "store", ID: id}
	return t
}

func TestViewNeverContainsValue(t *testing.T) {
	const (
		storeVal = "STORE-VALUE-4f9c1e7a"
		envVal   = "ENV-VALUE-77b3d2c0"
		fileVal  = "FILE-VALUE-0a8e5d91"
	)
	t.Setenv("VIEW_TEST_SEED_TOKEN", envVal)
	keyFile := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyFile, []byte(fileVal), 0o600); err != nil {
		t.Fatal(err)
	}
	base := config.Config{
		StaticTokens: []config.StaticToken{
			{Name: "seed-env", Value: config.KeyRef{Type: "env", Var: "VIEW_TEST_SEED_TOKEN"}, Env: []string{"SEED_TOKEN"},
				Egress: []config.TokenEgress{{Host: "api.example.com"}}},
		},
		GitHubApps: []config.GitHubApp{{Name: "seed-app", ClientID: "Iv1.abc", PrivateKey: config.KeyRef{Type: "file", Path: keyFile}}},
	}
	v := newViewFix(t, base, []config.AgentConfig{{Name: "p1", Bin: "claude", Secrets: []string{"legacy-one"}}})
	v.vals.m["web-tok"] = []byte(storeVal)
	if err := v.cm.PutTokens([]config.StaticToken{{Name: "web-tok", Value: config.KeyRef{Type: "store", ID: "web-tok"},
		Env: []string{"WEB_TOKEN"}, Egress: []config.TokenEgress{{Host: "api.web.example"}}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	v.srv.SecretCheck = func(string) (secretchain.Presence, error) { return secretchain.Present, nil }
	b, raw := v.get(t)
	for _, secret := range []string{storeVal, envVal, fileVal} {
		if strings.Contains(raw, secret) {
			t.Fatalf("response carries a value (%q): %s", secret, raw)
		}
	}
	// The statuses are there: the test is not passing on an empty body.
	if len(b.Tokens) != 2 || len(b.GitHubApps) != 1 || b.Tokens[0].Status != "ok" || b.GitHubApps[0].Status != "ok" ||
		len(b.Values) != 1 || len(b.LegacySecrets) != 1 {
		t.Fatalf("view is not populated: %s", raw)
	}
	// A reference's path is the user's own config, but a file is only stat-ed: its
	// directory must not leak a listing either.
	if strings.Contains(raw, filepath.Base(keyFile)) {
		t.Fatalf("response names the key file: %s", raw)
	}
}

func TestViewStatuses(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.pem")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEW_SET", "yes")
	t.Setenv("VIEW_EMPTY", "")
	mk := func(n string, ref config.KeyRef) config.StaticToken {
		return config.StaticToken{Name: n, Value: ref, Env: []string{"E_" + strings.ToUpper(strings.ReplaceAll(n, "-", "_"))},
			Egress: []config.TokenEgress{{Host: "api.example.com"}}}
	}
	base := config.Config{StaticTokens: []config.StaticToken{
		mk("store-ok", config.KeyRef{Type: "store", ID: "has"}),
		mk("store-miss", config.KeyRef{Type: "store", ID: "gone"}),
		mk("file-ok", config.KeyRef{Type: "file", Path: present}),
		mk("file-miss", config.KeyRef{Type: "file", Path: filepath.Join(dir, "absent.pem")}),
		mk("file-dir", config.KeyRef{Type: "file", Path: dir}),
		mk("env-set", config.KeyRef{Type: "env", Var: "VIEW_SET"}),
		mk("env-unset", config.KeyRef{Type: "env", Var: "VIEW_NOT_SET_ANYWHERE"}),
		mk("env-empty", config.KeyRef{Type: "env", Var: "VIEW_EMPTY"}),
	}}
	v := newViewFix(t, base, nil)
	v.vals.m["has"] = []byte("v")
	want := map[string]string{"store-ok": "ok", "store-miss": "missing", "file-ok": "ok", "file-miss": "missing",
		"file-dir": "missing", "env-set": "ok", "env-unset": "unset", "env-empty": "unset"}
	check := func(label string, want map[string]string) {
		t.Helper()
		b, raw := v.get(t)
		got := map[string]string{}
		for _, tk := range b.Tokens {
			got[tk.Name] = tk.Status
		}
		for n, s := range want {
			if got[n] != s {
				t.Errorf("%s: %s = %q, want %q\n%s", label, n, got[n], s, raw)
			}
		}
	}
	check("unlocked", want)

	// Locked: a value that is there is stored (store locked); one that is not is
	// still missing, because List works while locked.
	v.st.st.Locked = true
	want["store-ok"] = "stored_locked"
	check("locked", want)

	// A store that was never created holds nothing.
	v.st.st = secretstore.Status{Platform: "linux"}
	v.cm.vals = notInitVals{}
	want["store-ok"], want["store-miss"] = "missing", "missing"
	check("uninitialised", want)
}

// notInitVals is a store whose List says it was never created.
type notInitVals struct{ *fakeVals }

func (notInitVals) List() ([]secretstore.Entry, error) { return nil, secretstore.ErrNotInitialized }

func TestViewStoreListErrorIsNotMissing(t *testing.T) {
	base := config.Config{StaticTokens: []config.StaticToken{{Name: "t", Value: config.KeyRef{Type: "store", ID: "x"},
		Env: []string{"T"}, Egress: []config.TokenEgress{{Host: "api.example.com"}}}}}
	v := newViewFix(t, base, nil)
	v.cm.vals = errVals{errors.New("disk on fire at /secret/path")}
	b, raw := v.get(t)
	if b.Tokens[0].Status != "missing" || b.Tokens[0].Error == "" || strings.Contains(raw, "/secret/path") {
		t.Fatalf("a failing store read must say so without its text: %s", raw)
	}
}

type errVals struct{ err error }

func (errVals) Set(string, []byte, string) error     { return nil }
func (errVals) Remove(string, string) error          { return nil }
func (e errVals) List() ([]secretstore.Entry, error) { return nil, e.err }

func TestViewUsedBy(t *testing.T) {
	base := config.Config{
		AWSConnections: []config.AWSConnection{awsConn("prod-ro", "host-a", arnRead)},
		GitHubApps:     []config.GitHubApp{{Name: "gh", ClientID: "Iv1.x", PrivateKey: config.KeyRef{Type: "store", ID: "gh-key"}}},
		StaticTokens:   []config.StaticToken{tok("tk", "TK_TOKEN")},
	}
	base.StaticTokens[0].Value = config.KeyRef{Type: "store", ID: "tk"}
	profiles := []config.AgentConfig{
		{Name: "zeta", Bin: "claude", Runtime: config.RuntimeBox, AWSConnections: []string{"prod-ro"}, Tokens: []string{"tk"}},
		{Name: "alpha", Bin: "claude", Runtime: config.RuntimeBox, AWSConnections: []string{"prod-ro"},
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "o", App: "gh", Repos: []string{"r"}}}}},
		{Name: "lonely", Bin: "claude"},
	}
	v := newViewFix(t, base, profiles)
	v.vals.m["gh-key"], v.vals.m["tk"] = []byte("a"), []byte("b")
	b, raw := v.get(t)
	if got := strings.Join(b.AWS[0].UsedBy, ","); got != "alpha,zeta" {
		t.Errorf("aws used_by = %q\n%s", got, raw)
	}
	if got := strings.Join(b.GitHubApps[0].UsedBy, ","); got != "alpha" {
		t.Errorf("app used_by = %q", got)
	}
	if got := strings.Join(b.Tokens[0].UsedBy, ","); got != "zeta" {
		t.Errorf("token used_by = %q", got)
	}
	// Unused entries and the empty lists are [] on the wire, never null.
	v2 := newViewFix(t, config.Config{StaticTokens: []config.StaticToken{tok("free", "FREE")}}, nil)
	_, raw2 := v2.get(t)
	for _, bad := range []string{`"used_by":null`, `"values":null`, `"aws":null`, `"github_apps":null`, `"tokens":null`,
		`"legacy_secrets":null`, `"invalid":null`, `"env":null`, `"hosts":null`} {
		if strings.Contains(raw2, bad) {
			t.Errorf("response has %s: %s", bad, raw2)
		}
	}
	// A value shows who reads it.
	var found bool
	for _, val := range b.Values {
		if val.ID == "gh-key" {
			found = strings.Join(val.ReferencedBy, ",") == "github_apps/gh"
		}
	}
	if !found {
		t.Errorf("values referenced_by: %s", raw)
	}
}

func TestViewValuesReferencedBy(t *testing.T) {
	base := config.Config{SecretSources: []config.SecretSource{{Type: "store"}}}
	v := newViewFix(t, base, []config.AgentConfig{{Name: "p", Bin: "claude", Secrets: []string{"shared"}}})
	v.vals.m["shared"], v.vals.m["orphan"] = []byte("a"), []byte("b")
	b, raw := v.get(t)
	got := map[string]string{}
	for _, val := range b.Values {
		got[val.ID] = strings.Join(val.ReferencedBy, ",")
	}
	if got["shared"] != "agents/p.secrets" || got["orphan"] != "" || len(b.Values) != 2 {
		t.Fatalf("values = %v\n%s", got, raw)
	}
	if b.Values[0].ID != "orphan" {
		t.Errorf("values are not sorted by id: %s", raw)
	}
}

func TestViewLegacySecrets(t *testing.T) {
	profiles := []config.AgentConfig{
		{Name: "a", Bin: "claude", Secrets: []string{"two", "one", "locked"}},
		{Name: "b", Bin: "claude", Secrets: []string{"one", "broken"}},
	}
	v := newViewFix(t, config.Config{}, profiles)
	v.srv.SecretCheck = func(n string) (secretchain.Presence, error) {
		switch n {
		case "one":
			return secretchain.Present, nil
		case "locked":
			return secretchain.StoredLocked, nil
		case "broken":
			return secretchain.Absent, errors.New("read /home/x/secret: permission denied")
		}
		return secretchain.Absent, nil
	}
	b, raw := v.get(t)
	var got []string
	for _, s := range b.LegacySecrets {
		got = append(got, s.Name+"="+s.Status)
	}
	if want := "broken=invalid,locked=stored_locked,one=ok,two=missing"; strings.Join(got, ",") != want {
		t.Fatalf("legacy_secrets = %v, want %s", got, want)
	}
	if strings.Contains(raw, "/home/x") {
		t.Fatalf("a resolver error's text reached the response: %s", raw)
	}
	// No resolver wired: nothing resolves.
	v.srv.SecretCheck = nil
	b, _ = v.get(t)
	for _, s := range b.LegacySecrets {
		if s.Status != "missing" {
			t.Errorf("%s = %s without a resolver", s.Name, s.Status)
		}
	}
}

func TestViewSourceTags(t *testing.T) {
	base := config.Config{
		AWSConnections: []config.AWSConnection{awsConn("seed-aws", "host-a", arnRead)},
		GitHubApps:     []config.GitHubApp{{Name: "seed-app", ClientID: "Iv1.s", PrivateKey: config.KeyRef{Type: "store", ID: "seed-app-key"}}},
		StaticTokens:   []config.StaticToken{withStoreRef(tok("seed-tok", "SEED_TOK"), "seed-tok")},
	}
	v := newViewFix(t, base, nil)
	if _, err := v.cm.PutAWS(t.Context(), awsConn("web-aws", "host-b", arnWrite), true, true); err != nil {
		t.Fatal(err)
	}
	if err := v.cm.PutGitHubApp(config.GitHubApp{Name: "web-app", ClientID: "Iv1.w"}, []byte("pem"), true); err != nil {
		t.Fatal(err)
	}
	if err := v.cm.PutTokens([]config.StaticToken{tok("web-tok", "WEB_TOK")}, map[string][]byte{"web-tok": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	b, raw := v.get(t)
	src := map[string]string{}
	for _, a := range b.AWS {
		src[a.Name] = a.Source
	}
	for _, a := range b.GitHubApps {
		src[a.Name] = a.Source
	}
	for _, a := range b.Tokens {
		src[a.Name] = a.Source
	}
	want := map[string]string{"seed-aws": "config", "web-aws": "web", "seed-app": "config", "web-app": "web", "seed-tok": "config", "web-tok": "web"}
	for n, s := range want {
		if src[n] != s {
			t.Errorf("source of %s = %q, want %q", n, src[n], s)
		}
	}
	if t.Failed() {
		t.Log(raw)
	}
	if b.GitHubApps[1].KeyRefKind != "store" || b.Tokens[1].ValueKind != "store" {
		t.Errorf("ref kinds: %s", raw)
	}
}

func TestViewEntryFields(t *testing.T) {
	conn := awsConn("prod-ro", "host-a", arnRead)
	conn.Label, conn.Description, conn.Prod = "Prod read", "the prod account", true
	conn.Elevate = &config.AWSElevate{RoleARN: arnWrite, MaxMinutes: 30}
	tk := config.StaticToken{Name: "dd", Value: config.KeyRef{Type: "store", ID: "dd"}, Env: []string{"DD_API_KEY", "DD_SITE"},
		Egress:   []config.TokenEgress{{Host: "api.datadoghq.com"}, {Host: "api.eu.example", Port: 8443}, {Host: "10.0.0.5", Private: true}},
		Provider: "datadog", Template: "datadog", Label: "Datadog", Description: "d", Prod: true}
	v := newViewFix(t, config.Config{AWSConnections: []config.AWSConnection{conn}, StaticTokens: []config.StaticToken{tk}}, nil)
	b, raw := v.get(t)
	a := b.AWS[0]
	if a.AccountID != "123456789012" || a.Region != "us-east-1" || a.SourceProfile != "host-a" || a.RoleARN != arnRead ||
		a.Label != "Prod read" || a.Description != "the prod account" || !a.Prod || a.Elevate == nil || a.Status != "ok" {
		t.Errorf("aws row: %s", raw)
	}
	tr := b.Tokens[0]
	if tr.Provider != "datadog" || tr.Template != "datadog" || tr.Label != "Datadog" || !tr.Prod || tr.ValueKind != "store" ||
		strings.Join(tr.Env, ",") != "DD_API_KEY,DD_SITE" ||
		strings.Join(tr.Hosts, ",") != "api.datadoghq.com,api.eu.example:8443,10.0.0.5" {
		t.Errorf("token row: %s", raw)
	}
}

func TestViewStoreBlock(t *testing.T) {
	v := newViewFix(t, config.Config{}, nil)
	exp := time.Unix(1_800_000_000, 0).UTC()
	v.st.st = secretstore.Status{Initialized: true, Locked: false, ExpiresAt: exp, Platform: "linux", PassphraseMode: true}
	b, _ := v.get(t)
	if !b.Store.Initialized || b.Store.Locked || b.Store.ExpiresAt == nil || !b.Store.ExpiresAt.Equal(exp) ||
		b.Store.Platform != "linux" || !b.Store.PassphraseMode {
		t.Errorf("store = %+v", b.Store)
	}
	// No store wired: an uninitialised, unlocked-looking block, not a crash.
	v.srv.Store = nil
	b, raw := v.get(t)
	if b.Store.Initialized || b.Store.Locked || !strings.Contains(raw, `"expires_at":null`) {
		t.Errorf("no store: %s", raw)
	}
}

func TestViewRefusesBearer(t *testing.T) {
	v := newViewFix(t, config.Config{}, nil)
	for name, kind := range map[string]credKind{"bearer": credBearer, "none": credNone} {
		w := do(v.srv.handleCredentials, storeReq("GET", pathCreds, "", "", kind))
		if w.Code != 403 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// A cross-site fetch with the cookie is refused too (D8: cookie-only incl. GET).
	r := storeReq("GET", pathCreds, "", "http://evil.example", credCookie)
	if w := do(v.srv.handleCredentials, r); w.Code != 403 {
		t.Errorf("cross-origin: %d", w.Code)
	}
	r = storeReq("GET", pathCreds, "", "", credCookie)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := do(v.srv.handleCredentials, r); w.Code != 403 {
		t.Errorf("cross-site: %d", w.Code)
	}
	if w := do(v.srv.handleCredentials, storeReq("POST", pathCreds, "{}", credOrigin, credCookie)); w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}
}

func TestViewWithoutCreds(t *testing.T) {
	// With Creds nil the route is not registered; the handler alone must still not crash.
	var s Server
	if w := do(s.handleCredentials, storeReq("GET", pathCreds, "", "", credCookie)); w.Code != 404 {
		t.Errorf("no Creds: %d", w.Code)
	}
}

func TestViewAfterRestartNotChecked(t *testing.T) {
	conn := awsConn("web-aws", "host-a", arnRead)
	base := config.Config{AWSConnections: []config.AWSConnection{awsConn("seed-aws", "host-s", arnRead)}}
	v := newViewFix(t, base, nil)
	if _, err := v.cm.PutAWS(t.Context(), conn, true, false); err != nil {
		t.Fatal(err)
	}
	// Before the restart the check's verdict shows on the saved entry.
	b, raw := v.get(t)
	var web string
	for _, a := range b.AWS {
		if a.Name == "web-aws" {
			web = a.Base.State
		}
	}
	if web != string(awsprobe.NoneFound) {
		t.Fatalf("before restart: web-aws base = %q\n%s", web, raw)
	}
	// A new manager over the same data dir: the entries come back from
	// credentials.json, every verdict reads "not checked".
	f2, bad, err := credfile.Load(v.dir)
	if err != nil || bad {
		t.Fatalf("reload: bad=%v err=%v", bad, err)
	}
	cm2 := NewCredentialManager(base, f2, v.dir, v.pm, v.vals, v.rec, v.chk, func(config.Config) {}, nil)
	srv2 := &Server{Creds: cm2, Store: v.st}
	w := do(srv2.handleCredentials, storeReq("GET", pathCreds, "", "", credCookie))
	var b2 viewBody
	if err := json.Unmarshal(w.Body.Bytes(), &b2); err != nil {
		t.Fatal(err)
	}
	if len(b2.AWS) != 2 {
		t.Fatalf("after restart: %d connections: %s", len(b2.AWS), w.Body)
	}
	nc := awsprobe.NotChecked()
	for _, a := range b2.AWS {
		if a.Base.State != string(nc.State) || a.Base.Reason != string(nc.Reason) {
			t.Errorf("%s after restart: base = %+v, want state %q reason %q", a.Name, a.Base, nc.State, nc.Reason)
		}
	}
}

func TestViewInvalidEntriesAndBadFile(t *testing.T) {
	dir := t.TempDir()
	base := config.Config{StaticTokens: []config.StaticToken{withStoreRef(tok("seed-tok", "SEED_TOK"), "seed-tok")}}
	raw := `{
  "aws_connections": [
    {"name": "good-aws", "source_profile": "h", "role_arn": "` + arnRead + `", "region": "us-east-1"},
    {"name": "bad-aws", "source_profile": "h", "role_arn": "not-an-arn", "region": "us-east-1"}
  ],
  "static_tokens": [
    {"name": "seed-tok", "value": {"type": "store", "id": "x"}, "env": ["X"], "egress": [{"host": "a.example.com"}]},
    {"name": "typo-tok", "value": {"type": "store", "id": "y"}, "env": ["Y"], "egres": []}
  ]
}`
	if err := os.WriteFile(credfile.Path(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	f, bad, err := credfile.Load(dir)
	if err != nil || bad {
		t.Fatalf("load: %v %v", bad, err)
	}
	fx := newCredFixture(t, base, nil)
	cm := NewCredentialManager(base, f, dir, fx.pm, fx.vals, fx.rec, fx.chk, func(config.Config) {}, nil)
	srv := &Server{Creds: cm}
	get := func() viewBody {
		w := do(srv.handleCredentials, storeReq("GET", pathCreds, "", "", credCookie))
		var b viewBody
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := get()
	rows := map[string]string{}
	for _, a := range b.AWS {
		rows[a.Name] = a.Status + "/" + a.Source
		if a.Name == "bad-aws" && a.Error == "" {
			t.Errorf("an invalid entry must show its error")
		}
	}
	if rows["good-aws"] != "ok/web" || rows["bad-aws"] != "invalid/web" || len(b.AWS) != 2 {
		t.Errorf("aws rows = %v", rows)
	}
	// A web entry shadowing a seed is not a second row under the seed's name; an
	// undecodable one has no row at all. Both are in invalid.
	if len(b.Tokens) != 1 || b.Tokens[0].Name != "seed-tok" || b.Tokens[0].Source != "config" {
		t.Errorf("tokens = %+v", b.Tokens)
	}
	inv := map[string]string{}
	for _, e := range b.Invalid {
		inv[e.Kind+"/"+e.Name] = e.Error
	}
	for _, k := range []string{"aws/bad-aws", "token/seed-tok", "token/typo-tok"} {
		if inv[k] == "" {
			t.Errorf("invalid is missing %s: %v", k, inv)
		}
	}
	if b.FileBad {
		t.Errorf("file_bad is set for a readable file")
	}

	// A file that is not JSON: no entries, a banner, and a save clears it.
	cm.SetFileBad(true)
	if b = get(); !b.FileBad {
		t.Errorf("file_bad is not shown")
	}
	if err := cm.PutTokens([]config.StaticToken{tok("fresh", "FRESH")}, map[string][]byte{"fresh": []byte("v")}, true); err != nil {
		t.Fatal(err)
	}
	b = get()
	if b.FileBad {
		t.Errorf("file_bad survived a successful save")
	}
	// The save dropped what could not be loaded; the list follows the file.
	for _, e := range b.Invalid {
		if e.Name == "typo-tok" {
			t.Errorf("a dropped undecodable entry is still listed invalid: %+v", b.Invalid)
		}
	}
}
