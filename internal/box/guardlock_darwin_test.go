package box

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The real flags on a Mac: set, seen, and refused to a write, then cleared.
// The cleanup always clears the flag, so a failure never leaves an
// undeletable file in the temp directory.
func TestDarwinFlagsLockAndUnlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Chflags(p, 0) })
	ops := platformFlagOps()
	if ops == nil {
		t.Fatal("darwin must have the guard locks' flags")
	}
	if _, err := ops.caseSensitive(dir); err != nil {
		t.Fatalf("pathconf(_PC_CASE_SENSITIVE) on %s: %v", dir, err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if imm, err := ops.immutable(f); err != nil || imm {
		t.Fatalf("a new file is not immutable: %v %v", imm, err)
	}
	if err := ops.setImmutable(f, true); err != nil {
		t.Fatal(err)
	}
	if imm, err := ops.immutable(f); err != nil || !imm {
		t.Fatalf("want immutable: %v %v", imm, err)
	}
	if err := os.WriteFile(p, []byte("y"), 0o644); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("a locked file must refuse a write with EPERM, got %v", err)
	}
	if err := ops.setImmutable(f, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("y"), 0o644); err != nil {
		t.Fatalf("an unlocked file is writable again: %v", err)
	}
}
