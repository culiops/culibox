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
		got, noHead, many, _, _ := ParseStatus(rec(c.rec), 100)
		if noHead || many || len(got) != 1 || got[0].X != c.want {
			t.Errorf("%q: got %+v noHead=%v many=%v, want X=%s", c.rec, got, noHead, many, c.want)
		}
	}
	got, _, _, _, _ := ParseStatus(rec("? dir/"), 10)
	if got[0].P != "dir/" {
		t.Errorf("dir trailing slash lost: %+v", got)
	}
}

func TestParseStatusIgnoredAndHeaders(t *testing.T) {
	b := rec("# branch.oid "+oid, "# branch.head main", one(".M", "a"), "! ignored.log", "? new")
	got, noHead, _, _, _ := ParseStatus(b, 100)
	want := []Entry{{"a", "M"}, {"new", "U"}}
	if noHead || !slices.Equal(got, want) {
		t.Errorf("got %+v noHead=%v", got, noHead)
	}
}

func TestParseStatusRenameConsumesOrig(t *testing.T) {
	r2 := "2 R. N... 100644 100644 100644 " + oid + " " + oid + " R100 new name"
	got, _, _, _, _ := ParseStatus(rec(r2, "old name", one(".M", "z")), 100)
	want := []Entry{{"new name", "M"}, {"z", "M"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestParseStatusMalformedRenameDoesNotEatNext(t *testing.T) {
	got, _, _, _, _ := ParseStatus(rec("2 R. short", one(".M", "valid")), 100)
	if len(got) != 1 || got[0].P != "valid" {
		t.Errorf("got %+v", got)
	}
}

func TestParseStatusOddNames(t *testing.T) {
	names := []string{"with space.txt", "dir/ünï côde ✓.go", "new\nline.txt", "tab\there.txt", "trail space ", "a  b"}
	for _, n := range names {
		for _, r := range []string{one(".M", n), "? " + n} {
			got, _, _, _, _ := ParseStatus(rec(r), 100)
			if len(got) != 1 || got[0].P != n {
				t.Errorf("name %q via %q: got %+v", n, r[:1], got)
			}
		}
	}
	// Several records around a newline name stay separate.
	got, _, _, _, _ := ParseStatus(rec(one(".M", "a"), "? b\nc", one("A.", "d")), 100)
	if len(got) != 3 || got[1].P != "b\nc" {
		t.Errorf("got %+v", got)
	}
}

func TestParseStatusHostile(t *testing.T) {
	bad := []string{"/etc/passwd", "a/../../x", "..", "../x", "bad\xffutf8", "a/..", "", strings.Repeat("a", 5000)}
	for _, n := range bad {
		for _, r := range []string{one(".M", n), "? " + n} {
			got, _, many, _, _ := ParseStatus(rec(r), 100)
			if len(got) != 0 || many {
				t.Errorf("hostile %.20q accepted via %q: %+v", n, r[:1], got)
			}
		}
	}
	// Exactly the limit is fine, and a name with ".." inside a segment is fine.
	ok := []string{strings.Repeat("a", 4096), "a..b/c", "..foo"}
	for _, n := range ok {
		got, _, _, _, _ := ParseStatus(rec("? "+n), 100)
		if len(got) != 1 {
			t.Errorf("legit %.20q dropped", n)
		}
	}
	// Garbage records are skipped, not fatal.
	got, _, _, _, _ := ParseStatus(rec("1 .M short", "", "x", "u UU", one(".M", "good")), 100)
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
	got, _, many, _, _ := ParseStatus(build(5001), 5000)
	if !many || len(got) > 5000 {
		t.Errorf("5001: many=%v len=%d", many, len(got))
	}
	got, _, many, _, _ = ParseStatus(build(5000), 5000)
	if many || len(got) != 5000 {
		t.Errorf("5000: many=%v len=%d", many, len(got))
	}
}

func TestParseStatusNoHead(t *testing.T) {
	got, noHead, _, _, _ := ParseStatus(rec("# branch.oid (initial)", "# branch.head main", "? a"), 10)
	if !noHead || len(got) != 1 {
		t.Errorf("noHead=%v got=%+v", noHead, got)
	}
	_, noHead, _, _, _ = ParseStatus(rec("# branch.oid "+oid), 10)
	if noHead {
		t.Error("noHead set on a repo with a commit")
	}
}

func TestParseStatusBranchAndHead(t *testing.T) {
	b := rec("# branch.oid "+oid, "# branch.head feat/files-dock", "# branch.upstream origin/feat/files-dock", "# branch.ab +2 -0", one(".M", "a"))
	got, noHead, many, branch, head := ParseStatus(b, 100)
	if branch != "feat/files-dock" || head != oid || noHead || many || len(got) != 1 {
		t.Errorf("branch=%q head=%q noHead=%v many=%v got=%+v", branch, head, noHead, many, got)
	}
	// A repository with no commits has a branch but no head.
	_, noHead, _, branch, head = ParseStatus(rec("# branch.oid (initial)", "# branch.head main"), 10)
	if !noHead || branch != "main" || head != "" {
		t.Errorf("initial: noHead=%v branch=%q head=%q", noHead, branch, head)
	}
	// The headers come before the entries, so a cut-off list still has them.
	_, _, many, branch, head = ParseStatus(rec("# branch.oid "+oid, "# branch.head main", "? a", "? b"), 1)
	if !many || branch != "main" || head != oid {
		t.Errorf("toomany: many=%v branch=%q head=%q", many, branch, head)
	}
}

func TestParseStatusDetached(t *testing.T) {
	_, _, _, branch, head := ParseStatus(rec("# branch.oid "+oid, "# branch.head (detached)"), 10)
	if branch != "" || head != oid {
		t.Errorf("detached: branch=%q head=%q, want an empty branch and the head", branch, head)
	}
}

func TestParseStatusBranchAndHeadHostile(t *testing.T) {
	// The oid goes into a cache key and is compared with merge-base's output, so
	// only a full lowercase hex id is taken; a branch is text, never a control.
	for _, bad := range []string{"abc", strings.Repeat("A", 40), oid + "0", oid[:39], "(initial)", oid[:39] + "g", "../x"} {
		if _, _, _, _, head := ParseStatus(rec("# branch.oid "+bad), 10); head != "" {
			t.Errorf("head %q accepted as %q", bad, head)
		}
	}
	for _, bad := range []string{"bad\xffutf8", "tab\there", "esc\x1b[31m", strings.Repeat("a", 256)} {
		if _, _, _, branch, _ := ParseStatus(rec("# branch.head "+bad), 10); branch != "" {
			t.Errorf("branch %.20q accepted", bad)
		}
	}
	// 64 hex digits: a sha256 repository.
	h64 := strings.Repeat("ab", 32)
	if _, _, _, _, head := ParseStatus(rec("# branch.oid "+h64), 10); head != h64 {
		t.Errorf("sha256 head = %q", head)
	}
	if _, _, _, branch, _ := ParseStatus(rec("# branch.head "+strings.Repeat("a", 255)), 10); len(branch) != 255 {
		t.Error("a 255-byte branch name was dropped")
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

// raw is one `--raw` header of ParseNumstat's input; its path is the next record.
func raw(letter string) string {
	return ":100644 100644 " + oid[:7] + " " + oid[:7] + " " + letter
}

func TestParseNumstat(t *testing.T) {
	// git prints every raw record, then every numstat record (measured, git 2.39).
	b := rec(raw("M"), "a.go", raw("A"), "new.txt", raw("D"), "gone.txt", raw("T"), "link", raw("M"), "img.png",
		"3\t1\ta.go", "2\t0\tnew.txt", "0\t4\tgone.txt", "1\t1\tlink", "-\t-\timg.png")
	got, many := ParseNumstat(b, 100)
	want := []NumEntry{
		{P: "a.go", X: "M", A: 3, D: 1},
		{P: "new.txt", X: "A", A: 2},
		{P: "gone.txt", X: "D", D: 4},
		{P: "link", X: "M", A: 1, D: 1}, // a type change reads as a modification
		{P: "img.png", X: "M", Bin: true},
	}
	if many || !slices.Equal(got, want) {
		t.Fatalf("got %+v many=%v\nwant %+v", got, many, want)
	}
}

func TestParseNumstatShapes(t *testing.T) {
	// The task's own example: one raw record and its numstat record.
	got, _ := ParseNumstat([]byte(":100644 100644 "+oid+" "+oid+" M\x00a.go\x00"+"3\t1\ta.go\x00"), 10)
	if !slices.Equal(got, []NumEntry{{P: "a.go", X: "M", A: 3, D: 1}}) {
		t.Errorf("raw plus numstat: %+v", got)
	}
	// A binary file: both counts are "-".
	got, _ = ParseNumstat([]byte("-\t-\timg.png\x00"), 10)
	if !slices.Equal(got, []NumEntry{{P: "img.png", X: "M", Bin: true}}) {
		t.Errorf("binary: %+v", got)
	}
	// A path holds tabs and spaces: only the first two tabs separate fields.
	got, _ = ParseNumstat(rec(raw("A"), "t\tab c.txt", "2\t0\tt\tab c.txt"), 10)
	if !slices.Equal(got, []NumEntry{{P: "t\tab c.txt", X: "A", A: 2}}) {
		t.Errorf("tab in a path: %+v", got)
	}
	// A path that looks like a raw header is a path: the record after a header is always one.
	got, _ = ParseNumstat(rec(raw("A"), ":100644 100644 x y M", "1\t0\t:100644 100644 x y M"), 10)
	if !slices.Equal(got, []NumEntry{{P: ":100644 100644 x y M", X: "A", A: 1}}) {
		t.Errorf("header-shaped path: %+v", got)
	}
	// Numstat alone (no letters): the entry is a modification, the safe default.
	got, _ = ParseNumstat(rec("5\t2\tb.go"), 10)
	if !slices.Equal(got, []NumEntry{{P: "b.go", X: "M", A: 5, D: 2}}) {
		t.Errorf("numstat only: %+v", got)
	}
	// A raw record with no numstat has no counts, and a count is never guessed.
	got, _ = ParseNumstat(rec(raw("M"), "a.go", raw("M"), "b.go", "1\t1\tb.go"), 10)
	if !slices.Equal(got, []NumEntry{{P: "b.go", X: "M", A: 1, D: 1}}) {
		t.Errorf("raw without numstat: %+v", got)
	}
	// "U" is the web layer's untracked letter: no other letter can be it. An
	// unmerged or unknown letter is a modification.
	for _, l := range []string{"U", "X", "B", "C", "R", "m", ""} {
		got, _ = ParseNumstat(rec(raw(l), "f", "1\t0\tf"), 10)
		if len(got) != 1 || got[0].X != "M" {
			t.Errorf("letter %q: %+v, want X=M", l, got)
		}
	}
	// Nothing at all is no entries, not an error.
	if got, many := ParseNumstat(nil, 10); len(got) != 0 || many {
		t.Errorf("empty: %+v many=%v", got, many)
	}
}

func TestParseNumstatHostile(t *testing.T) {
	bad := []string{"/etc/passwd", "a/../../x", "..", "../x", "bad\xffutf8", "a/..", "", strings.Repeat("a", 5000)}
	for _, p := range bad {
		for _, b := range [][]byte{rec(raw("M"), p, "1\t0\t"+p), rec("1\t0\t" + p)} {
			if got, many := ParseNumstat(b, 100); len(got) != 0 || many {
				t.Errorf("hostile %.20q accepted: %+v", p, got)
			}
		}
	}
	// The counts are digits, both or neither "-", and never more than nine of them.
	for _, c := range []string{
		"x\t1\tf", "1\tx\tf", "-\t1\tf", "1\t-\tf", "-1\t0\tf", "+1\t0\tf", "1 \t0\tf", "\t\tf", "1\t0", "1", "10000000000\t0\tf",
		"1\t0\t", "1\t\tf", "0x10\t0\tf", "1e3\t0\tf",
	} {
		if got, many := ParseNumstat(rec(c), 100); len(got) != 0 || many {
			t.Errorf("malformed numstat %q accepted: %+v", c, got)
		}
	}
	// A header at the very end has no path record: it is skipped, not a panic.
	if got, _ := ParseNumstat(rec("1\t0\tf", raw("M")), 10); len(got) != 1 || got[0].P != "f" {
		t.Errorf("trailing header: %+v", got)
	}
	// One path twice: the first counts stand.
	got, _ := ParseNumstat(rec("1\t0\tf", "9\t9\tf"), 10)
	if !slices.Equal(got, []NumEntry{{P: "f", X: "M", A: 1}}) {
		t.Errorf("duplicate path: %+v", got)
	}
	// Legit names that merely contain "..".
	for _, n := range []string{strings.Repeat("a", 4096), "a..b/c", "..foo"} {
		if got, _ := ParseNumstat(rec("1\t0\t"+n), 10); len(got) != 1 {
			t.Errorf("legit %.20q dropped", n)
		}
	}
}

func TestParseNumstatCap(t *testing.T) {
	build := func(n int) []byte {
		var b []byte
		for i := 0; i < n; i++ {
			b = append(b, rec(raw("M"), fmt.Sprintf("f%d", i))...)
		}
		for i := 0; i < n; i++ {
			b = append(b, rec(fmt.Sprintf("1\t1\tf%d", i))...)
		}
		return b
	}
	got, many := ParseNumstat(build(5001), 5000)
	if !many || len(got) > 5000 {
		t.Errorf("5001: many=%v len=%d", many, len(got))
	}
	got, many = ParseNumstat(build(5000), 5000)
	if many || len(got) != 5000 || got[4999] != (NumEntry{P: "f4999", X: "M", A: 1, D: 1}) {
		t.Errorf("5000: many=%v len=%d", many, len(got))
	}
	// A dropped (unsafe) record does not count toward the cap.
	got, many = ParseNumstat(rec("1\t0\t../x", "1\t0\ta", "1\t0\tb"), 2)
	if many || len(got) != 2 {
		t.Errorf("dropped record counted: many=%v got=%+v", many, got)
	}
	got, many = ParseNumstat(rec("1\t0\ta", "1\t0\tb", "1\t0\tc"), 2)
	if !many || len(got) != 2 {
		t.Errorf("3 over a cap of 2: many=%v got=%+v", many, got)
	}
}
