package wsroots

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/datacompat"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if list, exists, err := Load(dir); list != nil || exists || err != nil {
		t.Fatalf("missing file: got %v %v %v", list, exists, err)
	}
	added := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	in := []Root{
		{Path: "/data/a", Added: added, AddedBy: "web"},
		{Path: "/data/b", Added: added, AddedBy: "web", LastUsed: added.Add(time.Hour)},
	}
	if err := Save(dir, in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	got, exists, err := Load(dir)
	if err != nil || !exists {
		t.Fatalf("Load: %v %v", exists, err)
	}
	if len(got) != 2 || got[0].Path != "/data/a" || !got[0].LastUsed.IsZero() ||
		got[1].Path != "/data/b" || !got[1].LastUsed.Equal(added.Add(time.Hour)) ||
		!got[0].Added.Equal(added) || got[0].AddedBy != "web" {
		t.Errorf("round trip lost data: %+v", got)
	}
	// An empty list is still a valid file and loads as a non-nil empty list.
	if err := Save(dir, nil); err != nil {
		t.Fatal(err)
	}
	if got, exists, err := Load(dir); err != nil || !exists || got == nil || len(got) != 0 {
		t.Errorf("empty list: %v %v %v", got, exists, err)
	}
	// No temp file is left behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("data dir has %d entries, want just %s", len(ents), FileName)
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `{`,
		"unknown ver":   `{"version":0,"roots":[]}`,
		"relative path": `{"version":1,"roots":[{"path":"rel/x"}]}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, exists, err := Load(dir); !errors.Is(err, ErrCorrupt) || !exists {
			t.Errorf("%s: err = %v exists = %v, want ErrCorrupt", name, err, exists)
		}
	}
}

func TestLoadNewerVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"version":2,"roots":{"x":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(dir)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, datacompat.ErrNewer) {
		t.Errorf("err = %v, want ErrCorrupt and ErrNewer", err)
	}
}
