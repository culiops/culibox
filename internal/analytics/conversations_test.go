package analytics

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// #330: the default roots include every profile's conversations folder.
func TestDefaultRootsIncludeConversationsFolders(t *testing.T) {
	got := DefaultRoots("/home/u", "/data")
	want := []string{
		"/home/u/.claude/projects",
		"/data/boxstate/*/.claude/projects",
		"/data/boxstate/.conversations/*",
		"/data/accounts/*/claude/projects",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DefaultRoots = %q, want %q", got, want)
	}
}

func defaultSvc(t *testing.T, base string) *Service {
	t.Helper()
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), DefaultRoots(filepath.Join(base, "home"), base), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetBoxState(filepath.Join(base, "boxstate"))
	t.Cleanup(func() { svc.Close() })
	return svc
}

// A transcript in a conversations folder is ingested, under its own path.
func TestIngestConversationsFolder(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "boxstate", ".conversations", "0123456789ab", "-w", "S1.jsonl")
	writeFile(t, p, line1+line2)
	svc := defaultSvc(t, base)
	st, err := svc.Ingest()
	if err != nil || st.Files != 1 || st.Errors != 0 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if !recorded(t, svc, p) {
		t.Fatalf("%s not recorded", p)
	}
}

// Adoption moves a transcript from a home into the folder: with its offsets
// carried first, the next pass reads nothing of it again.
func TestCarryOffsetsKeepsAMovedTranscriptCountedOnce(t *testing.T) {
	base := t.TempDir()
	from := filepath.Join(base, "boxstate", "ab-box-000000000000", ".claude", "projects", "-w", "S1.jsonl")
	to := filepath.Join(base, "boxstate", ".conversations", "0123456789ab", "-w", "S1.jsonl")
	writeFile(t, from, line1+line2)
	svc := defaultSvc(t, base)
	if st, err := svc.Ingest(); err != nil || st.Files != 1 {
		t.Fatalf("first pass st=%+v err=%v", st, err)
	}
	if err := svc.CarryOffsets(from, to); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Ingest()
	if err != nil || st.Files != 0 || st.BytesRead != 0 {
		t.Fatalf("the moved transcript was read again: st=%+v err=%v", st, err)
	}
}
