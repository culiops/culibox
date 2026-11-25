package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/session"
)

// activitySvc is a SessionService with one known session, its Info and a canned
// timeline page; it records what the handler asked the timeline for.
type activitySvc struct {
	*mockSvc
	known     bool
	info      session.Info
	tl        session.Timeline
	gotBefore int64
	gotLimit  int
}

func (s *activitySvc) Get(id string) (session.Info, bool) { return s.info, s.known && id == s.info.ID }
func (s *activitySvc) TimelineFor(id string, before int64, limit int) (session.Timeline, bool) {
	s.gotBefore, s.gotLimit = before, limit
	if !s.known || id != s.info.ID {
		return session.Timeline{}, false
	}
	return s.tl, true
}

var activityCreated = time.Unix(1_700_000_000, 0)

func newActivitySvc() *activitySvc {
	return &activitySvc{
		mockSvc: &mockSvc{},
		known:   true,
		info:    session.Info{ID: "s1", Agent: "claude", Created: activityCreated},
		tl:      session.Timeline{Claude: true, Events: []session.TimelineEvent{}, Files: []session.FileChange{}},
	}
}

func activityGet(a *API, url string, kind credKind) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", url, nil)
	r = r.WithContext(context.WithValue(r.Context(), credKindKey, kind))
	rec := httptest.NewRecorder()
	a.handleSession(rec, r)
	return rec
}

func decodeActivity(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json: %v: %s", err, rec.Body)
	}
	return m
}

func activityEvents(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["events"].([]any)
	if !ok {
		t.Fatalf("events is not an array: %v", m["events"])
	}
	out := make([]map[string]any, len(raw))
	for i, e := range raw {
		out[i] = e.(map[string]any)
	}
	return out
}

func credRow(at time.Time, provider, entry string, o broker.Outcome) broker.Row {
	return broker.Row{Time: at, SessionID: "s1", Provider: provider, Entry: entry, Outcome: o}
}

