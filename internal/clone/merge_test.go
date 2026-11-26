package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// mergeFx is a host repo of on branch br with one commit (a.txt = "a").
type mergeFx struct {
	t  *testing.T
	of string
	br string
}

func newMergeFx(t *testing.T, br string) *mergeFx {
	t.Helper()
	of := filepath.Join(t.TempDir(), "of")
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	noManagedSettings(t) // the host's own managed Claude Code settings never join a test
	x := &mergeFx{t: t, of: of, br: br}
	fixtureGit(t, of, "init", "-q")
	fixtureGit(t, of, "symbolic-ref", "HEAD", "refs/heads/"+br)
	x.commit("a", map[string]string{"a.txt": "a"})
	return x
}

// out runs a setup git command in of and returns its trimmed stdout.
func (x *mergeFx) out(args ...string) string {
	x.t.Helper()
	g, err := NewExecGit()
	if err != nil {
		x.t.Skip("no git on PATH")
	}
	env := append(CleanEnv(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@x", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@x")
	b, err := g.Run(context.Background(), x.of, env, args...)
	if err != nil {
		x.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func (x *mergeFx) write(rel, content string) {
	x.t.Helper()
	p := filepath.Join(x.of, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		x.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		x.t.Fatal(err)
	}
}

func (x *mergeFx) read(rel string) string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(x.of, filepath.FromSlash(rel)))
	if err != nil {
		x.t.Fatal(err)
	}
	return string(b)
}

// commit writes files (content "" deletes the file) and commits them on the
// checked-out branch.
func (x *mergeFx) commit(msg string, files map[string]string) {
	x.t.Helper()
	for rel, c := range files {
		if c == "" {
			fixtureGit(x.t, x.of, "rm", "-q", "--", rel)
			continue
		}
		x.write(rel, c)
		fixtureGit(x.t, x.of, "add", "--", rel)
	}
	fixtureGit(x.t, x.of, "commit", "-q", "--allow-empty", "-m", msg)
}

// box makes commits on top of the host branch's tip on a side branch, points
// refs/heads/box/<name> at the result and goes back to the host branch. Each
// map is one commit.
func (x *mergeFx) box(name string, commits ...map[string]string) string {
	x.t.Helper()
	fixtureGit(x.t, x.of, "checkout", "-q", "-b", "side")
	for i, c := range commits {
		x.commit("box "+string(rune('1'+i)), c)
	}
	sha := x.out("rev-parse", "HEAD")
	fixtureGit(x.t, x.of, "update-ref", "refs/heads/box/"+name, sha)
	fixtureGit(x.t, x.of, "checkout", "-q", x.br)
	fixtureGit(x.t, x.of, "branch", "-q", "-D", "side")
	return sha
}

func (x *mergeFx) tip() string { x.t.Helper(); return x.out("rev-parse", "refs/heads/"+x.br) }

// auditRec records Audit calls as "action|detail".
type auditRec struct {
	calls []string
	err   error
}

func (a *auditRec) fn(action, detail string) error {
	a.calls = append(a.calls, action+"|"+detail)
	return a.err
}

func (a *auditRec) actions() []string {
	var out []string
	for _, c := range a.calls {
		out = append(out, strings.SplitN(c, "|", 2)[0])
	}
	return out
}

// tips is the CloneTips of a clone whose own branch is where box/<br> is now:
// the box/<br> in the repo is this clone's work. Empty when there is no box/<br>.
func (x *mergeFx) tips() map[string]string {
	x.t.Helper()
	if sha := x.out("for-each-ref", "--format=%(objectname)", "refs/heads/box/"+x.br); sha != "" {
		return map[string]string{x.br: sha}
	}
	return map[string]string{}
}

func (x *mergeFx) run(g Git, a *auditRec) *MergeResult {
	x.t.Helper()
	return MergeHome(context.Background(), g, MergeRequest{Of: x.of, Audit: a.fn, CloneTips: x.tips()})
}

func wantMergeReason(t *testing.T, got *MergeResult, reason string) {
	t.Helper()
	if got == nil {
		t.Fatalf("result = nil, want reason %q", reason)
	}
	if got.Merged || got.Reason != reason {
		t.Fatalf("result = %+v, want reason %q and not merged", got, reason)
	}
}

func wantMerged(t *testing.T, got *MergeResult) {
	t.Helper()
	if got == nil || !got.Merged || got.Reason != "" {
		t.Fatalf("result = %+v, want merged", got)
	}
}

func TestMergeHomeFastForwards(t *testing.T) {
	x := newMergeFx(t, "main")
	from := x.tip()
	to := x.box("main",
		map[string]string{"a.txt": "a2", "b.txt": "b"},
		map[string]string{"c/d.txt": "d"})
	a := &auditRec{}
	got := x.run(newRec(t), a)
	wantMerged(t, got)
	if got.Branch != "main" || got.From != from || got.To != to || got.Commits != 2 || got.Files != 3 {
		t.Fatalf("result = %+v, want main %s..%s, 2 commits, 3 files", got, from, to)
	}
	if got.At.IsZero() {
		t.Fatal("At is not set")
	}
	if x.tip() != to {
		t.Fatalf("main = %s, want %s", x.tip(), to)
	}
	if c := x.read("a.txt"); c != "a2" {
		t.Fatalf("work tree a.txt = %q, want the box's a2", c)
	}
	if rl := x.out("reflog", "-1", "main"); !strings.Contains(rl, "culibox auto-merge") {
		t.Fatalf("reflog = %q, want culibox auto-merge", rl)
	}
	want := "clone_merge_home|main " + from[:7] + ".." + to[:7] + " 2 commits"
	if !slices.Equal(a.calls, []string{want}) {
		t.Fatalf("audit = %q, want [%q]", a.calls, want)
	}
}

func TestMergeHomeNothingToDo(t *testing.T) {
	t.Run("equal", func(t *testing.T) {
		x := newMergeFx(t, "main")
		fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.tip())
		if got := x.run(newRec(t), &auditRec{}); got != nil {
			t.Fatalf("result = %+v, want nil", got)
		}
	})
	t.Run("host ahead", func(t *testing.T) {
		x := newMergeFx(t, "main")
		fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.tip())
		x.commit("host", map[string]string{"h.txt": "h"})
		if got := x.run(newRec(t), &auditRec{}); got != nil {
			t.Fatalf("result = %+v, want nil", got)
		}
	})
	t.Run("no box branch", func(t *testing.T) {
		x := newMergeFx(t, "main")
		a := &auditRec{}
		if got := x.run(newRec(t), a); got != nil {
			t.Fatalf("result = %+v, want nil", got)
		}
		if len(a.calls) != 0 {
			t.Fatalf("audit = %q, want none", a.calls)
		}
	})
}

