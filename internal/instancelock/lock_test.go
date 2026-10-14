package instancelock

import (
	"errors"
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
