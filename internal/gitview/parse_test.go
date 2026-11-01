package gitview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const oid = "0123456789012345678901234567890123456789"

func rec(parts ...string) []byte {
	return []byte(strings.Join(parts, "\x00") + "\x00")
}

func one(xy, path string) string {
	return "1 " + xy + " N... 100644 100644 100644 " + oid + " " + oid + " " + path
}

func TestParseStatusCodes(t *testing.T) {
	unm := func(xy, path string) string {
		return "u " + xy + " N... 100644 100644 100644 100644 " + oid + " " + oid + " " + oid + " " + path
	}
	tests := []struct {
		rec  string
		want string
	}{
		{one(".M", "f"), "M"},
		{one("M.", "f"), "M"},
		{one("MM", "f"), "M"},
		{one("A.", "f"), "A"},
		{one("AM", "f"), "A"},
		{one(".D", "f"), "D"},
		{one("D.", "f"), "D"},
		{one("T.", "f"), "M"},
		{one("UU", "f"), "C"},
		{one("AA", "f"), "C"},
		{one("DD", "f"), "C"},
		{one("AU", "f"), "C"},
		{unm("UU", "f"), "C"},
		{unm("AA", "f"), "C"},
		{"? f", "U"},
		{"? dir/", "U"},
	}
	for _, c := range tests {
		got, noHead, many := ParseStatus(rec(c.rec), 100)
		if noHead || many || len(got) != 1 || got[0].X != c.want {
			t.Errorf("%q: got %+v noHead=%v many=%v, want X=%s", c.rec, got, noHead, many, c.want)
		}
	}
	got, _, _ := ParseStatus(rec("? dir/"), 10)
	if got[0].P != "dir/" {
		t.Errorf("dir trailing slash lost: %+v", got)
	}
}

func TestParseStatusIgnoredAndHeaders(t *testing.T) {
	b := rec("# branch.oid "+oid, "# branch.head main", one(".M", "a"), "! ignored.log", "? new")
	got, noHead, _ := ParseStatus(b, 100)
	want := []Entry{{"a", "M"}, {"new", "U"}}
	if noHead || !slices.Equal(got, want) {
		t.Errorf("got %+v noHead=%v", got, noHead)
	}
}

func TestParseStatusRenameConsumesOrig(t *testing.T) {
	r2 := "2 R. N... 100644 100644 100644 " + oid + " " + oid + " R100 new name"
	got, _, _ := ParseStatus(rec(r2, "old name", one(".M", "z")), 100)
	want := []Entry{{"new name", "M"}, {"z", "M"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestParseStatusOddNames(t *testing.T) {
	names := []string{"with space.txt", "dir/ünï côde ✓.go", "new\nline.txt", "tab\there.txt", "trail space ", "a  b"}
	for _, n := range names {
		for _, r := range []string{one(".M", n), "? " + n} {
			got, _, _ := ParseStatus(rec(r), 100)
			if len(got) != 1 || got[0].P != n {
				t.Errorf("name %q via %q: got %+v", n, r[:1], got)
			}
		}
	}
	// Several records around a newline name stay separate.
	got, _, _ := ParseStatus(rec(one(".M", "a"), "? b\nc", one("A.", "d")), 100)
	if len(got) != 3 || got[1].P != "b\nc" {
		t.Errorf("got %+v", got)
	}
}

func TestParseStatusHostile(t *testing.T) {
	bad := []string{"/etc/passwd", "a/../../x", "..", "../x", "a/..", "", strings.Repeat("a", 5000)}
	for _, n := range bad {
		for _, r := range []string{one(".M", n), "? " + n} {
			got, _, many := ParseStatus(rec(r), 100)
			if len(got) != 0 || many {
				t.Errorf("hostile %.20q accepted via %q: %+v", n, r[:1], got)
			}
		}
	}
	// Exactly the limit is fine, and a name with ".." inside a segment is fine.
	ok := []string{strings.Repeat("a", 4096), "a..b/c", "..foo"}
	for _, n := range ok {
		got, _, _ := ParseStatus(rec("? "+n), 100)
		if len(got) != 1 {
			t.Errorf("legit %.20q dropped", n)
		}
	}
	// Garbage records are skipped, not fatal.
	got, _, _ := ParseStatus(rec("1 .M short", "", "x", "u UU", one(".M", "good")), 100)
	if len(got) != 1 || got[0].P != "good" {
		t.Errorf("garbage handling: %+v", got)
	}
}

func TestParseStatusCap(t *testing.T) {
	build := func(n int) []byte {
		var b []byte
		for i := 0; i < n; i++ {
			b = append(b, rec(fmt.Sprintf("? f%d", i))...)
		}
		return b
	}
	got, _, many := ParseStatus(build(5001), 5000)
	if !many || len(got) > 5000 {
		t.Errorf("5001: many=%v len=%d", many, len(got))
	}
	got, _, many = ParseStatus(build(5000), 5000)
	if many || len(got) != 5000 {
		t.Errorf("5000: many=%v len=%d", many, len(got))
	}
}

func TestParseStatusNoHead(t *testing.T) {
	got, noHead, _ := ParseStatus(rec("# branch.oid (initial)", "# branch.head main", "? a"), 10)
	if !noHead || len(got) != 1 {
		t.Errorf("noHead=%v got=%+v", noHead, got)
	}
	_, noHead, _ = ParseStatus(rec("# branch.oid "+oid), 10)
	if noHead {
		t.Error("noHead set on a repo with a commit")
	}
}

func TestDiffKind(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "unchanged"},
		{"diff --git a/x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", "text"},
		{"diff --git a/x b/x\nBinary files a/x and b/x differ\n", "binary"},
		{"Binary files /dev/null and b/x differ\n", "binary"},
		{"diff --git a/x b/x\n@@ -1 +1 @@\n-Binary files a and b differ\n+x\n", "text"},
		{"diff --git a/x b/x\nsimilarity index 100%\n", "text"},
	}
	for _, c := range tests {
		if got := DiffKind([]byte(c.in)); got != c.want {
			t.Errorf("DiffKind(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
