package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/session"
)

type mockSvc struct {
	list            []session.Info
	getOK           bool
	killed          string
	spawnInfo       session.Info
	spawnErr        error
	spawnedCwd      string
	spawnedOptions  map[string]any
	spawnedRuntime  string
	spawnedAccount  string
	spawnedLogin    bool
	renamedID       string
	renamedName     string
	renameErr       error
	usage           session.Usage
	usageOK         bool
	attr            session.Attribution
	attrOK          bool
	peek            session.Peek
	peekOK          bool
	setReplyableID  string
	setReplyableV   bool
	setReplyableErr error
	sendMessageID   string
	sendMessageV    string
	sendMessageErr  error

	spawnedPrompt    string
	spawnedReplyable *bool
	spawnedParentID  string
	spawnedSpec      launcher.SpawnSpec
	interruptID      string
	interruptErr     error
	answerID         string
	answerChoice     string
	answerSHA        string
	answerErr        error
	restartID        string
	restartInfo      session.Info
	restartErr       error
	bringHomeID      string
	bringHomeErr     error
	boxOut           map[string]string // session id -> box output dir (boxed sessions only)
}

func (m *mockSvc) List() []session.Info { return m.list }
func (m *mockSvc) Spawn(s launcher.SpawnSpec) (session.Info, error) {
	m.spawnedCwd = s.Cwd
	m.spawnedOptions = s.Options
	m.spawnedRuntime = s.Runtime
	m.spawnedAccount = s.Account
	m.spawnedLogin = s.Login
	m.spawnedPrompt = s.Prompt
	m.spawnedReplyable = s.Replyable
	m.spawnedParentID = s.ParentID
	m.spawnedSpec = s
	if m.spawnErr != nil {
		return session.Info{}, m.spawnErr
	}
	return m.spawnInfo, nil
}

func TestAPI_ForwardsOptions(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"claude","options":{"skip":true,"effort":"high"}}`)))
	if rec.Code != 201 {
		t.Fatalf("code %d", rec.Code)
	}
	if svc.spawnedOptions["skip"] != true || svc.spawnedOptions["effort"] != "high" {
		t.Fatalf("options not forwarded: %v", svc.spawnedOptions)
	}
}
func TestAPI_ForwardsAccount(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"claude","account":"work"}`)))
	if rec.Code != 201 {
		t.Fatalf("code %d", rec.Code)
	}
	if svc.spawnedAccount != "work" {
		t.Fatalf("account not forwarded: %q", svc.spawnedAccount)
	}
}

func TestAPI_ForwardsLogin(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc}
	for _, c := range []struct {
		body string
		want bool
	}{{`{"agent":"claude","account":"work","login":true}`, true}, {`{"agent":"claude","account":"work"}`, false}} {
		rec := httptest.NewRecorder()
		a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(c.body)))
		if rec.Code != 201 {
			t.Fatalf("code %d", rec.Code)
		}
		if svc.spawnedLogin != c.want {
			t.Fatalf("%s: login = %v, want %v", c.body, svc.spawnedLogin, c.want)
		}
	}
}

// Account refusals name only what the caller just chose, so they are shown
// instead of the generic "could not start session".
func TestAPI_SpawnAccountRefusalsAreShown(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{launcher.ErrCodexAccount, "codex accounts are not supported"},
		{fmt.Errorf("%w %q", account.ErrUnknown, "ghost"), `unknown account "ghost"`},
		{fmt.Errorf("%w: profile %q carries secret X", launcher.ErrAccountConflict, "p"), "account conflict: profile \"p\" carries secret X"},
	}
	for _, c := range cases {
		svc := &mockSvc{spawnErr: c.err}
		a := &API{Sessions: svc}
		rec := httptest.NewRecorder()
		a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
			strings.NewReader(`{"agent":"claude","account":"work"}`)))
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 400 || body["error"] != c.err.Error() || !strings.Contains(body["error"], c.want) {
			t.Fatalf("err %v: got %d %s, want 400 with %q", c.err, rec.Code, rec.Body, c.want)
		}
	}
}

func (m *mockSvc) Kill(id string) error { m.killed = id; return nil }
func (m *mockSvc) Get(id string) (session.Info, bool) {
	return session.Info{ID: id, Name: m.renamedName}, m.getOK
}
func (m *mockSvc) Rename(id, name string) error {
	m.renamedID, m.renamedName = id, name
	return m.renameErr
}
func (m *mockSvc) TimelineFor(id string, beforeMS int64, limit int) (session.Timeline, bool) {
	return session.Timeline{}, false
}
func (m *mockSvc) UsageFor(id string) (session.Usage, bool) { return m.usage, m.usageOK }
func (m *mockSvc) AttributionFor(id string) (session.Attribution, bool) {
	return m.attr, m.attrOK
}
func (m *mockSvc) PeekFor(id string) (session.Peek, bool) { return m.peek, m.peekOK }
func (m *mockSvc) BoxOutputDir(id string) (string, bool) {
	d, ok := m.boxOut[id]
	return d, ok
}
func (m *mockSvc) SetReplyable(id string, v bool) error {
	m.setReplyableID, m.setReplyableV = id, v
	return m.setReplyableErr
}
func (m *mockSvc) SendMessage(id, text string) error {
	m.sendMessageID, m.sendMessageV = id, text
	return m.sendMessageErr
}
func (m *mockSvc) Interrupt(id string) error { m.interruptID = id; return m.interruptErr }
func (m *mockSvc) Answer(id, choice, sha string) error {
	m.answerID, m.answerChoice, m.answerSHA = id, choice, sha
	return m.answerErr
}
func (m *mockSvc) Restart(id string) (session.Info, error) {
	m.restartID = id
	return m.restartInfo, m.restartErr
}
func (m *mockSvc) BringHome(id string) error {
	m.bringHomeID = id
	return m.bringHomeErr
}

