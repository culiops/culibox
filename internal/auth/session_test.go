package auth

import (
	"net/http"
	"net/http/httptest"
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
	ck.Value = ck.Value[:len(ck.Value)-1] + "0" // flip last sig char
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
