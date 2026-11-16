package web

import (
	"errors"
	"os"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
)

// readOnlyValueRoutes is a token-routes fixture whose credentials.json holds a
// token entry with a field this build does not know (what a newer build wrote
// before a binary-only rollback). The entry names store id "newer-secret", so
// this build's in-use scan cannot see it, and the file is read-only (#287).
func readOnlyValueRoutes(t *testing.T) *tokenRoutes {
	t.Helper()
	a := newTokenRoutes(t)
	body := `{"static_tokens":[{"name":"newer","env":["NEWER"],"value":{"type":"store","id":"newer-secret"},"future_field":true}]}`
	if err := os.WriteFile(credfile.Path(a.fx.dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, bad, err := credfile.Load(a.fx.dir)
	if bad || err != nil || loaded.CanSave() == nil {
		t.Fatalf("setup: the file must load with content this build cannot read: bad %v err %v canSave %v", bad, err, loaded.CanSave())
	}
	a.fx.cm = NewCredentialManager(config.Config{}, loaded, a.fx.dir, a.fx.pm, a.fx.vals, a.fx.rec, a.fx.chk,
		func(config.Config) {}, nil)
	a.srv.Creds = a.fx.cm
	if refs := a.fx.cm.storeRefsLocked()["newer-secret"]; len(refs) != 0 {
		t.Fatalf("setup: this build should not see the entry's reference, got %v", refs)
	}
	a.fx.vals.m["newer-secret"] = []byte("pasted-token")
	return a
}

func TestRemoveValueRefusedWhileFileReadOnly(t *testing.T) {
	a := readOnlyValueRoutes(t)
	err := a.fx.cm.RemoveValue("newer-secret")
	if !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("RemoveValue = %v, want ErrCredReadOnly", err)
	}
	if string(a.fx.vals.m["newer-secret"]) != "pasted-token" || len(a.fx.vals.removed) != 0 {
		t.Fatalf("the value was removed: %v / %v", a.fx.vals.m, a.fx.vals.removed)
	}

	code, m, raw := a.call("DELETE", pathValues+"newer-secret", "")
	if code != 409 || m["code"] != "file_read_only" || m["error"] != ErrCredReadOnly.Error() {
		t.Fatalf("route = %d %s, want 409 file_read_only", code, raw)
	}
	if !a.fx.vals.has("newer-secret") {
		t.Fatal("the route deleted the value")
	}

	// An id nothing names is refused too: the scan cannot rule the unreadable
	// entry out, and an unused id cannot be told from one it uses.
	a.fx.vals.m["other"] = []byte("x")
	if err := a.fx.cm.RemoveValue("other"); !errors.Is(err, ErrCredReadOnly) || !a.fx.vals.has("other") {
		t.Fatalf("RemoveValue(other) = %v, present %v", err, a.fx.vals.has("other"))
	}
}

func TestReplaceValueRefusedWhileFileReadOnly(t *testing.T) {
	a := readOnlyValueRoutes(t)
	if err := a.fx.cm.SetValue("newer-secret", []byte("overwritten")); !errors.Is(err, ErrCredReadOnly) {
		t.Fatalf("SetValue = %v, want ErrCredReadOnly", err)
	}
	if string(a.fx.vals.m["newer-secret"]) != "pasted-token" {
		t.Fatalf("the value was replaced: %q", a.fx.vals.m["newer-secret"])
	}
	code, m, raw := a.call("PUT", pathValues+"newer-secret", `{"value":"overwritten"}`)
	if code != 409 || m["code"] != "file_read_only" {
		t.Fatalf("route = %d %s, want 409 file_read_only", code, raw)
	}
	if string(a.fx.vals.m["newer-secret"]) != "pasted-token" {
		t.Fatalf("the route replaced the value: %q", a.fx.vals.m["newer-secret"])
	}
}

// A new id overwrites nothing, so a read-only file does not stop it (the
// Accounts tab saves an account's first token this way).
func TestNewValueAllowedWhileFileReadOnly(t *testing.T) {
	a := readOnlyValueRoutes(t)
	if err := a.fx.cm.SetValue("fresh", []byte("v")); err != nil {
		t.Fatalf("SetValue(new id) = %v", err)
	}
	if string(a.fx.vals.m["fresh"]) != "v" {
		t.Fatalf("the new value is missing: %v", a.fx.vals.m)
	}
}

// The file turning unreadable mid-run (a hand edit) closes the same gap.
func TestRemoveValueRefusedWhenFileBecomesNotUnderstoodMidRun(t *testing.T) {
	fx := newCredFixture(t, config.Config{}, nil)
	fx.vals.m["x"] = []byte("v")
	bad := `{"static_tokens":[],"gcp_connections":[{}]}`
	if err := os.WriteFile(credfile.Path(fx.dir), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fx.cm.RemoveValue("x"); !errors.Is(err, ErrCredReadOnly) || !fx.vals.has("x") {
		t.Fatalf("RemoveValue = %v, present %v", err, fx.vals.has("x"))
	}
}

// With an understood file both routes work as before.
func TestValueRoutesUnchangedWhileFileUnderstood(t *testing.T) {
	a := newTokenRoutes(t)
	a.fx.vals.m["v1"] = []byte("old")
	if code, _, raw := a.call("PUT", pathValues+"v1", `{"value":"new"}`); code != 204 {
		t.Fatalf("replace = %d %s", code, raw)
	}
	if code, _, raw := a.call("DELETE", pathValues+"v1", ""); code != 204 {
		t.Fatalf("delete = %d %s", code, raw)
	}
}