func TestAPI_Attribution(t *testing.T) {
	svc := &mockSvc{attrOK: true, attr: session.Attribution{
		Tools:        []session.ToolStat{{Name: "Read", Kind: "tool", Calls: 33, CtxBytes: 1614490}},
		OutputTokens: 41000,
	}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/attribution", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d want 200", rec.Code)
	}
	var at session.Attribution
	_ = json.NewDecoder(rec.Body).Decode(&at)
	if len(at.Tools) != 1 || at.Tools[0].Name != "Read" || at.Tools[0].CtxBytes != 1614490 || at.OutputTokens != 41000 {
		t.Fatalf("body %+v", at)
	}
}

func TestAPI_AttributionMiss(t *testing.T) {
	svc := &mockSvc{attrOK: false}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/attribution", nil))
	if rec.Code != 204 {
		t.Fatalf("code %d want 204", rec.Code)
	}
}

func TestAPI_Usage(t *testing.T) {
	svc := &mockSvc{usageOK: true, usage: session.Usage{Model: "claude-opus-4-8", ContextTokens: 597348}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/usage", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d want 200", rec.Code)
	}
	var u session.Usage
	_ = json.NewDecoder(rec.Body).Decode(&u)
	if u.ContextTokens != 597348 || u.Model != "claude-opus-4-8" {
		t.Fatalf("body %+v", u)
	}
}

func TestAPI_UsageMiss(t *testing.T) {
	svc := &mockSvc{usageOK: false} // non-claude / no transcript
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/usage", nil))
	if rec.Code != 204 {
		t.Fatalf("code %d want 204", rec.Code)
	}
}