// auditAPI is an API whose activity rows come from a real audit store holding
// exactly these rows for session s1, so the tests exercise the SQL paging.
func auditAPI(t *testing.T, svc SessionService, creds []broker.Row, appr []broker.ApprovalRow) *API {
	t.Helper()
	au, err := broker.OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { au.Close() })
	for _, r := range creds {
		r.SessionID = "s1"
		if err := au.Record(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range appr {
		r.SessionID = "s1"
		if err := au.RecordApproval(r); err != nil {
			t.Fatal(err)
		}
	}
	return &API{Sessions: svc, Activity: ActivityAuditAdapter{
		Credentials: au.ActivityRows, Approvals: au.ActivityApprovals, Minted: au.CountMinted,
	}}
}

func TestActivityCookieOnly(t *testing.T) {
	a := auditAPI(t, newActivitySvc(), nil, nil)
	if rec := activityGet(a, "/api/sessions/s1/activity", credBearer); rec.Code != 403 {
		t.Fatalf("bearer: got %d", rec.Code)
	}
	if rec := activityGet(a, "/api/sessions/s1/activity", credNone); rec.Code != 403 {
		t.Fatalf("no credential: got %d", rec.Code)
	}
	rec := activityGet(a, "/api/sessions/s1/activity", credCookie)
	m := decodeActivity(t, rec)
	if m["claude"] != true || len(activityEvents(t, m)) != 0 {
		t.Fatalf("body = %v", m)
	}
	if rec := activityGet(a, "/api/sessions/nope/activity", credCookie); rec.Code != 404 {
		t.Fatalf("unknown: got %d", rec.Code)
	}
	r := httptest.NewRequest("POST", "/api/sessions/s1/activity", nil)
	r = r.WithContext(context.WithValue(r.Context(), credKindKey, credCookie))
	rec = httptest.NewRecorder()
	a.handleSession(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: got %d", rec.Code)
	}
}

func TestActivityLimitClamp(t *testing.T) {
	svc := newActivitySvc()
	a := auditAPI(t, svc, nil, nil)
	for _, c := range []struct {
		q    string
		want int
	}{{"", 100}, {"?limit=0", 100}, {"?limit=-3", 100}, {"?limit=5000", 200}, {"?limit=7", 7}} {
		if rec := activityGet(a, "/api/sessions/s1/activity"+c.q, credCookie); rec.Code != 200 || svc.gotLimit != c.want {
			t.Fatalf("%q: code %d, limit %d, want %d", c.q, rec.Code, svc.gotLimit, c.want)
		}
	}
	for _, q := range []string{"?before=abc", "?before=-1", "?limit=abc"} {
		if rec := activityGet(a, "/api/sessions/s1/activity"+q, credCookie); rec.Code != 400 {
			t.Fatalf("%q: got %d", q, rec.Code)
		}
	}
	if rec := activityGet(a, "/api/sessions/s1/activity?before=1234", credCookie); rec.Code != 200 || svc.gotBefore != 1234 {
		t.Fatalf("before not passed through: %d %d", rec.Code, svc.gotBefore)
	}
}

func TestActivityDropsAuditRowsBeforeCreated(t *testing.T) {
	old := activityCreated.Add(-time.Hour)
	creds := []broker.Row{
		credRow(activityCreated.Add(10*time.Second), "aws", "new", broker.OutcomeMinted),
		credRow(old, "aws", "stale-session-row", broker.OutcomeMinted),
	}
	appr := []broker.ApprovalRow{
		{Time: activityCreated.Add(20 * time.Second), Action: broker.ActionGranted, Minutes: 15, Reason: "fix"},
		{Time: old, Action: broker.ActionGranted, Minutes: 60, Reason: "stale"},
	}
	m := decodeActivity(t, activityGet(auditAPI(t, newActivitySvc(), creds, appr), "/api/sessions/s1/activity", credCookie))
	evs := activityEvents(t, m)
	if len(evs) != 2 {
		t.Fatalf("want the 2 rows from after created, got %v", evs)
	}
	if strings.Contains(m2s(m), "stale") {
		t.Fatalf("a row from before created leaked: %v", m)
	}
	if got := m["summary"].(map[string]any)["credentials"]; got != float64(1) {
		t.Fatalf("summary.credentials = %v, want 1", got)
	}
	// A row written in the second the session began is its own.
	creds = []broker.Row{credRow(activityCreated.Add(300*time.Millisecond), "aws", "x", broker.OutcomeMinted)}
	svc := newActivitySvc()
	svc.info.Created = activityCreated.Add(900 * time.Millisecond)
	m = decodeActivity(t, activityGet(auditAPI(t, svc, creds, nil), "/api/sessions/s1/activity", credCookie))
	if len(activityEvents(t, m)) != 1 {
		t.Fatalf("same-second row dropped: %v", m)
	}
}

func m2s(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }

func tlEv(ms int64, detail string) session.TimelineEvent {
	return session.TimelineEvent{TS: ms, Kind: "tool", Tool: "Bash", Detail: detail, State: "ok"}
}

func TestActivityMergesNewestFirst(t *testing.T) {
	base := activityCreated.UnixMilli()
	svc := newActivitySvc()
	svc.tl.Events = []session.TimelineEvent{tlEv(base+50_000, "t50"), tlEv(base+30_000, "t30"), tlEv(base+5_000, "t5")}
	svc.tl.ToolCalls = 3
	creds := []broker.Row{
		credRow(activityCreated.Add(40*time.Second), "aws", "culipulse-prod", broker.OutcomeMinted),
		credRow(activityCreated.Add(10*time.Second), "github", "", broker.OutcomeDeniedScope),
	}
	appr := []broker.ApprovalRow{{Time: activityCreated.Add(20 * time.Second), Action: broker.ActionGranted, Minutes: 15, Reason: "hotfix"}}
	a := auditAPI(t, svc, creds, appr)
	m := decodeActivity(t, activityGet(a, "/api/sessions/s1/activity", credCookie))
	evs := activityEvents(t, m)
	wantDetail := []string{"t50", "aws culipulse-prod", "t30", `granted 15 min · "hotfix"`, "github", "t5"}
	if len(evs) != len(wantDetail) {
		t.Fatalf("events = %v", evs)
	}
	for i, w := range wantDetail {
		if evs[i]["detail"] != w {
			t.Fatalf("event %d detail = %v, want %q (all: %v)", i, evs[i]["detail"], w, evs)
		}
	}
	if evs[1]["kind"] != "credential" || evs[1]["state"] != "minted" || evs[3]["kind"] != "approval" || evs[3]["state"] != "granted" || evs[4]["state"] != "refused" {
		t.Fatalf("kinds/states wrong: %v", evs)
	}
	sum := m["summary"].(map[string]any)
	if sum["tool_calls"] != float64(3) || sum["credentials"] != float64(1) {
		t.Fatalf("summary = %v", sum)
	}

	// Paging: limit 2 → the 2 newest, more; before=<oldest ts> → the next 2, then the rest.
	var got []string
	before := ""
	for page := 0; page < 10; page++ {
		m = decodeActivity(t, activityGet(a, "/api/sessions/s1/activity?limit=2"+before, credCookie))
		pe := activityEvents(t, m)
		for _, e := range pe {
			got = append(got, e["detail"].(string))
		}
		if m["more"] != true {
			break
		}
		before = "&before=" + strconvI(int64(pe[len(pe)-1]["ts"].(float64)))
		// The canned transcript page must honour before like the real one.
		svc.tl.Events = filterBefore(svcAllEvents(base), int64(pe[len(pe)-1]["ts"].(float64)))
	}
	if strings.Join(got, "|") != strings.Join(wantDetail, "|") {
		t.Fatalf("paged = %v, want %v", got, wantDetail)
	}
}

func svcAllEvents(base int64) []session.TimelineEvent {
	return []session.TimelineEvent{tlEv(base+50_000, "t50"), tlEv(base+30_000, "t30"), tlEv(base+5_000, "t5")}
}

func filterBefore(evs []session.TimelineEvent, before int64) []session.TimelineEvent {
	var out []session.TimelineEvent
	for _, e := range evs {
		if e.TS < before {
			out = append(out, e)
		}
	}
	return out
}

func strconvI(n int64) string { b, _ := json.Marshal(n); return string(b) }

// A cut never splits a millisecond, and `more` reflects what was left behind.
func TestActivityCutFinishesTheMillisecond(t *testing.T) {
	base := activityCreated.UnixMilli() + 60_000 // a whole second, like audit rows
	svc := newActivitySvc()
	svc.tl.Events = []session.TimelineEvent{tlEv(base, "a"), tlEv(base, "b")}
	creds := []broker.Row{credRow(time.UnixMilli(base), "aws", "c", broker.OutcomeMinted), credRow(activityCreated.Add(5*time.Second), "aws", "old", broker.OutcomeMinted)}
	m := decodeActivity(t, activityGet(auditAPI(t, svc, creds, nil), "/api/sessions/s1/activity?limit=1", credCookie))
	evs := activityEvents(t, m)
	if len(evs) != 3 || m["more"] != true {
		t.Fatalf("want all 3 same-ms rows and more=true, got %v more=%v", evs, m["more"])
	}
	m = decodeActivity(t, activityGet(auditAPI(t, svc, creds[:1], nil), "/api/sessions/s1/activity?limit=3", credCookie))
	if m["more"] != false {
		t.Fatalf("nothing left, more = %v", m["more"])
	}
}

// A session with more than the 200-row cap of credential rows pages back to its
// oldest row: no loss, no duplicates. Hidden outcomes do not shrink a page, and
// summary.credentials is the true minted count since created.
func TestActivityPagesPastTheAuditCap(t *testing.T) {
	var creds []broker.Row
	const minted = 250
	for i := 0; i < minted; i++ {
		creds = append(creds, credRow(activityCreated.Add(time.Duration(1+i/2)*time.Second), "aws", "e"+strconvI(int64(i)), broker.OutcomeMinted))
	}
	for i := 0; i < 300; i++ { // probes among them, and rows from before created
		creds = append(creds, credRow(activityCreated.Add(time.Duration(1+i/3)*time.Second), "aws", "probe", broker.OutcomeProbeMinted))
	}
	for i := 0; i < 20; i++ {
		creds = append(creds, credRow(activityCreated.Add(-time.Duration(1+i)*time.Minute), "aws", "stale", broker.OutcomeMinted))
	}
	a := auditAPI(t, newActivitySvc(), creds, nil)
	seen := map[string]bool{}
	before := ""
	var total int
	for page := 0; page < 20; page++ {
		m := decodeActivity(t, activityGet(a, "/api/sessions/s1/activity?limit=200"+before, credCookie))
		if got := m["summary"].(map[string]any)["credentials"]; got != float64(minted) {
			t.Fatalf("page %d: summary.credentials = %v, want %d", page, got, minted)
		}
		evs := activityEvents(t, m)
		for _, e := range evs {
			d := e["detail"].(string)
			if d == "aws probe" || d == "aws stale" {
				t.Fatalf("hidden or pre-created row shown: %v", e)
			}
			if seen[d] {
				t.Fatalf("duplicate %s on page %d", d, page)
			}
			seen[d] = true
			total++
		}
		if m["more"] != true {
			break
		}
		if len(evs) == 0 {
			t.Fatalf("page %d: more with an empty page", page)
		}
		before = "&before=" + strconvI(int64(evs[len(evs)-1]["ts"].(float64)))
	}
	if total != minted {
		t.Fatalf("paged %d rows, want %d", total, minted)
	}
}

// A failed audit read (rows or count) makes the reply partial, never a quiet
// empty history, and does not put the error text in it.
func TestActivityAuditFailureIsPartial(t *testing.T) {
	boom := errors.New("database is locked: token ghp_SECRET")
	for name, ad := range map[string]ActivityAuditAdapter{
		"credentials": {Credentials: func(string, int64, int64, int) ([]broker.Row, error) { return nil, boom }},
		"approvals":   {Approvals: func(string, int64, int64, int) ([]broker.ApprovalRow, error) { return nil, boom }},
		"count":       {Minted: func(string, int64) (int, error) { return 0, boom }},
	} {
		svc := newActivitySvc()
		svc.tl.Events = []session.TimelineEvent{tlEv(activityCreated.UnixMilli()+1000, "ls")}
		rec := activityGet(&API{Sessions: svc, Activity: ad}, "/api/sessions/s1/activity", credCookie)
		m := decodeActivity(t, rec)
		if m["partial"] != true || len(activityEvents(t, m)) != 1 || strings.Contains(rec.Body.String(), "SECRET") {
			t.Fatalf("%s: partial = %v, body %s", name, m["partial"], rec.Body)
		}
	}
	// And a healthy audit does not claim it.
	m := decodeActivity(t, activityGet(auditAPI(t, newActivitySvc(), nil, nil), "/api/sessions/s1/activity", credCookie))
	if m["partial"] != false {
		t.Fatalf("healthy audit: partial = %v", m["partial"])
	}
}

// A transcript page that has more behind it must not be skipped over by audit
// rows older than its end: the merged page stops at the transcript's floor.
func TestActivityTranscriptFloorBoundsTheMerge(t *testing.T) {
	base := activityCreated.UnixMilli()
	svc := newActivitySvc()
	svc.tl.Events = []session.TimelineEvent{tlEv(base+50_000, "t50"), tlEv(base+40_000, "t40")}
	svc.tl.More = true
	creds := []broker.Row{credRow(activityCreated.Add(45*time.Second), "aws", "c45", broker.OutcomeMinted), credRow(activityCreated.Add(10*time.Second), "aws", "c10", broker.OutcomeMinted)}
	m := decodeActivity(t, activityGet(auditAPI(t, svc, creds, nil), "/api/sessions/s1/activity", credCookie))
	evs := activityEvents(t, m)
	if len(evs) != 3 || m["more"] != true || evs[2]["detail"] != "t40" {
		t.Fatalf("want t50,c45,t40 and more, got %v", evs)
	}
}

func TestActivityNonClaude(t *testing.T) {
	svc := newActivitySvc()
	svc.info.Agent = "sh"
	svc.tl = session.Timeline{Claude: false, Events: []session.TimelineEvent{}, Files: nil}
	creds := []broker.Row{credRow(activityCreated.Add(time.Minute), "static", "", broker.OutcomeFetched)}
	creds[0].Subject = "npm-token"
	m := decodeActivity(t, activityGet(auditAPI(t, svc, creds, nil), "/api/sessions/s1/activity", credCookie))
	evs := activityEvents(t, m)
	if m["claude"] != false || len(evs) != 1 || evs[0]["kind"] != "credential" || evs[0]["detail"] != "static npm-token" {
		t.Fatalf("body = %v", m)
	}
	if f, ok := m["files"].([]any); !ok || len(f) != 0 {
		t.Fatalf("files = %v, want []", m["files"])
	}
}

func TestActivityNoSecrets(t *testing.T) {
	const token = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	row := credRow(activityCreated.Add(time.Minute), "github", "pat-1", broker.OutcomeGitHubError)
	row.Error = "github said " + token
	row.RoleARN, row.SourceIdentity, row.ScopeHash, row.Profile = "arn:aws:iam::1:role/secret-role", "src-ident", "scope-hash", "prof"
	appr := broker.ApprovalRow{Time: activityCreated.Add(2 * time.Minute), Action: broker.ActionRefused, Error: "err " + token, Cred: "cred-" + token, RoleARN: "arn:aws:iam::1:role/secret-role"}
	rec := activityGet(auditAPI(t, newActivitySvc(), []broker.Row{row}, []broker.ApprovalRow{appr}), "/api/sessions/s1/activity", credCookie)
	body := rec.Body.String()
	for _, bad := range []string{token, "github said", "secret-role", "src-ident", "scope-hash", "cred-"} {
		if strings.Contains(body, bad) {
			t.Fatalf("response leaks %q: %s", bad, body)
		}
	}
	// The github_error row is a failure and the refused approval stays a refusal.
	if !strings.Contains(body, "github pat-1") || !strings.Contains(body, `"state":"failed"`) || !strings.Contains(body, `"state":"refused"`) {
		t.Fatalf("mapped fields missing: %s", body)
	}
}

// Probes and revokes are the daemon's own work; sign-in rows are not a window's
// life. A reason is masked and bounded.
func TestActivityRowSelectionAndReasonBound(t *testing.T) {
	creds := []broker.Row{
		credRow(activityCreated.Add(1*time.Second), "aws", "a", broker.OutcomeProbeMinted),
		credRow(activityCreated.Add(2*time.Second), "github", "o", broker.OutcomeRevoked),
		credRow(activityCreated.Add(3*time.Second), "", "", broker.OutcomeSSOExpired),
	}
	appr := []broker.ApprovalRow{
		{Time: activityCreated.Add(4 * time.Second), Action: broker.ActionSSOLoginOK},
		{Time: activityCreated.Add(5 * time.Second), Action: broker.ActionDropped},
		{Time: activityCreated.Add(6 * time.Second), Action: broker.ActionGranted, Minutes: 5, Reason: strings.Repeat("x", 500) + "\n"},
	}
	evs := activityEvents(t, decodeActivity(t, activityGet(auditAPI(t, newActivitySvc(), creds, appr), "/api/sessions/s1/activity", credCookie)))
	if len(evs) != 3 {
		t.Fatalf("events = %v", evs)
	}
	if evs[0]["state"] != "granted" || len([]rune(evs[0]["detail"].(string))) > 230 || strings.Contains(evs[0]["detail"].(string), "\n") {
		t.Fatalf("reason not bounded: %v", evs[0])
	}
	if evs[1]["detail"] != "dropped" || evs[2]["detail"] != "aws" || evs[2]["state"] != "failed" {
		t.Fatalf("events = %v", evs)
	}
}

func TestActivityWithoutBrokerKeepsTranscript(t *testing.T) {
	svc := newActivitySvc()
	svc.tl.Events = []session.TimelineEvent{tlEv(activityCreated.UnixMilli()+1000, "ls")}
	svc.tl.Files = []session.FileChange{{Path: "/w/a.go", Add: 2, Del: 1}}
	svc.tl.Partial, svc.tl.WaitingSince = true, 42
	m := decodeActivity(t, activityGet(&API{Sessions: svc}, "/api/sessions/s1/activity", credCookie))
	sum := m["summary"].(map[string]any)
	if len(activityEvents(t, m)) != 1 || m["partial"] != true || m["waiting_since"] != float64(42) || sum["files_changed"] != float64(1) || sum["running_s"].(float64) <= 0 {
		t.Fatalf("body = %v", m)
	}
}

// A denial is containment working; an error is something that broke. The
// Activity view shows the first in neutral text and the second in red, so the
// state must tell them apart (UI plan 6 E6).
func TestCredentialStateSplitsRefusalFromFailure(t *testing.T) {
	for _, tc := range []struct {
		o     broker.Outcome
		state string
		show  bool
	}{
		{broker.OutcomeDeniedToken, "refused", true},
		{broker.OutcomeDeniedSession, "refused", true},
		{broker.OutcomeDeniedNotReadOnly, "refused", true},
		{broker.OutcomeDeniedBox, "refused", true},
		{broker.OutcomeDeniedScope, "refused", true},
		{broker.OutcomeSTSError, "failed", true},
		{broker.OutcomeRateLimited, "failed", true},
		{broker.OutcomeSSOExpired, "failed", true},
		{broker.OutcomeStoreLocked, "failed", true},
		{broker.OutcomeUnresolved, "failed", true},
		{broker.OutcomeNotInstalled, "failed", true},
		{broker.OutcomeScopeError, "failed", true},
		{broker.OutcomeGitHubError, "failed", true},
		{broker.OutcomeMinted, "minted", true},
		{broker.OutcomeFetched, "minted", true},
		{broker.OutcomeProbeMinted, "", false},
		{broker.OutcomeProbeFailed, "", false},
		{broker.OutcomeRevoked, "", false},
		{broker.OutcomeRevokeFailed, "", false},
	} {
		state, show := credentialState(tc.o)
		if state != tc.state || show != tc.show {
			t.Errorf("credentialState(%q) = (%q, %v), want (%q, %v)", tc.o, state, show, tc.state, tc.show)
		}
	}
}

// stubActivityRows serves canned broker rows to credentialEvents.
type stubActivityRows struct{ rows []broker.Row }

func (s stubActivityRows) ActivityCredentials(string, int64, int64, int) ([]broker.Row, error) {
	return s.rows, nil
}
func (stubActivityRows) ActivityApprovals(string, int64, int64, int) ([]broker.ApprovalRow, error) {
	return nil, nil
}
func (stubActivityRows) ActivityMinted(string, int64) (int, error) { return 0, nil }

// A credential row mirrors the broker provider (empty is AWS) so the page picks
// a glyph from a declared field; an approval row has none, and the field is
// left out of the JSON when empty.
func TestCredentialEventsProvider(t *testing.T) {
	at := activityCreated.Add(time.Minute)
	rows := []broker.Row{
		credRow(at, "github", "", broker.OutcomeMinted),
		credRow(at, "static", "datadog-read", broker.OutcomeFetched),
		credRow(at, "", "culipulse-prod", broker.OutcomeMinted),
		credRow(at, "aws", "culipulse-prod", broker.OutcomeMinted),
	}
	evs, _, err := credentialEvents(stubActivityRows{rows}, "s1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github", "static", "aws", "aws"}
	if len(evs) != len(want) {
		t.Fatalf("events = %+v", evs)
	}
	for i, w := range want {
		if evs[i].Kind != "credential" || evs[i].Provider != w {
			t.Errorf("event %d: kind %q provider %q, want credential %q", i, evs[i].Kind, evs[i].Provider, w)
		}
	}

	// Through the handler: credential rows carry it, approval rows do not.
	creds := []broker.Row{credRow(activityCreated.Add(20*time.Second), "github", "", broker.OutcomeMinted)}
	appr := []broker.ApprovalRow{{Time: activityCreated.Add(10 * time.Second), Action: broker.ActionGranted, Minutes: 15}}
	a := auditAPI(t, newActivitySvc(), creds, appr)
	got := activityEvents(t, decodeActivity(t, activityGet(a, "/api/sessions/s1/activity", credCookie)))
	if len(got) != 2 || got[0]["kind"] != "credential" || got[0]["provider"] != "github" {
		t.Fatalf("credential row: %v", got)
	}
	if _, has := got[1]["provider"]; got[1]["kind"] != "approval" || has {
		t.Fatalf("approval row must carry no provider: %v", got[1])
	}
}
