package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/auth"
)

func loginServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		Session:  auth.NewSession([]byte("0123456789abcdef0123456789abcdef"), 30*24*time.Hour),
		Password: "s3cret",
	}
}

func postForm(target string, form url.Values) *http.Request {
	r := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+r.Host)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func TestLogin_CorrectPasswordSetsCookie(t *testing.T) {
	s := loginServer(t)
	w := httptest.NewRecorder()
	s.handleLogin(w, postForm("/login", url.Values{"password": {"s3cret"}}))
	res := w.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("code=%d want 303", res.StatusCode)
	}
	if len(res.Cookies()) == 0 || res.Cookies()[0].Name != auth.CookieName {
		t.Fatal("no session cookie set")
	}
}

func TestLogin_WrongPasswordNoCookie(t *testing.T) {
	s := loginServer(t)
	w := httptest.NewRecorder()
	// A fetch/XHR caller (no navigate/text-html) gets a plain 401.
	s.handleLogin(w, postForm("/login", url.Values{"password": {"nope"}}))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("cookie set on failed login")
	}
}

func TestLogin_WrongPasswordHTMLRedirects(t *testing.T) {
	s := loginServer(t)
	r := postForm("/login", url.Values{"password": {"nope"}})
	r.Header.Set("Sec-Fetch-Mode", "navigate") // a real browser form submit
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?e=1" {
		t.Fatalf("code=%d loc=%q want 303 /login?e=1", w.Code, w.Header().Get("Location"))
	}
}

func TestLogin_CrossSitePostRejected(t *testing.T) {
	s := loginServer(t)
	r := postForm("/login", url.Values{"password": {"s3cret"}})
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d want 403", w.Code)
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	s := loginServer(t)
	w := httptest.NewRecorder()
	s.handleLogout(w, postForm("/logout", url.Values{}))
	c := w.Result().Cookies()
	if len(c) == 0 || c[0].MaxAge >= 0 {
		t.Fatal("logout did not expire the cookie")
	}
}
