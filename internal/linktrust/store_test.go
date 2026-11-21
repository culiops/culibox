package linktrust

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
	in := []Host{
		{Actor: "web", Host: "github.com", Subdomains: true, Added: added},
		{Actor: "web", Host: "docs.example.com", Added: added},
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
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	want := `{"version":1,"hosts":[{"actor":"web","host":"github.com","subdomains":true,"added":"2026-11-21T10:00:00Z"},` +
		`{"actor":"web","host":"docs.example.com","subdomains":false,"added":"2026-11-21T10:00:00Z"}]}` + "\n"
	if string(b) != want {
		t.Errorf("file = %s, want %s", b, want)
	}
	got, exists, err := Load(dir)
	if err != nil || !exists {
		t.Fatalf("Load: %v %v", exists, err)
	}
	if len(got) != 2 || got[0] != in[0] || got[1].Host != "docs.example.com" || got[1].Subdomains || !got[1].Added.Equal(added) {
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
		"not json":    `{`,
		"unknown ver": `{"version":0,"hosts":[]}`,
		"bad host":    `{"version":1,"hosts":[{"actor":"web","host":"Bad.Example.com"}]}`,
		"ip host":     `{"version":1,"hosts":[{"actor":"web","host":"10.0.0.1"}]}`,
		"empty actor": `{"version":1,"hosts":[{"actor":"","host":"github.com"}]}`,
		"duplicate":   `{"version":1,"hosts":[{"actor":"web","host":"github.com"},{"actor":"web","host":"github.com","subdomains":true}]}`,
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

func TestLoadAllowsSameHostForTwoActors(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":1,"hosts":[{"actor":"web","host":"github.com"},{"actor":"alice","host":"github.com"}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := Load(dir); err != nil || len(got) != 2 {
		t.Fatalf("Load = %v, %v", got, err)
	}
}

func TestLoadNewerVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"version":2,"hosts":{"x":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(dir)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, datacompat.ErrNewer) {
		t.Errorf("err = %v, want ErrCorrupt and ErrNewer", err)
	}
}
