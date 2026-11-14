package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/credtemplates"
	"github.com/culiops/culibox/internal/secretstore"
)

const (
	pathTokens = "/api/credentials/tokens"
	pathApps   = "/api/credentials/github-apps"
	pathValues = "/api/store/values/"
)

type tokenRoutes struct {
	srv   *Server
	fx    *credFixture
	store *fakeSecretStore
}

func newTokenRoutes(t *testing.T) *tokenRoutes {
	t.Helper()
	fx := newCredFixture(t, config.Config{}, nil)
	st := &fakeSecretStore{
		st:   secretstore.Status{Initialized: true, Locked: true, Platform: "linux", PassphraseMode: true},
		pass: "correct horse battery",
	}
	return &tokenRoutes{srv: &Server{Creds: fx.cm, CredAudit: fx.rec, Store: st, StoreUnlockTTL: 48 * time.Hour}, fx: fx, store: st}
}

func (a *tokenRoutes) handler(path string) http.HandlerFunc {
	switch {
	case path == "/api/credentials/templates":
		return a.srv.handleCredTemplates
	case strings.HasPrefix(path, pathTokens):
		return a.srv.handleCredTokens
	case strings.HasPrefix(path, pathApps):
		return a.srv.handleCredGitHubApps
	case strings.HasPrefix(path, pathValues):
		return a.srv.handleStoreValue
	case path == "/api/store/init":
		return a.srv.handleStoreInit
	}
	panic("no handler for " + path)
}

func (a *tokenRoutes) do(method, path, body string) *httptest.ResponseRecorder {
	return do(a.handler(path), storeReq(method, path, body, credOrigin, credCookie))
}

func (a *tokenRoutes) call(method, path, body string) (int, map[string]any, string) {
	w := a.do(method, path, body)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m, w.Body.String()
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fromTpl(template, name string, fields map[string]string, values ...string) string {
	b, _ := json.Marshal(map[string]any{"template": template, "name": name, "fields": fields, "values": values})
	return string(b)
}

func TestTemplatesRoute(t *testing.T) {
	a := newTokenRoutes(t)
	code, m, _ := a.call("GET", "/api/credentials/templates", "")
	if code != 200 {
		t.Fatalf("got %d", code)
	}
	ts, _ := m["templates"].([]any)
	if len(ts) != len(credtemplates.All()) {
		t.Fatalf("templates: %v", m)
	}
	w := do(a.srv.handleCredTemplates, storeReq("GET", "/api/credentials/templates", "", "", credBearer))
	if w.Code != 403 {
		t.Fatalf("bearer: %d", w.Code)
	}
	if w := a.do("POST", "/api/credentials/templates", "{}"); w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
}

func TestFromTemplateStoresValue(t *testing.T) {
	a := newTokenRoutes(t)
	const secret = "sntrys_VERY-SECRET-0123456789"
	code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("sentry", "sen", map[string]string{"region": "eu"}, secret))
	if code != 201 {
		t.Fatalf("got %d %s", code, raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("response carries the value: %s", raw)
	}
	if string(a.fx.vals.m["sen"]) != secret {
		t.Fatalf("store: %v", a.fx.vals.m)
	}
	got := a.fx.file(t).StaticTokens
	if len(got) != 1 || got[0].Template != "sentry" || got[0].Value != (config.KeyRef{Type: "store", ID: "sen"}) {
		t.Fatalf("entry: %+v", got)
	}
	if strings.Contains(a.fx.fileBytes(t), secret) {
		t.Fatal("the credentials file carries the value")
	}
	for _, e := range a.fx.rec.events {
		if strings.Contains(e.Detail, secret) || strings.Contains(e.Name, secret) {
			t.Fatalf("audit carries the value: %+v", e)
		}
	}
}

func TestFromTemplateDatadogTwoValuesFitTheCap(t *testing.T) {
	a := newTokenRoutes(t)
	big := strings.Repeat("k", secretstore.MaxValueBytes)
	body := fromTpl("datadog", "dd", map[string]string{"site": "datadoghq.com"}, big, big)
	if len(body) > credValueBodyMax {
		t.Fatalf("two max values need %d bytes, cap is %d", len(body), credValueBodyMax)
	}
	code, m, raw := a.call("POST", pathTokens+"/from-template", body)
	if code != 201 || len(a.fx.vals.m["dd-api"]) != secretstore.MaxValueBytes || len(a.fx.vals.m["dd-app"]) != secretstore.MaxValueBytes {
		t.Fatalf("got %d %.200s", code, raw)
	}
	if names, _ := m["names"].([]any); len(names) != 2 {
		t.Fatalf("names: %v", m)
	}
}

func TestFromTemplateValueCount(t *testing.T) {
	a := newTokenRoutes(t)
	for _, body := range []string{
		fromTpl("datadog", "dd", map[string]string{"site": "datadoghq.com"}, "only-one"),
		fromTpl("datadog", "dd", map[string]string{"site": "datadoghq.com"}, "a", "b", "c"),
		fromTpl("sentry", "s", map[string]string{"region": "us"}),
	} {
		if code, _, raw := a.call("POST", pathTokens+"/from-template", body); code != 400 {
			t.Errorf("got %d %s", code, raw)
		}
	}
	if len(a.fx.vals.m) != 0 || a.fx.fileBytes(t) != "" {
		t.Fatalf("something was written: %v", a.fx.vals.m)
	}
}

func TestEmptyValue400NoEntry(t *testing.T) {
	a := newTokenRoutes(t)
	if code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("sentry", "s", map[string]string{"region": "us"}, "")); code != 400 {
		t.Fatalf("from-template: %d %s", code, raw)
	}
	// Datadog: the first value is fine, the second is empty: nothing is written.
	if code, _, _ := a.call("POST", pathTokens+"/from-template", fromTpl("datadog", "d", map[string]string{"site": "datadoghq.com"}, "k", "")); code != 400 {
		t.Fatalf("datadog: %d", code)
	}
	adv := jsonBody(t, map[string]any{"entry": tok("a", "A_TOKEN"), "value": ""})
	if code, _, _ := a.call("POST", pathTokens, adv); code != 400 {
		t.Fatalf("advanced: %d", code)
	}
	if len(a.fx.vals.m) != 0 || a.fx.fileBytes(t) != "" || len(a.fx.rec.events) != 0 {
		t.Fatalf("something was written: %v", a.fx.vals.m)
	}
}

