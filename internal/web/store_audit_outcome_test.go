package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/secretstore"
)

// realStoreSrv is a Server over the real secretstore.Store with a fake keeper
// that refuses to save, the way a kernel keyring does in a box whose seccomp
// profile blocks keyctl (#288).
func realStoreSrv(t *testing.T) (*Server, *secretstore.FakeKeeper, *fakeSecretAudit) {
	t.Helper()
	k := secretstore.NewFakeKeeper(true)
	au := &fakeSecretAudit{}
	srv := &Server{Store: secretstore.Open(t.TempDir(), k, au), StoreAudit: au, StoreUnlockTTL: 48 * time.Hour,
		SeccompProbe: okProbe(true, nil)}
	return srv, k, au
}

func auditActions(au *fakeSecretAudit) []string {
	var out []string
	for _, e := range au.events {
		out = append(out, e.Action)
	}
	return out
}

func requireActions(t *testing.T, au *fakeSecretAudit, want ...string) {
	t.Helper()
	got := auditActions(au)
	if len(got) != len(want) {
		t.Fatalf("audit trail %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit trail %v, want %v", got, want)
		}
	}
}

// POST /api/store/init: the store is created, the unlock that follows is refused
// by the keeper, and the trail says so instead of leaving a bare "unlocked".
func TestStoreInitKeeperRefusesUnlockIsAudited(t *testing.T) {
	srv, k, au := realStoreSrv(t)
	k.FailSave(errors.New("kernel keyring add: operation not permitted"))
	w := do(srv.handleStoreInit, storeReq("POST", "/api/store/init", `{"passphrase":"correct horse battery"}`, credOrigin, credCookie))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"unlocked":false`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	requireActions(t, au, "init", "unlocked", "unlock_failed")
}

// POST /api/store/unlock: 500, the store stays locked, and the same two rows.
func TestStoreUnlockKeeperRefusesIsAudited(t *testing.T) {
	srv, k, au := realStoreSrv(t)
	if err := srv.Store.Init([]byte("correct horse battery"), "cli"); err != nil {
		t.Fatal(err)
	}
	au.events = nil
	k.FailSave(errors.New("kernel keyring add: operation not permitted"))
	w := do(srv.handleStoreUnlock, storeReq("POST", "/api/store/unlock", `{"passphrase":"correct horse battery"}`, credOrigin, credCookie))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !srv.Store.Status().Locked {
		t.Fatal("store is not locked")
	}
	requireActions(t, au, "unlocked", "unlock_failed")
}