// box/X is shared by every clone of the repo and written by `culibox clone
// fetch` too: only a box/X at this clone's own tip for X is merged.
func TestMergeHomeOnlyThisClonesTip(t *testing.T) {
	cases := []struct {
		name  string
		tips  func(x *mergeFx, to string) map[string]string
		merge bool
	}{
		{"own tip", func(x *mergeFx, to string) map[string]string { return map[string]string{"main": to} }, true},
		{"another clone's", func(x *mergeFx, to string) map[string]string { return map[string]string{"main": x.tip()} }, false},
		{"no entry", func(x *mergeFx, to string) map[string]string { return map[string]string{"feat": to} }, false},
		{"nil map", func(x *mergeFx, to string) map[string]string { return nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newMergeFx(t, "main")
			before := x.tip()
			to := x.box("main", map[string]string{"b.txt": "b"})
			rec := newRec(t)
			a := &auditRec{}
			got := MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: a.fn, CloneTips: c.tips(x, to)})
			if c.merge {
				wantMerged(t, got)
				if x.tip() != to {
					t.Fatalf("main = %s, want %s", x.tip(), to)
				}
				return
			}
			if got != nil {
				t.Fatalf("result = %+v, want nil (not this clone's work)", got)
			}
			if x.tip() != before || len(a.calls) != 0 || mergeCalled(rec) {
				t.Fatalf("main moved or audit written: %q", a.calls)
			}
		})
	}
}

func TestMergeHomeSameNameOnly(t *testing.T) {
	x := newMergeFx(t, "master")
	to := x.box("master", map[string]string{"b.txt": "b"})
	got := x.run(newRec(t), &auditRec{})
	wantMerged(t, got)
	if got.Branch != "master" || x.tip() != to {
		t.Fatalf("result = %+v, master = %s, want merged into master at %s", got, x.tip(), to)
	}

	y := newMergeFx(t, "master")
	before := y.tip()
	y.box("feat", map[string]string{"b.txt": "b"})
	if got := y.run(newRec(t), &auditRec{}); got != nil {
		t.Fatalf("box/feat with master checked out: result = %+v, want nil", got)
	}
	if y.tip() != before {
		t.Fatal("master moved")
	}
}

func TestMergeHomeDetached(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	fixtureGit(t, x.of, "checkout", "-q", "--detach")
	wantMergeReason(t, x.run(newRec(t), &auditRec{}), MergeDetached)
}

// A detached HEAD is reported only when this clone's work is waiting: a repo
// with no box work of this profile has nothing to say about it.
func TestMergeHomeDetachedNeedsBoxWork(t *testing.T) {
	// No box/main at all.
	x := newMergeFx(t, "main")
	fixtureGit(t, x.of, "checkout", "-q", "--detach")
	if got := x.run(newRec(t), &auditRec{}); got != nil {
		t.Fatalf("no box work: result = %+v, want nil", got)
	}
	// A box/main this clone did not write.
	y := newMergeFx(t, "main")
	to := y.box("main", map[string]string{"b.txt": "b"})
	fixtureGit(t, y.of, "checkout", "-q", "--detach")
	got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: y.of, Audit: (&auditRec{}).fn,
		CloneTips: map[string]string{"main": strings.Repeat("0", 40)}})
	if got != nil {
		t.Fatalf("another clone's box/main: result = %+v, want nil", got)
	}
	// This clone's box/main, already the branch's own tip: nothing waits.
	fixtureGit(t, y.of, "update-ref", "refs/heads/main", to)
	if got := y.run(newRec(t), &auditRec{}); got != nil {
		t.Fatalf("box/main equals main: result = %+v, want nil", got)
	}
	// This clone's box/main is ahead of main: waiting work, detached is news.
	z := newMergeFx(t, "main")
	z.box("main", map[string]string{"b.txt": "b"})
	fixtureGit(t, z.of, "checkout", "-q", "--detach")
	wantMergeReason(t, z.run(newRec(t), &auditRec{}), MergeDetached)
}

// The detached check lists the branches once, whatever the number of tips, and
// never passes a branch name back to git.
func TestMergeHomeDetachedListsBranchesOnce(t *testing.T) {
	x := newMergeFx(t, "main")
	tips := map[string]string{}
	for _, n := range []string{"b1", "b2", "b3", "d\u00e9j\u00e0", "feat/x", "feat/-y"} {
		fixtureGit(t, x.of, "update-ref", "refs/heads/"+n, x.tip())
		tips[n] = x.tip() // box/<n> does not exist: nothing waits
	}
	to := x.box("main", map[string]string{"b.txt": "b"}) // box/main, ahead
	tips["main"] = to
	fixtureGit(t, x.of, "checkout", "-q", "--detach")
	rec := newRec(t)
	got := MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: (&auditRec{}).fn, CloneTips: tips})
	wantMergeReason(t, got, MergeDetached)
	refs, others := 0, 0
	for _, c := range rec.calls {
		switch {
		case slices.Contains(c.args, "for-each-ref"):
			refs++
		case slices.Contains(c.args, "rev-parse"):
			others++
		}
		for _, a := range c.args {
			for n := range tips {
				if a == n || strings.HasSuffix(a, "refs/heads/"+n) || strings.HasSuffix(a, "refs/heads/box/"+n) {
					t.Fatalf("a branch name went back into argv: %q", c.args)
				}
			}
		}
	}
	if refs != 1 || others != 0 {
		t.Fatalf("for-each-ref ran %d times, rev-parse %d times; want 1 and 0 (%d calls)", refs, others, len(rec.calls))
	}

	// Nothing waiting: the same single listing says so.
	y := newMergeFx(t, "main")
	ytips := map[string]string{}
	for _, n := range []string{"b1", "b2", "b3"} {
		fixtureGit(t, y.of, "update-ref", "refs/heads/"+n, y.tip())
		fixtureGit(t, y.of, "update-ref", "refs/heads/box/"+n, y.tip()) // at the host's own tip
		ytips[n] = y.tip()
	}
	ytips["gone"] = y.tip() // a branch the host does not have, with no box/gone either
	fixtureGit(t, y.of, "checkout", "-q", "--detach")
	rec = newRec(t)
	if got := MergeHome(context.Background(), rec, MergeRequest{Of: y.of, Audit: (&auditRec{}).fn, CloneTips: ytips}); got != nil {
		t.Fatalf("nothing waiting: result = %+v, want nil", got)
	}
	if len(rec.calls) != 2 { // symbolic-ref, for-each-ref
		t.Fatalf("calls = %d, want 2: %q", len(rec.calls), rec.calls)
	}
	// No usable tip: no listing at all.
	rec = newRec(t)
	MergeHome(context.Background(), rec, MergeRequest{Of: y.of, Audit: (&auditRec{}).fn, CloneTips: map[string]string{"": "x", "b1": ""}})
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want only the symbolic-ref", len(rec.calls))
	}
}