func TestValue64KiBOK(t *testing.T) {
	a := newTokenRoutes(t)
	v := strings.Repeat("a", secretstore.MaxValueBytes)
	code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, v))
	if code != 201 || len(a.fx.vals.m["gh"]) != secretstore.MaxValueBytes {
		t.Fatalf("got %d %.200s", code, raw)
	}
}

func TestValueOver64KiB413NoOrphan(t *testing.T) {
	a := newTokenRoutes(t)
	v := strings.Repeat("a", secretstore.MaxValueBytes+1)
	if code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, v)); code != 413 {
		t.Fatalf("from-template: %d %.200s", code, raw)
	}
	adv := jsonBody(t, map[string]any{"entry": tok("a", "A_TOKEN"), "value": v})
	if code, _, _ := a.call("POST", pathTokens, adv); code != 413 {
		t.Fatalf("advanced: %d", code)
	}
	if code, _, _ := a.call("PUT", pathValues+"x", jsonBody(t, map[string]any{"value": v})); code != 413 {
		t.Fatalf("value: %d", code)
	}
	// Datadog: a good first value and an oversize second one leave nothing.
	dd := fromTpl("datadog", "dd", map[string]string{"site": "datadoghq.com"}, "good", v)
	if code, _, _ := a.call("POST", pathTokens+"/from-template", dd); code != 413 {
		t.Fatalf("datadog: %d", code)
	}
	// A body past the request cap is refused before it is parsed.
	huge := fromTpl("github-pat", "gh", nil, strings.Repeat("a", credValueBodyMax))
	if code, _, _ := a.call("POST", pathTokens+"/from-template", huge); code != 413 {
		t.Fatalf("huge body: %d", code)
	}
	if len(a.fx.vals.m) != 0 || a.fx.fileBytes(t) != "" {
		t.Fatalf("an orphan: %v", a.fx.vals.m)
	}
	// The store itself refusing (not just the handler's own check) leaves none either.
	small := fromTpl("github-pat", "gh", nil, "ok")
	a.fx.vals.setErr = secretstore.ErrTooLarge
	if code, _, _ := a.call("POST", pathTokens+"/from-template", small); code != 413 {
		t.Fatalf("store too large: %d", code)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("an entry was written")
	}
}

// The store refusing an id that differs only in case from a stored one (the
// macOS APFS overwrite) is a conflict the caller can fix, not a server fault,
// and leaves no entry behind.
func TestStoreCaseCollisionIsAConflict(t *testing.T) {
	a := newTokenRoutes(t)
	a.fx.vals.setErr = &secretstore.CaseCollisionError{ID: "gh", Existing: "GH"}
	code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "ok"))
	if code != 409 || !strings.Contains(raw, `\"gh\" collides with existing \"GH\"`) || !strings.Contains(raw, "id_collision") {
		t.Fatalf("collision: %d %s", code, raw)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("an entry was written")
	}
}

func TestTrailingNewlineVerbatim(t *testing.T) {
	a := newTokenRoutes(t)
	// The JSON escape forces the slow decode path; a literal raw newline cannot
	// appear in a JSON string, so both spellings go through "\n".
	body := `{"template":"github-pat","name":"gh","fields":{},"values":["ghp_abc\n"]}`
	if code, _, raw := a.call("POST", pathTokens+"/from-template", body); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := string(a.fx.vals.m["gh"]); got != "ghp_abc\n" {
		t.Fatalf("stored %q", got)
	}
	// And through the value route.
	if code, _, _ := a.call("PUT", pathValues+"gh", `{"value":"ghp_def\r\n"}`); code != 204 {
		t.Fatalf("put: %d", code)
	}
	if got := string(a.fx.vals.m["gh"]); got != "ghp_def\r\n" {
		t.Fatalf("stored %q", got)
	}
}

