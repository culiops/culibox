package web

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"
)

// loginLimiter slows failed logins to blunt online guessing. A single operator
// never logs in fast enough for this to chafe.
type loginLimiter struct {
	mu   sync.Mutex
	fail time.Time
}

var logins loginLimiter

func (l *loginLimiter) penalize() {
	l.mu.Lock()
	l.fail = time.Now()
	l.mu.Unlock()
	time.Sleep(500 * time.Millisecond)
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
	if subtle.ConstantTimeCompare([]byte(pass), []byte(s.Password)) != 1 {
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