// listGit answers for-each-ref with canned output, everything else from git.
type listGit struct {
	*recGit
	out string
}

func (l listGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if slices.Contains(args, "for-each-ref") {
		return []byte(l.out), nil
	}
	return l.recGit.Run(ctx, dir, env, args...)
}

// A branch list that does not read exactly as expected is a failure, not "nothing waits".
func TestMergeHomeDetachedMalformedBranchList(t *testing.T) {
	id := strings.Repeat("a", 40)
	for name, out := range map[string]string{
		"cut short":       "refs/heads/main commit " + id,
		"two fields":      "refs/heads/main " + id + "\n",
		"four fields":     "refs/heads/main commit " + id + " x\n",
		"not a branch":    "refs/tags/main commit " + id + "\n",
		"bad id":          "refs/heads/main commit zz\n",
		"empty name":      "refs/heads/ commit " + id + "\n",
		"blank line":      "refs/heads/main commit " + id + "\n\n",
		"garbage":         "hello\n",
		"first line fine": "refs/heads/main commit " + id + "\nrefs/heads/x commit\n",
	} {
		t.Run(name, func(t *testing.T) {
			x := newMergeFx(t, "main")
			fixtureGit(t, x.of, "checkout", "-q", "--detach")
			got := MergeHome(context.Background(), listGit{newRec(t), out}, MergeRequest{Of: x.of, Audit: (&auditRec{}).fn,
				CloneTips: map[string]string{"main": id}})
			wantMergeReason(t, got, MergeFailed)
		})
	}
	t.Run("a well-formed list that is empty", func(t *testing.T) {
		x := newMergeFx(t, "main")
		fixtureGit(t, x.of, "checkout", "-q", "--detach")
		got := MergeHome(context.Background(), listGit{newRec(t), ""}, MergeRequest{Of: x.of, Audit: (&auditRec{}).fn,
			CloneTips: map[string]string{"main": id}})
		if got != nil {
			t.Fatalf("result = %+v, want nil", got)
		}
	})
}

func TestMergeHomeDiverged(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	x.commit("host", map[string]string{"h.txt": "h"})
	before := x.tip()
	a := &auditRec{}
	wantMergeReason(t, x.run(newRec(t), a), MergeDiverged)
	if x.tip() != before {
		t.Fatal("main moved")
	}
	if len(a.calls) != 0 {
		t.Fatalf("audit = %q, want none for a pre-check refusal", a.calls)
	}
}

func TestMergeHomeDirty(t *testing.T) {
	t.Run("modified", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"b.txt": "b"})
		x.write("a.txt", "mine")
		wantMergeReason(t, x.run(newRec(t), &auditRec{}), MergeDirty)
		if x.read("a.txt") != "mine" {
			t.Fatal("the dirty file was touched")
		}
	})
	t.Run("staged", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"b.txt": "b"})
		x.write("s.txt", "s")
		fixtureGit(t, x.of, "add", "s.txt")
		wantMergeReason(t, x.run(newRec(t), &auditRec{}), MergeDirty)
	})
	t.Run("untracked unrelated", func(t *testing.T) {
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"b.txt": "b"})
		x.write("notes.txt", "n")
		wantMerged(t, x.run(newRec(t), &auditRec{}))
		if x.read("notes.txt") != "n" {
			t.Fatal("the untracked file changed")
		}
	})
}

func TestMergeHomeUntrackedInTheWay(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"new.txt": "box"})
	x.write("new.txt", "mine")
	before := x.tip()
	a := &auditRec{}
	got := x.run(newRec(t), a)
	wantMergeReason(t, got, MergeFailed)
	if !strings.HasPrefix(got.Detail, "git merge: exit status") || !strings.Contains(got.Detail, "untracked") {
		t.Fatalf("Detail = %q, want git's message", got.Detail)
	}
	if x.read("new.txt") != "mine" || x.tip() != before {
		t.Fatal("the untracked file or main changed")
	}
	if !slices.Equal(a.actions(), []string{"clone_merge_home", "clone_merge_home_failed"}) {
		t.Fatalf("audit = %q, want clone_merge_home then clone_merge_home_failed", a.calls)
	}
}

// git merge overwrites an ignored file by default; the user's own (a .env, a
// local config) must survive a box commit that force-added the same name.
func TestMergeHomeIgnoredFileInTheWay(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("ignore", map[string]string{".gitignore": "local.cfg\n"})
	before := x.tip()
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side")
	x.write("local.cfg", "box")
	fixtureGit(t, x.of, "add", "-f", "--", "local.cfg")
	fixtureGit(t, x.of, "commit", "-q", "-m", "box adds local.cfg")
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.out("rev-parse", "HEAD"))
	fixtureGit(t, x.of, "checkout", "-q", "main")
	fixtureGit(t, x.of, "branch", "-q", "-D", "side")
	x.write("local.cfg", "mine")
	a := &auditRec{}
	got := x.run(newRec(t), a)
	// The ignore check refuses it before git runs (#370).
	wantMergeReason(t, got, MergeGuarded)
	if x.read("local.cfg") != "mine" || x.tip() != before || len(a.calls) != 0 {
		t.Fatalf("local.cfg = %q, main moved = %v, audit %q", x.read("local.cfg"), x.tip() != before, a.calls)
	}
}

func TestMergeHomeRepoMergeFFFalse(t *testing.T) {
	x := newMergeFx(t, "main")
	to := x.box("main", map[string]string{"b.txt": "b"})
	fixtureGit(t, x.of, "config", "merge.ff", "false")
	wantMerged(t, x.run(newRec(t), &auditRec{}))
	if x.tip() != to {
		t.Fatalf("main = %s, want the box's %s (a fast-forward, no merge commit)", x.tip(), to)
	}
}

func TestMergeHomeRunsNoHooks(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	marker := filepath.Join(t.TempDir(), "ran")
	for _, h := range []string{"post-merge", "post-checkout", "reference-transaction", "pre-merge-commit"} {
		p := filepath.Join(x.of, ".git", "hooks", h)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho "+h+" >> "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wantMerged(t, x.run(newRec(t), &auditRec{}))
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("hooks ran: %s", b)
	}
}

func TestMergeHomeHooksPathDenied(t *testing.T) {
	cases := []struct {
		name, hooks, file string
		guarded           bool
	}{
		{"top .githooks", ".githooks", ".githooks/x", true},
		{"relative tools/hk", "tools/hk", "tools/hk/y", true},
		{"relative ./Tools/HK/ folded", "./Tools/HK/", "tools/hk/y", true},
		{"absolute inside", "ABS", "abs/hk/z", true},
		{"outside the repo", "/elsewhere/hooks", "tools/hk/y", false},
		{"relative outside", "../hooks", "tools/hk/y", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newMergeFx(t, "main")
			x.box("main", map[string]string{c.file: "#!/bin/sh\n"})
			hooks := c.hooks
			if hooks == "ABS" {
				hooks = filepath.Join(x.of, "abs", "hk")
			}
			fixtureGit(t, x.of, "config", "core.hooksPath", hooks)
			got := x.run(newRec(t), &auditRec{})
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.file) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.file)
			}
		})
	}
}