func TestPeekEndpoint(t *testing.T) {
	// 200 with the payload when PeekFor hits.
	svc := &mockSvc{peekOK: true, peek: session.Peek{Text: "line one", Pending: true}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/peek", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d want 200", rec.Code)
	}
	var p session.Peek
	_ = json.NewDecoder(rec.Body).Decode(&p)
	if p.Text != "line one" || !p.Pending {
		t.Fatalf("body %+v", p)
	}

	// 204 when PeekFor misses.
	svc = &mockSvc{peekOK: false}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/peek", nil))
	if rec.Code != 204 {
		t.Fatalf("code %d want 204", rec.Code)
	}

	// 405 on a non-GET method.
	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("POST", "/api/sessions/x-1/peek", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code %d want 405", rec.Code)
	}

	// The path-suffix routing must not collide with the existing /usage and
	// /attribution branches: a peek-only fake should still 204 (not error) on
	// those routes, and a usage/attribution-only fake should still 204 on peek.
	svc = &mockSvc{peekOK: true, peek: session.Peek{Text: "x"}}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/usage", nil))
	if rec.Code != 204 {
		t.Fatalf("usage route code %d want 204 (peek must not have intercepted it)", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/x-1/attribution", nil))
	if rec.Code != 204 {
		t.Fatalf("attribution route code %d want 204 (peek must not have intercepted it)", rec.Code)
	}
}

// replyableReq builds a same-origin JSON POST the /replyable handler accepts.
func replyableReq(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/x-1/replyable", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func TestReplyableEndpoint(t *testing.T) {
	// 204 on success.
	svc := &mockSvc{}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, replyableReq(`{"replyable":true}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("success: code %d want 204, body %s", rec.Code, rec.Body)
	}
	if svc.setReplyableID != "x-1" || svc.setReplyableV != true {
		t.Fatalf("not forwarded: id=%q v=%v", svc.setReplyableID, svc.setReplyableV)
	}

	// 404 for an unknown id.
	svc = &mockSvc{setReplyableErr: session.ErrNoSession}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, replyableReq(`{"replyable":true}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: code %d want 404, body %s", rec.Code, rec.Body)
	}

	// 403 for a non-claude session: the opt-in is scoped by isClaudeBin in the
	// session layer, and "no such session" would be a lie about a session that
	// is right there in the list.
	svc = &mockSvc{setReplyableErr: session.ErrNotClaude}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, replyableReq(`{"replyable":true}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-claude: code %d want 403, body %s", rec.Code, rec.Body)
	}

	// 405 on GET.
	svc = &mockSvc{}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/sessions/x-1/replyable", nil)
	a.handleSession(rec, getReq)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: code %d want 405, body %s", rec.Code, rec.Body)
	}

	// 400 on a malformed body.
	rec = httptest.NewRecorder()
	a.handleSession(rec, replyableReq(`{not json`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: code %d want 400, body %s", rec.Code, rec.Body)
	}

	// 403 on a cross-origin POST with cookie credentials. This must call
	// handleSession DIRECTLY — the same way the other four cases do, bypassing
	// authGuard/csrfGuard entirely — so the rejection is produced by the
	// handler's own checkMutationGuard(w, r) call (api.go), not by the outer
	// router middleware. Going through the full router (as an earlier version
	// of this test did) reaches csrfGuard first: it rejects a non-bearer,
	// non-same-origin POST before the request ever hits the mux entry for
	// /api/sessions/, so the inline guard would never execute and the test
	// would still pass even if it were deleted from the handler. A direct
	// handleSession call has no authGuard-set credential in its context, so
	// credKindOf(r) reads credNone (not credBearer) exactly like an
	// unauthenticated-context cookie request would — checkMutationGuard's
	// sameOrigin(r) branch is what has to reject the cross-site header.
	svc = &mockSvc{}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	cross := httptest.NewRequest(http.MethodPost, "/api/sessions/x-1/replyable", strings.NewReader(`{"replyable":true}`))
	cross.Header.Set("Content-Type", "application/json")
	cross.AddCookie(&http.Cookie{Name: "session", Value: "irrelevant-to-the-inline-guard"})
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	a.handleSession(rec, cross)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site cookie POST: code %d want 403, body %s", rec.Code, rec.Body)
	}
}

// messageReq builds a same-origin JSON POST the /message handler accepts.
func messageReq(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/x-1/message", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func TestMessageEndpoint(t *testing.T) {
	// 204 on success.
	svc := &mockSvc{}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"hello"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("success: code %d want 204, body %s", rec.Code, rec.Body)
	}
	if svc.sendMessageID != "x-1" || svc.sendMessageV != "hello" {
		t.Fatalf("not forwarded: id=%q text=%q", svc.sendMessageID, svc.sendMessageV)
	}

	// 400: malformed JSON body.
	svc = &mockSvc{}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{not json`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: code %d want 400, body %s", rec.Code, rec.Body)
	}
	// The status alone would still pass if a refactor moved the guard BELOW the
	// dispatch — on the one endpoint in this daemon that writes into a live
	// agent's terminal. Assert the side effect never happened.
	if svc.sendMessageID != "" {
		t.Fatalf("malformed body reached the session layer: id=%q text=%q", svc.sendMessageID, svc.sendMessageV)
	}

	// 400: ErrBadText mapping.
	svc = &mockSvc{sendMessageErr: session.ErrBadText}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"two\nlines"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ErrBadText: code %d want 400, body %s", rec.Code, rec.Body)
	}

	// 403: ErrNotReplyable mapping.
	svc = &mockSvc{sendMessageErr: session.ErrNotReplyable}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"hello"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ErrNotReplyable: code %d want 403, body %s", rec.Code, rec.Body)
	}

	// 404: ErrNoSession mapping.
	svc = &mockSvc{sendMessageErr: session.ErrNoSession}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"hello"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ErrNoSession: code %d want 404, body %s", rec.Code, rec.Body)
	}

	// 409: ErrNotWaiting and ErrPending are two distinct reasons collapsing to
	// the same status — assert both separately so neither being dropped or
	// swapped for the other slips past.
	svc = &mockSvc{sendMessageErr: session.ErrNotWaiting}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"hello"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("ErrNotWaiting: code %d want 409, body %s", rec.Code, rec.Body)
	}

	svc = &mockSvc{sendMessageErr: session.ErrPending}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"hello"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("ErrPending: code %d want 409, body %s", rec.Code, rec.Body)
	}

	// 410: any other error (e.g. the PTY write failed — the session died
	// between the guard check and the write).
	svc = &mockSvc{sendMessageErr: errors.New("write failed")}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	a.handleSession(rec, messageReq(`{"text":"hello"}`))
	if rec.Code != http.StatusGone {
		t.Fatalf("write failure: code %d want 410, body %s", rec.Code, rec.Body)
	}

	// 405 on GET.
	svc = &mockSvc{}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/sessions/x-1/message", nil)
	a.handleSession(rec, getReq)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: code %d want 405, body %s", rec.Code, rec.Body)
	}
	if svc.sendMessageID != "" {
		t.Fatalf("GET reached the session layer: id=%q text=%q", svc.sendMessageID, svc.sendMessageV)
	}

	// 403 on a cross-origin POST with cookie credentials — same constraint as
	// the /replyable cross-origin case: call handleSession directly so the
	// rejection comes from checkMutationGuard inside the handler, not from
	// the outer router's csrfGuard (which would reject the request before it
	// ever reaches this branch and mask a deleted inline guard).
	svc = &mockSvc{}
	a = &API{Sessions: svc}
	rec = httptest.NewRecorder()
	cross := httptest.NewRequest(http.MethodPost, "/api/sessions/x-1/message", strings.NewReader(`{"text":"hello"}`))
	cross.Header.Set("Content-Type", "application/json")
	cross.AddCookie(&http.Cookie{Name: "session", Value: "irrelevant-to-the-inline-guard"})
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	a.handleSession(rec, cross)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site cookie POST: code %d want 403, body %s", rec.Code, rec.Body)
	}
	if svc.sendMessageID != "" {
		t.Fatalf("cross-site POST reached the session layer: id=%q text=%q", svc.sendMessageID, svc.sendMessageV)
	}
}

func TestAPI_Rename(t *testing.T) {
	svc := &mockSvc{getOK: true}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("PATCH", "/api/sessions/x-1",
		strings.NewReader(`{"name":"new"}`)))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if svc.renamedID != "x-1" || svc.renamedName != "new" {
		t.Fatalf("rename not forwarded: id=%q name=%q", svc.renamedID, svc.renamedName)
	}
	var info session.Info
	_ = json.NewDecoder(rec.Body).Decode(&info)
	if info.Name != "new" {
		t.Fatalf("body name=%q want new", info.Name)
	}
}

