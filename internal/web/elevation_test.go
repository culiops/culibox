package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/ssologin"
)

type fakeElev struct {
	grants, drops, refused []string
	sso                    int
	grantErr, ssoErr       error
}

func (f *fakeElev) Grant(id string, minutes int, reason, label, cred string) (broker.ElevationView, error) {
	f.grants = append(f.grants, fmt.Sprintf("%s/%d/%s/%s/%s", id, minutes, reason, label, cred))
	if f.grantErr != nil {
		return broker.ElevationView{}, f.grantErr
	}
	return broker.ElevationView{Active: true, Until: time.Unix(1_800_003_600, 0).UTC()}, nil
}

func (f *fakeElev) Drop(id, cred string) (broker.ElevationView, error) {
	f.drops = append(f.drops, id+"/"+cred)
	return broker.ElevationView{LastValid: time.Unix(1_800_000_900, 0).UTC()}, nil
}

func (f *fakeElev) Refused(id, cred, reason string) { f.refused = append(f.refused, id+"/"+cred) }

func (f *fakeElev) SSOLogin(id string) (ssologin.State, error) {
	f.sso++
	if f.ssoErr != nil {
		return ssologin.State{}, f.ssoErr
	}
	return ssologin.State{State: "waiting", URI: "https://device.sso.ap-southeast-1.amazonaws.com/", URITrusted: true, Code: "ABCD-EFGH"}, nil
}

const host = "100.76.118.121:8913"

func elevReq(method, path, origin, fetchSite string, kind credKind) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(`{"minutes":60,"reason":"apply"}`))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if fetchSite != "" {
		r.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	return r.WithContext(context.WithValue(r.Context(), credKindKey, kind))
}

