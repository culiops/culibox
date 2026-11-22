package handback

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "handback")}
}

func fl(path string, c Class) Flag {
	return Flag{Path: path, Classes: []Class{c}, Detail: "d " + path}
}

func paths(fs []Flag) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

func TestStoreKeysAndID(t *testing.T) {
	if got := InPlaceKey("/w"); got != "in-place\x00/w" {
		t.Fatalf("InPlaceKey = %q", got)
	}
	if got := CloneKey("/a", "b"); got != "clone\x00/a\x00b" {
		t.Fatalf("CloneKey = %q", got)
	}
	id := RecordID(InPlaceKey("/w"))
	if !recordIDRe.MatchString(id) {
		t.Fatalf("RecordID %q is not 32 lower hex", id)
	}
	if id == RecordID(InPlaceKey("/x")) || id != RecordID(InPlaceKey("/w")) {
		t.Fatal("RecordID must be stable and distinct per key")
	}
}

func TestStoreMergeCreates(t *testing.T) {
	s := newStore(t)
	key := InPlaceKey("/ws")
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	rec, err := s.Merge(key, Record{Kind: "in-place", Workspace: "/ws"}, at,
		Report{Flags: []Flag{fl("a.pdf", ClassMismatch)}, Inspected: 7, Truncated: "cap hit"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != RecordID(key) || rec.Kind != "in-place" || rec.Workspace != "/ws" || !rec.LastScan.Equal(at) ||
		len(rec.Flags) != 1 || rec.Inspected != 7 || rec.Truncated != "cap hit" {
		t.Fatalf("unexpected record %+v", rec)
	}
	st, err := os.Stat(filepath.Join(s.Dir, rec.ID+".json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("record file: %v %v", st, err)
	}
	if d, err := os.Stat(s.Dir); err != nil || d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: %v %v", d, err)
	}
}

func TestStoreMergeReplacesSamePathKeepsOthers(t *testing.T) {
	s := newStore(t)
	key := CloneKey("/repo", "box/x")
	base := Record{Kind: "clone", CloneOf: "/repo", Branch: "box/x"}
	t1 := time.Unix(1000, 0).UTC()
	t2 := time.Unix(2000, 0).UTC()
	if _, err := s.Merge(key, base, t1, Report{Flags: []Flag{fl("a", ClassArchive), fl("b", ClassArchive)}, Inspected: 2, Truncated: "first"}); err != nil {
		t.Fatal(err)
	}
	newA := Flag{Path: "a", Classes: []Class{ClassMismatch}, Detail: "newer"}
	rec, err := s.Merge(key, base, t2, Report{Flags: []Flag{newA, fl("c", ClassExecutable)}, Inspected: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Flag{}
	for _, f := range rec.Flags {
		got[f.Path] = f
	}
	if len(rec.Flags) != 3 || got["a"].Detail != "newer" || got["b"].Detail != "d b" || got["c"].Path != "c" {
		t.Fatalf("flags = %+v", rec.Flags)
	}
	if !rec.LastScan.Equal(t2) || rec.Inspected != 5 {
		t.Fatalf("LastScan/Inspected = %v %d", rec.LastScan, rec.Inspected)
	}
	if rec.Truncated != "first" {
		t.Fatalf("an empty Truncated must not clear the earlier one, got %q", rec.Truncated)
	}
	rec, err = s.Merge(key, base, t2, Report{Truncated: "second"})
	if err != nil || rec.Truncated != "second" {
		t.Fatalf("latest non-empty Truncated wins: %q %v", rec.Truncated, err)
	}
	// Strongest first, as SortFlags orders them.
	want := append([]Flag(nil), rec.Flags...)
	SortFlags(want)
	if fmt.Sprint(paths(want)) != fmt.Sprint(paths(rec.Flags)) {
		t.Fatalf("flags not in SortFlags order: %v", paths(rec.Flags))
	}
}

func TestStoreMergeRefreshesBase(t *testing.T) {
	s := newStore(t)
	key := CloneKey("/repo", "b")
	if _, err := s.Merge(key, Record{Kind: "clone", CloneOf: "/old", Branch: "b"}, time.Unix(1, 0), Report{}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Merge(key, Record{Kind: "clone", CloneOf: "/repo", Branch: "b"}, time.Unix(2, 0), Report{})
	if err != nil || rec.CloneOf != "/repo" {
		t.Fatalf("base not refreshed: %+v %v", rec, err)
	}
}

func TestStoreMergeCapKeepsStrongest(t *testing.T) {
	s := newStore(t)
	key := InPlaceKey("/ws")
	base := Record{Kind: "in-place", Workspace: "/ws"}
	max := DefaultLimits().MaxFlags
	var weak []Flag
	for i := 0; i < max; i++ {
		weak = append(weak, fl(fmt.Sprintf("w%05d.zip", i), ClassArchive))
	}
	if _, err := s.Merge(key, base, time.Unix(1, 0), Report{Flags: weak}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Merge(key, base, time.Unix(2, 0), Report{Flags: []Flag{fl("evil.pdf", ClassMismatch)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Flags) != max {
		t.Fatalf("len = %d, want %d", len(rec.Flags), max)
	}
	if rec.Flags[0].Path != "evil.pdf" {
		t.Fatalf("the stronger flag must survive the cap, first = %q", rec.Flags[0].Path)
	}
}

func TestStoreReviewedClearsKeepsLastScan(t *testing.T) {
	s := newStore(t)
	key := InPlaceKey("/ws")
	at := time.Unix(5000, 0).UTC()
	rec, err := s.Merge(key, Record{Kind: "in-place", Workspace: "/ws"}, at, Report{Flags: []Flag{fl("a", ClassExecutable)}, Truncated: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.Pending(); err != nil || len(p) != 1 {
		t.Fatalf("pending = %v %v", p, err)
	}
	if err := s.Reviewed(rec.ID); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Pending(); err != nil || len(p) != 0 {
		t.Fatalf("reviewed record must leave Pending: %v %v", p, err)
	}
	last, ok, err := s.LastScan(key)
	if err != nil || !ok || !last.Equal(at) {
		t.Fatalf("LastScan after review = %v %v %v", last, ok, err)
	}
	// A later scan finds the record without the reviewed flags and without the old Truncated.
	rec, err = s.Merge(key, Record{Kind: "in-place", Workspace: "/ws"}, at.Add(time.Hour), Report{})
	if err != nil || len(rec.Flags) != 0 || rec.Truncated != "" {
		t.Fatalf("after review: %+v %v", rec, err)
	}
}

func TestStoreReviewedRejectsBadID(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"../x", "", "abc", strings.Repeat("g", 32), strings.Repeat("A", 32), strings.Repeat("a", 33), "../" + strings.Repeat("a", 29)} {
		if err := s.Reviewed(id); err == nil {
			t.Errorf("Reviewed(%q) = nil, want error", id)
		}
	}
	if err := s.Reviewed(RecordID("unknown")); err != nil {
		t.Errorf("Reviewed of an unknown valid id should be a no-op, got %v", err)
	}
}

func TestStoreLastScanUnknown(t *testing.T) {
	s := newStore(t)
	if _, ok, err := s.LastScan(InPlaceKey("/nope")); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	// Asking must not have to create the data dir.
	if _, err := os.Stat(s.Dir); err == nil {
		t.Log("dir exists after read; acceptable")
	}
	if p, err := s.Pending(); err != nil || len(p) != 0 {
		t.Fatalf("pending on empty = %v %v", p, err)
	}
}

func TestStorePendingNewestFirst(t *testing.T) {
	s := newStore(t)
	for i, w := range []string{"/a", "/b", "/c"} {
		fls := []Flag{fl("x", ClassArchive)}
		if w == "/b" {
			fls = nil // no flags: not pending
		}
		if _, err := s.Merge(InPlaceKey(w), Record{Kind: "in-place", Workspace: w}, time.Unix(int64(1000+i), 0), Report{Flags: fls}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.Pending()
	if err != nil || len(p) != 2 || p[0].Workspace != "/c" || p[1].Workspace != "/a" {
		t.Fatalf("pending = %+v %v", p, err)
	}
}

func TestStoreReopenSeesData(t *testing.T) {
	s := newStore(t)
	key := InPlaceKey("/ws")
	if _, err := s.Merge(key, Record{Kind: "in-place", Workspace: "/ws"}, time.Unix(9, 0), Report{Flags: []Flag{fl("a", ClassArchive)}}); err != nil {
		t.Fatal(err)
	}
	s2 := Store{Dir: s.Dir}
	p, err := s2.Pending()
	if err != nil || len(p) != 1 || p[0].Flags[0].Path != "a" {
		t.Fatalf("reopened store: %+v %v", p, err)
	}
}

func TestStoreConcurrentMergesLoseNothing(t *testing.T) {
	s := newStore(t)
	key := CloneKey("/repo", "b")
	base := Record{Kind: "clone", CloneOf: "/repo", Branch: "b"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for g := 0; g < 2; g++ {
		wg.Add(1)
		// Each goroutine uses its own Store value, as the daemon and the CLI would.
		go func(g int, st Store) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_, err := st.Merge(key, base, time.Unix(int64(i), 0), Report{Flags: []Flag{fl(fmt.Sprintf("g%d-%d", g, i), ClassArchive)}})
				if err != nil {
					errs <- err
					return
				}
			}
		}(g, Store{Dir: s.Dir})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	p, err := s.Pending()
	if err != nil || len(p) != 1 || len(p[0].Flags) != 100 {
		n := 0
		if len(p) > 0 {
			n = len(p[0].Flags)
		}
		t.Fatalf("lost writes: %d flags, err %v", n, err)
	}
}

// Review Focus 3: the store's own files are untrusted.
func TestStoreRefusesBadRecordFiles(t *testing.T) {
	key := InPlaceKey("/ws")
	id := RecordID(key)
	base := Record{Kind: "in-place", Workspace: "/ws"}
	cases := map[string]func(t *testing.T, dir string){
		"corrupt json": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"over 4 MiB": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"flags":[],"pad":"`+strings.Repeat("x", 4<<20)+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "secret.json")
			if err := os.WriteFile(target, []byte(`{"id":"x","kind":"in-place","last_scan":"2026-01-01T00:00:00Z","flags":[{"path":"p","classes":["archive"],"detail":"d"}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, id+".json")); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, id+".json"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			if err := os.MkdirAll(s.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			plant(t, s.Dir)
			if _, _, err := s.LastScan(key); err == nil {
				t.Error("LastScan: want error")
			}
			if _, err := s.Merge(key, base, time.Unix(1, 0), Report{}); err == nil {
				t.Error("Merge: want error")
			}
			if err := s.Reviewed(id); err == nil {
				t.Error("Reviewed: want error")
			}
			if _, err := s.Pending(); err == nil {
				t.Error("Pending: want error")
			}
			if name == "symlink" {
				// The target must be untouched by the refused calls.
				if fi, err := os.Lstat(filepath.Join(s.Dir, id+".json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("the symlink was replaced or followed: %v %v", fi, err)
				}
			}
		})
	}
}

func TestStorePendingReturnsGoodRecordsBesideBadOne(t *testing.T) {
	s := newStore(t)
	if _, err := s.Merge(InPlaceKey("/ok"), Record{Kind: "in-place", Workspace: "/ok"}, time.Unix(1, 0), Report{Flags: []Flag{fl("a", ClassArchive)}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, RecordID("bad")+".json"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending()
	if err == nil || len(p) != 1 {
		t.Fatalf("want the good record and an error, got %d records, err %v", len(p), err)
	}
}

func TestStoreReadSanitizesAndPinsID(t *testing.T) {
	s := newStore(t)
	key := InPlaceKey("/ws")
	id := RecordID(key)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"../../evil","kind":"in-place","workspace":"/ws","last_scan":"2026-01-01T00:00:00Z","flags":[{"path":"a‮pdf.exe","classes":["executable"],"detail":"x\u0007y"}]}`
	if err := os.WriteFile(filepath.Join(s.Dir, id+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending()
	if err != nil || len(p) != 1 {
		t.Fatal(p, err)
	}
	if p[0].ID != id {
		t.Errorf("id must come from the file name, got %q", p[0].ID)
	}
	if f := p[0].Flags[0]; strings.ContainsRune(f.Path, '‮') || strings.ContainsRune(f.Detail, '\a') {
		t.Errorf("strings not re-sanitized: %q %q", f.Path, f.Detail)
	}
}

func TestStoreNoTempFilesLeft(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Merge(InPlaceKey("/ws"), Record{Kind: "in-place"}, time.Unix(int64(i), 0), Report{Flags: []Flag{fl("a", ClassArchive)}}); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(s.Dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

// A record of 5,000 maximum-size flags would exceed the read cap; the writer
// must trim the weakest and say so, so the file it wrote is one it can read.
func TestStoreWriteTrimsToReadCap(t *testing.T) {
	s := newStore(t)
	key := InPlaceKey("/ws")
	big := strings.Repeat("é", 1000)
	var flags []Flag
	for i := 0; i < DefaultLimits().MaxFlags; i++ {
		flags = append(flags, Flag{Path: fmt.Sprintf("%04d%s.zip", i, big), Classes: []Class{ClassArchive}, Detail: big, Target: big})
	}
	flags = append(flags, Flag{Path: "strong.pdf", Classes: []Class{ClassMismatch}, Detail: big})
	rec, err := s.Merge(key, Record{Kind: "in-place"}, time.Unix(1, 0), Report{Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, rec.ID+".json"))
	if err != nil || fi.Size() > maxRecordFile {
		t.Fatalf("file size %v err %v", fi, err)
	}
	p, err := s.Pending()
	if err != nil || len(p) != 1 {
		t.Fatalf("the written file must be readable: %v %v", p, err)
	}
	if p[0].Flags[0].Path != "strong.pdf" || !strings.Contains(p[0].Truncated, "record size cap") {
		t.Fatalf("strongest must survive and the trim be reported: first=%q trunc=%q", p[0].Flags[0].Path, p[0].Truncated)
	}
}