func TestMergeHomeDenyVariants(t *testing.T) {
	cases := []struct {
		path    string
		guarded bool
	}{
		{".claude/settings.json", true},
		{".Claude/settings.json", true},
		{"sub/.claude/x", true}, // added then deleted
		{"CLAUDE.md", true},
		{"CLAUDE.local.md", true},
		{"docs/claude.md", true},
		{"AGENTS.md", true},
		{".mcp.json", true},
		{".envrc", true},
		{".gitattributes", true},
		{".gitmodules", true},
		{"mise.toml", true},
		{"sub/.mise.toml", true},
		{".husky/pre-commit", true},
		{".vscode/tasks.json", true},
		{".devcontainer/devcontainer.json", true},
		{".mise/tasks/x", true},
		{".pre-commit-config.yaml", true},
		{"sub/.pre-commit-hooks.yaml", true},
		{"lefthook.yml", true},
		{"lefthook.yaml", true},
		{".lefthook.yml", true},
		{".lefthook.yaml", true},
		{"lefthook-local.yml", true},
		{"lefthook-local.yaml", true},
		{".lefthook-local.yml", true},
		{".lefthook-local.yaml", true},
		{".overcommit.yml", true},
		{"mise.local.toml", true},
		{"sub/.mise.local.toml", true},
		{"mise/config.toml", true},
		{"Mise/tasks/build", true},
		{".config/mise.toml", true},
		{".config/mise/config.toml", true},
		{".Config/x", true},
		{"mi\u017fe.toml", true}, // a long s folds to s (strings.EqualFold)
		// Names and patterns direnv, nix, devenv, devbox, node and yarn load by themselves.
		{".env", true},
		{"sub/.env", true},
		{"flake.nix", true},
		{"sub/flake.lock", true},
		{"shell.nix", true},
		{"default.nix", true},
		{"devenv.nix", true},
		{"devenv.yaml", true},
		{"devenv.lock", true},
		{"devbox.json", true},
		{".npmrc", true},
		{"sub/.npmrc", true},
		{".yarnrc.yml", true},
		{".yarnrc", true},
		{".env.local", true},
		{"sub/.env.production", true},
		{".envrc.local", true},
		{"sub/.envrc.local", true},
		{".envrc.d", true},
		{"mise.test.toml", true},
		{"sub/.mise.dev.toml", true},
		{"mi\u017fe.ci.toml", true}, // the fold applies to patterns too
		{"proj.code-workspace", true},
		{"sub/My.Code-Workspace", true},
		{".aiderignore", true},
		{".aider.conf.yml", true},
		{"sub/.aider.chat.history.md", true},
		{"docs/env.md", false},
		{".environment", false},
		{"sub/mise.toml.md", false},
		{"package.json", false}, // a runner's manifest is denied only when a config runs it
		{"Makefile", false},
		{"docs/workspace.md", false},
		// Other editors' and agents' project config, at the top.
		{".idea/workspace.xml", true},
		{".zed/settings.json", true},
		{".cursor/rules/a.mdc", true},
		{".cursor/mcp.json", true},
		{".gemini/settings.json", true},
		{".codex/config.toml", true},
		{".windsurf/rules/x.md", true},
		{".continue/config.json", true},
		{".Idea/x", true},
		{"sub/.idea/x", false},
		{"sub/.cursor/x", false},
		{".github/copilot-instructions.md", true},
		{".github/instructions/a.instructions.md", true},
		{".github/prompts/p.prompt.md", true},
		{".GitHub/Prompts/p.md", true},
		{".github/prompts-old/x", false}, // components, not a text prefix
		{".github/copilot-instructions.md.bak", false},
		{".github/workflows/ci.yml", false}, // CI needs the user's push: named in the docs instead
		{".github/CODEOWNERS", false},
		{"sub/.github/prompts/x", false},
		{"sub/mise/x", false}, // top-level dirs only
		{"sub/.config/x", false},
		{"src/vscode/x.go", false},
		{"docs/husky.md", false},
		{"sub/.vscode/tasks.json", false}, // top-level dirs only
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			x := newMergeFx(t, "main")
			before := x.tip()
			commits := []map[string]string{{c.path: "x\n"}}
			if c.path == "sub/.claude/x" {
				commits = append(commits, map[string]string{c.path: ""})
			}
			x.box("main", commits...)
			a := &auditRec{}
			got := x.run(newRec(t), a)
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.path) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.path)
			}
			if x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("main moved or audit written: %q", a.calls)
			}
		})
	}
}

func TestMergeHomeGuardedPathsCapped(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"CLAUDE.md": "1", "AGENTS.md": "2", ".envrc": "3", ".mcp.json": "4", "ok.txt": "5"})
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if len(got.Paths) != 3 {
		t.Fatalf("Paths = %q, want 3 examples", got.Paths)
	}
}

func TestMergeHomeGitlinkDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side")
	fixtureGit(t, x.of, "update-index", "--add", "--cacheinfo", "160000,"+x.out("rev-parse", "HEAD")+",lib")
	fixtureGit(t, x.of, "commit", "-q", "-m", "gitlink")
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.out("rev-parse", "HEAD"))
	fixtureGit(t, x.of, "checkout", "-q", "main")
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if !slices.Contains(got.Paths, "lib") {
		t.Fatalf("Paths = %q, want lib", got.Paths)
	}
}

func TestMergeHomeFlagged(t *testing.T) {
	x := newMergeFx(t, "main")
	before := x.tip()
	x.box("main", map[string]string{"tool": string(elfBytes(128, 1))})
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeFlagged)
	if !slices.Contains(got.Paths, "tool") {
		t.Fatalf("Paths = %q, want tool", got.Paths)
	}
	if x.tip() != before {
		t.Fatal("main moved")
	}
}

func inspected(r *recGit) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if slices.Contains(c.args, "cat-file") {
			return true
		}
	}
	return false
}

