package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/secretstore"
)

type fakeSecretStore struct {
	mu        sync.Mutex
	st        secretstore.Status
	pass      string
	unlocks   []string // actor/ttl per successful unlock
	attempts  int
	locks     []string
	unlockErr error
}

func (f *fakeSecretStore) Status() secretstore.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeSecretStore) Unlock(pass []byte, ttl time.Duration, actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.unlockErr != nil {
		return f.unlockErr
	}
	if string(pass) != f.pass {
		return secretstore.ErrBadPassphrase
	}
	f.st.Locked = false
	f.unlocks = append(f.unlocks, actor+"/"+ttl.String())
	return nil
}

func (f *fakeSecretStore) Lock(actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Locked = true
	f.locks = append(f.locks, actor)
	return nil
}

type fakeSecretAudit struct{ events []secretstore.Event }

func (f *fakeSecretAudit) RecordStore(e secretstore.Event) error {
	f.events = append(f.events, e)
	return nil
}

func storeSrv() (*Server, *fakeSecretStore, *fakeSecretAudit) {
	fs := &fakeSecretStore{
		st:   secretstore.Status{Initialized: true, Locked: true, Values: 3, Platform: "linux", PassphraseMode: true},
		pass: "correct horse battery",
	}
	au := &fakeSecretAudit{}
	return &Server{Store: fs, StoreAudit: au, StoreInUse: true, StoreUnlockTTL: 48 * time.Hour,
		StorePlaintextCopies: []string{"CLAUDE_TOKEN"}}, fs, au
}

func storeReq(method, path, body, origin string, kind credKind) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r.WithContext(context.WithValue(r.Context(), credKindKey, kind))
}

