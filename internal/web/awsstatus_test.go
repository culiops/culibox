package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/ssologin"
)

func awsAdapter(info map[string]session.Info, rows []broker.Row, rowsErr error, gotLimit *int) AWSStatusAdapter {
	return AWSStatusAdapter{
		Get: func(id string) (session.Info, bool) { i, ok := info[id]; return i, ok },
		Stats: func(id string) (broker.Stats, bool) {
			if id == "claude-3" {
				return broker.Stats{Mints: 2, CacheHits: 5, LastMint: time.Unix(1700000000, 0).UTC(), LastError: "boom"}, true
			}
			return broker.Stats{}, false
		},
		Recent: func(id string, limit int) ([]broker.Row, error) {
			if gotLimit != nil {
				*gotLimit = limit
			}
			return rows, rowsErr
		},
	}
}

func TestAWSStatusAdapter(t *testing.T) {
	info := map[string]session.Info{
		"claude-3": {ID: "claude-3", AWS: &session.AWSInfo{Label: "sandbox", AccountID: "123456789012",
			RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent", Prod: true}},
		"claude-4": {ID: "claude-4"},
	}
	var limit int
	rows := []broker.Row{{SessionID: "claude-3", Outcome: broker.OutcomeMinted, Time: time.Unix(1700000000, 0)}}
	ad := awsAdapter(info, rows, nil, &limit)

	st, ok := ad.AWSStatus("claude-3")
	if !ok {
		t.Fatal("bound session reported no status")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Label     string `json:"label"`
		AccountID string `json:"account_id"`
		RoleARN   string `json:"role_arn"`
		Prod      bool   `json:"prod"`
		Stats     struct {
			Mints     int       `json:"mints"`
			CacheHits int       `json:"cache_hits"`
			LastMint  time.Time `json:"last_mint"`
			LastError string    `json:"last_error"`
		} `json:"stats"`
		Recent []broker.Row `json:"recent"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Label != "sandbox" || got.AccountID != "123456789012" || !got.Prod || !strings.HasSuffix(got.RoleARN, "agentbox-agent") {
		t.Fatalf("binding fields %+v", got)
	}
	if got.Stats.Mints != 2 || got.Stats.CacheHits != 5 || got.Stats.LastError != "boom" || got.Stats.LastMint.IsZero() {
		t.Fatalf("stats %+v", got.Stats)
	}
	if len(got.Recent) != 1 || got.Recent[0].Outcome != broker.OutcomeMinted {
		t.Fatalf("recent %+v", got.Recent)
	}
	if limit != 20 {
		t.Fatalf("audit rows read with limit %d, want the fixed 20 (the store treats <=0 as unlimited)", limit)
	}
	for _, banned := range []string{"nonce", "token", "secret", "AccessKey"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(banned)) {
			t.Errorf("status carries %q: %s", banned, raw)
		}
	}
}

func TestAWSStatusAdapterMisses(t *testing.T) {
	info := map[string]session.Info{"claude-4": {ID: "claude-4"}}
	ad := awsAdapter(info, nil, nil, nil)
	if _, ok := ad.AWSStatus("claude-4"); ok {
		t.Error("a session without a binding has an AWS status")
	}
	if _, ok := ad.AWSStatus("gone-1"); ok {
		t.Error("an unknown session has an AWS status")
	}
}

func TestAWSStatusAdapterNoStatsNoRows(t *testing.T) {
	info := map[string]session.Info{"claude-5": {ID: "claude-5", AWS: &session.AWSInfo{Label: "x"}}}
	ad := awsAdapter(info, nil, errors.New("db closed"), nil)
	st, ok := ad.AWSStatus("claude-5")
	if !ok {
		t.Fatal("bound session reported no status")
	}
	raw, _ := json.Marshal(st)
	// A fresh session has never minted, and a failing audit store must not take the status down.
	if !strings.Contains(string(raw), `"recent":[]`) || !strings.Contains(string(raw), `"mints":0`) {
		t.Fatalf("status %s", raw)
	}
}

func TestAWSStatusIncludesEgress(t *testing.T) {
	info := map[string]session.Info{"claude-3": {ID: "claude-3", AWS: &session.AWSInfo{Label: "x"}}}
	decode := func(ad AWSStatusAdapter) map[string]json.RawMessage {
		t.Helper()
		st, ok := ad.AWSStatus("claude-3")
		if !ok {
			t.Fatal("bound session reported no status")
		}
		raw, _ := json.Marshal(st)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	ad := awsAdapter(info, nil, nil, nil)
	if _, has := decode(ad)["egress"]; has {
		t.Error("nil Egress must leave the egress key out")
	}

	ad.Egress = func(id string) (egress.Stats, bool) { return egress.Stats{Allowed: 2, Denied: 1}, true }
	if got := string(decode(ad)["egress"]); got != `{"allowed":2,"denied":1,"mode":"allowlist"}` {
		t.Errorf("egress = %s", got)
	}

	// No tunnel or denial yet (Stats ok=false): zeros, never a missing row.
	ad.Egress = func(id string) (egress.Stats, bool) { return egress.Stats{}, false }
	if got := string(decode(ad)["egress"]); got != `{"allowed":0,"denied":0,"mode":"allowlist"}` {
		t.Errorf("egress before any traffic = %s", got)
	}
}

func TestAWSStatusCarriesElevateElevationAndSSO(t *testing.T) {
	info := map[string]session.Info{
		"claude-3": {ID: "claude-3", AWS: &session.AWSInfo{Label: "sandbox", RoleARN: "arn:aws:iam::1:role/r",
			Elevate: &session.ElevateInfo{RoleARN: "arn:aws:iam::1:role/w", MaxMinutes: 60, AllowIAM: true}}},
		"claude-4": {ID: "claude-4", AWS: &session.AWSInfo{Label: "plain"}},
	}
	grantCalls := 0
	ad := awsAdapter(info, nil, nil, nil)
	ad.Grant = func(id string) GrantContext {
		grantCalls++
		return GrantContext{Blocked: "another box uses this folder"}
	}
	ad.Elevation = func(id string) (broker.ElevationView, bool) {
		return broker.ElevationView{Active: true, Until: time.Unix(1_800_003_600, 0).UTC()}, true
	}
	ad.SSO = func(id string) (SSOStatus, bool) {
		return SSOStatus{Expired: true, Source: "example-sso", Command: "aws sso login --profile p",
			Login: &ssologin.State{State: "waiting", Code: "ABCD-EFGH"}}, true
	}
	decode := func(id string) map[string]any {
		t.Helper()
		st, ok := ad.AWSStatusFor(id, true)
		if !ok {
			t.Fatal("bound session reported no status")
		}
		raw, _ := json.Marshal(st)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := decode("claude-3")
	el, _ := m["elevate"].(map[string]any)
	if el["role_arn"] != "arn:aws:iam::1:role/w" || el["max_minutes"] != float64(60) || el["allow_iam"] != true ||
		el["blocked"] != "another box uses this folder" {
		t.Fatalf("elevate %v", el)
	}
	if hs, ok := el["host_sessions"].([]any); !ok || len(hs) != 0 {
		t.Fatalf("an empty host_sessions encodes as [], got %v", el["host_sessions"])
	}
	if e, _ := m["elevation"].(map[string]any); e["until"] == nil || e["active"] != true {
		t.Fatalf("elevation %v", m["elevation"])
	}
	sso, _ := m["sso"].(map[string]any)
	login, _ := sso["login"].(map[string]any)
	if sso["command"] != "aws sso login --profile p" || login["code"] != "ABCD-EFGH" || sso["expired"] != true {
		t.Fatalf("sso %v", sso)
	}

	if grantCalls != 1 {
		t.Fatalf("Grant called %d times for one elevatable session", grantCalls)
	}
	m = decode("claude-4")
	if _, has := m["elevate"]; has || grantCalls != 1 {
		t.Fatalf("an unelevatable session has no elevate key and costs no Grant call: %v calls=%d", m["elevate"], grantCalls)
	}

	// Host sessions listed by the service reach the wire as id and name.
	ad.Grant = func(string) GrantContext {
		return GrantContext{HostSessions: []HostSession{{ID: "zsh-1", Name: "work"}}}
	}
	raw, _ := json.Marshal(func() any { st, _ := ad.AWSStatus("claude-3"); return st }())
	if !strings.Contains(string(raw), `"host_sessions":[{"id":"zsh-1","name":"work"}]`) || strings.Contains(string(raw), `"blocked"`) {
		t.Fatalf("%s", raw)
	}

	// Without the funcs wired the reply carries neither key (older wiring).
	plain := awsAdapter(info, nil, nil, nil)
	st, _ := plain.AWSStatus("claude-3")
	raw, _ = json.Marshal(st)
	if strings.Contains(string(raw), `"elevation"`) || strings.Contains(string(raw), `"sso"`) {
		t.Fatalf("%s", raw)
	}
}

// The device code and URL belong to the browser session that started the flow:
// a bearer token reading the same status gets the state, not the code.
func TestAWSStatusHidesTheDeviceCodeFromNonCookieCallers(t *testing.T) {
	info := map[string]session.Info{"claude-3": {ID: "claude-3", AWS: &session.AWSInfo{Label: "sandbox"}}}
	ad := awsAdapter(info, nil, nil, nil)
	ad.SSO = func(id string) (SSOStatus, bool) {
		return SSOStatus{Expired: true, Command: "aws sso login --profile p", Login: &ssologin.State{
			State: "waiting", URI: "https://acme.awsapps.com/start/#/device", URITrusted: true, Code: "ABCD-EFGH",
			Expires: time.Unix(1_800_000_600, 0).UTC()}}, true
	}
	get := func(kind credKind) string {
		a := &API{Sessions: &mockSvc{}, AWS: ad}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/sessions/claude-3/aws", nil)
		a.handleSession(w, r.WithContext(context.WithValue(r.Context(), credKindKey, kind)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	for _, kind := range []credKind{credBearer, credNone} {
		b := get(kind)
		if strings.Contains(b, "ABCD-EFGH") || strings.Contains(b, "awsapps.com") || !strings.Contains(b, `"state":"waiting"`) ||
			!strings.Contains(b, `"expires"`) || !strings.Contains(b, `"command":"aws sso login --profile p"`) {
			t.Errorf("kind %d: %s", kind, b)
		}
	}
	if b := get(credCookie); !strings.Contains(b, "ABCD-EFGH") || !strings.Contains(b, "acme.awsapps.com") {
		t.Errorf("the cookie session gets the code: %s", b)
	}
	// The bare AWSStatus (no credential known) fails closed.
	st, _ := ad.AWSStatus("claude-3")
	if raw, _ := json.Marshal(st); strings.Contains(string(raw), "ABCD-EFGH") {
		t.Errorf("AWSStatus without a credential kind leaks the code: %s", raw)
	}
}
