package gitview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	shaA = "42142204ed1e843e7cb60c8919c217d7dd5dcf66"
	shaB = "530f15eab796ec421724ef86a5558db1c62fcff2"
	shaZ = "0000000000000000000000000000000000000000"
)

// realPorcelain is git 2.39.5's blame --porcelain of a 5-line file: two authors, a commit seen again without its
// metadata, and an uncommitted last line.
const realPorcelain = shaA + ` 1 1 1
author Minh
author-mail <m@x.io>
author-time 1795438746
author-tz +0000
committer Minh
committer-mail <m@x.io>
committer-time 1795438746
committer-tz +0000
summary add f (#12)
filename f
	a
` + shaB + ` 2 2 1
author Lan Tran
author-mail <l@x.io>
author-time 1795438750
author-tz +0000
committer Lan Tran
committer-mail <l@x.io>
committer-time 1795438750
committer-tz +0000
summary edit
previous ` + shaA + ` f
filename f
	B
` + shaA + ` 3 3 1
	c
` + shaB + ` 4 4 1
	d
` + shaZ + ` 5 5 1
author Not Committed Yet
author-mail <not.committed.yet>
author-time 1795438760
author-tz +0000
committer Not Committed Yet
committer-mail <not.committed.yet>
committer-time 1795438760
committer-tz +0000
summary Version of f from f
previous ` + shaB + ` f
filename f
	e
`

func TestParseBlameReal(t *testing.T) {
	commits, lines, err := ParseBlame([]byte(realPorcelain))
	if err != nil {
		t.Fatal(err)
	}
	want := []BlameCommit{
		{SHA: shaA, Author: "Minh", Email: "m@x.io", Time: 1795438746, Summary: "add f (#12)", Ref: "#12"},
		{SHA: shaB, Author: "Lan Tran", Email: "l@x.io", Time: 1795438750, Summary: "edit"},
		{SHA: shaZ, Author: "Not Committed Yet", Email: "not.committed.yet", Time: 1795438760, Summary: "Version of f from f"},
	}
	if !slices.Equal(commits, want) {
		t.Fatalf("commits\n got %+v\nwant %+v", commits, want)
	}
	if w := [][2]int{{0, 1}, {1, 1}, {0, 1}, {1, 1}, {2, 1}}; !slices.Equal(lines, w) {
		t.Fatalf("lines %v, want %v", lines, w)
	}
	if !ZeroSHA(commits[2].SHA) || ZeroSHA(commits[0].SHA) {
		t.Fatal("ZeroSHA")
	}
}

// group builds one porcelain group: a header for n lines starting at final line at, metadata only when meta is set.
func group(sha string, at, n int, meta string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d %d %d\n", sha, at, at, n)
	b.WriteString(meta)
	for i := range n {
		if i > 0 {
			fmt.Fprintf(&b, "%s %d %d\n", sha, at+i, at+i)
		}
		fmt.Fprintf(&b, "\tline %d\n", at+i)
	}
	return b.String()
}

func meta(author, summary string) string {
	return "author " + author + "\nauthor-mail <a@x>\nauthor-time 100\nsummary " + summary + "\nfilename f\n"
}