func do(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestStoreStatus(t *testing.T) {
	srv, fs, _ := storeSrv()
	for _, kind := range []credKind{credCookie, credBearer} {
		w := do(srv.handleStore, storeReq("GET", "/api/store", "", "", kind))
		if w.Code != 200 {
			t.Fatalf("kind %v: %d %s", kind, w.Code, w.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["initialized"] != true || got["locked"] != true || got["platform"] != "linux" ||
			got["values"] != float64(3) || got["in_use"] != true || got["expires_at"] != nil {
			t.Fatalf("body %v", got)
		}
		if c, _ := got["plaintext_copies"].([]any); len(c) != 1 || c[0] != "CLAUDE_TOKEN" {
			t.Fatalf("plaintext_copies %v", got["plaintext_copies"])
		}
	}
	exp := time.Unix(1_800_000_000, 0).UTC()
	fs.st.Locked, fs.st.ExpiresAt = false, exp
	var got struct {
		ExpiresAt *time.Time `json:"expires_at"`
		Locked    bool       `json:"locked"`
	}
	w := do(srv.handleStore, storeReq("GET", "/api/store", "", "", credCookie))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Locked || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("unlocked body %s", w.Body)
	}
	// An empty copy list is [] on the wire, never null.
	srv.StorePlaintextCopies = nil
	w = do(srv.handleStore, storeReq("GET", "/api/store", "", "", credCookie))
	if !strings.Contains(w.Body.String(), `"plaintext_copies":[]`) {
		t.Fatalf("want []: %s", w.Body)
	}
	if w := do(srv.handleStore, storeReq("POST", "/api/store", "", "", credCookie)); w.Code != 405 {
		t.Fatalf("POST /api/store: %d", w.Code)
	}
}

func TestStoreNilIs404(t *testing.T) {
	srv := &Server{}
	for _, c := range []struct {
		h    http.HandlerFunc
		m, p string
	}{
		{srv.handleStore, "GET", "/api/store"},
		{srv.handleStoreUnlock, "POST", "/api/store/unlock"},
		{srv.handleStoreLock, "POST", "/api/store/lock"},
	} {
		if w := do(c.h, storeReq(c.m, c.p, `{}`, "http://"+host, credCookie)); w.Code != 404 {
			t.Errorf("%s: %d", c.p, w.Code)
		}
	}
}

func TestStoreUnlock(t *testing.T) {
	same := "http://" + host
	good := `{"passphrase":"correct horse battery"}`
	cases := []struct {
		name, method, body, origin string
		kind                       credKind
		code                       int
		msg                        string
	}{
		{"bearer", "POST", good, same, credBearer, 403, "unlocking the store needs this page's signed-in browser session"},
		{"no credential", "POST", good, same, credNone, 403, ""},
		{"cookie no origin", "POST", good, "", credCookie, 403, ""},
		{"cookie cross origin", "POST", good, "http://evil.example", credCookie, 403, ""},
		{"oversize", "POST", `{"passphrase":"` + strings.Repeat("a", 2000) + `"}`, same, credCookie, 400, ""},
		{"unknown field", "POST", `{"passphrase":"x","extra":1}`, same, credCookie, 400, ""},
		{"two values", "POST", good + good, same, credCookie, 400, ""},
		{"not json", "POST", `nope`, same, credCookie, 400, ""},
		{"empty passphrase", "POST", `{"passphrase":""}`, same, credCookie, 400, ""},
		{"wrong", "POST", `{"passphrase":"wrong wrong wrong"}`, same, credCookie, 401, "wrong passphrase"},
		{"get", "GET", "", same, credCookie, 405, ""},
		{"right", "POST", good, same, credCookie, 204, ""},
	}
	for _, c := range cases {
		srv, fs, _ := storeSrv()
		w := do(srv.handleStoreUnlock, storeReq(c.method, "/api/store/unlock", c.body, c.origin, c.kind))
		if w.Code != c.code {
			t.Errorf("%s: code %d (%s), want %d", c.name, w.Code, w.Body, c.code)
		}
		if c.msg != "" && !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("%s: body %s, want %q", c.name, w.Body, c.msg)
		}
		if c.code == 204 {
			if len(fs.unlocks) != 1 || fs.unlocks[0] != "web/48h0m0s" || fs.st.Locked {
				t.Errorf("%s: unlocks %v locked %v", c.name, fs.unlocks, fs.st.Locked)
			}
		} else if len(fs.unlocks) != 0 {
			t.Errorf("%s: store was unlocked", c.name)
		}
		if c.code == 403 && fs.attempts != 0 {
			t.Errorf("%s: a refused request reached the store", c.name)
		}
	}
}

func TestStoreUnlockErrorMapping(t *testing.T) {
	same := "http://" + host
	for _, c := range []struct {
		err  error
		code int
	}{
		{secretstore.ErrNotApplicable, 400},
		{secretstore.ErrNotInitialized, 409},
		{errors.New("disk on fire /secret/path"), 500},
	} {
		srv, fs, _ := storeSrv()
		fs.unlockErr = c.err
		w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", `{"passphrase":"abcdefghijkl"}`, same, credCookie))
		if w.Code != c.code || strings.Contains(w.Body.String(), "disk on fire") {
			t.Errorf("%v: %d %s", c.err, w.Code, w.Body)
		}
	}
}

func TestUnlockRateLimit(t *testing.T) {
	same := "http://" + host
	srv, fs, au := storeSrv()
	now := time.Unix(1_800_000_000, 0)
	srv.storeLimit.now = func() time.Time { return now }
	wrong := `{"passphrase":"wrong wrong wrong"}`
	right := `{"passphrase":"correct horse battery"}`
	for i := 1; i <= 5; i++ {
		if w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", wrong, same, credCookie)); w.Code != 401 {
			t.Fatalf("failure %d: %d", i, w.Code)
		}
	}
	before := fs.attempts
	w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", right, same, credCookie))
	if w.Code != 429 {
		t.Fatalf("6th attempt (right passphrase): %d %s", w.Code, w.Body)
	}
	if fs.attempts != before || len(fs.unlocks) != 0 {
		t.Fatalf("a limited attempt must not reach the store")
	}
	if len(au.events) != 1 || au.events[0].Action != "unlock_rate_limited" || au.events[0].Actor != "web" ||
		strings.Contains(au.events[0].Detail, "correct") {
		t.Fatalf("audit %+v", au.events)
	}
	now = now.Add(15 * time.Minute)
	if w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", right, same, credCookie)); w.Code != 204 {
		t.Fatalf("after the window: %d %s", w.Code, w.Body)
	}
}

