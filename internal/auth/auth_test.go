package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

func TestBasicAuth(t *testing.T) {
	called := false
	h := BasicAuth("me", "secret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("me", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != 200 {
		t.Fatalf("correct creds failed: code=%d called=%v", rec.Code, called)
	}

	called = false
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("me", "wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if called || rec.Code != 401 {
		t.Fatalf("wrong creds: code=%d called=%v", rec.Code, called)
	}
}

func TestResolveCred(t *testing.T) {
	t.Setenv("AC_PASS", "s3cr3t")
	u, p, err := ResolveCred(config.Config{BasicAuthUser: "me", BasicAuthPassEnv: "AC_PASS"})
	if err != nil || u != "me" || p != "s3cr3t" {
		t.Fatalf("resolve: %v %q %q", err, u, p)
	}
	if _, _, err := ResolveCred(config.Config{BasicAuthUser: "me", BasicAuthPassEnv: "MISSING_VAR_XYZ"}); err == nil {
		t.Fatal("missing env should error")
	}
}

func TestCookieAuth(t *testing.T) {
	ca, err := NewCookieAuth()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	ca.Issue(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie issued")
	}

	req := httptest.NewRequest("GET", "/ws/x", nil)
	req.AddCookie(cookies[0])
	if !ca.Valid(req) {
		t.Fatal("issued cookie should be valid")
	}
	if ca.Valid(httptest.NewRequest("GET", "/ws/x", nil)) {
		t.Fatal("no cookie should be invalid")
	}
}