func TestCreateWhileLocked(t *testing.T) {
	a := newTokenRoutes(t) // the fake store is locked
	if !a.store.Status().Locked {
		t.Fatal("fixture should be locked")
	}
	if code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "v1")); code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	if code, _, _ := a.call("PUT", pathValues+"gh", `{"value":"v2"}`); code != 204 || string(a.fx.vals.m["gh"]) != "v2" {
		t.Fatalf("replace: %d %v", code, a.fx.vals.m)
	}
	// Free the value from the entry, then delete the value while locked.
	if code, _, _ := a.call("DELETE", pathTokens+"/gh", ""); code != 204 {
		t.Fatalf("delete entry: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathValues+"gh", ""); code != 204 || a.fx.vals.has("gh") {
		t.Fatalf("delete value: %d", code)
	}
}

func TestReplaceKeepsStoreID(t *testing.T) {
	a := newTokenRoutes(t)
	// Advanced create with a custom store id.
	e := tok("api", "API_TOKEN")
	e.Value = config.KeyRef{Type: "store", ID: "custom-id"}
	if code, _, raw := a.call("POST", pathTokens, jsonBody(t, map[string]any{"entry": e, "value": "v1"})); code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	if string(a.fx.vals.m["custom-id"]) != "v1" {
		t.Fatalf("store: %v", a.fx.vals.m)
	}
	// Replace with a new value; the request does not name the id (or names another).
	for _, ent := range []config.StaticToken{tok("api", "API_TOKEN"), func() config.StaticToken {
		x := tok("api", "API_TOKEN")
		x.Value = config.KeyRef{Type: "store", ID: "elsewhere"}
		return x
	}()} {
		if code, _, raw := a.call("PUT", pathTokens+"/api", jsonBody(t, map[string]any{"entry": ent, "value": "v2"})); code != 200 {
			t.Fatalf("replace: %d %s", code, raw)
		}
		if string(a.fx.vals.m["custom-id"]) != "v2" || a.fx.vals.has("api") || a.fx.vals.has("elsewhere") || len(a.fx.vals.m) != 1 {
			t.Fatalf("store after replace: %v", a.fx.vals.m)
		}
		if got := a.fx.file(t).StaticTokens[0].Value; got != (config.KeyRef{Type: "store", ID: "custom-id"}) {
			t.Fatalf("entry value: %+v", got)
		}
	}
	// An edit with no value at all (label only) keeps the reference.
	lab := tok("api", "API_TOKEN")
	lab.Label = "renamed"
	if code, _, raw := a.call("PUT", pathTokens+"/api", jsonBody(t, map[string]any{"entry": lab})); code != 200 {
		t.Fatalf("label edit: %d %s", code, raw)
	}
	got := a.fx.file(t).StaticTokens[0]
	if got.Label != "renamed" || got.Value != (config.KeyRef{Type: "store", ID: "custom-id"}) {
		t.Fatalf("entry: %+v", got)
	}
}

func TestTemplateTokenEditsOnlyLabelProdDescriptionValue(t *testing.T) {
	a := newTokenRoutes(t)
	if code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("pagerduty", "pd", map[string]string{"region": "us"}, "v1")); code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	cur := a.fx.file(t).StaticTokens[0]

	ok := cur.Clone()
	ok.Label, ok.Description, ok.Prod = "On call", "paging", true
	if code, _, raw := a.call("PUT", pathTokens+"/pd", jsonBody(t, map[string]any{"entry": ok, "value": "v2"})); code != 200 {
		t.Fatalf("allowed edit: %d %s", code, raw)
	}
	if string(a.fx.vals.m["pd"]) != "v2" {
		t.Fatalf("value: %v", a.fx.vals.m)
	}

	for name, mut := range map[string]func(*config.StaticToken){
		"env": func(e *config.StaticToken) { e.Env = []string{"OTHER_TOKEN"} },
		"config_env": func(e *config.StaticToken) {
			e.ConfigEnv = map[string]string{"PAGERDUTY_API_HOST": "https://x.example"}
		},
		"egress":           func(e *config.StaticToken) { e.Egress = append(e.Egress, config.TokenEgress{Host: "evil.example"}) },
		"provider":         func(e *config.StaticToken) { e.Provider = "other" },
		"template":         func(e *config.StaticToken) { e.Template = "sentry" },
		"template-removed": func(e *config.StaticToken) { e.Template = "" },
	} {
		bad := cur.Clone()
		mut(&bad)
		code, m, raw := a.call("PUT", pathTokens+"/pd", jsonBody(t, map[string]any{"entry": bad}))
		if code != 400 || !strings.Contains(raw, "delete it and create it again") {
			t.Errorf("%s: %d %s", name, code, raw)
		}
		_ = m
	}
	if after := a.fx.file(t).StaticTokens[0]; after.Env[0] != "PAGERDUTY_USER_API_KEY" || len(after.Egress) != 1 {
		t.Fatalf("a refused edit changed the entry: %+v", after)
	}
}

