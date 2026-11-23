package gitview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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

// blameSub is the git subcommand of a blame-or-log argv: gitSub, past any "-c key=value" before it.
func blameSub(a []string) string {
	i := slices.Index(a, "safe.directory=*")
	if i < 0 {
		return ""
	}
	for i++; i+1 < len(a) && a[i] == "-c"; i += 2 {
	}
	if i < len(a) {
		return a[i]
	}
	return ""
}

func blameSubs(f *fakeDocker) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, a := range f.args {
		out = append(out, blameSub(a))
	}
	return out
}

const logRecord = shaB + "\x00Lan Tran\x00l@x.io\x001795438750\x00edit\x00\n"

// blameDocker answers log with logRecord and blame with realPorcelain, unless the hooks say otherwise.
type blameHooks struct {
	log, blame func(ctx context.Context, a []string, w io.Writer) error
}

func blameDocker(h blameHooks) *fakeDocker {
	return &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		switch blameSub(a) {
		case "log":
			if h.log != nil {
				return h.log(ctx, a, w)
			}
			_, err := io.WriteString(w, logRecord)
			return err
		case "blame":
			if h.blame != nil {
				return h.blame(ctx, a, w)
			}
			_, err := io.WriteString(w, realPorcelain)
			return err
		case "status":
			return okStatus()(ctx, a, w)
		}
		return nil
	}}
}

var bkey = BlameKey{Head: shaB, Version: strings.Repeat("1", 64)}

func always() bool { return true }

func TestBlameRunsLogThenBlame(t *testing.T) {
	f := blameDocker(blameHooks{})
	r := newRunner(f, newClock())
	b := r.Blame(context.Background(), tgt, "f", bkey, always)
	if b.State != "ok" || b.Last == nil || b.Last.SHA != shaB || len(b.Commits) != 3 || len(b.Lines) != 5 || b.IgnoredRevs {
		t.Fatalf("blame = %+v", b)
	}
	if got := blameSubs(f); !slices.Equal(got, []string{"log", "blame"}) {
		t.Fatalf("runs %v", got)
	}
	if a := f.args[1]; a[len(a)-1] != "f" || a[len(a)-2] != "--" || slices.Contains(a, "--ignore-revs-file") {
		t.Fatalf("blame argv %q", a)
	}
}

func TestBlameNoCommitTouchedIt(t *testing.T) {
	f := blameDocker(blameHooks{log: func(context.Context, []string, io.Writer) error { return nil }})
	b := newRunner(f, newClock()).Blame(context.Background(), tgt, "f", bkey, always)
	if b.State != "ok" || b.Last != nil {
		t.Fatalf("blame = %+v", b)
	}
}

func TestBlameUntrackedFromGit(t *testing.T) {
	f := blameDocker(blameHooks{
		log: func(context.Context, []string, io.Writer) error { return nil },
		blame: func(context.Context, []string, io.Writer) error {
			return gitFatal(t, "fatal: no such path 'g' in HEAD")
		},
	})
	r := newRunner(f, newClock())
	if b := r.Blame(context.Background(), tgt, "g", bkey, always); b.State != "untracked" || len(b.Lines) != 0 {
		t.Fatalf("blame = %+v", b)
	}
}

