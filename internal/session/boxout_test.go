package session

import (
	"path/filepath"
	"testing"

	"github.com/culiops/culibox/internal/box"
)

func TestBoxOutputDir(t *testing.T) {
	m, state := newBoxFilesManager(t)
	m.sessions["b"] = &Session{ID: "b", Box: "box-1"}
	m.sessions["h"] = &Session{ID: "h"}

	got, ok := m.BoxOutputDir("b")
	if want := box.OutputDir(state, "box-1"); !ok || got != want {
		t.Fatalf("boxed session = (%q, %v), want (%q, true)", got, ok, want)
	}
	if want := filepath.Join(state, "boxstate", ".boxout", "box-1", "playwright-mcp"); got != want {
		t.Fatalf("path shape = %q, want %q", got, want)
	}
	if got, ok := m.BoxOutputDir("h"); ok || got != "" {
		t.Fatalf("host session = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := m.BoxOutputDir("nope"); ok || got != "" {
		t.Fatalf("unknown id = (%q, %v), want (\"\", false)", got, ok)
	}
	m.stateDir = ""
	if got, ok := m.BoxOutputDir("b"); ok || got != "" {
		t.Fatalf("empty stateDir = (%q, %v), want (\"\", false)", got, ok)
	}
}