func TestWebTemplateMustBeInCatalog(t *testing.T) {
	a := newTokenRoutes(t)
	e := tok("a", "A_TOKEN")
	e.Template = "not-a-template"
	if code, _, raw := a.call("POST", pathTokens, jsonBody(t, map[string]any{"entry": e, "value": "v"})); code != 400 {
		t.Fatalf("%d %s", code, raw)
	}
	if len(a.fx.vals.m) != 0 {
		t.Fatalf("orphan: %v", a.fx.vals.m)
	}
	e.Template = "grafana"
	if code, _, raw := a.call("POST", pathTokens, jsonBody(t, map[string]any{"entry": e, "value": "v"})); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestOtherTemplateStaysEditable(t *testing.T) {
	a := newTokenRoutes(t)
	fields := map[string]string{"provider": "acme", "env": "ACME_TOKEN", "hosts": "api.acme.example"}
	if code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("other", "acme", fields, "v")); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	cur := a.fx.file(t).StaticTokens[0]
	if cur.Template != "" {
		t.Fatalf("other should not lock the entry: %+v", cur)
	}
	cur.Env = []string{"ACME_API_TOKEN"}
	if code, _, raw := a.call("PUT", pathTokens+"/acme", jsonBody(t, map[string]any{"entry": cur})); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestTokenConflictsAndMessages(t *testing.T) {
	a := newTokenRoutes(t)
	body := fromTpl("github-pat", "gh", nil, "v1")
	if code, _, _ := a.call("POST", pathTokens+"/from-template", body); code != 201 {
		t.Fatal(code)
	}
	code, m, _ := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "TOPSECRET2"))
	if code != 409 || m["code"] != "exists" {
		t.Fatalf("duplicate: %d %v", code, m)
	}
	if string(a.fx.vals.m["gh"]) != "v1" {
		t.Fatalf("a refused create changed the value: %v", a.fx.vals.m)
	}
	// Reserved name, unknown field, trailing data, wrong shapes, unknown template.
	for name, tc := range map[string]struct{ path, body string }{
		"reserved":      {pathTokens, jsonBody(t, map[string]any{"entry": tok("from-template", "A_TOKEN"), "value": "TOPSECRET3"})},
		"unknown field": {pathTokens + "/from-template", `{"template":"github-pat","name":"x","values":["TOPSECRET4"],"extra":1}`},
		"trailing":      {pathTokens + "/from-template", `{"template":"github-pat","name":"x","values":["TOPSECRET5"]} {}`},
		"values string": {pathTokens + "/from-template", `{"template":"github-pat","name":"x","values":"TOPSECRET6"}`},
		"template":      {pathTokens + "/from-template", fromTpl("nope", "x", nil, "TOPSECRET7")},
		"bad field":     {pathTokens + "/from-template", fromTpl("sentry", "x", map[string]string{"region": "mars"}, "TOPSECRET8")},
		"bad name":      {pathTokens + "/from-template", fromTpl("github-pat", "Bad Name", nil, "TOPSECRET9")},
	} {
		code, _, raw := a.call("POST", tc.path, tc.body)
		if code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
		if strings.Contains(raw, "TOPSECRET") {
			t.Errorf("%s: the error echoes the value: %s", name, raw)
		}
	}
	if len(a.fx.vals.m) != 1 {
		t.Fatalf("a refused request stored something: %v", a.fx.vals.m)
	}
}

func TestStoreFailureDoesNotShowPath(t *testing.T) {
	a := newTokenRoutes(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	a.fx.vals.setErr = errors.New("open /var/lib/agent-box/store/values/x.age: permission denied")
	code, _, raw := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "SECRETVALUE"))
	if code != 500 || strings.Contains(raw, "/var/lib") || strings.Contains(raw, "SECRETVALUE") {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(logs.String(), "SECRETVALUE") {
		t.Fatalf("log carries the value: %s", logs.String())
	}
	code, _, raw = a.call("PUT", pathValues+"x", `{"value":"SECRETVALUE"}`)
	if code != 500 || strings.Contains(raw, "/var/lib") {
		t.Fatalf("value route: %d %s", code, raw)
	}
}

func TestStoreMissingIs409(t *testing.T) {
	a := newTokenRoutes(t)
	a.fx.vals.setErr = secretstore.ErrNotInitialized
	code, m, _ := a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "v"))
	if code != 409 || m["code"] != "store_missing" {
		t.Fatalf("%d %v", code, m)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("an entry was written")
	}
}

func TestDeleteTokenKeepsValueAndRefusesSeed(t *testing.T) {
	a := newTokenRoutes(t)
	a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "v"))
	if code, _, _ := a.call("DELETE", pathTokens+"/gh", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if !a.fx.vals.has("gh") {
		t.Fatal("deleting an entry removed its value")
	}
	if code, m, _ := a.call("DELETE", pathTokens+"/gh", ""); code != 404 || m["code"] != "not_found" {
		t.Fatalf("again: %d %v", code, m)
	}
}

