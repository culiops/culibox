package account

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func claudeJSONPath(r Resolved) string { return filepath.Join(r.ConfigDir, ".claude.json") }

func mustCreate(t *testing.T, s *Store, name, provider string) Resolved {
	t.Helper()
	r, err := s.Create(Account{Name: name, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProvisionSeedsOnboarding(t *testing.T) {
	s, _ := newStore(t)
	r := mustCreate(t, s, "work", ProviderClaude)
	p := claudeJSONPath(r)
	got := readJSON(t, p)
	want := map[string]any{"hasCompletedOnboarding": true, "theme": "dark"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(".claude.json = %v, want %v", got, want)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestProvisionOnboardingMergesExisting(t *testing.T) {
	s, _ := newStore(t)
	r := mustCreate(t, s, "work", ProviderClaude)
	p := claudeJSONPath(r)
	orig := `{"firstStartTime":"2026-11-20T00:00:00Z","userID":"abc","theme":"light","projects":{"/x":{"hasTrustDialogAccepted":false}}}`
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, p)
	if got["hasCompletedOnboarding"] != true || got["theme"] != "light" {
		t.Fatalf("flag/theme wrong: %v", got)
	}
	want := map[string]any{
		"firstStartTime": "2026-11-20T00:00:00Z",
		"userID":         "abc",
		"projects":       map[string]any{"/x": map[string]any{"hasTrustDialogAccepted": false}},
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestProvisionOnboardingFlipsFalse(t *testing.T) {
	s, _ := newStore(t)
	r := mustCreate(t, s, "work", ProviderClaude)
	p := claudeJSONPath(r)
	if err := os.WriteFile(p, []byte(`{"hasCompletedOnboarding": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, p)
	if got["hasCompletedOnboarding"] != true || got["theme"] != "dark" {
		t.Fatalf(".claude.json = %v", got)
	}
}

func TestProvisionOnboardingNoWriteWhenDone(t *testing.T) {
	s, _ := newStore(t)
	r := mustCreate(t, s, "work", ProviderClaude)
	p := claudeJSONPath(r)
	if err := os.WriteFile(p, []byte(`{"hasCompletedOnboarding":true,"theme":"light","userID":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Error("provision rewrote a .claude.json that was already complete")
	}
}

func TestProvisionOnboardingLeavesBadFile(t *testing.T) {
	big := append(bytes.Repeat([]byte(" "), 9<<20), '{', '}')
	for name, content := range map[string][]byte{
		"truncated": []byte(`{"userID":`),
		"null":      []byte(`null`),
		"array":     []byte(`[]`),
		"string":    []byte(`"x"`),
		"too big":   big,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			r := mustCreate(t, s, "work", ProviderClaude)
			p := claudeJSONPath(r)
			if err := os.WriteFile(p, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Update("work", "", false); err != nil {
				t.Fatalf("Update: %v", err)
			}
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("the file was changed (%d bytes, was %d)", len(got), len(content))
			}
		})
	}
}

func TestProvisionOnboardingSkipsNonRegular(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		s, _ := newStore(t)
		r := mustCreate(t, s, "work", ProviderClaude)
		p := claudeJSONPath(r)
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		content := []byte(`{"userID":"abc"}`)
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update("work", "", false); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got, _ := os.ReadFile(target); !bytes.Equal(got, content) {
			t.Errorf("the link target was changed: %s", got)
		}
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced: %v %v", fi, err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		s, _ := newStore(t)
		r := mustCreate(t, s, "work", ProviderClaude)
		p := claudeJSONPath(r)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update("work", "", false); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			t.Errorf("the directory was replaced: %v %v", fi, err)
		}
	})
}

func TestProvisionNoOnboardingForCodex(t *testing.T) {
	s, _ := newStore(t)
	r := mustCreate(t, s, "cx", ProviderCodex)
	if _, err := os.Lstat(claudeJSONPath(r)); !os.IsNotExist(err) {
		t.Errorf(".claude.json for a codex account: %v", err)
	}
}

func TestOpenSeedsOnboardingForExistingAccount(t *testing.T) {
	data := t.TempDir()
	def := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(data, "accounts.json")
	s, err := Open(reg, data, def)
	if err != nil {
		t.Fatal(err)
	}
	r := mustCreate(t, s, "work", ProviderClaude)
	p := claudeJSONPath(r)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"userID":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(reg, data, def)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := readJSON(t, p)
	if got["hasCompletedOnboarding"] != true || got["userID"] != "abc" {
		t.Fatalf(".claude.json = %v", got)
	}
}
