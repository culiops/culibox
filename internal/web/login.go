package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

// loginLimiter throttles failed logins. The lock is held across the sleep, so
// concurrent guesses are serialized rather than each sleeping in parallel, and
// the delay escalates with consecutive failures and resets on a success. One
// operator, so a single global counter is the whole threat model — no per-IP
// tracking needed.
type loginLimiter struct {
	mu       sync.Mutex
	failures int
}

var logins loginLimiter

func (l *loginLimiter) penalize() {
	l.mu.Lock()
	defer l.mu.Unlock() // held across the sleep → serializes parallel guesses
	l.failures++
	d := time.Duration(l.failures) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	time.Sleep(d)
}

func (l *loginLimiter) reset() {
	l.mu.Lock()
	l.failures = 0
	l.mu.Unlock()
}

// handleLogin verifies the master password (POST) and issues a session cookie.
// GET /login is served as a static form by the router; this handler owns POST.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	pass := r.FormValue("password")
	// Compare fixed-length digests: subtle.ConstantTimeCompare is constant-time
	// only for equal-length inputs, so comparing the raw strings would leak the
	// password length via timing.
	got := sha256.Sum256([]byte(pass))
	want := sha256.Sum256([]byte(s.Password))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		logins.penalize()
		// A browser form submit is a navigation: bounce back to the form with an
		// error marker. A fetch/XHR caller gets a plain 401.
		if wantsHTMLResponse(r) {
			http.Redirect(w, r, "/login?e=1", http.StatusSeeOther)
			return
		}
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	logins.reset()
	s.Session.Issue(w, secureCookie(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	s.Session.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