func TestAPI_RenameUnknown(t *testing.T) {
	svc := &mockSvc{renameErr: session.ErrNotFound}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("PATCH", "/api/sessions/nope",
		strings.NewReader(`{"name":"x"}`)))
	if rec.Code != 404 {
		t.Fatalf("code %d want 404", rec.Code)
	}
}

func TestAPI_RenameEmpty(t *testing.T) {
	svc := &mockSvc{renameErr: session.ErrInvalidName}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("PATCH", "/api/sessions/x-1",
		strings.NewReader(`{"name":"  "}`)))
	if rec.Code != 400 {
		t.Fatalf("code %d want 400", rec.Code)
	}
}

func TestAPI_ListSpawnDelete(t *testing.T) {
	svc := &mockSvc{
		list:      []session.Info{{ID: "bash-1", Agent: "bash"}},
		getOK:     true,
		spawnInfo: session.Info{ID: "bash-2", Agent: "bash"},
	}
	a := &API{Sessions: svc}

	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("GET", "/api/sessions", nil))
	if rec.Code != 200 {
		t.Fatalf("GET code %d", rec.Code)
	}
	var got []session.Info
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ID != "bash-1" {
		t.Fatalf("list %+v", got)
	}

	rec = httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"bash"}`)))
	if rec.Code != 201 {
		t.Fatalf("POST code %d body %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("DELETE", "/api/sessions/bash-1", nil))
	if rec.Code != 204 {
		t.Fatalf("DELETE code %d", rec.Code)
	}
	if svc.killed != "bash-1" {
		t.Fatalf("killed %q", svc.killed)
	}
}

func TestAPI_DefaultsCwd(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc, DefaultCwd: "/home/me"}

	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"bash"}`)))
	if svc.spawnedCwd != "/home/me" {
		t.Fatalf("empty cwd should default to DefaultCwd, got %q", svc.spawnedCwd)
	}

	rec = httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"bash","cwd":"/tmp/x"}`)))
	if svc.spawnedCwd != "/tmp/x" {
		t.Fatalf("explicit cwd should win, got %q", svc.spawnedCwd)
	}
}

func TestAPI_DeleteUnknownIs404(t *testing.T) {
	a := &API{Sessions: &mockSvc{getOK: false}}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("DELETE", "/api/sessions/ghost", nil))
	if rec.Code != 404 {
		t.Fatalf("want 404 got %d", rec.Code)
	}
}

func TestAPI_SpawnErrorIsStructured(t *testing.T) {
	a := &API{Sessions: &mockSvc{spawnErr: errors.New("unknown agent")}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"x"}`)))
	if rec.Code != 400 {
		t.Fatalf("want 400 got %d", rec.Code)
	}
	var body map[string]string
	if json.Unmarshal(rec.Body.Bytes(), &body); body["error"] == "" {
		t.Fatalf("expected structured error body, got %s", rec.Body)
	}
}

// An unrecognised tier must be rejected outright. Falling through to a default
// would let a typo silently de-contain a session.
func TestSpawnRejectsUnknownRuntime(t *testing.T) {
	a := &API{Sessions: &mockSvc{spawnInfo: session.Info{ID: "x"}}, DefaultCwd: "/tmp"}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"bash","runtime":"vm"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body)
	}
}

func TestSpawnAcceptsTheHostTierOverride(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc, DefaultCwd: "/tmp"}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"bash","runtime":"host"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}
	if svc.spawnedRuntime != "host" {
		t.Fatalf("spec.Runtime = %q, want host", svc.spawnedRuntime)
	}
}

// spawnCwd POSTs body to a handler whose ProfileCwd is pc and returns the cwd
// the session service was asked to spawn in.
func spawnCwd(t *testing.T, pc func(string) string, body string) string {
	t.Helper()
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc, DefaultCwd: "/tmp", ProfileCwd: pc}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}
	return svc.spawnedCwd
}

func TestSpawnOmittedCwdUsesProfileDefault(t *testing.T) {
	pc := func(agent string) string {
		if agent == "p" {
			return "/srv/clone"
		}
		return ""
	}
	if got := spawnCwd(t, pc, `{"agent":"p"}`); got != "/srv/clone" {
		t.Fatalf("cwd = %q, want the profile's default_cwd /srv/clone", got)
	}
}

func TestSpawnWhitespaceCwdUsesProfileDefault(t *testing.T) {
	pc := func(string) string { return "/srv/clone" }
	if got := spawnCwd(t, pc, `{"agent":"p","cwd":"   "}`); got != "/srv/clone" {
		t.Fatalf("cwd = %q, want /srv/clone", got)
	}
}

func TestSpawnOmittedCwdNoProfileDefault(t *testing.T) {
	for _, v := range []string{"", "  "} {
		pc := func(string) string { return v }
		if got := spawnCwd(t, pc, `{"agent":"p"}`); got != "/tmp" {
			t.Fatalf("ProfileCwd=%q: cwd = %q, want DefaultCwd /tmp", v, got)
		}
	}
}

