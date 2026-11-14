package datacompat

import (
	"errors"
	"strings"
	"testing"
)

func TestNewerWrapsAndNamesFile(t *testing.T) {
	err := Newer("x.json", "version 3")
	if !errors.Is(err, ErrNewer) {
		t.Fatalf("errors.Is(err, ErrNewer) is false for %v", err)
	}
	for _, want := range []string{"x.json", "version 3", Hint} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
	if got, want := err.Error(), "x.json was written by a newer CuliBox (version 3); "+Hint; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}
