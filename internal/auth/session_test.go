package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testSession() *Session {
	return NewSession([]byte("0123456789abcdef0123456789abcdef"), 30*24*time.Hour)
}

func issued(s *Session, secure bool) *http.Cookie {
	w := httptest.NewRecorder()
	s.Issue(w, secure)
	return w.Result().Cookies()[0]
}

func TestSession_ValidRoundTrip(t *testing.T) {
	s := testSession()
	ck := issued(s, false)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	ok, refresh := s.Verify(r)
	if !ok || refresh {
		t.Fatalf("fresh cookie: ok=%v refresh=%v, want true,false", ok, refresh)
	}
}

func TestSession_Expired(t *testing.T) {
	s := testSession()
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }
	ck := issued(s, false)
	s.now = func() time.Time { return base.Add(31 * 24 * time.Hour) } // past ttl
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	if ok, _ := s.Verify(r); ok {
		t.Fatal("expired cookie accepted")
	}
}

func TestSession_Tampered(t *testing.T) {
	s := testSession()
	ck := issued(s, false)
	// Deterministic same-length tamper: flip the last signature char to a
	// guaranteed-different value (appending would test length-mismatch instead).
	b := []byte(ck.Value)
	if last := len(b) - 1; b[last] == '0' {
		b[last] = '1'
	} else {
		b[last] = '0'
	}
	ck.Value = string(b)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	if ok, _ := s.Verify(r); ok {
		t.Fatal("tampered cookie accepted")
	}
}

func TestSession_SlidingRefresh(t *testing.T) {
	s := testSession()
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }
	ck := issued(s, false)
	// 2 days later: still valid, past the 24h refresh threshold.
	s.now = func() time.Time { return base.Add(2 * 24 * time.Hour) }
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	ok, refresh := s.Verify(r)
	if !ok || !refresh {
		t.Fatalf("aged cookie: ok=%v refresh=%v, want true,true", ok, refresh)
	}
}

func TestSession_SecureFlag(t *testing.T) {
	s := testSession()
	if !issued(s, true).Secure {
		t.Fatal("secure=true not set")
	}
	if issued(s, false).Secure {
		t.Fatal("secure=false leaked as true")
	}
}

func TestSession_NoCookie(t *testing.T) {
	s := testSession()
	r := httptest.NewRequest("GET", "/", nil)
	if ok, _ := s.Verify(r); ok {
		t.Fatal("missing cookie accepted")
	}
}

// The browser keeps the cookie for a grace past its signed expiry, so the server can
// tell "your login expired" from "never signed in". Issue sets Expires = exp + grace.
func TestSession_CookieOutlivesItsExpiryByTheGrace(t *testing.T) {
	s := testSession()
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }
	ck := issued(s, false)
	exp := base.Add(30 * 24 * time.Hour).Unix()
	if expiredGrace <= 0 {
		t.Fatal("expiredGrace must be positive, or an expired visit looks like a first one")
	}
	if got, want := ck.Expires.Unix(), exp+int64(expiredGrace/time.Second); got != want {
		t.Fatalf("Expires=%d want exp+grace=%d", got, want)
	}
	// The signed value still carries the real expiry, not the grace one.
	if !strings.HasPrefix(ck.Value, strconv.FormatInt(exp, 10)+".") {
		t.Fatalf("cookie value %q does not carry exp=%d", ck.Value, exp)
	}
}

// The grace buys no authority: Verify rejects the cookie at the signed expiry and at
// every later moment, including inside the grace and after it.
func TestSession_GraceGivesNoAuthority(t *testing.T) {
	s := testSession()
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }
	ck := issued(s, false)
	exp := time.Unix(base.Add(30*24*time.Hour).Unix(), 0)
	for _, c := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"1s before exp", exp.Add(-time.Second), true},
		{"at exp", exp, false},
		{"1s after exp", exp.Add(time.Second), false},
		{"mid grace", exp.Add(expiredGrace / 2), false},
		{"1s before the grace ends", exp.Add(expiredGrace - time.Second), false},
		{"at the end of the grace", exp.Add(expiredGrace), false},
		{"after the grace", exp.Add(expiredGrace + time.Second), false},
	} {
		s.now = func() time.Time { return c.at }
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(ck)
		if ok, _ := s.Verify(r); ok != c.want {
			t.Fatalf("%s: Verify=%v want %v", c.name, ok, c.want)
		}
	}
}

// The sliding refresh goes through Issue, so the re-issued cookie has the same grace.
func TestSession_RefreshReissueKeepsTheGrace(t *testing.T) {
	s := testSession()
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }
	ck := issued(s, false)
	later := base.Add(2 * 24 * time.Hour)
	s.now = func() time.Time { return later }
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	if ok, refresh := s.Verify(r); !ok || !refresh {
		t.Fatalf("aged cookie: ok=%v refresh=%v, want true,true", ok, refresh)
	}
	re := issued(s, false)
	if got, want := re.Expires.Unix(), later.Add(30*24*time.Hour).Unix()+int64(expiredGrace/time.Second); got != want {
		t.Fatalf("re-issued Expires=%d want %d", got, want)
	}
}

// Logout still deletes the cookie outright: no grace on the clearing cookie.
func TestSession_ClearDeletesTheCookie(t *testing.T) {
	w := httptest.NewRecorder()
	testSession().Clear(w)
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Name != CookieName || c[0].Value != "" || c[0].MaxAge >= 0 || !c[0].Expires.IsZero() {
		t.Fatalf("Clear = %+v, want an empty cookie with MaxAge<0 and no Expires", c)
	}
}
