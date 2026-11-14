package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func importPath(t *testing.T, dd, of string) string {
	t.Helper()
	h, err := cloneKeyHash(of)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dd, importRecordDir, h)
}

func TestImportRecordRoundTrip(t *testing.T) {
	dd, of := t.TempDir(), "/some/of"
	if _, exists, err := ReadImportRecord(dd, of); err != nil || exists {
		t.Fatalf("no record yet: exists=%v err=%v", exists, err)
	}
	if _, exists, err := ReadImportRecord(filepath.Join(t.TempDir(), "absent"), of); err != nil || exists {
		t.Fatalf("absent data dir: exists=%v err=%v", exists, err)
	}
	if err := AppendImported(dd, of, []string{shaN(1), shaN(2)}); err != nil {
		t.Fatal(err)
	}
	if err := AppendImported(dd, of, []string{shaN(2), shaN(3)}); err != nil {
		t.Fatal(err)
	}
	got, exists, err := ReadImportRecord(dd, of)
	if err != nil || !exists || len(got) != 3 || !got[shaN(1)] || !got[shaN(2)] || !got[shaN(3)] {
		t.Fatalf("got %v exists=%v err=%v", got, exists, err)
	}
	// Another clone_of has its own record.
	if _, exists, _ := ReadImportRecord(dd, "/other/of"); exists {
		t.Fatal("a record is shared between two clone_of paths")
	}
	if raw, _ := os.ReadFile(importPath(t, dd, of)); string(raw) != shaN(1)+"\n"+shaN(2)+"\n"+shaN(3)+"\n" {
		t.Fatalf("file = %q; want each SHA once, oldest first", raw)
	}
	fi, err := os.Stat(importPath(t, dd, of))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("record file: %v %v", fi, err)
	}
	di, _ := os.Stat(filepath.Join(dd, importRecordDir))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
	if err := AppendImported(dd, of, []string{"not-a-sha"}); err == nil {
		t.Error("Append accepted a malformed SHA")
	}
	if err := AppendImported(dd, of, nil); err != nil {
		t.Errorf("Append of nothing: %v", err)
	}
}

func TestImportRecordRefusesSymlinkedDir(t *testing.T) {
	dd, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(dd, importRecordDir)); err != nil {
		t.Fatal(err)
	}
	if err := AppendImported(dd, "/some/of", []string{shaN(1)}); err == nil {
		t.Fatal("append through a symlinked clone-imports dir succeeded")
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Fatalf("os.Root allowed a write through a symlink: %v", ents)
	}
	if _, _, err := ReadImportRecord(dd, "/some/of"); err == nil {
		t.Fatal("read through a symlinked dir succeeded")
	}
}