// A range found to carry flagged files is not inspected again: the flags come
// from its blobs, which do not change for the same From..To.
func TestMergeHomeFlaggedMemo(t *testing.T) {
	x := newMergeFx(t, "main")
	before := x.tip()
	x.box("main", map[string]string{"tool": string(elfBytes(128, 1))})
	rec := newRec(t)
	first := x.run(rec, &auditRec{})
	wantMergeReason(t, first, MergeFlagged)
	if !inspected(rec) {
		t.Fatal("the first pass did not inspect the range")
	}
	rec = newRec(t)
	got := MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: (&auditRec{}).fn, Prev: first, CloneTips: x.tips()})
	if got != first {
		t.Fatalf("result = %+v, want Prev %+v", got, first)
	}
	if inspected(rec) || mergeCalled(rec) || x.tip() != before {
		t.Fatal("the same flagged range was inspected again, or merged")
	}
	// Held right after the tips: no scan of the tree, the log, the config or the
	// links runs for a result that cannot change.
	for _, c := range rec.calls {
		for _, sub := range []string{"status", "log", "diff-tree", "config", "ls-files", "check-attr", "cat-file"} {
			if slices.Contains(c.args, sub) {
				t.Fatalf("a held flagged result ran git %s: %q", sub, c.args)
			}
		}
	}

	// A refusal that found no flag (the scan could not finish: a deadline or a
	// git failure may pass) is inspected again.
	incomplete := &MergeResult{At: first.At, Branch: "main", From: first.From, To: first.To,
		Reason: MergeFlagged, Detail: "the commits could not be fully inspected: could not inspect: x"}
	rec = newRec(t)
	got = MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: (&auditRec{}).fn, Prev: incomplete, CloneTips: x.tips()})
	wantMergeReason(t, got, MergeFlagged)
	if got == incomplete || !inspected(rec) {
		t.Fatal("an incomplete inspection was not run again")
	}

	// Another range is inspected.
	other := *first
	other.To = strings.Repeat("0", 40)
	rec = newRec(t)
	got = MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: (&auditRec{}).fn, Prev: &other, CloneTips: x.tips()})
	wantMergeReason(t, got, MergeFlagged)
	if got == &other || !inspected(rec) {
		t.Fatal("a different range was not inspected")
	}
}

// plainGit hides the Stream and RunStdin methods of the recorder.
type plainGit struct{ g Git }

func (p plainGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return p.g.Run(ctx, dir, env, args...)
}

// noStreamStdinGit hides only the Stream method: the attribute check works, the
// handback inspection cannot.
type noStreamStdinGit struct{ r *recGit }

func (n noStreamStdinGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return n.r.Run(ctx, dir, env, args...)
}

func (n noStreamStdinGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	return n.r.RunStdin(ctx, dir, env, stdin, args...)
}

func TestMergeHomeInspectionUnavailable(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	got := x.run(noStreamStdinGit{newRec(t)}, &auditRec{})
	wantMergeReason(t, got, MergeFlagged) // no StreamGit: the inspection is a refusal, fail closed
}

// With neither method the attribute check, which runs first, cannot be made.
func TestMergeHomeAttributeCheckUnavailable(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	got := x.run(plainGit{newRec(t)}, &auditRec{})
	wantMergeReason(t, got, MergeFailed)
	if got.Attempted {
		t.Fatalf("result = %+v, a failed pre-check is not an attempted merge", got)
	}
}

func TestMergeHomeFilter(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("attrs", map[string]string{".gitattributes": "*.bin filter=lfs\n", "a.bin": "one"})
	before := x.tip()
	x.box("main", map[string]string{"a.bin": "two"})
	rec := newRec(t)
	got := x.run(rec, &auditRec{})
	wantMergeReason(t, got, MergeFilter)
	if inspected(rec) {
		t.Fatal("the full handback scan ran before the cheap attribute check")
	}
	if !slices.Contains(got.Paths, "a.bin") || x.tip() != before {
		t.Fatalf("result = %+v, main moved = %v", got, x.tip() != before)
	}
}

// movingGit commits on main right after the guard's check-attr call.
type movingGit struct {
	*recGit
	x     *mergeFx
	moved bool
}

func (m *movingGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	out, err := m.recGit.RunStdin(ctx, dir, env, stdin, args...)
	if !m.moved && slices.Contains(args, "check-attr") {
		m.moved = true
		m.x.commit("host moved", map[string]string{"h.txt": "h"})
	}
	return out, err
}

func TestMergeHomeMoved(t *testing.T) {
	x := newMergeFx(t, "main")
	to := x.box("main", map[string]string{"b.txt": "b"})
	m := &movingGit{recGit: newRec(t), x: x}
	a := &auditRec{}
	wantMergeReason(t, x.run(m, a), MergeMoved)
	if !m.moved {
		t.Fatal("check-attr was not called")
	}
	if x.tip() == to || len(a.calls) != 0 {
		t.Fatalf("main = %s (box %s), audit %q", x.tip(), to, a.calls)
	}
}

// dirtyingGit edits a tracked file right after the guard's check-attr call.
type dirtyingGit struct {
	*recGit
	x       *mergeFx
	dirtied bool
}

func (d *dirtyingGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	out, err := d.recGit.RunStdin(ctx, dir, env, stdin, args...)
	if !d.dirtied && slices.Contains(args, "check-attr") {
		d.dirtied = true
		d.x.write("a.txt", "edited while checked")
	}
	return out, err
}

// The tree is clean when checked and again right before the merge: an edit
// made while the guard ran is never merged over.
func TestMergeHomeDirtiedWhileChecked(t *testing.T) {
	x := newMergeFx(t, "main")
	before := x.tip()
	x.box("main", map[string]string{"b.txt": "b"})
	d := &dirtyingGit{recGit: newRec(t), x: x}
	a := &auditRec{}
	wantMergeReason(t, x.run(d, a), MergeDirty)
	if !d.dirtied {
		t.Fatal("check-attr was not called")
	}
	if x.tip() != before || x.read("a.txt") != "edited while checked" || len(a.calls) != 0 || mergeCalled(d.recGit) {
		t.Fatalf("main moved, the edit was lost or audit written: %q", a.calls)
	}
}

func TestMergeHomeAuditFailsClosed(t *testing.T) {
	for _, nilAudit := range []bool{false, true} {
		x := newMergeFx(t, "main")
		before := x.tip()
		x.box("main", map[string]string{"b.txt": "b"})
		rec := newRec(t)
		req := MergeRequest{Of: x.of, Audit: (&auditRec{err: errors.New("disk full")}).fn, CloneTips: x.tips()}
		if nilAudit {
			req.Audit = nil
		}
		got := MergeHome(context.Background(), rec, req)
		wantMergeReason(t, got, MergeFailed)
		if got.Detail != "audit unavailable" {
			t.Fatalf("Detail = %q, want audit unavailable", got.Detail)
		}
		if x.tip() != before || mergeCalled(rec) {
			t.Fatal("merged without an audit row")
		}
	}
}

func mergeCalled(r *recGit) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if slices.Contains(c.args, "merge") {
			return true
		}
	}
	return false
}

// gitCalled reports whether any recorded call ran the git subcommand sub.
func gitCalled(r *recGit, sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if slices.Contains(c.args, sub) {
			return true
		}
	}
	return false
}

// failMergeGit fails the merge call, as git refusing it would.
type failMergeGit struct{ *recGit }

