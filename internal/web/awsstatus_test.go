package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/ssologin"
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
	info := map[string]session.Info{"claude-3": {ID: "claude-3", Egress: "allowlist", AWS: &session.AWSInfo{Label: "x"}}}
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

// The egress object's mode is Info.Egress, not a constant; a session without
// egress control (Info.Egress "") carries no egress object at all.
func TestAWSStatusEgressModeFromInfo(t *testing.T) {
	decode := func(mode string) map[string]json.RawMessage {
		t.Helper()
		info := map[string]session.Info{"claude-3": {ID: "claude-3", Egress: mode, AWS: &session.AWSInfo{Label: "x"}}}
		ad := awsAdapter(info, nil, nil, nil)
		ad.Egress = func(id string) (egress.Stats, bool) { return egress.Stats{Allowed: 1, Denied: 4}, true }
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
	if got := string(decode("allowlist")["egress"]); got != `{"allowed":1,"denied":4,"mode":"allowlist"}` {
		t.Errorf("allowlist: egress = %s", got)
	}
	if got := string(decode("open")["egress"]); got != `{"allowed":1,"denied":4,"mode":"open"}` {
		t.Errorf("open: egress = %s", got)
	}
	if _, has := decode("")["egress"]; has {
		t.Error(`Info.Egress "" must leave the egress key out`)
	}
}

func TestEgressStatusRoute(t *testing.T) {
	info := map[string]session.Info{
		"box-1":  {ID: "box-1", Egress: "open"},
		"host-1": {ID: "host-1"},
	}
	svc := EgressStatusAdapter{
		Get: func(id string) (session.Info, bool) { i, ok := info[id]; return i, ok },
		Stats: func(id string) (egress.Stats, bool) {
			if id == "box-1" {
				return egress.Stats{Allowed: 3, Denied: 2}, true
			}
			return egress.Stats{}, false
		},
	}
	do := func(a *API, method, id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleSession(w, httptest.NewRequest(method, "/api/sessions/"+id+"/egress", nil))
		return w
	}
	if w := do(&API{Sessions: &mockSvc{}}, "GET", "box-1"); w.Code != 404 || !strings.Contains(w.Body.String(), "egress control not enabled") {
		t.Errorf("nil service: %d %s", w.Code, w.Body)
	}
	a := &API{Sessions: &mockSvc{}, Egress: svc}
	if w := do(a, "POST", "box-1"); w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}
	for _, id := range []string{"nope", "host-1"} {
		if w := do(a, "GET", id); w.Code != 204 {
			t.Errorf("%s: %d %s", id, w.Code, w.Body)
		}
	}
	w := do(a, "GET", "box-1")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"egress":{"allowed":3,"denied":2,"mode":"open"}}` {
		t.Errorf("box-1: %d %s", w.Code, w.Body)
	}
	// A box with no tunnel yet (Stats ok=false) still has a line: zeros.
	info["box-2"] = session.Info{ID: "box-2", Egress: "open"}
	w = do(a, "GET", "box-2")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"egress":{"allowed":0,"denied":0,"mode":"open"}}` {
		t.Errorf("box-2: %d %s", w.Code, w.Body)
	}
}

func TestAWSStatusApprovals(t *testing.T) {
	au, err := broker.OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { au.Close() })
	created := time.Unix(1_800_000_000, 0)
	rec := func(sid string, at time.Time, a broker.Action, reason, entry string) {
		t.Helper()
		r := broker.ApprovalRow{SessionID: sid, Time: at, Action: a, Minutes: 30, Reason: reason, Entry: entry}
		if a == broker.ActionGranted {
			r.Until = at.Add(30 * time.Minute)
		}
		if err := au.RecordApproval(r); err != nil {
			t.Fatal(err)
		}
	}
	// Rows from before the session existed (an earlier session's id, reused),
	// and another session's row, must never show.
	rec("claude-3", created.Add(-time.Hour), broker.ActionGranted, "old session", "prod")
	rec("claude-9", created.Add(time.Minute), broker.ActionGranted, "someone else", "prod")
	// 25 newer rows: only the newest 20 come back.
	for i := 0; i < 25; i++ {
		rec("claude-3", created.Add(time.Duration(i+1)*time.Minute), broker.ActionGranted, fmt.Sprintf("r%02d", i), "prod")
	}
	// Operator text is cleaned like the activity timeline's.
	rec("claude-3", created.Add(time.Hour), broker.ActionExtended, "fix\nit\x1b[31m "+strings.Repeat("x", 300), "")

	info := map[string]session.Info{
		"claude-3": {ID: "claude-3", Created: created, AWS: &session.AWSInfo{Label: "sandbox"}},
	}
	ad := awsAdapter(info, nil, nil, nil)
	ad.Approvals = func(id string, sinceTS int64, limit int) ([]broker.ApprovalRow, error) {
		return au.ActivityApprovals(id, sinceTS, 0, limit)
	}
	decode := func(cookie bool) map[string]any {
		t.Helper()
		st, ok := ad.AWSStatusFor("claude-3", cookie)
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

	m := decode(true)
	appr, _ := m["approvals"].([]any)
	if len(appr) != 20 {
		t.Fatalf("approvals = %d rows, want the newest 20", len(appr))
	}
	first := appr[0].(map[string]any)
	if first["action"] != "extended" || first["ts"] != float64(created.Add(time.Hour).UnixMilli()) || first["minutes"] != float64(30) {
		t.Fatalf("newest row = %v", first)
	}
	reason, _ := first["reason"].(string)
	if strings.ContainsAny(reason, "\n\x1b") || len([]rune(reason)) > 200 || !strings.HasPrefix(reason, "fix it") {
		t.Fatalf("reason not cleaned: %q", reason)
	}
	second := appr[1].(map[string]any)
	if second["action"] != "granted" || second["name"] != "prod" || second["reason"] != "r24" ||
		second["until"] != float64(created.Add(25*time.Minute+30*time.Minute).UnixMilli()) {
		t.Fatalf("granted row = %v", second)
	}
	for _, a := range appr {
		row := a.(map[string]any)
		if row["ts"].(float64) < float64(created.UnixMilli()) {
			t.Fatalf("a row older than the session: %v", row)
		}
		if r, _ := row["reason"].(string); r == "old session" || r == "someone else" {
			t.Fatalf("another session's row: %v", row)
		}
	}

	// A token (PAT / bearer) caller never gets operator text.
	if _, has := decode(false)["approvals"]; has {
		t.Fatal("approvals present for a non-cookie caller")
	}

	// No approvals yet: [] on the wire, never null; unwired: absent.
	ad.Approvals = func(string, int64, int) ([]broker.ApprovalRow, error) { return nil, nil }
	if a, ok := decode(true)["approvals"].([]any); !ok || len(a) != 0 {
		t.Fatalf("empty approvals = %v", decode(true)["approvals"])
	}
	ad.Approvals = nil
	if _, has := decode(true)["approvals"]; has {
		t.Fatal("approvals present without a reader")
	}
	// A failed read leaves the status without them (and logs), not an error.
	ad.Approvals = func(string, int64, int) ([]broker.ApprovalRow, error) { return nil, errors.New("disk") }
	if _, has := decode(true)["approvals"]; has {
		t.Fatal("approvals present after a failed read")
	}
}
