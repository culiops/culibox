//go:build upgrade

package upgradetest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustTake(t *testing.T, base string) Snapshot {
	t.Helper()
	s, err := Take(base)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTakeRecordsTypesModesAndHashes(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "f"), "x", 0o600)
	if err := os.Mkdir(filepath.Join(d, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(d, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	s := mustTake(t, d)
	sum := sha256.Sum256([]byte("x"))
	want := map[string]Entry{
		"f":    {Type: "file", Mode: 0o600, Size: 1, SHA256: hex.EncodeToString(sum[:])},
		"dir":  {Type: "dir", Mode: 0o700},
		"link": {Type: "symlink", Target: "f"},
	}
	for k, w := range want {
		got, ok := s[k]
		if !ok {
			t.Fatalf("missing %s in %v", k, s)
		}
		if k == "link" {
			got.Mode = 0 // symlink modes differ by OS; only the type and target matter
		}
		if !got.same(w) {
			t.Errorf("%s: got %+v, want %+v", k, got, w)
		}
	}
	if len(s) != len(want) {
		t.Errorf("snapshot has %d entries, want %d: %v", len(s), len(want), s)
	}
}

func TestDiffEqualTreesHasNoViolations(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "a/b"), "1", 0o600)
	s := mustTake(t, d)
	if v := Diff(s, s, DiffOptions{}); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffChangedFileFailsWithoutRule(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "profiles.json"), "{}", 0o600)
	before := mustTake(t, d)
	writeFile(t, filepath.Join(d, "profiles.json"), "{} ", 0o600)
	v := Diff(before, mustTake(t, d), DiffOptions{})
	if len(v) != 1 || !strings.Contains(v[0], "profiles.json") {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffModeChangeFails(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "f"), "x", 0o600)
	before := mustTake(t, d)
	if err := os.Chmod(filepath.Join(d, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := Diff(before, mustTake(t, d), DiffOptions{}); len(v) != 1 {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffRuleAllowsChange(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "audit.db-wal"), "1", 0o600)
	before := mustTake(t, d)
	writeFile(t, filepath.Join(d, "audit.db-wal"), "22", 0o600)
	o := DiffOptions{Rules: []Rule{{Pattern: "audit.db*", Why: "rows checked"}}}
	if v := Diff(before, mustTake(t, d), o); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffRuleCheckRejects(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "sessions.json"), "{}", 0o600)
	before := mustTake(t, d)
	writeFile(t, filepath.Join(d, "sessions.json"), `{"x":1}`, 0o600)
	o := DiffOptions{Rules: []Rule{{Pattern: "sessions.json", Check: func(b, a *Entry) error {
		return errors.New("entry added")
	}}}}
	v := Diff(before, mustTake(t, d), o)
	if len(v) != 1 || !strings.Contains(v[0], "entry added") {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffUnknownNewFileFails(t *testing.T) {
	d := t.TempDir()
	before := mustTake(t, d)
	writeFile(t, filepath.Join(d, "last-version"), "v1 abc\n", 0o600)
	v := Diff(before, mustTake(t, d), DiffOptions{Rules: []Rule{{Pattern: "audit.db*", AllowNew: true}}})
	if len(v) != 1 || !strings.Contains(v[0], "new") || !strings.Contains(v[0], "last-version") {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffVanishedFileFails(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "tokens.json"), "{}", 0o600)
	before := mustTake(t, d)
	if err := os.Remove(filepath.Join(d, "tokens.json")); err != nil {
		t.Fatal(err)
	}
	v := Diff(before, mustTake(t, d), DiffOptions{Rules: []Rule{{Pattern: "tokens.json"}}})
	if len(v) != 1 || !strings.Contains(v[0], "vanished") {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffAllowVanish(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "audit.db-shm"), "x", 0o600)
	before := mustTake(t, d)
	if err := os.Remove(filepath.Join(d, "audit.db-shm")); err != nil {
		t.Fatal(err)
	}
	o := DiffOptions{Rules: []Rule{{Pattern: "audit.db*", AllowVanish: true}}}
	if v := Diff(before, mustTake(t, d), o); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
}

func TestDiffDoubleStarMatchesSubtree(t *testing.T) {
	d := t.TempDir()
	before := mustTake(t, d)
	writeFile(t, filepath.Join(d, "abduco/abduco/u/x@h"), "", 0o600)
	o := DiffOptions{Rules: []Rule{{Pattern: "abduco", AllowNew: true}, {Pattern: "abduco/**", AllowNew: true, AllowVanish: true}}}
	if v := Diff(before, mustTake(t, d), o); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
	// ** needs at least one segment, so abduco/abduco/** covers neither
	// abduco nor abduco/abduco themselves.
	v := Diff(before, mustTake(t, d), DiffOptions{Rules: []Rule{{Pattern: "abduco/abduco/**", AllowNew: true}}})
	if len(v) != 2 || !strings.HasPrefix(v[0], "abduco: new") || !strings.HasPrefix(v[1], "abduco/abduco: new") {
		t.Fatalf("want the two parent dirs reported, got %v", v)
	}
}

func TestDiffMapBefore(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, ".local/share/agent-box/a"), "1", 0o600)
	before := mustTake(t, d)
	if err := os.RemoveAll(filepath.Join(d, ".local/share/agent-box")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d, ".local/share/culibox/a"), "1", 0o600)
	if err := os.Chmod(filepath.Join(d, ".local/share/culibox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(d, ".local/share/agent-box"), 0o700); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	o := DiffOptions{
		MapBefore: func(rel string) string {
			if rel == ".local/share/agent-box" || strings.HasPrefix(rel, ".local/share/agent-box/") {
				return ".local/share/culibox" + strings.TrimPrefix(rel, ".local/share/agent-box")
			}
			return rel
		},
	}
	if v := Diff(before, mustTake(t, d), o); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
}
