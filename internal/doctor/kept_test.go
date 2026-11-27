package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
)

// seedKeptStore writes a store's state.json the way the keep pass does (dirs
// 0700, file 0600). key is the folder name; st the state.
func seedKeptStore(t *testing.T, dataDir, key string, st map[string]any) {
	t.Helper()
	dir := filepath.Join(dataDir, "boxstate", ".kept", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedKeptFor seeds a store whose state hashes to its key.
func seedKeptFor(t *testing.T, dataDir, profile string, sizes ...int64) string {
	t.Helper()
	key := box.ConversationsKey("default", "/w", "acct", profile)
	var kept []map[string]any
	for i, n := range sizes {
		kept = append(kept, map[string]any{"path": ".config/app" + string(rune('a'+i)), "bytes": n, "files": 2, "status": "kept"})
	}
	seedKeptStore(t, dataDir, key, map[string]any{"profile": profile, "workspace": "/w", "account": "acct", "kept": kept})
	return key
}

func TestDoctorKeptNone(t *testing.T) {
	dataDir := t.TempDir()
	if rows := checkKeptStores(dataDir, "default", nil); len(rows) != 0 {
		t.Fatalf("rows = %v", rows)
	}
	if rows := checkKeptStores(filepath.Join(dataDir, "missing"), "default", nil); len(rows) != 0 {
		t.Fatalf("rows = %v", rows)
	}
}

func TestDoctorKeptSizes(t *testing.T) {
	dataDir := t.TempDir()
	seedKeptFor(t, dataDir, "p1", 1<<20, 2<<20)
	seedKeptFor(t, dataDir, "p2", 3<<20)
	rows := checkKeptStores(dataDir, "default", map[string]bool{"p1": true, "p2": true})
	if len(rows) != 1 || rows[0].Status != Pass || rows[0].Check != "kept box folders" {
		t.Fatalf("rows = %v", rows)
	}
	for _, want := range []string{"2 stores", "6.0 MiB", filepath.Join(dataDir, "boxstate", ".kept")} {
		if !strings.Contains(rows[0].Detail, want) {
			t.Errorf("detail %q lacks %q", rows[0].Detail, want)
		}
	}
}

func TestDoctorKeptOver5GiB(t *testing.T) {
	dataDir := t.TempDir()
	key := seedKeptFor(t, dataDir, "p1", 6<<30)
	rows := checkKeptStores(dataDir, "default", map[string]bool{"p1": true})
	if len(rows) != 1 || rows[0].Status != Warn {
		t.Fatalf("rows = %v", rows)
	}
	if !strings.Contains(rows[0].Detail, key) || !strings.Contains(rows[0].Detail, "5 GiB") {
		t.Errorf("detail = %q", rows[0].Detail)
	}
}

func TestDoctorKeptOrphanStore(t *testing.T) {
	dataDir := t.TempDir()
	key := seedKeptFor(t, dataDir, "gone", 1<<20)
	rows := checkKeptStores(dataDir, "default", map[string]bool{"other": true})
	if len(rows) != 1 || rows[0].Status != Warn {
		t.Fatalf("rows = %v", rows)
	}
	if !strings.Contains(rows[0].Detail, key) || !strings.Contains(rows[0].Detail, "gone") {
		t.Errorf("detail = %q", rows[0].Detail)
	}
	// R5: no profile list (nil) means no orphan judgment for a named profile.
	if rows := checkKeptStores(dataDir, "default", nil); rows[0].Status != Pass {
		t.Fatalf("nil profiles must not warn: %v", rows)
	}
}

// A state that does not hash to its folder names nobody: always a warning.
func TestDoctorKeptUnattributedStore(t *testing.T) {
	dataDir := t.TempDir()
	key := box.ConversationsKey("default", "/w", "acct", "p1")
	seedKeptStore(t, dataDir, key, map[string]any{"profile": "someone-else", "workspace": "/w", "account": "acct"})
	rows := checkKeptStores(dataDir, "default", nil)
	if len(rows) != 1 || rows[0].Status != Warn || !strings.Contains(rows[0].Detail, key) {
		t.Fatalf("rows = %v", rows)
	}
}
