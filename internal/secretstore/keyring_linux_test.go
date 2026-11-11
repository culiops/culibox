//go:build linux

package secretstore

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const testIdentity = "AGE-SECRET-KEY-TEST"

// requireKeyctl skips the test where the kernel keyring is blocked (seccomp
// profiles answer EPERM, kernels without keyrings ENOSYS).
func requireKeyctl(t *testing.T) {
	t.Helper()
	if _, err := unix.KeyctlGetKeyringID(unix.KEY_SPEC_USER_KEYRING, false); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS) {
			t.Skipf("keyctl is blocked here: %v", err)
		}
		t.Fatalf("keyctl probe: %v", err)
	}
}

// keyringRefused reports a Save error that means this environment's keyring
// refuses the operation (seccomp EPERM, no keyrings ENOSYS, restricted
// permissions EACCES, key quota EDQUOT), so a test skips. Any other error is
// a real failure.
func keyringRefused(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EDQUOT)
}

// saveOrSkip saves and skips only when the keyring refuses the operation.
func saveOrSkip(t *testing.T, k Keeper, ttl time.Duration) {
	t.Helper()
	if err := k.Save(testIdentity, ttl); err != nil {
		if keyringRefused(err) {
			t.Skipf("kernel keyring unavailable here: %v", err)
		}
		t.Fatalf("save: %v", err)
	}
}

func newTestKeeper(t *testing.T) Keeper {
	t.Helper()
	requireKeyctl(t)
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
	requireKeyctl(t)
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

func TestKeyringPermissions(t *testing.T) {
	k := newTestKeeper(t)
	saveOrSkip(t, k, time.Hour)
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", k.(*keyringKeeper).desc, 0)
	if err != nil {
		t.Fatal(err)
	}
	// "type;uid;gid;perm;description"
	desc, err := unix.KeyctlString(unix.KEYCTL_DESCRIBE, id)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.SplitN(desc, ";", 5)
	if len(f) != 5 || f[3] != "3f0b0000" {
		t.Fatalf("describe %q: want perm 3f0b0000 (possessor all, user view|read|search)", desc)
	}
	// A re-save keeps the mask.
	if err := k.Save(testIdentity, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestKeyErrMapping(t *testing.T) {
	if err := keyErr(unix.EACCES, "read"); err == nil || errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("EACCES: %v", err)
	}
	for _, e := range []error{unix.ENOKEY, unix.EKEYEXPIRED, unix.EKEYREVOKED} {
		if err := keyErr(e, "read"); !errors.Is(err, ErrLocked) {
			t.Fatalf("%v: %v", e, err)
		}
	}
}

// A refused SETPERM must not leave the freshly added key behind: it would
// carry the default permissions instead of keyPerm.
func TestKeyringSetpermFailureLeavesNoKey(t *testing.T) {
	k := newTestKeeper(t)
	saveOrSkip(t, k, time.Hour) // the keyring works here
	if err := k.Clear(); err != nil {
		t.Fatal(err)
	}
	old := keyctlSetperm
	keyctlSetperm = func(int, uint32) error { return unix.EACCES }
	t.Cleanup(func() { keyctlSetperm = old })
	err := k.Save(testIdentity, time.Hour)
	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("want EACCES wrapped, got %v", err)
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("a key was left behind: %v", err)
	}
	if _, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", k.(*keyringKeeper).desc, 0); !errors.Is(err, unix.ENOKEY) {
		t.Fatalf("key still linked in @u: %v", err)
	}
}