func (f failMergeGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if slices.Contains(args, "merge") {
		f.recGit.mu.Lock()
		f.recGit.calls = append(f.recGit.calls, call{dir, slices.Clone(env), slices.Clone(args)})
		f.recGit.mu.Unlock()
		return nil, errors.New("git -c: exit status 128: untracked file in the way")
	}
	return f.recGit.Run(ctx, dir, env, args...)
}

// Only a merge git refused is Attempted; the audit failing is not.
func TestMergeHomeAttemptedOnlyForARefusedMerge(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	got := x.run(failMergeGit{newRec(t)}, &auditRec{})
	wantMergeReason(t, got, MergeFailed)
	if !got.Attempted || !strings.HasPrefix(got.Detail, "git merge: ") {
		t.Fatalf("result = %+v, want attempted with a git merge detail", got)
	}

	y := newMergeFx(t, "main")
	y.box("main", map[string]string{"b.txt": "b"})
	got = y.run(newRec(t), &auditRec{err: errors.New("disk full")})
	wantMergeReason(t, got, MergeFailed)
	if got.Attempted {
		t.Fatalf("result = %+v, audit unavailable is not an attempted merge", got)
	}
}

// A merge git refused for this range is not tried again inside the window, and
// nothing is scanned or inspected either: nothing merges in the window, so the
// checks would only repeat.
func TestMergeHomeRetryWindow(t *testing.T) {
	x := newMergeFx(t, "main")
	from := x.tip()
	to := x.box("main", map[string]string{"b.txt": "b"})
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	prev := &MergeResult{At: now.Add(-10 * time.Minute), Branch: "main", From: from, To: to, Reason: MergeFailed, Attempted: true, Detail: "x"}
	rec := newRec(t)
	a := &auditRec{}
	got := MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: a.fn, Prev: prev, Now: func() time.Time { return now }, CloneTips: x.tips()})
	if got != prev {
		t.Fatalf("result = %+v, want Prev unchanged", got)
	}
	if mergeCalled(rec) || len(a.calls) != 0 {
		t.Fatalf("retried inside the window: audit %q", a.calls)
	}
	for _, sub := range []string{"log", "status", "diff-tree", "check-attr", "cat-file"} {
		if gitCalled(rec, sub) {
			t.Fatalf("git %s ran inside the retry window", sub)
		}
	}

	prev.At = now.Add(-31 * time.Minute)
	rec = newRec(t)
	got = MergeHome(context.Background(), rec, MergeRequest{Of: x.of, Audit: a.fn, Prev: prev, Now: func() time.Time { return now }, CloneTips: x.tips()})
	wantMerged(t, got)
	if !mergeCalled(rec) || !got.At.Equal(now) {
		t.Fatalf("not attempted after the window, or At = %v", got.At)
	}
}

// An earlier failure that never reached git merge (a pre-check, the audit) does
// not hold the window: the cause may be gone and the merge is checked again.
func TestMergeHomeRetryWindowIgnoresPreCheckFailure(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, detail := range []string{"cannot check git attributes: x", "audit unavailable"} {
		x := newMergeFx(t, "main")
		from := x.tip()
		to := x.box("main", map[string]string{"b.txt": "b"})
		prev := &MergeResult{At: now.Add(-time.Minute), Branch: "main", From: from, To: to, Reason: MergeFailed, Detail: detail}
		got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Audit: (&auditRec{}).fn, Prev: prev, Now: func() time.Time { return now }, CloneTips: x.tips()})
		wantMerged(t, got)
	}
}

func TestMergeHomeHardened(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	rec := newRec(t)
	wantMerged(t, x.run(rec, &auditRec{}))
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, c := range rec.calls {
		if c.dir != x.of || !slices.Contains(c.args, "core.hooksPath=/dev/null") || !slices.Contains(c.env, "GIT_CONFIG_GLOBAL=/dev/null") {
			t.Fatalf("call %q in %q env %q is not hardened", c.args, c.dir, c.env)
		}
		if slices.Contains(c.args, "merge") && !slices.Contains(c.args, "submodule.recurse=false") {
			t.Fatalf("merge call %q lacks submodule.recurse=false", c.args)
		}
		if slices.Contains(c.args, "merge") && !slices.Contains(c.env, "GIT_NO_REPLACE_OBJECTS=1") {
			t.Fatalf("merge call env %q lacks GIT_NO_REPLACE_OBJECTS=1", c.env)
		}
	}
}

