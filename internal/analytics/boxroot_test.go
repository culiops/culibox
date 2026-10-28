package analytics

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// boxSvc returns a Service whose only root pattern is the default box one, with
// <base>/boxstate registered as box-controlled.
func boxSvc(t *testing.T, base string) *Service {
	t.Helper()
	svc, err := New(filepath.Join(t.TempDir(), "a.db"),
		[]string{filepath.Join(base, "boxstate", "*", ".claude", "projects")}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetBoxState(filepath.Join(base, "boxstate"))
	t.Cleanup(func() { svc.Close() })
	return svc
}

func recorded(t *testing.T, svc *Service, path string) bool {
	t.Helper()
	_, off, _, _, err := svc.store.State(path)
	if err != nil {
		t.Fatal(err)
	}
	return off > 0
}

func TestIngestBoxStillIngestsRealTranscripts(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p", "S1.jsonl")
	writeFile(t, p, line1+line2)
	svc := boxSvc(t, base)
	st, err := svc.Ingest()
	if err != nil || st.Files != 1 || st.Errors != 0 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if !recorded(t, svc, p) {
		t.Fatalf("path key %s not recorded", p)
	}
}

// Box A's .claude is a symlink to box B's: A must not ingest B's files under A's keys.
func TestIngestBoxSymlinkedClaudeDirNotFollowed(t *testing.T) {
	base := t.TempDir()
	bFile := filepath.Join(base, "boxstate", "B", ".claude", "projects", "-p", "S1.jsonl")
	writeFile(t, bFile, line1+line2)
	if err := os.MkdirAll(filepath.Join(base, "boxstate", "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "boxstate", "B", ".claude"), filepath.Join(base, "boxstate", "A", ".claude")); err != nil {
		t.Fatal(err)
	}
	svc := boxSvc(t, base)
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	aKey := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p", "S1.jsonl")
	if recorded(t, svc, aKey) {
		t.Fatal("B's transcript was ingested under A's path")
	}
	if !recorded(t, svc, bFile) {
		t.Fatal("B's own transcript was not ingested")
	}
	rows, _ := svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 1 || rows[0].Input != 100 {
		t.Fatalf("double-counted or lost: %+v", rows)
	}
}

// Same, but the symlink points at a directory outside boxstate (the host's).
func TestIngestBoxSymlinkedClaudeDirToHostNotFollowed(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(t.TempDir(), "home-claude")
	writeFile(t, filepath.Join(host, "projects", "-p", "S9.jsonl"), line1+line2)
	if err := os.MkdirAll(filepath.Join(base, "boxstate", "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(host, filepath.Join(base, "boxstate", "A", ".claude")); err != nil {
		t.Fatal(err)
	}
	svc := boxSvc(t, base)
	st, _ := svc.Ingest()
	if st.Files != 0 || st.Scanned != 0 {
		t.Fatalf("followed a symlink out of the box: %+v", st)
	}
	rows, _ := svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 0 {
		t.Fatalf("host transcripts ingested via a box: %+v", rows)
	}
}

// A symlinked `projects` dir, and a symlinked project subdir, are not followed
// (even when the target stays inside the same box).
func TestIngestBoxSymlinkedProjectSubdirNotFollowed(t *testing.T) {
	base := t.TempDir()
	box := filepath.Join(base, "boxstate", "A")
	other := filepath.Join(base, "boxstate", "B", ".claude", "projects", "-victim")
	writeFile(t, filepath.Join(other, "S1.jsonl"), line1+line2)
	writeFile(t, filepath.Join(box, ".claude", "projects", "-real", "S2.jsonl"), line3)
	if err := os.Symlink(other, filepath.Join(box, ".claude", "projects", "-evil")); err != nil {
		t.Fatal(err)
	}
	svc := boxSvc(t, base)
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if recorded(t, svc, filepath.Join(box, ".claude", "projects", "-evil", "S1.jsonl")) {
		t.Fatal("symlinked project subdir was followed")
	}
	if !recorded(t, svc, filepath.Join(box, ".claude", "projects", "-real", "S2.jsonl")) {
		t.Fatal("real transcript missing")
	}

	// `projects` itself a symlink (to a sibling dir inside the same box).
	box2 := filepath.Join(base, "boxstate", "C")
	writeFile(t, filepath.Join(box2, ".claude", "real", "-p", "S3.jsonl"), line1)
	if err := os.Symlink(filepath.Join(box2, ".claude", "real"), filepath.Join(box2, ".claude", "projects")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if recorded(t, svc, filepath.Join(box2, ".claude", "projects", "-p", "S3.jsonl")) {
		t.Fatal("symlinked projects dir was followed")
	}
}

// A symlinked transcript inside a box is refused, as on the host.
func TestIngestBoxSkipsSymlinkedJSONL(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	writeFile(t, outside, line1+line2)
	link := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p", "x.jsonl")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	svc := boxSvc(t, base)
	st, _ := svc.Ingest()
	if st.Files != 0 || st.BytesRead != 0 || st.Errors != 1 {
		t.Fatalf("symlink not refused: %+v", st)
	}
}

// A FIFO in a box must not block the pass.
func TestIngestBoxSkipsFIFO(t *testing.T) {
	base := t.TempDir()
	fifo := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p", "x.jsonl")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := boxSvc(t, base)
	done := make(chan IngestStats)
	go func() { st, _ := svc.Ingest(); done <- st }()
	select {
	case st := <-done:
		if st.Errors != 1 || st.Files != 0 {
			t.Fatalf("FIFO not refused: %+v", st)
		}
	case <-time.After(2 * time.Second):
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			f.Close()
		}
		t.Fatal("ingest blocked on a FIFO in a box")
	}
}

// A symlinked box dir itself (boxstate/A -> elsewhere) is refused.
func TestIngestBoxSymlinkedBoxDirNotFollowed(t *testing.T) {
	base := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "x")
	writeFile(t, filepath.Join(elsewhere, ".claude", "projects", "-p", "S1.jsonl"), line1+line2)
	if err := os.MkdirAll(filepath.Join(base, "boxstate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(base, "boxstate", "A")); err != nil {
		t.Fatal(err)
	}
	svc := boxSvc(t, base)
	st, _ := svc.Ingest()
	if st.Files != 0 {
		t.Fatalf("followed a symlinked box dir: %+v", st)
	}
}

// Host roots keep today's walk (a configured literal root outside boxstate).
func TestIngestHostRootUnchangedWithBoxStateSet(t *testing.T) {
	base := t.TempDir()
	hostRoot := filepath.Join(t.TempDir(), "projects")
	writeFile(t, filepath.Join(hostRoot, "-p", "S1.jsonl"), line1+line2)
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), []string{hostRoot}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetBoxState(filepath.Join(base, "boxstate"))
	if st, err := svc.Ingest(); err != nil || st.Files != 1 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}