func TestParseBlameMergesRunsAndReadsBoundary(t *testing.T) {
	sha256 := strings.Repeat("ab", 32)
	in := group(shaA, 1, 3, meta("A", "s")+"boundary\n") + group(sha256, 4, 1, meta("B", "t")) + group(shaA, 5, 2, "")
	commits, lines, err := ParseBlame([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !commits[0].Boundary || commits[1].Boundary || commits[1].SHA != sha256 {
		t.Fatalf("commits %+v", commits)
	}
	if w := [][2]int{{0, 3}, {1, 1}, {0, 2}}; !slices.Equal(lines, w) {
		t.Fatalf("lines %v, want %v", lines, w)
	}
	// Two groups of the same commit back to back are one run.
	_, lines, err = ParseBlame([]byte(group(shaA, 1, 1, meta("A", "s")) + group(shaA, 2, 2, "")))
	if err != nil || !slices.Equal(lines, [][2]int{{0, 3}}) {
		t.Fatalf("lines %v err %v", lines, err)
	}
}

func TestParseBlameRejects(t *testing.T) {
	good := group(shaA, 1, 1, meta("A", "s"))
	for name, in := range map[string]string{
		"gap in final lines":    good + group(shaB, 3, 1, meta("B", "t")),
		"starts past line 1":    group(shaA, 2, 1, meta("A", "s")),
		"content before header": "\tx\n" + good,
		"group count short":     shaA + " 1 1 2\n" + meta("A", "s") + "\tone\n",
		"two contents":          shaA + " 1 1 1\n" + meta("A", "s") + "\tone\n\ttwo\n",
		"uppercase sha":         strings.ToUpper(shaA) + " 1 1 1\n" + meta("A", "s") + "\tx\n",
		"short sha":             shaA[:39] + " 1 1 1\n" + meta("A", "s") + "\tx\n",
		"time not a number":     shaA + " 1 1 1\nauthor A\nauthor-time soon\n\tx\n",
		"no metadata":           shaA + " 1 1 1\n\tx\n",
		// A name holding a newline cannot slip in a header of its own: it breaks the line count.
		"injected header": shaA + " 1 1 1\nauthor A\n" + shaB + " 1 1 1\nauthor-time 1\nsummary s\n\tx\n",
	} {
		if c, l, err := ParseBlame([]byte(in)); err == nil {
			t.Errorf("%s: accepted: %+v %v", name, c, l)
		}
	}
	if c, l, err := ParseBlame(nil); err != nil || len(c) != 0 || len(l) != 0 {
		t.Errorf("empty file: %v %v %v", c, l, err)
	}
}

func TestParseBlameHostileText(t *testing.T) {
	long := strings.Repeat("é", 300)
	in := group(shaA, 1, 1, meta("Eve‮", "<img src=x onerror=alert(1)>")) +
		group(shaB, 2, 1, meta("B\x01ob", long)) +
		group(strings.Repeat("c", 40), 3, 1, meta("M\xffallory", "⁦rtl⁩ ‏mark"))
	commits, _, err := ParseBlame([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if commits[0].Author != "Eve" || commits[0].Summary != "<img src=x onerror=alert(1)>" {
		t.Errorf("commit 0 %+v", commits[0])
	}
	if commits[1].Author != "Bob" || utf8.RuneCountInString(commits[1].Summary) != 256 || !strings.HasSuffix(commits[1].Summary, "…") {
		t.Errorf("commit 1 %q %q", commits[1].Author, commits[1].Summary)
	}
	if !utf8.ValidString(commits[2].Author) || commits[2].Summary != "rtl mark" {
		t.Errorf("commit 2 %q %q", commits[2].Author, commits[2].Summary)
	}
}

func TestParseLogLast(t *testing.T) {
	c, err := ParseLogLast([]byte(shaB + "\x00Lan Tran\x00l@x.io\x001795438746\x00edit (#7)\x00\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w := (BlameCommit{SHA: shaB, Author: "Lan Tran", Email: "l@x.io", Time: 1795438746, Summary: "edit (#7)", Ref: "#7"}); *c != w {
		t.Fatalf("got %+v", *c)
	}
	if c, err := ParseLogLast(nil); c != nil || err != nil {
		t.Fatalf("empty: %v %v", c, err)
	}
	for _, in := range []string{
		shaB + "\x00A\x00e\x001\x00",           // four fields
		shaB + "\x00A\x00e\x001\x00s\x00x\x00", // a NUL inside the summary
		shaB + "\x00A\x00e\x00soon\x00s\x00",   // time
		"HEAD\x00A\x00e\x001\x00s\x00",         // id
		shaB + "\x00A\x00e\x001\x00s",          // unterminated
	} {
		if c, err := ParseLogLast([]byte(in)); err == nil {
			t.Errorf("%q accepted: %+v", in, c)
		}
	}
}

func TestRefOf(t *testing.T) {
	for in, want := range map[string]string{
		"fix (#12)":                      "#12",
		"fix (#12)  ":                    "#12",
		"Merge pull request #7 from a/b": "#7",
		"x (!34)":                        "!34",
		"x #12 y":                        "",
		"(#1234567890)":                  "",
		"fix (#0)":                       "",
		"Merge pull request #7":          "#7",
		"Merge pull request #7x":         "",
		"see (#12) later":                "",
	} {
		if got := refOf(in); got != want {
			t.Errorf("refOf(%q) = %q, want %q", in, got, want)
		}
	}
}