func TestSpawnExplicitCwdNotRewritten(t *testing.T) {
	pc := func(string) string { return "/srv/clone" }
	if got := spawnCwd(t, pc, `{"agent":"p","cwd":"/elsewhere"}`); got != "/elsewhere" {
		t.Fatalf("cwd = %q, want the explicit /elsewhere", got)
	}
}

func TestSpawnNilProfileCwd(t *testing.T) {
	if got := spawnCwd(t, nil, `{"agent":"p"}`); got != "/tmp" {
		t.Fatalf("cwd = %q, want DefaultCwd /tmp", got)
	}
}

func TestAPI_ListCarriesNeedOnlyWhenSet(t *testing.T) {
	m := &mockSvc{list: []session.Info{
		{ID: "a", Activity: "waiting", Claude: true, Need: "ask", Ask: "Bash(x)"},
		{ID: "b", Activity: "idle"},
	}}
	a := &API{Sessions: m}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("GET", "/api/sessions", nil))
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["need"] != "ask" || got[0]["ask"] != "Bash(x)" {
		t.Errorf("waiting claude item = %v, want need=ask ask=Bash(x)", got[0])
	}
	for _, k := range []string{"need", "ask"} {
		if _, ok := got[1][k]; ok {
			t.Errorf("idle item carries %q; the key must be omitted", k)
		}
	}
	if got[1]["activity"] != "idle" {
		t.Errorf("activity=%v want idle on the wire", got[1]["activity"])
	}
}

// postReq builds a same-origin JSON POST to path.
func postReq(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func crossSiteReq(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	return r
}

func TestSpawnForwardsPromptAndReplyable(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "claude-1"}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w","prompt":"fix the build","replyable":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if svc.spawnedPrompt != "fix the build" || svc.spawnedReplyable == nil || !*svc.spawnedReplyable {
		t.Fatalf("prompt=%q replyable=%v", svc.spawnedPrompt, svc.spawnedReplyable)
	}
	svc = &mockSvc{}
	a = &API{Sessions: svc}
	a.handleSessions(httptest.NewRecorder(), postReq("/api/sessions", `{"agent":"claude","cwd":"/w"}`))
	if svc.spawnedReplyable != nil {
		t.Fatal("an absent replyable must reach the launcher as nil (profile default)")
	}
}

func TestSpawnPromptRefusalsAreShown(t *testing.T) {
	for _, e := range []error{
		fmt.Errorf("%w: it is empty", launcher.ErrBadPrompt),
		launcher.ErrPromptUnsupported,
	} {
		a := &API{Sessions: &mockSvc{spawnErr: e}}
		rec := httptest.NewRecorder()
		a.handleSessions(rec, postReq("/api/sessions", `{"agent":"bash","prompt":"x y"}`))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), e.Error()) {
			t.Errorf("%v: code %d body %s", e, rec.Code, rec.Body)
		}
	}
}

// TestSpawnErrHidesDetailByDefault is writeSpawnErr's hide-the-detail branch:
// the one a regression could silently defeat by switching to err.Error(),
// leaking a secret name, a binary path, or the box-start cwd into the 400
// body. Pins the body to EXACTLY the generic message, not just "doesn't
// contain the secret" (a partial leak would still pass a looser check).
func TestSpawnErrHidesDetailByDefault(t *testing.T) {
	leaky := errors.New(`launcher: resolving secret "DB_PASSWORD": open /etc/agent-box/secrets/db: permission denied`)
	a := &API{Sessions: &mockSvc{spawnErr: leaky}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d want 400", rec.Code)
	}
	if got := rec.Body.String(); got != `{"error":"could not start session"}`+"\n" {
		t.Fatalf("body = %q, want exactly the generic message — no secret name, path, or detail", got)
	}
}

