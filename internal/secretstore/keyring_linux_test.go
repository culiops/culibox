//go:build linux

package secretstore

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

const testIdentity = "AGE-SECRET-KEY-TEST"

func newTestKeeper(t *testing.T) Keeper {
	t.Helper()
	k := NewKeeper(t.TempDir())
	t.Cleanup(func() { _ = k.Clear() })
	return k
}

func TestKeyringRoundTrip(t *testing.T) {
	k := newTestKeeper(t)
	if !k.NeedsPassphrase() {
		t.Fatal("linux keeper must need a passphrase")
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("empty keyring: %v", err)
	}
	if err := k.Save(testIdentity, time.Hour); err != nil {
		t.Skipf("kernel keyring unavailable here: %v", err)
	}
	id, exp, err := k.Load()
	if err != nil || id != testIdentity {
		t.Fatalf("load: %q %v", id, err)
	}
	if d := time.Until(exp); d < time.Hour-5*time.Second || d > time.Hour+5*time.Second {
		t.Fatalf("expiry in %v, want about 1h", d)
	}
	// re-Save replaces
	if err := k.Save("AGE-SECRET-KEY-OTHER", time.Hour); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := k.Load(); id != "AGE-SECRET-KEY-OTHER" {
		t.Fatalf("re-save kept %q", id)
	}
	if err := k.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("after clear: %v", err)
	}
	if err := k.Clear(); err != nil {
		t.Fatalf("clear of an absent key: %v", err)
	}
}

func TestKeyringSeparateDataDirs(t *testing.T) {
	a, b := newTestKeeper(t), newTestKeeper(t)
	if err := a.Save(testIdentity, time.Hour); err != nil {
		t.Skipf("kernel keyring unavailable here: %v", err)
	}
	if _, _, err := b.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("unlocking one data dir unlocked another: %v", err)
	}
}

func TestKeyringSurvivesProcess(t *testing.T) {
	if dir := os.Getenv("AB_KEYRING_HELPER_DIR"); dir != "" {
		id, _, err := NewKeeper(dir).Load()
		if err != nil {
			t.Fatalf("child load: %v", err)
		}
		if id != testIdentity {
			t.Fatalf("child read %q", id)
		}
		return
	}
	dir := t.TempDir()
	k := NewKeeper(dir)
	t.Cleanup(func() { _ = k.Clear() })
	if err := k.Save(testIdentity, time.Hour); err != nil {
		t.Skipf("kernel keyring unavailable here: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestKeyringSurvivesProcess$")
	cmd.Env = append(os.Environ(), "AB_KEYRING_HELPER_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestKeyringExpiry(t *testing.T) {
	k := newTestKeeper(t)
	if err := k.Save(testIdentity, 2*time.Second); err != nil {
		t.Skipf("kernel keyring unavailable here: %v", err)
	}
	if _, _, err := k.Load(); err != nil {
		t.Fatalf("load before expiry: %v", err)
	}
	time.Sleep(3 * time.Second)
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestKeyringRejectsBadTTL(t *testing.T) {
	k := newTestKeeper(t)
	if err := k.Save(testIdentity, 0); err == nil {
		t.Fatal("ttl 0 accepted")
	}
}