func TestImportRecordRefusesSymlinkedFileAndJunk(t *testing.T) {
	dd, of := t.TempDir(), "/some/of"
	if err := AppendImported(dd, of, []string{shaN(1)}); err != nil {
		t.Fatal(err)
	}
	p := importPath(t, dd, of)
	other := filepath.Join(t.TempDir(), "x")
	if err := os.WriteFile(other, []byte(shaN(1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	if err := os.Symlink(other, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadImportRecord(dd, of); err == nil {
		t.Fatal("read followed a symlinked record")
	}
	os.Remove(p)
	for _, bad := range []string{"zz\n", shaN(1) + "\n\n" + shaN(2) + "\n", strings.Repeat("a", 500) + "\n"} {
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadImportRecord(dd, of); err == nil {
			t.Errorf("record %q was accepted", bad)
		}
		if err := AppendImported(dd, of, []string{shaN(9)}); err == nil {
			t.Errorf("append on corrupt record %q succeeded", bad)
		}
		if raw, _ := os.ReadFile(p); string(raw) != bad {
			t.Errorf("a corrupt record was rewritten: %q", raw)
		}
	}
}

func TestImportRecordCapsLines(t *testing.T) {
	dd, of := t.TempDir(), "/some/of"
	var first []string
	for i := 0; i < maxImportLines; i++ {
		first = append(first, shaN(i))
	}
	if err := AppendImported(dd, of, first); err != nil {
		t.Fatal(err)
	}
	if err := AppendImported(dd, of, []string{shaN(maxImportLines), shaN(maxImportLines + 1)}); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadImportRecord(dd, of)
	if err != nil || len(got) != maxImportLines {
		t.Fatalf("len=%d err=%v; want %d", len(got), err, maxImportLines)
	}
	if got[shaN(0)] || got[shaN(1)] || !got[shaN(2)] || !got[shaN(maxImportLines+1)] {
		t.Fatal("the cap did not drop the oldest SHAs")
	}
	// A hand-edited file over the cap is refused on read.
	var sb strings.Builder
	for i := 0; i <= maxImportLines; i++ {
		sb.WriteString(shaN(i) + "\n")
	}
	if err := os.WriteFile(importPath(t, dd, of), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadImportRecord(dd, of); err == nil {
		t.Fatal("read accepted a file over the line cap")
	}
}

func TestImportRecordConcurrentAppends(t *testing.T) {
	dd, of := t.TempDir(), "/some/of"
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AppendImported(dd, of, []string{shaN(i)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := ReadImportRecord(dd, of)
	if err != nil || len(got) != 20 {
		t.Fatalf("len=%d err=%v; a lost update", len(got), err)
	}
	ents, _ := os.ReadDir(filepath.Join(dd, importRecordDir))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// rewrittenAway reads filter-repo's commit-map from the user's repo: not through a
// symlink, and a malformed line is an error, never a pass.
func TestRewrittenAwayCommitMap(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true)
	// An unreachable commit, as a box-made one: the only way past the history
	// check is the commit-map.
	tree := gitIn(t, of, "", "rev-parse", "HEAD^{tree}")
	c := gitIn(t, of, "", "-c", "user.name=F", "-c", "user.email=f@x", "commit-tree", tree, "-m", "orphan")
	mapPath := filepath.Join(of, ".git", "filter-repo", "commit-map")
	if err := os.MkdirAll(filepath.Dir(mapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	boxMade := map[string]bool{c: true}
	run := func(content string) (string, error) {
		t.Helper()
		if err := os.WriteFile(mapPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return rewrittenAway(ctx, rec, of, c, nil, boxMade)
	}
	zero := strings.Repeat("0", 40)
	header := "old                                      new\n"
	if got, err := run(header + c + " " + zero + "\n"); err != nil || got != c {
		t.Errorf("dropped commit: got %q, %v; want %s", got, err, c)
	}
	if got, err := run(header + c + " " + shaN(5) + "\n"); err != nil || got != c {
		t.Errorf("rewritten commit: got %q, %v; want %s", got, err, c)
	}
	if got, err := run(header + c + " " + c + "\n"); err != nil || got != "" {
		t.Errorf("unchanged commit: got %q, %v; want none", got, err)
	}
	if got, err := run(header + shaN(1) + " " + zero + "\n"); err != nil || got != "" {
		t.Errorf("other commit: got %q, %v; want none", got, err)
	}
	for _, bad := range []string{header + "zz\n", header + c + " " + zero + " x\n", header + "\n"} {
		if got, err := run(bad); err == nil {
			t.Errorf("map %q: got %q, nil; want an error", bad, got)
		}
	}
	// A symlinked map is not read.
	os.Remove(mapPath)
	other := filepath.Join(t.TempDir(), "map")
	if err := os.WriteFile(other, []byte(header+c+" "+zero+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, mapPath); err != nil {
		t.Fatal(err)
	}
	if got, err := rewrittenAway(ctx, rec, of, c, nil, boxMade); err == nil {
		t.Errorf("a symlinked commit-map was read: %q", got)
	}
	// No map at all: nothing rewritten.
	os.Remove(mapPath)
	if got, err := rewrittenAway(ctx, rec, of, c, nil, boxMade); err != nil || got != "" {
		t.Errorf("no map: got %q, %v", got, err)
	}
}

// A spurious ENOENT on the first creation of .lock (macOS, #293) is retried; a
// real one (the directory is gone) is returned after the bound.
func TestOpenCreateRetryENOENT(t *testing.T) {
	enoent := &os.PathError{Op: "openat", Path: ".lock", Err: syscall.ENOENT}
	calls := 0
	f, err := openCreateRetryENOENT(func() (*os.File, error) {
		calls++
		if calls < 3 {
			return nil, enoent
		}
		return os.Open(os.DevNull)
	})
	if err != nil || f == nil || calls != 3 {
		t.Fatalf("f=%v err=%v calls=%d; want success on the 3rd try", f, err, calls)
	}
	f.Close()

	calls = 0
	if _, err := openCreateRetryENOENT(func() (*os.File, error) { calls++; return nil, enoent }); !errors.Is(err, syscall.ENOENT) || calls != lockOpenTries {
		t.Fatalf("err=%v calls=%d; want ENOENT after %d tries", err, calls, lockOpenTries)
	}

	calls = 0
	eacces := &os.PathError{Op: "openat", Path: ".lock", Err: syscall.EACCES}
	if _, err := openCreateRetryENOENT(func() (*os.File, error) { calls++; return nil, eacces }); !errors.Is(err, syscall.EACCES) || calls != 1 {
		t.Fatalf("err=%v calls=%d; any other error is returned at once", err, calls)
	}
}