// TestSpawnErrShowsUnboxableWorkspace is writeSpawnErr's one actionable
// exception: a wrapped box.ErrUnboxableWorkspace's own message (which names
// the cwd the caller just supplied) must reach the client.
func TestSpawnErrShowsUnboxableWorkspace(t *testing.T) {
	wrapped := fmt.Errorf("%w: /home/op (is $HOME). Pick a project directory, "+
		`or choose "Run on trusted host" to run it uncontained on purpose`, box.ErrUnboxableWorkspace)
	a := &API{Sessions: &mockSvc{spawnErr: wrapped}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/home/op"}`))
	var got struct {
		Error string `json:"error"`
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d want 400", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	if got.Error != wrapped.Error() {
		t.Fatalf("error = %q, want the unboxable-workspace detail %q", got.Error, wrapped.Error())
	}
}

// A workspace refusal names the conflicting box and path; the user needs it
// to pick another folder, so it is shown rather than hidden.
func TestSpawnErrShowsWorkspaceHeld(t *testing.T) {
	wrapped := fmt.Errorf(`%w: /w is closed to other boxes while a session in it can write to AWS: session "deploy" (aws-1) has a write window open until 15:04. Wait for it to end (or drop the window in that session), or use a separate folder or worktree.`, box.ErrWorkspaceHeld)
	a := &API{Sessions: &mockSvc{spawnErr: wrapped}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w"}`))
	var got struct {
		Error string `json:"error"`
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d want 400", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	if got.Error != wrapped.Error() {
		t.Fatalf("error = %q, want %q", got.Error, wrapped.Error())
	}
}

// TestRestartErrHidesDetailByDefault: /restart's unrecognised-error branch
// also goes through writeSpawnErr (see the brief's "(restart <id>)" agent
// label) — same hide-the-detail contract applies there too.
func TestRestartErrHidesDetailByDefault(t *testing.T) {
	leaky := errors.New(`launcher: resolving secret "DB_PASSWORD": open /etc/agent-box/secrets/db: permission denied`)
	a := &API{Sessions: &mockSvc{restartErr: leaky}}
	rec := httptest.NewRecorder()
	a.handleSession(rec, postReq("/api/sessions/claude-3/restart", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d want 400", rec.Code)
	}
	if got := rec.Body.String(); got != `{"error":"could not start session"}`+"\n" {
		t.Fatalf("body = %q, want exactly the generic message — no secret name, path, or detail", got)
	}
}

// TestRestartErrShowsUnboxableWorkspace: the same actionable exception on the
// restart path (a restarted session can hit an unboxable cwd too, e.g. the
// workspace was replaced by a symlink since the original spawn).
func TestRestartErrShowsUnboxableWorkspace(t *testing.T) {
	wrapped := fmt.Errorf("%w: /home/op (is $HOME). Pick a project directory, "+
		`or choose "Run on trusted host" to run it uncontained on purpose`, box.ErrUnboxableWorkspace)
	a := &API{Sessions: &mockSvc{restartErr: wrapped}}
	rec := httptest.NewRecorder()
	a.handleSession(rec, postReq("/api/sessions/claude-3/restart", ""))
	var got struct {
		Error string `json:"error"`
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d want 400", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	if got.Error != wrapped.Error() {
		t.Fatalf("error = %q, want the unboxable-workspace detail %q", got.Error, wrapped.Error())
	}
}

func TestInterruptEndpoint(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{nil, http.StatusNoContent},
		{session.ErrNoSession, http.StatusNotFound},
		{session.ErrNotLive, http.StatusConflict},
		{session.ErrBinUnresolved, http.StatusConflict},
		{session.ErrTooSoon, http.StatusTooManyRequests},
		{errors.New("write /dev/ptmx: input/output error"), http.StatusGone},
	}
	for _, c := range cases {
		svc := &mockSvc{interruptErr: c.err}
		rec := httptest.NewRecorder()
		(&API{Sessions: svc}).handleSession(rec, postReq("/api/sessions/x-1/interrupt", ""))
		if rec.Code != c.code || svc.interruptID != "x-1" {
			t.Errorf("err %v: code %d want %d (id %q)", c.err, rec.Code, c.code, svc.interruptID)
		}
		// The 410 catch-all must not leak the raw write error (a PTY path, an
		// errno string) into the client-facing body.
		if c.code == http.StatusGone && strings.Contains(rec.Body.String(), "ptmx") {
			t.Errorf("err %v: 410 body leaked the raw write error: %s", c.err, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}}).handleSession(rec, crossSiteReq("/api/sessions/x-1/interrupt", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}}).handleSession(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/x-1/interrupt", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d want 405", rec.Code)
	}
}

func TestAnswerEndpoint(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	body := `{"choice":"yes","ask_sha256":"` + sha + `"}`
	cases := []struct {
		err  error
		code int
	}{
		{nil, http.StatusNoContent},
		{session.ErrBadChoice, http.StatusBadRequest},
		{session.ErrNoSession, http.StatusNotFound},
		{session.ErrNotClaude, http.StatusForbidden},
		{session.ErrNotReplyable, http.StatusForbidden},
		{session.ErrNotLive, http.StatusConflict},
		{session.ErrNotBlocked, http.StatusConflict},
		{session.ErrPromptChanged, http.StatusConflict},
		{session.ErrTooSoon, http.StatusTooManyRequests},
		{errors.New("write: broken pipe"), http.StatusGone},
	}
	for _, c := range cases {
		svc := &mockSvc{answerErr: c.err}
		rec := httptest.NewRecorder()
		(&API{Sessions: svc}).handleSession(rec, postReq("/api/sessions/x-1/answer", body))
		if rec.Code != c.code {
			t.Errorf("err %v: code %d want %d", c.err, rec.Code, c.code)
		}
		if svc.answerID != "x-1" || svc.answerChoice != "yes" || svc.answerSHA != sha {
			t.Errorf("not forwarded: %q %q %q", svc.answerID, svc.answerChoice, svc.answerSHA)
		}
		// The 410 catch-all must not leak the raw write error into the body.
		if c.code == http.StatusGone && strings.Contains(rec.Body.String(), "broken pipe") {
			t.Errorf("err %v: 410 body leaked the raw write error: %s", c.err, rec.Body)
		}
	}
	if !strings.Contains(func() string {
		rec := httptest.NewRecorder()
		(&API{Sessions: &mockSvc{answerErr: session.ErrPromptChanged}}).handleSession(rec, postReq("/api/sessions/x-1/answer", body))
		return rec.Body.String()
	}(), "prompt changed") {
		t.Error("the 409 body must name the prompt change (the client words its toast on it)")
	}
	for _, bad := range []string{
		`{"choice":"yes","ask_sha256":"` + strings.ToUpper(sha) + `"}`, // not lowercase
		`{"choice":"yes","ask_sha256":"abc"}`,
		`{"choice":"yes"}`,
		`{not json`,
		`{"choice":"yes","ask_sha256":"` + sha + `","pad":"` + strings.Repeat("x", 1100) + `"}`, // over the 1 KiB cap
	} {
		svc := &mockSvc{}
		rec := httptest.NewRecorder()
		(&API{Sessions: svc}).handleSession(rec, postReq("/api/sessions/x-1/answer", bad))
		if rec.Code != http.StatusBadRequest || svc.answerID != "" {
			t.Errorf("%.40s: code %d (forwarded=%q), want 400 and not forwarded", bad, rec.Code, svc.answerID)
		}
	}
	rec := httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}}).handleSession(rec, crossSiteReq("/api/sessions/x-1/answer", body))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d want 403", rec.Code)
	}
}