// The window slides: a failure leaves it 15 minutes after it happened.
func TestUnlockRateLimitSlides(t *testing.T) {
	same := "http://" + host
	srv, _, _ := storeSrv()
	t0 := time.Unix(1_800_000_000, 0)
	now := t0
	srv.storeLimit.now = func() time.Time { return now }
	wrong := `{"passphrase":"wrong wrong wrong"}`
	for i := 0; i < 5; i++ {
		do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", wrong, same, credCookie))
	}
	now = t0.Add(14 * time.Minute)
	if w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", `{"passphrase":"correct horse battery"}`, same, credCookie)); w.Code != 429 {
		t.Fatalf("inside the window: %d", w.Code)
	}
	now = t0.Add(15 * time.Minute)
	if w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", `{"passphrase":"correct horse battery"}`, same, credCookie)); w.Code != 204 {
		t.Fatalf("after 15m: %d", w.Code)
	}
}

func TestUnlockNeverLogged(t *testing.T) {
	same := "http://" + host
	var buf bytes.Buffer
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })

	srv, _, _ := storeSrv()
	const wrongPass, rightPass = "Zx-wrong-pass-9917", "correct horse battery"
	srv.Store.(*fakeSecretStore).pass = rightPass
	var out strings.Builder
	for _, p := range []string{wrongPass, rightPass} {
		w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", `{"passphrase":"`+p+`"}`, same, credCookie))
		out.WriteString(w.Body.String())
	}
	// And through the full handler chain's mux, if the routes are registered.
	if strings.Contains(buf.String(), wrongPass) || strings.Contains(buf.String(), rightPass) ||
		strings.Contains(out.String(), wrongPass) || strings.Contains(out.String(), rightPass) {
		t.Fatalf("a passphrase leaked.\nlog: %q\nresponses: %q", buf.String(), out.String())
	}
}

func TestStoreLock(t *testing.T) {
	same := "http://" + host
	for _, c := range []struct {
		name, origin string
		kind         credKind
		code         int
	}{
		{"bearer", same, credBearer, 403},
		{"cookie no origin", "", credCookie, 403},
		{"cookie cross origin", "http://evil.example", credCookie, 403},
		{"cookie same origin", same, credCookie, 204},
	} {
		srv, fs, _ := storeSrv()
		fs.st.Locked = false
		w := do(srv.handleStoreLock, storeReq("POST", "/api/store/lock", "", c.origin, c.kind))
		if w.Code != c.code {
			t.Errorf("%s: %d", c.name, w.Code)
		}
		if (c.code == 204) != (len(fs.locks) == 1 && fs.locks[0] == "web") {
			t.Errorf("%s: locks %v", c.name, fs.locks)
		}
	}
	srv, _, _ := storeSrv()
	if w := do(srv.handleStoreLock, storeReq("GET", "/api/store/lock", "", "", credCookie)); w.Code != 405 {
		t.Fatalf("GET lock: %d", w.Code)
	}
}

func TestStoreRoutesOnlyWithAStore(t *testing.T) {
	srv, _, _ := storeSrv()
	srv.AllowedHosts = []string{host}
	// Registered: an unauthenticated API call is refused by the guard (401),
	// not routed to the static file server's 404.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/store", nil)
	r.Host = host
	srv.Handler().ServeHTTP(w, r)
	if w.Code == 404 {
		t.Fatalf("with a store the route must exist: %d", w.Code)
	}
	none := &Server{AllowedHosts: []string{host}}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/api/store", nil)
	r.Host = host
	none.Handler().ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatalf("without a store: %d", w.Code)
	}
}