func TestParseMergeLog(t *testing.T) {
	sha := strings.Repeat("a", 40)
	meta := ":100644 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " M"
	good := sha + "\x00\n" + meta + "\x00p/q\x00" + sha + "\x00"
	n, ents, err := parseMergeLog([]byte(good))
	if err != nil || n != 2 || len(ents) != 1 || ents[0].path != "p/q" {
		t.Fatalf("good: n=%d ents=%+v err=%v", n, ents, err)
	}
	for name, bad := range map[string]string{
		"meta first":      meta + "\x00p\x00",
		"no path":         sha + "\x00" + meta + "\x00",
		"short meta":      sha + "\x00:100644 M\x00p\x00",
		"bad mode":        sha + "\x00:10064x 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " M\x00p\x00",
		"combined":        sha + "\x00::100644 100644 100644 a b c MM\x00p\x00",
		"rename":          sha + "\x00:100644 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " R100\x00p\x00q\x00",
		"garbage":         "hello\x00",
		"no trailing nul": sha,
		"empty path":      sha + "\x00" + meta + "\x00\x00",
		"no commits":      "",
	} {
		if _, _, err := parseMergeLog([]byte(bad)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A box merge commit whose first parent is an older host commit restores that
// commit's version of a guarded file: the first-parent log of From..To does
// not list it, the From..To tree diff does.
func TestMergeHomeMergeRestoresOldGuardedFile(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("O", map[string]string{".claude/settings.json": `{"hooks":"evil-old"}`})
	o := x.tip()
	x.commit("From", map[string]string{".claude/settings.json": "{}"})
	from := x.tip()
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side", o)
	x.write("x", "x")
	fixtureGit(t, x.of, "add", "x")
	fixtureGit(t, x.of, "commit", "-q", "-m", "O plus x")
	tree := x.out("rev-parse", "HEAD^{tree}")
	m := x.out("commit-tree", tree, "-p", o, "-p", from, "-m", "m")
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", m)
	fixtureGit(t, x.of, "checkout", "-q", "main")
	fixtureGit(t, x.of, "branch", "-q", "-D", "side")

	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if !slices.Contains(got.Paths, ".claude/settings.json") {
		t.Fatalf("Paths = %q, want .claude/settings.json", got.Paths)
	}
	if got.Files != 2 { // x, and the settings file the tree diff adds
		t.Fatalf("Files = %d, want 2 (log and tree diff union)", got.Files)
	}
	if x.tip() != from || x.read(".claude/settings.json") != "{}" {
		t.Fatal("main or the settings file changed")
	}
}

// The user's own git reads a global or system core.hooksPath that CleanEnv
// hides; a relative one names a directory inside every repo.
func TestMergeHomeGlobalHooksPathDenied(t *testing.T) {
	type setup func(t *testing.T, home string)
	put := func(t *testing.T, p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hk := "[core]\n\thooksPath = scripts/hooks\n"
	cases := []struct {
		name    string
		setup   setup
		guarded bool
	}{
		{"home .gitconfig", func(t *testing.T, home string) { put(t, filepath.Join(home, ".gitconfig"), hk) }, true},
		{"home .config/git/config", func(t *testing.T, home string) { put(t, filepath.Join(home, ".config", "git", "config"), hk) }, true},
		{"XDG_CONFIG_HOME", func(t *testing.T, home string) {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			put(t, filepath.Join(xdg, "git", "config"), hk)
		}, true},
		{"system", func(t *testing.T, home string) {
			p := filepath.Join(t.TempDir(), "gitconfig")
			put(t, p, hk)
			old := mergeSystemGitConfig
			mergeSystemGitConfig = p
			t.Cleanup(func() { mergeSystemGitConfig = old })
		}, true},
		{"included from ~", func(t *testing.T, home string) {
			put(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = ~/.gitconfig.local\n")
			put(t, filepath.Join(home, ".gitconfig.local"), hk)
		}, true},
		{"global outside the repo", func(t *testing.T, home string) {
			put(t, filepath.Join(home, ".gitconfig"), "[core]\n\thooksPath = /elsewhere/hooks\n")
		}, false},
		{"no global config", func(t *testing.T, home string) {}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", "")
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			home := t.TempDir()
			c.setup(t, home)
			x := newMergeFx(t, "main")
			x.box("main", map[string]string{"scripts/hooks/pre-commit": "#!/bin/sh\n"})
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: home, Audit: (&auditRec{}).fn, CloneTips: x.tips()})
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, "scripts/hooks/pre-commit") {
				t.Fatalf("Paths = %q", got.Paths)
			}
		})
	}
}

// A symlink at a parent of a nested hooks dir re-points it: tools -> evil makes
// tools/hk evil/hk.
func TestMergeHomeHooksParentSymlinkDenied(t *testing.T) {
	x := newMergeFx(t, "main")
	fixtureGit(t, x.of, "config", "core.hooksPath", "tools/hk")
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side")
	x.write("evil/hk/pre-commit", "#!/bin/sh\n")
	if err := os.Symlink("evil", filepath.Join(x.of, "tools")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "add", "evil", "tools")
	fixtureGit(t, x.of, "commit", "-q", "-m", "link")
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.out("rev-parse", "HEAD"))
	fixtureGit(t, x.of, "checkout", "-q", "main")
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if !slices.Contains(got.Paths, "tools") {
		t.Fatalf("Paths = %q, want tools", got.Paths)
	}
}