func TestElevateAuthMatrix(t *testing.T) {
	const el, sso = "/api/sessions/aws-1/aws/elevate", "/api/sessions/aws-1/aws/sso-login"
	same := "http://" + host
	cases := []struct {
		name, method, path, origin, site string
		kind                             credKind
		code, grants, drops, refused, ss int
	}{
		{"cookie same origin", "POST", el, same, "same-origin", credCookie, 200, 1, 0, 0, 0},
		{"cookie https origin", "POST", el, "https://" + host, "", credCookie, 200, 1, 0, 0, 0},
		{"cookie no origin", "POST", el, "", "", credCookie, 403, 0, 0, 1, 0},
		{"cookie cross origin", "POST", el, "http://evil.example", "cross-site", credCookie, 403, 0, 0, 1, 0},
		{"cookie origin ok but cross-site", "POST", el, same, "cross-site", credCookie, 403, 0, 0, 1, 0},
		{"bearer", "POST", el, same, "", credBearer, 403, 0, 0, 1, 0},
		{"no credential", "POST", el, same, "", credNone, 403, 0, 0, 1, 0},
		{"drop cookie", "DELETE", el, "", "", credCookie, 200, 0, 1, 0, 0},
		{"drop bearer", "DELETE", el, "", "", credBearer, 200, 0, 1, 0, 0},
		{"drop no credential", "DELETE", el, "", "", credNone, 403, 0, 0, 0, 0},
		{"get elevate", "GET", el, "", "", credCookie, 405, 0, 0, 0, 0},
		{"sso cookie", "POST", sso, same, "same-origin", credCookie, 200, 0, 0, 0, 1},
		{"sso bearer", "POST", sso, same, "", credBearer, 403, 0, 0, 0, 0},
		{"sso no origin", "POST", sso, "", "", credCookie, 403, 0, 0, 0, 0},
		{"sso cross-site", "POST", sso, same, "cross-site", credCookie, 403, 0, 0, 0, 0},
		{"sso get", "GET", sso, same, "", credCookie, 405, 0, 0, 0, 0},
	}
	for _, c := range cases {
		f := &fakeElev{}
		a := &API{Sessions: &mockSvc{}, Elevation: f, AWS: fakeAWS{}}
		w := httptest.NewRecorder()
		a.handleSession(w, elevReq(c.method, c.path, c.origin, c.site, c.kind))
		if w.Code != c.code || len(f.grants) != c.grants || len(f.drops) != c.drops || len(f.refused) != c.refused || f.sso != c.ss {
			t.Errorf("%s: code %d grants %v drops %v refused %v sso %d", c.name, w.Code, f.grants, f.drops, f.refused, f.sso)
		}
	}
	f := &fakeElev{}
	(&API{Sessions: &mockSvc{}, Elevation: f}).handleSession(httptest.NewRecorder(), elevReq("POST", el, "", "", credBearer))
	if len(f.refused) != 1 || f.refused[0] != "aws-1/bearer" {
		t.Fatalf("a bearer grant attempt is audited with its credential kind: %v", f.refused)
	}
	f = &fakeElev{}
	(&API{Sessions: &mockSvc{}, Elevation: f}).handleSession(httptest.NewRecorder(), elevReq("POST", el, "http://"+host, "", credCookie))
	if len(f.grants) != 1 || f.grants[0] != "aws-1/60/apply//cookie" {
		t.Fatalf("grant args %v", f.grants)
	}

	// csrfGuard turns a cross-origin cookie POST away before the handler runs;
	// through the real router that refusal is still audited, once, as a cookie
	// attempt, and only for POST .../aws/elevate.
	for _, c := range []struct {
		name, method, path string
		refused            int
	}{
		{"cross-origin elevate", "POST", el, 1},
		{"cross-origin sso-login", "POST", sso, 0},
		{"cross-origin drop", "DELETE", el, 0},
		{"cross-origin other route", "POST", "/api/sessions/aws-1/message", 0},
		{"cross-origin malformed id", "POST", "/api/sessions/" + strings.Repeat("x", 81) + "/aws/elevate", 0},
	} {
		f := &fakeElev{}
		s := &Server{AllowedHosts: []string{host}, API: &API{Sessions: &mockSvc{}, Elevation: f}, Terminal: http.NotFoundHandler()}
		ck := issueCookie(t, s)
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"minutes":60}`))
		r.Host = host
		r.Header.Set("Origin", "http://evil.example")
		r.AddCookie(ck)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || len(f.grants) != 0 || len(f.drops) != 0 || len(f.refused) != c.refused {
			t.Errorf("%s: code %d grants %v drops %v refused %v", c.name, w.Code, f.grants, f.drops, f.refused)
		}
		if c.refused == 1 && f.refused[0] != "aws-1/cookie" {
			t.Errorf("%s: audited as %v, want aws-1/cookie", c.name, f.refused)
		}
	}
	// A bearer POST is exempt from csrfGuard and is audited by the handler, once.
	{
		f := &fakeElev{}
		s := &Server{AllowedHosts: []string{host}, API: &API{Sessions: &mockSvc{}, Elevation: f}, Terminal: http.NotFoundHandler()}
		ts, err := auth.OpenTokenStore(t.TempDir() + "/tokens.json")
		if err != nil {
			t.Fatal(err)
		}
		s.Tokens = ts
		tok, _, err := ts.Create("t")
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", el, strings.NewReader(`{"minutes":60}`))
		r.Host = host
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || len(f.grants) != 0 || len(f.refused) != 1 || f.refused[0] != "aws-1/bearer" {
			t.Errorf("bearer via router: code %d grants %v refused %v", w.Code, f.grants, f.refused)
		}
	}
}

func TestElevateWithoutAnyCredentialIs401(t *testing.T) {
	f := &fakeElev{}
	s := &Server{AllowedHosts: []string{host}, API: &API{Sessions: &mockSvc{}, Elevation: f}, Terminal: http.NotFoundHandler()}
	r := httptest.NewRequest("POST", "/api/sessions/aws-1/aws/elevate", strings.NewReader(`{"minutes":60}`))
	r.Host = host
	r.Header.Set("Origin", "http://"+host)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || len(f.refused) != 0 {
		t.Fatalf("got %d refused %v", w.Code, f.refused)
	}
}

func TestElevateErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
		body string
	}{
		{fmt.Errorf("%w: this session's box is shared; restart it to get its own box", broker.ErrElevateRefused), 409, `"this session's box is shared; restart it to get its own box"`},
		{ErrElevationUnknown, 404, "no such session"},
		{errors.New("boom"), 500, "elevation failed"},
	}
	for _, c := range cases {
		a := &API{Sessions: &mockSvc{}, Elevation: &fakeElev{grantErr: c.err}}
		w := httptest.NewRecorder()
		a.handleSession(w, elevReq("POST", "/api/sessions/aws-1/aws/elevate", "http://"+host, "", credCookie))
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.body) || strings.Contains(w.Body.String(), "elevation refused:") {
			t.Errorf("%v: %d %s", c.err, w.Code, w.Body)
		}
		if c.code == 500 && strings.Contains(w.Body.String(), "boom") {
			t.Errorf("an unrecognised error leaks its text: %s", w.Body)
		}
	}
	for _, bad := range []string{"/api/sessions/a%2Fb/aws/elevate", "/api/sessions/" + strings.Repeat("x", 81) + "/aws/elevate",
		"/api/sessions/a%2Fb/aws/sso-login"} {
		f := &fakeElev{}
		w := httptest.NewRecorder()
		(&API{Sessions: &mockSvc{}, Elevation: f}).handleSession(w, elevReq("POST", bad, "", "", credBearer))
		if w.Code != 404 || len(f.refused) != 0 {
			t.Errorf("%s: %d refused=%v (a malformed id is never audited)", bad, w.Code, f.refused)
		}
	}
	w := httptest.NewRecorder()
	r := elevReq("POST", "/api/sessions/aws-1/aws/elevate", "http://"+host, "", credCookie)
	r.Body = io.NopCloser(strings.NewReader("{"))
	(&API{Sessions: &mockSvc{}, Elevation: &fakeElev{}}).handleSession(w, r)
	if w.Code != 400 {
		t.Fatalf("bad JSON: %d", w.Code)
	}
}

func TestSSOLoginFallbackCarriesTheCommand(t *testing.T) {
	f := &fakeElev{ssoErr: &SSOLoginError{Command: "aws sso login --profile sandbox-admin", Err: ssologin.ErrUnparsed}}
	w := httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}, Elevation: f}).handleSession(w, elevReq("POST", "/api/sessions/aws-1/aws/sso-login", "http://"+host, "", credCookie))
	if w.Code != 502 || !strings.Contains(w.Body.String(), `"command":"aws sso login --profile sandbox-admin"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), ssologin.ErrUnparsed.Error()) {
		t.Errorf("the underlying error text reached the response: %s", w.Body)
	}
	e := &SSOLoginError{Command: "c", Err: ssologin.ErrUnparsed}
	if !errors.Is(e, ssologin.ErrUnparsed) || e.Error() == "" || (&SSOLoginError{}).Error() == "" {
		t.Errorf("SSOLoginError: %q", e.Error())
	}
}