func TestRestartEndpoint(t *testing.T) {
	svc := &mockSvc{restartInfo: session.Info{ID: "claude-9", Name: "api"}}
	rec := httptest.NewRecorder()
	(&API{Sessions: svc}).handleSession(rec, postReq("/api/sessions/claude-3/restart", ""))
	if rec.Code != http.StatusCreated || svc.restartID != "claude-3" || !strings.Contains(rec.Body.String(), `"claude-9"`) {
		t.Fatalf("code %d id %q body %s", rec.Code, svc.restartID, rec.Body)
	}
	for _, c := range []struct {
		err  error
		code int
	}{
		{session.ErrNoSession, http.StatusNotFound},
		{session.ErrNotExited, http.StatusConflict},
		{session.ErrRestarting, http.StatusConflict},
		{errors.New("launcher: unknown agent"), http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		(&API{Sessions: &mockSvc{restartErr: c.err}}).handleSession(rec, postReq("/api/sessions/claude-3/restart", ""))
		if rec.Code != c.code {
			t.Errorf("err %v: code %d want %d", c.err, rec.Code, c.code)
		}
	}
	rec = httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}}).handleSession(rec, crossSiteReq("/api/sessions/claude-3/restart", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d want 403", rec.Code)
	}
}

// POST /bring-home starts a "now" return and answers 202 at once; the
// refusals map to 404 (no session) and 409 (not a clone, or already returning).
func TestBringHomeEndpoint(t *testing.T) {
	svc := &mockSvc{}
	rec := httptest.NewRecorder()
	(&API{Sessions: svc}).handleSession(rec, postReq("/api/sessions/claude-3/bring-home", ""))
	if rec.Code != http.StatusAccepted || svc.bringHomeID != "claude-3" || !strings.Contains(rec.Body.String(), `"started":true`) {
		t.Fatalf("code %d id %q body %s", rec.Code, svc.bringHomeID, rec.Body)
	}
	for _, c := range []struct {
		err  error
		code int
	}{
		{session.ErrNoSession, http.StatusNotFound},
		{session.ErrNotClone, http.StatusConflict},
		{session.ErrReturning, http.StatusConflict},
	} {
		rec := httptest.NewRecorder()
		(&API{Sessions: &mockSvc{bringHomeErr: c.err}}).handleSession(rec, postReq("/api/sessions/claude-3/bring-home", ""))
		if rec.Code != c.code {
			t.Errorf("err %v: code %d want %d", c.err, rec.Code, c.code)
		}
		if c.code == http.StatusConflict && !strings.Contains(rec.Body.String(), c.err.Error()) {
			t.Errorf("err %v: body %s must carry the message", c.err, rec.Body)
		}
	}
	rec = httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}}).handleSession(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/claude-3/bring-home", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}}).handleSession(rec, crossSiteReq("/api/sessions/claude-3/bring-home", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d want 403", rec.Code)
	}
}

// ErrBoxUnavailable carries its remedy (a command, a path) and is shown as-is,
// like ErrUnboxableWorkspace, instead of the generic message.
func TestSpawnErrShowsBoxUnavailableMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSpawnErr(rec, "claude", fmt.Errorf("%w: run culibox box setup", box.ErrBoxUnavailable))
	if !strings.Contains(rec.Body.String(), "culibox box setup") {
		t.Fatalf("the remedy must reach the user: %s", rec.Body.String())
	}
}

// ErrBoxOwned names the directory the caller just sent and the way out, so it
// is shown as a 400 with its message.
func TestWriteSpawnErrShowsBoxOwned(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSpawnErr(rec, "claude", fmt.Errorf("%w: /c/clone is inside the clone /c/clone of a clone-mode profile", box.ErrBoxOwned))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "is inside the clone") {
		t.Fatalf("the reason must reach the user: %s", rec.Body.String())
	}
}

type fakeAWS map[string]any

func (f fakeAWS) AWSStatus(id string) (any, bool) { v, ok := f[id]; return v, ok }

func TestSessionAWSStatus(t *testing.T) {
	a := &API{Sessions: &mockSvc{}, AWS: fakeAWS{"claude-3": map[string]any{"label": "sandbox", "mints": 2}}}

	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/claude-3/aws", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d want 200: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["label"] != "sandbox" || body["mints"] != float64(2) {
		t.Fatalf("body %+v", body)
	}

	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/nope/aws", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unknown session: code %d want 204", rec.Code)
	}

	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("POST", "/api/sessions/claude-3/aws", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: code %d want 405", rec.Code)
	}

	// No broker: the route is a 404, not a 204 that looks like "no binding".
	a = &API{Sessions: &mockSvc{}}
	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/claude-3/aws", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no broker: code %d want 404", rec.Code)
	}
}