func TestMergeDeniedHooksParent(t *testing.T) {
	for p, want := range map[string]bool{"tools": true, "Tools": true, "tools/hk": true, "tools/hk/x": true,
		"tool": false, "tools/h": false, "toolsx": false} {
		if got := mergeDenied(p, []string{"tools/hk"}); got != want {
			t.Errorf("mergeDenied(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestParseDiffTree(t *testing.T) {
	meta := ":160000 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " T"
	ents, err := parseDiffTree([]byte(meta + "\x00lib\x00"))
	if err != nil || len(ents) != 1 || ents[0].path != "lib" || !ents[0].gitlink {
		t.Fatalf("ents=%+v err=%v", ents, err)
	}
	if ents, err := parseDiffTree(nil); err != nil || ents != nil {
		t.Fatalf("empty: ents=%+v err=%v", ents, err)
	}
	for name, bad := range map[string]string{
		"commit id": strings.Repeat("a", 40) + "\x00" + meta + "\x00p\x00",
		"no path":   meta + "\x00",
		"cut short": meta + "\x00p",
	} {
		if _, err := parseDiffTree([]byte(bad)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A repo top with more entries than the link check lists ends the merge failed.
func TestMergeHomeHugeTopFailsClosed(t *testing.T) {
	noSystemConfig(t)
	x := newMergeFx(t, "main")
	noManagedSettings(t)
	before := x.tip()
	x.box("main", map[string]string{"b.txt": "b"})
	for i := 0; i < mergeLinkTop+10; i++ {
		x.write(fmt.Sprintf("u%05d", i), "")
	}
	a := &auditRec{}
	wantMergeReason(t, x.run(newRec(t), a), MergeFailed)
	if x.tip() != before || len(a.calls) != 0 {
		t.Fatal("merged or audited")
	}
}

// boxForce is box with one commit whose files are added with -f and literal
// pathspecs: an ignored path or a name shaped like pathspec magic is committed
// as written.
func (x *mergeFx) boxForce(name string, files map[string]string) string {
	x.t.Helper()
	fixtureGit(x.t, x.of, "checkout", "-q", "-b", "side")
	for rel, c := range files {
		x.write(rel, c)
		fixtureGit(x.t, x.of, "--literal-pathspecs", "add", "-f", "--", rel)
	}
	fixtureGit(x.t, x.of, "commit", "-q", "-m", "box forces")
	sha := x.out("rev-parse", "HEAD")
	fixtureGit(x.t, x.of, "update-ref", "refs/heads/box/"+name, sha)
	fixtureGit(x.t, x.of, "checkout", "-q", x.br)
	fixtureGit(x.t, x.of, "branch", "-q", "-D", "side")
	return sha
}

// A path the commits add or change that the host's ignore rules match (the
// repo's .gitignore files, info/exclude, the user's global ignores) is where
// host tools load from (a .venv, node_modules), so it refuses the merge, even
// when the host tracks it (#370).
func TestMergeHomeIgnoredPathGuarded(t *testing.T) {
	put := func(t *testing.T, p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hostForce := func(x *mergeFx, files map[string]string) {
		x.t.Helper()
		for rel, c := range files {
			x.write(rel, c)
			fixtureGit(x.t, x.of, "add", "-f", "--", rel)
		}
		fixtureGit(x.t, x.of, "commit", "-q", "-m", "host forces")
	}
	cases := []struct {
		name   string
		setup  func(x *mergeFx, home string)
		change string
		want   string // MergeGuarded, or "" for merged
	}{
		{".gitignore .venv/, a .pth added", func(x *mergeFx, home string) {
			x.commit("ignore", map[string]string{".gitignore": ".venv/\n"})
		}, ".venv/lib/python3.12/site-packages/x.pth", MergeGuarded},
		{"info/exclude rule", func(x *mergeFx, home string) {
			put(x.t, filepath.Join(x.of, ".git", "info", "exclude"), "build-out/\n")
		}, "build-out/x.js", MergeGuarded},
		{"global default ignore file", func(x *mergeFx, home string) {
			put(x.t, filepath.Join(home, ".config", "git", "ignore"), ".direnv/\n")
		}, ".direnv/x", MergeGuarded},
		{"XDG default ignore file", func(x *mergeFx, home string) {
			xdg := x.t.TempDir()
			x.t.Setenv("XDG_CONFIG_HOME", xdg)
			put(x.t, filepath.Join(xdg, "git", "ignore"), ".xd/\n")
		}, ".xd/x", MergeGuarded},
		{"global core.excludesFile", func(x *mergeFx, home string) {
			put(x.t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/.gi\n")
			put(x.t, filepath.Join(home, ".gi"), "secret-cache/\n")
		}, "secret-cache/a", MergeGuarded},
		{"system core.excludesFile", func(x *mergeFx, home string) {
			sys := filepath.Join(x.t.TempDir(), "gitconfig")
			ex := filepath.Join(x.t.TempDir(), "sysignore")
			put(x.t, sys, "[core]\n\texcludesFile = "+ex+"\n")
			put(x.t, ex, "sys-cache/\n")
			mergeSystemGitConfig = sys // restored by the case's noSystemConfig
		}, "sys-cache/a", MergeGuarded},
		{"repo core.excludesFile is not overridden", func(x *mergeFx, home string) {
			ex := filepath.Join(x.t.TempDir(), "repoignore")
			put(x.t, ex, "rp/\n")
			fixtureGit(x.t, x.of, "config", "core.excludesFile", ex)
			put(x.t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/.gi\n")
			put(x.t, filepath.Join(home, ".gi"), "hm/\n")
		}, "rp/a", MergeGuarded},
		{"force-added tracked file under an ignored folder", func(x *mergeFx, home string) {
			x.commit("ignore", map[string]string{".gitignore": "dist/\n"})
			hostForce(x, map[string]string{"dist/app.js": "host"})
		}, "dist/app.js", MergeGuarded},
		{"tracked .venv, not ignored, merges", func(x *mergeFx, home string) {
			x.commit("venv", map[string]string{".venv/lib/site.py": "host"})
		}, ".venv/lib/site.py", ""},
		{"tracked .venv, ignored, guarded", func(x *mergeFx, home string) {
			x.commit("ignore", map[string]string{".gitignore": ".venv/\n"})
			hostForce(x, map[string]string{".venv/lib/site.py": "host"})
		}, ".venv/lib/site.py", MergeGuarded},
		{"a pathspec-looking name is a name", func(x *mergeFx, home string) {}, ":(top)x.txt", ""},
		{"a glob-magic name is a name", func(x *mergeFx, home string) {}, ":(glob)y.txt", ""},
		{"an ignored colon name is checked as written", func(x *mergeFx, home string) {
			x.commit("ignore", map[string]string{".gitignore": ":*\n"})
		}, ":(top)x.txt", MergeGuarded},
		{"nothing ignored merges", func(x *mergeFx, home string) {}, "src/a.go", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noSystemConfig(t)
			home := t.TempDir()
			x := newMergeFx(t, "main")
			c.setup(x, home)
			before := x.tip()
			to := x.boxForce("main", map[string]string{c.change: "box"})
			a := &auditRec{}
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: home, Audit: a.fn, CloneTips: x.tips()})
			if c.want == "" {
				wantMerged(t, got)
				if x.tip() != to {
					t.Fatalf("main = %s, want %s", x.tip(), to)
				}
				return
			}
			wantMergeReason(t, got, c.want)
			if !slices.Contains(got.Paths, c.change) || len(a.calls) != 0 || x.tip() != before {
				t.Fatalf("result = %+v, audit %q, main moved = %v", got, a.calls, x.tip() != before)
			}
		})
	}
}

// failIgnoreGit fails the check-ignore call.
type failIgnoreGit struct{ *recGit }

func (f failIgnoreGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	if slices.Contains(args, "check-ignore") {
		return nil, errors.New("git -c: exit status 128: fatal: boom")
	}
	return f.recGit.RunStdin(ctx, dir, env, stdin, args...)
}

func TestMergeHomeIgnoreCheckFailsClosed(t *testing.T) {
	x := newMergeFx(t, "main")
	before := x.tip()
	x.box("main", map[string]string{"b.txt": "b"})
	a := &auditRec{}
	got := x.run(failIgnoreGit{newRec(t)}, a)
	wantMergeReason(t, got, MergeFailed)
	if !strings.HasPrefix(got.Detail, "cannot check ignore rules") || len(a.calls) != 0 || x.tip() != before {
		t.Fatalf("result = %+v, audit %q", got, a.calls)
	}
}

// stdinRecGit keeps what the check-ignore call read on its stdin.
type stdinRecGit struct {
	*recGit
	in *bytes.Buffer
}

func (s stdinRecGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	if slices.Contains(args, "check-ignore") && stdin != nil {
		stdin = io.TeeReader(stdin, s.in)
	}
	return s.recGit.RunStdin(ctx, dir, env, stdin, args...)
}

// The range paths reach check-ignore only on its stdin, each under an explicit
// ":(top)" so the rest is read as a name, never as pathspec magic; the call is
// hardened like every other.
func TestMergeHomeIgnoreCheckStdinOnly(t *testing.T) {
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"zz-unique-name.txt": "b"})
	rec := stdinRecGit{newRec(t), &bytes.Buffer{}}
	wantMerged(t, x.run(rec, &auditRec{}))
	if got := rec.in.String(); got != ":(top)zz-unique-name.txt\x00" {
		t.Fatalf("check-ignore stdin = %q", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	found := false
	for _, c := range rec.calls {
		for _, a := range c.args {
			if strings.Contains(a, "zz-unique-name") {
				t.Fatalf("call %q names a range path in its arguments", c.args)
			}
		}
		if !slices.Contains(c.args, "check-ignore") {
			continue
		}
		found = true
		if !slices.Contains(c.args, "core.hooksPath=/dev/null") || !slices.Contains(c.args, "--no-index") ||
			!slices.Contains(c.args, "--stdin") || !slices.Contains(c.args, "-z") {
			t.Fatalf("check-ignore args %q", c.args)
		}
		if !slices.Contains(c.env, "GIT_CONFIG_GLOBAL=/dev/null") || slices.Contains(c.env, "GIT_LITERAL_PATHSPECS=1") {
			// check-ignore refuses literal pathspecs on every path (git 2.43:
			// "pathspec magic not supported by this command: 'literal'").
			t.Fatalf("check-ignore env %q", c.env)
		}
	}
	if !found {
		t.Fatal("no check-ignore call")
	}
}
