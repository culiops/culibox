package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSidecarJSON(t *testing.T, p string, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSidecarJSON(t *testing.T, p string) map[string]map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRewriteSidecarPrefix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	writeSidecarJSON(t, p, map[string]any{
		"claude-1": map[string]any{"agent": "claude", "config_dir": "/r/agent-box/accounts/work/claude", "created": "2026-11-13T00:00:00Z", "future_key": "kept"},
		"claude-2": map[string]any{"agent": "claude", "created": "2026-11-13T00:00:00Z"},
		"claude-3": map[string]any{"agent": "claude", "config_dir": "/r/agent-box", "created": "2026-11-13T00:00:00Z"},
	})
	if err := RewriteSidecarPrefix(p, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	m := readSidecarJSON(t, p)
	if got := m["claude-1"]["config_dir"]; got != "/r/culibox/accounts/work/claude" {
		t.Errorf("claude-1 config_dir = %v", got)
	}
	if got := m["claude-3"]["config_dir"]; got != "/r/culibox" {
		t.Errorf("claude-3 config_dir = %v", got)
	}
	if _, ok := m["claude-2"]["config_dir"]; ok {
		t.Error("claude-2 gained a config_dir")
	}
	if m["claude-1"]["future_key"] != "kept" || m["claude-1"]["agent"] != "claude" {
		t.Errorf("other keys not kept: %v", m["claude-1"])
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	// The daemon still reads it.
	sc, err := openSidecar(p)
	if err != nil {
		t.Fatal(err)
	}
	if sc.all()["claude-1"].ConfigDir != "/r/culibox/accounts/work/claude" {
		t.Errorf("openSidecar reads %+v", sc.all()["claude-1"])
	}
	// Idempotent.
	if err := RewriteSidecarPrefix(p, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	if got := readSidecarJSON(t, p)["claude-1"]["config_dir"]; got != "/r/culibox/accounts/work/claude" {
		t.Errorf("after second run config_dir = %v", got)
	}
}

func TestRewriteSidecarPrefixKeepsSiblingPrefix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	writeSidecarJSON(t, p, map[string]any{
		"claude-1": map[string]any{"agent": "claude", "config_dir": "/r/agent-box-prod/accounts/work/claude", "created": "2026-11-13T00:00:00Z"},
	})
	if err := RewriteSidecarPrefix(p, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	if got := readSidecarJSON(t, p)["claude-1"]["config_dir"]; got != "/r/agent-box-prod/accounts/work/claude" {
		t.Errorf("sibling config_dir rewritten to %v", got)
	}
}

func TestRewriteSidecarPrefixMissingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sessions.json")
	if err := RewriteSidecarPrefix(p, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatalf("missing file: %v, want nil", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("missing file was created: %v", err)
	}
}