// The kill-switch refusal is a 403 on every reply endpoint. /message and
// /answer would otherwise fall through to the 410 "session is gone" arm, which
// would tell the client a live session had died.
func TestRepliesDisabledEndpoints403(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	cases := []struct {
		name string
		svc  *mockSvc
		req  *http.Request
	}{
		{"replyable", &mockSvc{setReplyableErr: session.ErrRepliesDisabled}, replyableReq(`{"replyable":true}`)},
		{"message", &mockSvc{sendMessageErr: session.ErrRepliesDisabled}, messageReq(`{"text":"hello"}`)},
		{"answer", &mockSvc{answerErr: session.ErrRepliesDisabled},
			postReq("/api/sessions/x-1/answer", `{"choice":"yes","ask_sha256":"`+sha+`"}`)},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		(&API{Sessions: c.svc}).handleSession(rec, c.req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code %d want 403, body %s", c.name, rec.Code, rec.Body)
		}
	}
}

// A spawn override the profile dropped is reported in the 201 body only: the
// session itself, its sidecar and the list never carry it.
func TestSpawnReportsDroppedOptions(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "claude-1", Agent: "claude", Cwd: "/w"}}
	var seen launcher.SpawnSpec
	a := &API{Sessions: svc, OptionsDropped: func(s launcher.SpawnSpec) []launcher.OptionDrop {
		seen = s
		return []launcher.OptionDrop{{Key: "model", Reason: "this profile does not expose this option"}}
	}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w","options":{"model":"claude-sonnet-5"}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if seen.Agent != "claude" || seen.Options["model"] != "claude-sonnet-5" {
		t.Fatalf("the hook saw %+v", seen)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Every field Info already marshals is still there.
	var want map[string]any
	b, _ := json.Marshal(svc.spawnInfo)
	_ = json.Unmarshal(b, &want)
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("field %q = %v, want %v", k, got[k], v)
		}
	}
	drops, ok := got["options_dropped"].([]any)
	if !ok || len(drops) != 1 {
		t.Fatalf("options_dropped = %v", got["options_dropped"])
	}
	d := drops[0].(map[string]any)
	if d["key"] != "model" || d["reason"] != "this profile does not expose this option" {
		t.Fatalf("drop = %v", d)
	}
	if strings.Contains(rec.Body.String(), "claude-sonnet-5") {
		t.Fatalf("the rejected value was echoed: %s", rec.Body)
	}
}

func TestSpawnOmitsOptionsDroppedWhenNothingDropped(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "claude-1"}}
	for name, hook := range map[string]func(launcher.SpawnSpec) []launcher.OptionDrop{
		"nil hook":   nil,
		"no drops":   func(launcher.SpawnSpec) []launcher.OptionDrop { return nil },
		"empty list": func(launcher.SpawnSpec) []launcher.OptionDrop { return []launcher.OptionDrop{} },
	} {
		a := &API{Sessions: svc, OptionsDropped: hook}
		rec := httptest.NewRecorder()
		a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w","options":{"effort":"low"}}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: code %d", name, rec.Code)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if _, has := got["options_dropped"]; has {
			t.Fatalf("%s: options_dropped present: %s", name, rec.Body)
		}
		if got["id"] != "claude-1" {
			t.Fatalf("%s: id = %v", name, got["id"])
		}
	}
}

// A locked store refuses the spawn with 423 and a stable code the UI can act
// on. The body carries only the fixed text: the wrapped secret name goes to the
// server log, never the response.
func TestSpawnErrStoreLocked(t *testing.T) {
	wrapped := fmt.Errorf("%w (DB_PASSWORD)", launcher.ErrStoreLocked)
	a := &API{Sessions: &mockSvc{spawnErr: wrapped}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w"}`))
	if rec.Code != http.StatusLocked {
		t.Fatalf("code %d want 423", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	want := map[string]string{"error": launcher.ErrStoreLocked.Error(), "code": "store_locked"}
	if len(got) != 2 || got["error"] != want["error"] || got["code"] != want["code"] {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if strings.Contains(rec.Body.String(), "DB_PASSWORD") {
		t.Fatal("the secret name leaked into the response")
	}
}

// parent_id is set only by the cookie-only shell endpoint. A spawn body that
// names one (an agent forging nesting through the PAT path) is ignored.
func TestSpawnBodyCannotSetParentID(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc, DefaultCwd: "/tmp"}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"bash","parent_id":"p1"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}
	if svc.spawnedParentID != "" {
		t.Fatalf("spec.ParentID = %q, want empty", svc.spawnedParentID)
	}
}

// A Keychain failure is 503 store_unavailable with the fix, never 423: the
// spawn dialog must show the message, not the unlock form. The wrapped secret
// name stays in the server log.
func TestSpawnStoreUnavailableIs503(t *testing.T) {
	ue := &secretstore.UnavailableError{Kind: secretstore.KindAccess, Reason: "User interaction is not allowed"}
	wrapped := fmt.Errorf("%w: %w (DB_PASSWORD)", launcher.ErrStoreUnavailable, ue)
	a := &API{Sessions: &mockSvc{spawnErr: wrapped}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, postReq("/api/sessions", `{"agent":"claude","cwd":"/w"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d want 503", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	if got["code"] != "store_unavailable" || got["error"] != ue.Error() || !strings.Contains(got["error"], ue.Fix()) {
		t.Fatalf("body = %v", got)
	}
	if strings.Contains(rec.Body.String(), "DB_PASSWORD") {
		t.Fatal("the secret name leaked into the response")
	}
}
