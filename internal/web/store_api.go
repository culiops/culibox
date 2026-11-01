package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	Init(pass []byte, actor string) error
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

// wait is how long until another attempt is allowed: zero when not limited,
// else until the oldest failure leaves the window. Caller holds mu.
func (l *unlockLimiter) wait() time.Duration {
	now := l.clock()
	l.prune(now)
	if len(l.failures) < unlockMaxFailures {
		return 0
	}
	return l.failures[0].Add(unlockWindow).Sub(now)
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

// storeUnavailableBody says why the macOS Keychain did not give the store key:
// kind is "access", "key_lost" or "store_unreadable"; reason is short printable
// text; fix is what the operator does (always present).
type storeUnavailableBody struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
}

func unavailableBody(ue *secretstore.UnavailableError) *storeUnavailableBody {
	if ue == nil {
		return nil
	}
	return &storeUnavailableBody{Kind: string(ue.Kind), Reason: ue.Reason, Fix: ue.Fix()}
}

// storeStatusBody is the GET /api/store answer: state and counts, never a
// value or an id. The one place a path can appear is unavailable.fix, which
// names the store directory for key_lost / store_unreadable; the endpoint is
// operator-only, by design.
type storeStatusBody struct {
	Initialized     bool                  `json:"initialized"`
	Locked          bool                  `json:"locked"`
	ExpiresAt       *time.Time            `json:"expires_at"`
	Platform        string                `json:"platform"`
	Values          int                   `json:"values"`
	InUse           bool                  `json:"in_use"`
	PlaintextCopies []string              `json:"plaintext_copies"`
	Unavailable     *storeUnavailableBody `json:"unavailable,omitempty"`
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
		Values: st.Values, InUse: s.storeInUse(), PlaintextCopies: append([]string{}, s.StorePlaintextCopies...),
		Unavailable: unavailableBody(st.Unavailable)}
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
	if wait := s.storeLimit.wait(); wait > 0 {
		if s.StoreAudit != nil {
			_ = s.StoreAudit.RecordStore(secretstore.Event{Action: "unlock_rate_limited", Actor: storeActor})
		}
		secs := int((wait + time.Second - 1) / time.Second) // round up
		mins := (secs + 59) / 60
		unit := "minutes"
		if mins == 1 {
			unit = "minute"
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("too many wrong passphrases — try again in %d %s", mins, unit))
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
	case errors.Is(err, secretstore.ErrKeeperUnavailable):
		writeStoreUnavailable(w, err)
	default:
		// The error text is not shown: it can name a path. (Only the typed
		// Keychain failure above carries a path, deliberately, in its fix.)
		writeErr(w, http.StatusInternalServerError, "could not unlock the store")
	}
}

// writeStoreUnavailable answers a Keychain failure: 503 store_unavailable with
// the UnavailableError's own message (reason + fix, which names the store
// directory only for key_lost / store_unreadable). Never 423: nothing to unlock.
func writeStoreUnavailable(w http.ResponseWriter, err error) {
	msg := secretstore.ErrKeeperUnavailable.Error()
	var ue *secretstore.UnavailableError
	if errors.As(err, &ue) {
		msg = ue.Error()
	}
	writeErrCode(w, http.StatusServiceUnavailable, msg, "store_unavailable")
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

// storeInUse is whether a configured reference reads the store, asked at
// request time because a credential save can change it. nil = false.
func (s *Server) storeInUse() bool { return s.StoreInUse != nil && s.StoreInUse() }
