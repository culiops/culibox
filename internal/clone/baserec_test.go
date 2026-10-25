package clone

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shaN is a well-formed SHA that differs per n.
func shaN(n int) string { return fmt.Sprintf("%040x", n+1) }

func TestBaseRecordMissingIsNotExists(t *testing.T) {
	rec, err := ReadBaseRecord(t.TempDir(), "/some/clone")
	if err != nil || rec.Exists || rec.Unverified || len(rec.SHAs) != 0 {
		t.Fatalf("rec=%+v err=%v; want zero, nil", rec, err)
	}
}

func TestResetThenAppendDedupes(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	a, b := shaN(1), shaN(2)
	for _, f := range []func() error{
		func() error { return ResetBaseRecord(dd, dir, a) },
		func() error { return AppendBase(dd, dir, a) },
		func() error { return AppendBase(dd, dir, b) },
		func() error { return AppendBase(dd, dir, b) },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := ReadBaseRecord(dd, dir)
	if err != nil || !rec.Exists || rec.Unverified || !slices.Equal(rec.SHAs, []string{a, b}) {
		t.Fatalf("rec=%+v err=%v; want [A B] trusted", rec, err)
	}
	p, _ := baseRecordPath(dd, dir)
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	ds, _ := os.Stat(filepath.Dir(p))
	if ds.Mode().Perm() != 0o700 {
		t.Errorf("record dir mode = %v, want 0700", ds.Mode().Perm())
	}
}

func TestAppendWithoutRecordMarksUnverified(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	a := shaN(1)
	if err := AppendBase(dd, dir, a); err != nil {
		t.Fatal(err)
	}
	p, _ := baseRecordPath(dd, dir)
	raw, _ := os.ReadFile(p)
	if string(raw) != "unverified\n"+a+"\n" {
		t.Fatalf("file = %q", raw)
	}
	rec, err := ReadBaseRecord(dd, dir)
	if err != nil || !rec.Unverified || !slices.Equal(rec.SHAs, []string{a}) {
		t.Fatalf("rec=%+v err=%v", rec, err)
	}
}

func TestResetClearsUnverified(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	if err := AppendBase(dd, dir, shaN(1)); err != nil {
		t.Fatal(err)
	}
	if err := ResetBaseRecord(dd, dir, shaN(2)); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadBaseRecord(dd, dir)
	if err != nil || rec.Unverified || !slices.Equal(rec.SHAs, []string{shaN(2)}) {
		t.Fatalf("rec=%+v err=%v", rec, err)
	}
}

func TestAppendCapsAt1000(t *testing.T) {
	for _, unverified := range []bool{false, true} {
		dd, dir := t.TempDir(), "/some/clone"
		if unverified {
			if err := AppendBase(dd, dir, shaN(0)); err != nil {
				t.Fatal(err)
			}
		} else if err := ResetBaseRecord(dd, dir, shaN(0)); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < 1005; i++ {
			if err := AppendBase(dd, dir, shaN(i)); err != nil {
				t.Fatal(err)
			}
		}
		rec, err := ReadBaseRecord(dd, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.SHAs) != maxBaseLines || rec.SHAs[0] != shaN(5) || rec.SHAs[len(rec.SHAs)-1] != shaN(1004) {
			t.Errorf("unverified=%v: len=%d first=%s last=%s; want 1000, #5 .. #1004", unverified, len(rec.SHAs), rec.SHAs[0], rec.SHAs[len(rec.SHAs)-1])
		}
		if rec.Unverified != unverified {
			t.Errorf("Unverified = %v, want %v (the marker must survive the cap)", rec.Unverified, unverified)
		}
	}
}

func TestBaseRecordRefusesSymlinkAndJunk(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	p, _ := baseRecordPath(dd, dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(shaN(1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBaseRecord(dd, dir); err == nil {
		t.Error("a symlink at the record path was read")
	}
	os.Remove(p)

	junk := "zz\n"
	if err := os.WriteFile(p, []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBaseRecord(dd, dir); err == nil {
		t.Error("a junk line was accepted")
	}
	if err := AppendBase(dd, dir, shaN(2)); err == nil {
		t.Error("Append on a corrupt record succeeded")
	}
	if raw, _ := os.ReadFile(p); string(raw) != junk {
		t.Errorf("a corrupt record was rewritten: %q", raw)
	}
	// A junk line below a valid marker is junk too; so is an empty line.
	for _, bad := range []string{"unverified\nzz\n", shaN(1) + "\n\n" + shaN(2) + "\n", "unverified\nunverified\n"} {
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadBaseRecord(dd, dir); err == nil {
			t.Errorf("record %q was accepted", bad)
		}
	}
	if err := ResetBaseRecord(dd, dir, "not-a-sha"); err == nil {
		t.Error("Reset accepted a malformed SHA")
	}
	if err := AppendBase(dd, dir, strings.Repeat("A", 40)); err == nil {
		t.Error("Append accepted a malformed SHA")
	}
}

func TestBaseRecordKeyIsResolvedDir(t *testing.T) {
	dd := t.TempDir()
	real := filepath.Join(t.TempDir(), "clone")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "spelling")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p1, err1 := baseRecordPath(dd, real)
	p2, err2 := baseRecordPath(dd, link)
	if err1 != nil || err2 != nil || p1 != p2 {
		t.Fatalf("%q (%v) != %q (%v)", p1, err1, p2, err2)
	}
	if filepath.Dir(p1) != filepath.Join(dd, baseRecordDir) {
		t.Errorf("record not under %s: %s", baseRecordDir, p1)
	}
}
