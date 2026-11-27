package analytics

import (
	"path/filepath"
	"testing"
)

// A box file is read at most boxMaxReadPerPass bytes per pass; the offset
// advances by what was consumed and the next pass continues from there. Host
// roots are not capped.
func TestIngestBoxPerPassCap(t *testing.T) {
	old := boxMaxReadPerPass
	defer func() { boxMaxReadPerPass = old }()
	// Window = two whole lines plus a few bytes of the third (a partial line,
	// left unconsumed).
	boxMaxReadPerPass = int64(len(line1+line2)) + 10

	base := t.TempDir()
	p := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p", "S1.jsonl")
	writeFile(t, p, line1+line2+line3)
	svc := boxSvc(t, base)

	st, err := svc.Ingest()
	if err != nil || st.Errors != 0 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if st.BytesRead != int64(len(line1+line2)) {
		t.Fatalf("pass 1 read %d bytes, want %d (the cap bounds the window)", st.BytesRead, len(line1+line2))
	}
	_, off, _, _, _, _ := svc.store.State(p)
	if off != int64(len(line1+line2)) {
		t.Fatalf("offset after pass 1 = %d, want %d", off, len(line1+line2))
	}
	rows, _ := svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 1 || rows[0].Input != 100 {
		t.Fatalf("pass 1 rows: %+v", rows)
	}

	st, _ = svc.Ingest()
	if st.BytesRead != int64(len(line3)) {
		t.Fatalf("pass 2 read %d bytes, want %d", st.BytesRead, len(line3))
	}
	_, off, _, _, _, _ = svc.store.State(p)
	if off != int64(len(line1+line2+line3)) {
		t.Fatalf("offset after pass 2 = %d", off)
	}
	rows, _ = svc.ByDayModel("2026-08-01", "2026-08-31")
	if rows[0].Input != 150 {
		t.Fatalf("pass 2 rows: %+v", rows)
	}

	// The same file under a host root is read whole in one pass.
	root := t.TempDir()
	hp := filepath.Join(root, "-p", "S1.jsonl")
	writeFile(t, hp, line1+line2+line3)
	hs := openTemp(t)
	var hst IngestStats
	ingestRoot(hs, root, nil, &hst)
	if hst.BytesRead != int64(len(line1+line2+line3)) {
		t.Fatalf("host root was capped: %+v", hst)
	}
}
