package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/secretstore"
)

// StoreService is the encrypted store as the web layer sees it
// (*secretstore.Store satisfies it).
type StoreService interface {
	Status() secretstore.Status
	Unlock(pass []byte, ttl time.Duration, actor string) error
	Lock(actor string) error
}

const (
	storeActor        = "web"
	storeBodyMax      = 1 << 10 // an unlock body is one short passphrase
	unlockMaxFailures = 5
	unlockWindow      = 15 * time.Minute
)

// unlockLimiter allows unlockMaxFailures wrong passphrases per unlockWindow for
// the whole daemon (not per client: one operator, and a bearer or a stolen
// cookie must not multiply the guesses). Its mutex is held for a whole attempt,
// so concurrent requests cannot all pass the check before any failure lands.
// The zero value works; now is injectable for tests.
type unlockLimiter struct {
	mu       sync.Mutex
	failures []time.Time
	now      func() time.Time
}

func (l *unlockLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// prune drops failures at least unlockWindow old. Caller holds mu.
func (l *unlockLimiter) prune(now time.Time) {
	kept := l.failures[:0]
	for _, f := range l.failures {
		if now.Sub(f) < unlockWindow {
			kept = append(kept, f)
		}
	}
	l.failures = kept
}

// limited reports whether another attempt is refused. Caller holds mu.
func (l *unlockLimiter) limited() bool {
	l.prune(l.clock())
	return len(l.failures) >= unlockMaxFailures
}

func (l *unlockLimiter) fail() { l.failures = append(l.failures, l.clock()) }

// secretBytes is a JSON string decoded straight into a byte slice the handler
// can zero. A string with no escapes is copied without ever becoming a Go
// string; one with escapes falls back to the standard decoder.
type secretBytes []byte

func (b *secretBytes) UnmarshalJSON(data []byte) error {
	if n := len(data); n >= 2 && data[0] == '"' && data[n-1] == '"' && !bytes.ContainsAny(data[1:n-1], `\"`) {
		*b = append((*b)[:0], data[1:n-1]...)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*b = []byte(s)
	return nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// storeStatusBody is the GET /api/store answer: state and counts, never a
// value, an id or a path.
type storeStatusBody struct {
	Initialized     bool       `json:"initialized"`
	Locked          bool       `json:"locked"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Platform        string     `json:"platform"`
	Values          int        `json:"values"`
	InUse           bool       `json:"in_use"`
	PlaintextCopies []string   `json:"plaintext_copies"`
}

// handleStore is GET /api/store. Any authenticated caller, bearer included.
func (s *Server) handleStore(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st := s.Store.Status()
	body := storeStatusBody{Initialized: st.Initialized, Locked: st.Locked, Platform: st.Platform,
		Values: st.Values, InUse: s.StoreInUse, PlaintextCopies: append([]string{}, s.StorePlaintextCopies...)}
	if !st.ExpiresAt.IsZero() {
		t := st.ExpiresAt
		body.ExpiresAt = &t
	}
	writeJSON(w, http.StatusOK, body)
}

// storeBrowserOnly is the one rule for changing the store's lock: this page's
// own signed-in browser session, on this origin (the handleElevate pattern). A
// bearer token is refused, so a leaked token cannot unlock the store.
func storeBrowserOnly(w http.ResponseWriter, r *http.Request, what string) bool {
	if credKindOf(r) != credCookie || !strictSameOrigin(r) {
		writeErr(w, http.StatusForbidden, what+" the store needs this page's signed-in browser session")
		return false
	}
	return true
}

// handleStoreUnlock is POST /api/store/unlock {"passphrase"}. The body is read
// into memory the handler zeroes, and is never logged or echoed.
func (s *Server) handleStoreUnlock(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !storeBrowserOnly(w, r, "unlocking") {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, storeBodyMax))
	defer zero(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	var req struct {
		Passphrase secretBytes `json:"passphrase"`
	}
	defer func() { zero(req.Passphrase) }()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.Decode(&json.RawMessage{}) != io.EOF || len(req.Passphrase) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	s.storeLimit.mu.Lock()
	defer s.storeLimit.mu.Unlock()
	if s.storeLimit.limited() {
		if s.StoreAudit != nil {
			_ = s.StoreAudit.RecordStore(secretstore.Event{Action: "unlock_rate_limited", Actor: storeActor})
		}
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, "too many wrong passphrases — try again in 15 minutes")
		return
	}
	switch err := s.Store.Unlock(req.Passphrase, s.StoreUnlockTTL, storeActor); {
	case err == nil:
		s.storeLimit.failures = nil
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, secretstore.ErrBadPassphrase):
		s.storeLimit.fail()
		writeErr(w, http.StatusUnauthorized, "wrong passphrase")
	case errors.Is(err, secretstore.ErrNotApplicable):
		writeErr(w, http.StatusBadRequest, "this platform's store unlocks with the Keychain, not a passphrase")
	case errors.Is(err, secretstore.ErrNotInitialized):
		writeErr(w, http.StatusConflict, "the store is not initialized — run \"agent-box store init\"")
	default:
		// The error text is not shown: it can name a path.
		writeErr(w, http.StatusInternalServerError, "could not unlock the store")
	}
}

// handleStoreLock is POST /api/store/lock: cheap, but under the same rule.
func (s *Server) handleStoreLock(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !storeBrowserOnly(w, r, "locking") {
		return
	}
	switch err := s.Store.Lock(storeActor); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, secretstore.ErrNotApplicable):
		writeErr(w, http.StatusBadRequest, "this platform's store locks with the Keychain")
	default:
		writeErr(w, http.StatusInternalServerError, "could not lock the store")
	}
}
