package instancelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Review focus #1: a second serve on the same data dir must be refused before
// it can sweep or adopt anything.
func TestAcquire_SecondHolderRefused(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire = %v, want ErrLocked", err)
	}
	release()
	r2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	r2()
}

// Held only looks: it never creates serve.lock and never keeps a lock.
func TestHeld(t *testing.T) {
	dir := t.TempDir()
	held, err := Held(dir)
	if err != nil || held {
		t.Fatalf("Held(no file) = %v, %v; want false, nil", held, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "serve.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Held created serve.lock: %v", err)
	}
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := Held(dir); err != nil || !held {
		t.Fatalf("Held(acquired) = %v, %v; want true, nil", held, err)
	}
	release()
	if held, err := Held(dir); err != nil || held {
		t.Fatalf("Held(released) = %v, %v; want false, nil", held, err)
	}
	// Held left nothing behind: a serve can still take the lock.
	r2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire after Held: %v", err)
	}
	r2()
}