func TestBlameFailures(t *testing.T) {
	for name, c := range map[string]struct {
		h     blameHooks
		state string
	}{
		"timeout": {blameHooks{blame: func(ctx context.Context, _ []string, _ io.Writer) error {
			<-ctx.Done()
			return ctx.Err()
		}}, "slow"},
		"log timeout": {blameHooks{log: func(ctx context.Context, _ []string, _ io.Writer) error {
			<-ctx.Done()
			return ctx.Err()
		}}, "slow"},
		"too big": {blameHooks{blame: func(_ context.Context, _ []string, w io.Writer) error {
			for {
				if _, err := io.WriteString(w, strings.Repeat("x", 1<<20)); err != nil {
					return err
				}
			}
		}}, "toobig"},
		"garbage": {blameHooks{blame: func(_ context.Context, _ []string, w io.Writer) error {
			_, err := io.WriteString(w, "<html>\n")
			return err
		}}, "unavailable"},
		"bad log record": {blameHooks{log: func(_ context.Context, _ []string, w io.Writer) error {
			_, err := io.WriteString(w, "nope\x00\n")
			return err
		}}, "unavailable"},
		"not a repo": {blameHooks{log: func(context.Context, []string, io.Writer) error {
			return gitFatal(t, "fatal: not a git repository (or any of the parent directories): .git")
		}}, "notrepo"},
		"docker fails": {blameHooks{blame: func(context.Context, []string, io.Writer) error {
			return errors.New("docker run: Unable to find image 'img:1' locally")
		}}, "unavailable"},
	} {
		f := blameDocker(c.h)
		r := newRunner(f, newClock())
		r.Timeout = 50 * time.Millisecond
		b := r.Blame(context.Background(), tgt, "f", bkey, always)
		if b.State != c.state {
			t.Errorf("%s: state %q (%+v), want %q", name, b.State, b.Reason, c.state)
		}
		n := f.callCount()
		r.Blame(context.Background(), tgt, "f", bkey, always)
		if f.callCount() == n {
			t.Errorf("%s: a failure was cached", name)
		}
	}
}