func TestSSOLoginSuccessBody(t *testing.T) {
	w := httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}, Elevation: &fakeElev{}}).handleSession(w, elevReq("POST", "/api/sessions/aws-1/aws/sso-login", "http://"+host, "", credCookie))
	for _, want := range []string{`"login":{`, `"state":"waiting"`, `"code":"ABCD-EFGH"`, `"uri_trusted":true`} {
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%d %s (missing %s)", w.Code, w.Body, want)
		}
	}
}

// The sign-in profile comes from the session's recorded binding, inside the
// service: the handler hands it the session id and nothing from the request.
func TestSSOLoginProfileComesFromTheSession(t *testing.T) {
	var gotID string
	calls := 0
	svc := ssoSpy{f: &fakeElev{}, on: func(id string) { gotID = id; calls++ }}
	r := elevReq("POST", "/api/sessions/aws-1/aws/sso-login?profile=evil&source_profile=evil", "http://"+host, "", credCookie)
	r.Body = io.NopCloser(strings.NewReader(`{"profile":"evil","source_profile":"evil","command":"rm -rf /"}`))
	r.Header.Set("X-Profile", "evil")
	w := httptest.NewRecorder()
	(&API{Sessions: &mockSvc{}, Elevation: svc}).handleSession(w, r)
	if w.Code != 200 || calls != 1 || gotID != "aws-1" {
		t.Fatalf("%d calls %d id %q", w.Code, calls, gotID)
	}
	if strings.Contains(w.Body.String(), "evil") {
		t.Errorf("request text echoed: %s", w.Body)
	}
}

type ssoSpy struct {
	f  *fakeElev
	on func(id string)
}

func (s ssoSpy) Grant(id string, m int, r, l, c string) (broker.ElevationView, error) {
	return s.f.Grant(id, m, r, l, c)
}
func (s ssoSpy) Drop(id, c string) (broker.ElevationView, error) { return s.f.Drop(id, c) }
func (s ssoSpy) Refused(id, c, r string)                         { s.f.Refused(id, c, r) }
func (s ssoSpy) SSOLogin(id string) (ssologin.State, error)      { s.on(id); return s.f.SSOLogin(id) }

func TestElevationRoutesAre404WithoutTheBroker(t *testing.T) {
	for _, p := range []string{"elevate", "sso-login"} {
		w := httptest.NewRecorder()
		(&API{Sessions: &mockSvc{}}).handleSession(w, elevReq("POST", "/api/sessions/aws-1/aws/"+p, "http://"+host, "", credCookie))
		if w.Code != 404 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}