func TestDeleteValueInUse409(t *testing.T) {
	a := newTokenRoutes(t)
	a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "v"))
	code, m, raw := a.call("DELETE", pathValues+"gh", "")
	if code != 409 || m["code"] != "in_use" {
		t.Fatalf("%d %s", code, raw)
	}
	by, _ := m["used_by"].([]any)
	if len(by) != 1 || by[0] != "static_tokens/gh" {
		t.Fatalf("used_by: %v", m)
	}
	if !a.fx.vals.has("gh") {
		t.Fatal("the value was removed")
	}
	if code, _, _ := a.call("DELETE", pathValues+"nothing", ""); code != 404 {
		t.Fatalf("missing: %d", code)
	}
	if code, _, _ := a.call("PUT", pathValues+"bad%20id", `{"value":"v"}`); code != 400 {
		t.Fatalf("bad id: %d", code)
	}
	for _, body := range []string{`{}`, `{"value":""}`, `{"value":"v","x":1}`, `{"value":"v"} 1`} {
		if code, _, _ := a.call("PUT", pathValues+"gh", body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
}

func TestGitHubAppRoutes(t *testing.T) {
	a := newTokenRoutes(t)
	const pem = "-----BEGIN PRIVATE KEY-----\nSECRETKEYBODY\n-----END PRIVATE KEY-----\n"
	app := config.GitHubApp{Name: "bot", ClientID: "Iv1.abc123"}
	code, _, raw := a.call("POST", pathApps, jsonBody(t, map[string]any{"app": app, "private_key_value": pem}))
	if code != 201 || strings.Contains(raw, "SECRETKEYBODY") {
		t.Fatalf("%d %s", code, raw)
	}
	if string(a.fx.vals.m["gh-bot-key"]) != pem {
		t.Fatalf("store: %v", a.fx.vals.m)
	}
	// Replace without a key keeps the reference; with one, the same id.
	app.ClientID = "Iv1.def456"
	if code, _, raw := a.call("PUT", pathApps+"/bot", jsonBody(t, map[string]any{"app": app})); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, _ := a.call("PUT", pathApps+"/bot", jsonBody(t, map[string]any{"app": app, "private_key_value": pem + "x"})); code != 200 {
		t.Fatal(code)
	}
	if len(a.fx.vals.m) != 1 || string(a.fx.vals.m["gh-bot-key"]) != pem+"x" {
		t.Fatalf("store: %v", a.fx.vals.m)
	}
	if got := a.fx.file(t).GitHubApps[0]; got.ClientID != "Iv1.def456" || got.PrivateKey.ID != "gh-bot-key" {
		t.Fatalf("app: %+v", got)
	}
	// Empty or oversize key.
	if code, _, _ := a.call("PUT", pathApps+"/bot", jsonBody(t, map[string]any{"app": app, "private_key_value": ""})); code != 400 {
		t.Fatalf("empty key: %d", code)
	}
	if code, _, _ := a.call("PUT", pathApps+"/bot", jsonBody(t, map[string]any{"app": app, "private_key_value": strings.Repeat("k", secretstore.MaxValueBytes+1)})); code != 413 {
		t.Fatalf("big key: %d", code)
	}
	app2 := config.GitHubApp{Name: "bot2", ClientID: "Iv1.abc123"}
	if code, _, _ := a.call("POST", pathApps, jsonBody(t, map[string]any{"app": app2})); code != 400 {
		t.Fatalf("create without a key: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathApps+"/bot", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathApps+"/bot", ""); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
}

// everyTokenRoute is each route this file serves with a body valid for it.
var everyTokenRoute = []struct{ method, path, body string }{
	{"GET", "/api/credentials/templates", ""},
	{"POST", pathTokens, `{"entry":{"name":"a","env":["A_TOKEN"],"egress":[{"host":"api.example.com"}]},"value":"v"}`},
	{"POST", pathTokens + "/from-template", `{"template":"github-pat","name":"gh","values":["v"]}`},
	{"PUT", pathTokens + "/a", `{"entry":{"name":"a","env":["A_TOKEN"],"egress":[{"host":"api.example.com"}]},"value":"v"}`},
	{"DELETE", pathTokens + "/a", ""},
	{"POST", pathApps, `{"app":{"name":"bot","client_id":"Iv1.abc"},"private_key_value":"k"}`},
	{"PUT", pathApps + "/bot", `{"app":{"name":"bot","client_id":"Iv1.abc"},"private_key_value":"k"}`},
	{"DELETE", pathApps + "/bot", ""},
	{"PUT", pathValues + "x", `{"value":"v"}`},
	{"DELETE", pathValues + "x", ""},
	{"POST", "/api/store/init", `{"passphrase":"correct horse battery"}`},
}

func TestTokenRoutesRefuseBearerAndCrossOrigin(t *testing.T) {
	a := newTokenRoutes(t)
	for _, rt := range everyTokenRoute {
		h := a.handler(rt.path)
		cases := map[string]*http.Request{
			"bearer":        storeReq(rt.method, rt.path, rt.body, credOrigin, credBearer),
			"no credential": storeReq(rt.method, rt.path, rt.body, credOrigin, credNone),
		}
		if rt.method != "GET" {
			cases["cross origin"] = storeReq(rt.method, rt.path, rt.body, "http://evil.example", credCookie)
			cases["no origin"] = storeReq(rt.method, rt.path, rt.body, "", credCookie)
		}
		for what, r := range cases {
			if w := do(h, r); w.Code != 403 {
				t.Errorf("%s %s with %s: %d", rt.method, rt.path, what, w.Code)
			}
		}
	}
	if len(a.fx.vals.m) != 0 || a.fx.fileBytes(t) != "" || len(a.store.inits) != 0 || len(a.fx.rec.events) != 0 {
		t.Fatal("a refused request changed something")
	}
}

func TestTokenRoutesMethodsAndPaths(t *testing.T) {
	a := newTokenRoutes(t)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", pathTokens, 405},
		{"PUT", pathTokens, 405},
		{"GET", pathTokens + "/a", 405},
		{"POST", pathTokens + "/a", 405},
		{"PUT", pathTokens + "/from-template", 405},
		{"POST", pathTokens + "/a/b", 404},
		{"PUT", pathTokens + "/Bad_Name", 400},
		{"GET", pathApps, 405},
		{"POST", pathApps + "/bot", 405},
		{"GET", pathValues + "x", 405},
		{"PUT", pathValues, 404},
		{"GET", "/api/store/init", 405},
	} {
		if w := a.do(tc.method, tc.path, `{}`); w.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func okProbe(on bool, err error) func() (bool, error) { return func() (bool, error) { return on, err } }

func initStoreRoutes(t *testing.T, st secretstore.Status) *tokenRoutes {
	a := newTokenRoutes(t)
	a.store.st = st
	return a
}

func TestStoreInitSeccompOff409(t *testing.T) {
	a := initStoreRoutes(t, secretstore.Status{Platform: "linux", PassphraseMode: true})
	a.srv.SeccompProbe = okProbe(false, nil)
	code, m, _ := a.call("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`)
	if code != 409 || m["code"] != "seccomp_off" {
		t.Fatalf("%d %v", code, m)
	}
	if !strings.Contains(m["error"].(string), "Docker seccomp is off on this host. Turn it on before creating the store, or every boxed session will be refused.") {
		t.Fatalf("message: %v", m["error"])
	}
	if len(a.store.inits) != 0 {
		t.Fatal("Init ran")
	}
}

func TestStoreInitSeccompUnknown409(t *testing.T) {
	a := initStoreRoutes(t, secretstore.Status{Platform: "linux", PassphraseMode: true})
	a.srv.SeccompProbe = okProbe(false, errors.New("docker: cannot connect to /var/run/docker.sock"))
	code, m, raw := a.call("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`)
	if code != 409 || m["code"] != "seccomp_unknown" || strings.Contains(raw, "docker.sock") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(a.store.inits) != 0 {
		t.Fatal("Init ran")
	}
}

func TestStoreInitLinuxUnlocks(t *testing.T) {
	a := initStoreRoutes(t, secretstore.Status{Platform: "linux", PassphraseMode: true})
	a.srv.SeccompProbe = okProbe(true, nil)
	code, m, raw := a.call("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`)
	if code != 201 || m["unlocked"] != true || strings.Contains(raw, "correct horse") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(a.store.inits) != 1 || a.store.inits[0] != "correct horse battery/web" {
		t.Fatalf("inits: %v", a.store.inits)
	}
	if len(a.store.unlocks) != 1 || a.store.unlocks[0] != "web/"+(48*time.Hour).String() {
		t.Fatalf("unlocks: %v", a.store.unlocks)
	}
}

func TestStoreInitNoProbeAndMac(t *testing.T) {
	a := initStoreRoutes(t, secretstore.Status{Platform: "darwin"})
	// SeccompProbe nil: no docker gate on this host.
	code, m, _ := a.call("POST", "/api/store/init", ``)
	if code != 201 || m["unlocked"] != true {
		t.Fatalf("%d %v", code, m)
	}
	if len(a.store.inits) != 1 || len(a.store.unlocks) != 0 {
		t.Fatalf("mac: inits %v unlocks %v", a.store.inits, a.store.unlocks)
	}
}

func TestStoreInitShortPassphrase400(t *testing.T) {
	a := initStoreRoutes(t, secretstore.Status{Platform: "linux", PassphraseMode: true})
	for _, body := range []string{`{}`, `{"passphrase":"short"}`, `{"passphrase":"x","extra":1}`} {
		code, _, raw := a.call("POST", "/api/store/init", body)
		if code != 400 {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	if len(a.store.inits) != 0 {
		t.Fatal("Init ran")
	}
}

func TestStoreInitExists409(t *testing.T) {
	// Already initialized: the status check answers.
	a := newTokenRoutes(t)
	code, m, _ := a.call("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`)
	if code != 409 || m["code"] != "exists" || len(a.store.inits) != 0 {
		t.Fatalf("%d %v", code, m)
	}
	// Lost the race: Init itself says it exists.
	b := initStoreRoutes(t, secretstore.Status{Platform: "linux", PassphraseMode: true})
	b.store.initErr = secretstore.ErrExists
	code, m, _ = b.call("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`)
	if code != 409 || m["code"] != "exists" {
		t.Fatalf("%d %v", code, m)
	}
}

func TestStoreInitOtherFailureHidesPath(t *testing.T) {
	a := initStoreRoutes(t, secretstore.Status{Platform: "linux", PassphraseMode: true})
	a.store.initErr = errors.New("create store dir: /home/x/.agent-box/store: permission denied")
	code, _, raw := a.call("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`)
	if code != 500 || strings.Contains(raw, "/home/x") || strings.Contains(raw, "correct horse") {
		t.Fatalf("%d %s", code, raw)
	}
}

// TestTokenRoutesAreRegistered sends every route through the real handler with a
// bearer token: each must reach its handler and be refused there (403), not fall
// through to a 404, and nothing is changed.
func TestTokenRoutesAreRegistered(t *testing.T) {
	s, plain := routedServer(t)
	a := newTokenRoutes(t)
	s.Creds, s.CredAudit, s.Store = a.srv.Creds, a.srv.CredAudit, a.store
	h := s.Handler()
	for _, rt := range everyTokenRoute {
		r := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		r.Host = "example.com"
		r.Header.Set("Authorization", "Bearer "+plain)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", rt.method, rt.path, w.Code)
		}
	}
}

// A token's value reaches the box, so the web must not point one at a host file
// (~/.aws/credentials, an ssh key, the data dir): only the store, on the web.
func TestTokenRoutesRefuseFileValueRef(t *testing.T) {
	a := newTokenRoutes(t)
	e := tok("f", "F_TOKEN")
	e.Value = config.KeyRef{Type: "file", Path: "/home/u/.aws/credentials"}
	code, m, raw := a.call("POST", pathTokens, jsonBody(t, map[string]any{"entry": e}))
	if code != 400 || !strings.Contains(raw, "encrypted store") || strings.Contains(raw, ".aws") {
		t.Fatalf("POST: %d %v %s", code, m, raw)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatalf("an entry was written: %s", a.fx.fileBytes(t))
	}
	// PUT over a store-backed entry, switching it to a file reference.
	if code, _, raw := a.call("POST", pathTokens, jsonBody(t, map[string]any{"entry": tok("g", "G_TOKEN"), "value": "v1"})); code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	before := a.fx.fileBytes(t)
	e = tok("g", "G_TOKEN")
	e.Value = config.KeyRef{Type: "file", Path: "/home/u/.ssh/id_ed25519"}
	code, _, raw = a.call("PUT", pathTokens+"/g", jsonBody(t, map[string]any{"entry": e}))
	if code != 400 || !strings.Contains(raw, "encrypted store") {
		t.Fatalf("PUT: %d %s", code, raw)
	}
	if a.fx.fileBytes(t) != before {
		t.Fatalf("a refused PUT changed the file")
	}
}

func TestDefaultAppKeyID(t *testing.T) {
	long := strings.Repeat("a", 64)
	for name, want := range map[string]string{
		"bot":    "gh-bot-key",
		"my.app": "gh-my-app-key",
		"a_b-c":  "gh-a_b-c-key",
		long:     "gh-" + strings.Repeat("a", 57) + "-key",
	} {
		got := defaultAppKeyID(name)
		if got != want || !secretstore.ValidID(got) {
			t.Errorf("%q -> %q, want %q (valid=%v)", name, got, want, secretstore.ValidID(got))
		}
	}
	if got := defaultAppKeyID(strings.Repeat(".", 64)); !secretstore.ValidID(got) || len(got) != 64 {
		t.Errorf("dots -> %q", got)
	}
}

// An App whose name has a "." or is long can still take a pasted key; a second
// name that maps to the same id is refused rather than overwriting the first.
func TestGitHubAppPasteKeyForAwkwardNames(t *testing.T) {
	a := newTokenRoutes(t)
	const pem = "-----BEGIN PRIVATE KEY-----\nK\n-----END PRIVATE KEY-----\n"
	long := strings.Repeat("a", 60)
	for _, n := range []string{"my.app", long} {
		app := config.GitHubApp{Name: n, ClientID: "Iv1.abc"}
		if code, _, raw := a.call("POST", pathApps, jsonBody(t, map[string]any{"app": app, "private_key_value": pem})); code != 201 {
			t.Fatalf("%s: %d %s", n, code, raw)
		}
	}
	if !a.fx.vals.has("gh-my-app-key") || !a.fx.vals.has(defaultAppKeyID(long)) {
		t.Fatalf("store: %v", a.fx.vals.m)
	}
	clash := config.GitHubApp{Name: "my-app", ClientID: "Iv1.abc"}
	code, m, raw := a.call("POST", pathApps, jsonBody(t, map[string]any{"app": clash, "private_key_value": pem}))
	if code != 409 || m["code"] != "in_use" {
		t.Fatalf("clash: %d %s", code, raw)
	}
	for _, e := range a.fx.file(t).GitHubApps {
		if e.Name == "my-app" {
			t.Fatal("the refused App was written")
		}
	}
}

// A token created without a value that references another token's store id is
// 409 in_use naming the owner, and no entry is written.
func TestTokenForeignStoreRef409(t *testing.T) {
	a := newTokenRoutes(t)
	a.call("POST", pathTokens+"/from-template", fromTpl("github-pat", "gh", nil, "v"))
	body := jsonBody(t, map[string]any{"entry": map[string]any{"name": "thief", "env": []string{"THIEF"},
		"egress": []map[string]any{{"host": "evil.example"}}, "value": map[string]any{"type": "store", "id": "gh"}}})
	code, m, raw := a.call("POST", pathTokens, body)
	if code != 409 || m["code"] != "in_use" {
		t.Fatalf("%d %s", code, raw)
	}
	if by, _ := m["used_by"].([]any); len(by) != 1 || by[0] != "static_tokens/gh" {
		t.Fatalf("used_by: %v", m)
	}
	if got := a.fx.file(t).StaticTokens; len(got) != 1 {
		t.Fatalf("file: %+v", got)
	}
}

// A credentials.json with content this build cannot read is read-only for the
// whole run: every write is refused with the banner text before any store
// value or audit row, the file is untouched, and fixing it on disk changes
// nothing until a restart.
func TestCredentialsReadOnlyWhenFileNotUnderstood(t *testing.T) {
	a := newTokenRoutes(t)
	body := `{"static_tokens":[],"gcp_connections":[{}]}`
	if err := os.WriteFile(credfile.Path(a.fx.dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, bad, err := credfile.Load(a.fx.dir)
	if bad || err != nil || loaded.CanSave() == nil {
		t.Fatalf("setup: bad %v err %v canSave %v", bad, err, loaded.CanSave())
	}
	a.fx.cm = NewCredentialManager(config.Config{}, loaded, a.fx.dir, a.fx.pm, a.fx.vals, a.fx.rec, a.fx.chk,
		func(config.Config) {}, nil)
	a.srv.Creds = a.fx.cm

	err = a.fx.cm.PutTokens([]config.StaticToken{withStoreRef(tok("t1", "T1"), "t1")}, map[string][]byte{"t1": []byte("v")}, true)
	if !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("PutTokens = %v, want ErrCredReadOnly", err)
	}
	_, err = a.fx.cm.PutAWS(context.Background(), awsConn("a1", "p", arnRead), true, false)
	if !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("PutAWS = %v, want ErrCredReadOnly", err)
	}
	if a.fx.chk.n() != 0 {
		t.Errorf("a read-only manager ran %d base-role checks", a.fx.chk.n())
	}
	if got := a.fx.fileBytes(t); got != body {
		t.Errorf("file = %q, want it untouched", got)
	}
	if len(a.fx.vals.m) != 0 || len(a.fx.rec.events) != 0 {
		t.Errorf("read-only refusal left values %v / audit rows %v", a.fx.vals.m, a.fx.rec.events)
	}
	var vb struct {
		FileBad bool `json:"file_bad"`
	}
	w := do(a.srv.handleCredentials, storeReq("GET", pathCreds, "", "", credCookie))
	if err := json.Unmarshal(w.Body.Bytes(), &vb); err != nil || !vb.FileBad {
		t.Errorf("file_bad = %v (%v), want true", vb.FileBad, err)
	}

	// The file is fixed on disk; this run still refuses (no mid-run re-check).
	if err := os.WriteFile(credfile.Path(a.fx.dir), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = a.fx.cm.PutTokens([]config.StaticToken{withStoreRef(tok("t2", "T2"), "t2")}, map[string][]byte{"t2": []byte("v")}, true)
	if !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("after the file was fixed: PutTokens = %v, want ErrCredReadOnly", err)
	}

	code, m, raw := a.call("POST", pathTokens+"/from-template", fromTpl("sentry", "sen", map[string]string{"region": "eu"}, "sntrys_value"))
	const banner = "credentials.json has content this CuliBox cannot read, so this screen will not save over it. Fix the file, or run the CuliBox that wrote it."
	if code != 409 || m["code"] != "file_read_only" || m["error"] != banner {
		t.Fatalf("route = %d %s, want 409 file_read_only with the banner", code, raw)
	}
	if ErrCredReadOnly.Error() != banner {
		t.Errorf("ErrCredReadOnly = %q", ErrCredReadOnly)
	}
}

// A hand edit made while CuliBox runs can leave credentials.json with content
// this build cannot read. The next write is refused before it records an audit
// row or overwrites a store value, and the refusal sticks even if the file is
// put right again (that takes effect on a restart).
func TestCredentialsFileBecomesNotUnderstoodMidRun(t *testing.T) {
	fx := newCredFixture(t, config.Config{}, nil)
	if err := fx.cm.PutTokens([]config.StaticToken{tok("t1", "T1")}, map[string][]byte{"t1": []byte("old")}, true); err != nil {
		t.Fatal(err)
	}
	good := fx.fileBytes(t)
	rowsBefore := len(fx.rec.events)

	bad := `{"static_tokens":[],"gcp_connections":[{}]}`
	if err := os.WriteFile(credfile.Path(fx.dir), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	err := fx.cm.PutTokens([]config.StaticToken{tok("t1", "T1")}, map[string][]byte{"t1": []byte("new")}, false)
	if !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("replace = %v, want ErrCredReadOnly", err)
	}
	if got := string(fx.vals.m["t1"]); got != "old" {
		t.Errorf("store value = %q, want it unchanged", got)
	}
	if len(fx.rec.events) != rowsBefore {
		t.Errorf("audit rows %d -> %d, want none recorded", rowsBefore, len(fx.rec.events))
	}
	if got := fx.fileBytes(t); got != bad {
		t.Errorf("file = %q, want it untouched", got)
	}
	if _, err := fx.cm.PutAWS(context.Background(), awsConn("a1", "p", arnRead), true, false); !errors.Is(err, ErrCredReadOnly) || fx.chk.n() != 0 {
		t.Errorf("PutAWS = %v after %d checks, want a refusal before any check", err, fx.chk.n())
	}

	if err := os.WriteFile(credfile.Path(fx.dir), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	err = fx.cm.PutTokens([]config.StaticToken{tok("t1", "T1")}, map[string][]byte{"t1": []byte("newer")}, false)
	if !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("after the file was restored: replace = %v, want ErrCredReadOnly", err)
	}
	if got := string(fx.vals.m["t1"]); got != "old" {
		t.Errorf("store value = %q, want it unchanged", got)
	}
}