func TestBlameIgnoreRevsRetry(t *testing.T) {
	var withFlag []bool
	var mu sync.Mutex
	f := blameDocker(blameHooks{blame: func(_ context.Context, a []string, w io.Writer) error {
		has := slices.Contains(a, "--ignore-revs-file")
		mu.Lock()
		withFlag = append(withFlag, has)
		mu.Unlock()
		if has {
			return gitFatal(t, "fatal: invalid object name: zzz")
		}
		_, err := io.WriteString(w, realPorcelain)
		return err
	}})
	k := bkey
	k.IgnoreRevs = true
	b := newRunner(f, newClock()).Blame(context.Background(), tgt, "f", k, always)
	if b.State != "ok" || b.IgnoredRevs || !slices.Equal(withFlag, []bool{true, false}) {
		t.Fatalf("blame %+v, runs with the flag %v", b, withFlag)
	}

	f = blameDocker(blameHooks{})
	b = newRunner(f, newClock()).Blame(context.Background(), tgt, "f", k, always)
	if b.State != "ok" || !b.IgnoredRevs || !slices.Contains(f.args[1], "--ignore-revs-file") {
		t.Fatalf("blame %+v", b)
	}
	// A timeout is not the file's fault: no retry.
	f = blameDocker(blameHooks{blame: func(ctx context.Context, _ []string, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	r := newRunner(f, newClock())
	r.Timeout = 30 * time.Millisecond
	if b := r.Blame(context.Background(), tgt, "f", k, always); b.State != "slow" || runsOfBlame(f) != 1 {
		t.Fatalf("blame %+v, %d blame runs", b, runsOfBlame(f))
	}
}

func runsOfBlame(f *fakeDocker) int {
	n := 0
	for _, s := range blameSubs(f) {
		if s == "blame" {
			n++
		}
	}
	return n
}

func TestBlameCache(t *testing.T) {
	f := blameDocker(blameHooks{})
	r := newRunner(f, newClock())
	ctx := context.Background()
	r.Blame(ctx, tgt, "f", bkey, always)
	b := r.Blame(ctx, tgt, "f", bkey, always)
	if f.callCount() != 2 || b.State != "ok" {
		t.Fatalf("second call ran git: %d runs", f.callCount())
	}
	b.Commits[0].Author = "changed by a caller"
	if again := r.Blame(ctx, tgt, "f", bkey, always); again.Commits[0].Author != "Minh" {
		t.Fatal("the cache hands out its own slices")
	}
	k := bkey
	k.Version = strings.Repeat("2", 64)
	r.Blame(ctx, tgt, "f", k, always)
	k = bkey
	k.Head = shaA
	r.Blame(ctx, tgt, "f", k, always)
	if f.callCount() != 6 {
		t.Fatalf("a new version or HEAD must run again: %d runs", f.callCount())
	}
	// The file changed while git read it: not cached, and said so.
	k.Version = strings.Repeat("3", 64)
	if b := r.Blame(ctx, tgt, "f", k, func() bool { return false }); b.State != "changed" || len(b.Lines) != 0 {
		t.Fatalf("blame %+v", b)
	}
	r.Blame(ctx, tgt, "f", k, always)
	if f.callCount() != 10 {
		t.Fatalf("a changed answer was cached: %d runs", f.callCount())
	}
}

func TestBlameCacheIsLRU(t *testing.T) {
	f := blameDocker(blameHooks{})
	c := newClock()
	r := newRunner(f, c)
	ctx := context.Background()
	for i := range maxBlameCache {
		r.Blame(ctx, tgt, fmt.Sprintf("f%d", i), bkey, always)
		c.add(time.Second)
	}
	r.Blame(ctx, tgt, "f0", bkey, always) // f0 is now the newest
	c.add(time.Second)
	r.Blame(ctx, tgt, "new", bkey, always) // evicts f1
	n := f.callCount()
	r.Blame(ctx, tgt, "f0", bkey, always)
	if f.callCount() != n {
		t.Fatal("f0 was evicted although it was used last")
	}
	r.Blame(ctx, tgt, "f1", bkey, always)
	if f.callCount() != n+2 {
		t.Fatal("f1 was not evicted")
	}
}

func TestBlameRefusals(t *testing.T) {
	f := blameDocker(blameHooks{})
	r := newRunner(f, newClock())
	for _, c := range []struct {
		t   Target
		rel string
	}{
		{Target{Top: "repo"}, "f"},
		{tgt, "../f"},
		{tgt, ""},
		{tgt, "/etc/passwd"},
	} {
		if b := r.Blame(context.Background(), c.t, c.rel, bkey, always); b.State != "unavailable" {
			t.Errorf("%+v %q: %+v", c.t, c.rel, b)
		}
	}
	if f.callCount() != 0 {
		t.Fatalf("%d runs for refused input", f.callCount())
	}
}

// A repository whose own blame.ignoreRevsFile names a missing file fails every plain blame: the retry drops every list.
func TestBlameRetryDropsTheRepositorysList(t *testing.T) {
	var runs [][]string
	var mu sync.Mutex
	f := blameDocker(blameHooks{blame: func(_ context.Context, a []string, w io.Writer) error {
		mu.Lock()
		runs = append(runs, a)
		mu.Unlock()
		if !slices.Contains(a, "--no-ignore-revs-file") {
			return gitFatal(t, "fatal: could not open object name list: nosuch")
		}
		_, err := io.WriteString(w, realPorcelain)
		return err
	}})
	b := newRunner(f, newClock()).Blame(context.Background(), tgt, "f", bkey, always)
	if b.State != "ok" || b.IgnoredRevs || len(runs) != 2 {
		t.Fatalf("blame %+v after %d runs", b, len(runs))
	}
	if slices.Contains(runs[1], "--ignore-revs-file") {
		t.Fatalf("retry argv %q", runs[1])
	}
}

// What would fail the same way again is not retried: no history, not a repository.
func TestBlameNoRetryForNoHistory(t *testing.T) {
	k := bkey
	k.IgnoreRevs = true
	for _, msg := range []string{"fatal: no such path 'g' in HEAD", "fatal: not a git repository (or any of the parent directories): .git"} {
		f := blameDocker(blameHooks{blame: func(context.Context, []string, io.Writer) error { return gitFatal(t, msg) }})
		newRunner(f, newClock()).Blame(context.Background(), tgt, "g", k, always)
		if n := runsOfBlame(f); n != 1 {
			t.Errorf("%q: %d blame runs, want 1", msg, n)
		}
	}
}

// One cache entry per file: a newer version replaces the older one.
func TestBlameCacheKeepsOneVersionPerFile(t *testing.T) {
	f := blameDocker(blameHooks{})
	r := newRunner(f, newClock())
	ctx := context.Background()
	for i := range 40 {
		k := bkey
		k.Version = fmt.Sprintf("%064d", i)
		r.Blame(ctx, tgt, "f", k, always)
	}
	r.Blame(ctx, tgt, "other", bkey, always)
	r.mu.Lock()
	n := len(r.blameCache)
	r.mu.Unlock()
	if n != 2 {
		t.Fatalf("%d cache entries, want 2 (one per file)", n)
	}
}
