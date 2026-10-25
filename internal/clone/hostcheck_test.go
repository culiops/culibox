package clone

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func ofRev(t *testing.T, of, rev string) string {
	t.Helper()
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	out, err := g.Run(context.Background(), of, CleanEnv(), "rev-parse", rev)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// commitIn adds one commit to the repo at dir.
func commitIn(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "add", name)
	fixtureGit(t, dir, "commit", "-q", "-m", name)
}

func TestBaseBranchMainElseMaster(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, true)
	// git init's default branch depends on init.defaultBranch; pin it.
	fixtureGit(t, of, "branch", "-M", "master")
	br, sha, err := BaseBranch(context.Background(), g, of)
	if err != nil || br != "master" || sha != ofRev(t, of, "master") {
		t.Fatalf("got (%q,%q,%v); want master", br, sha, err)
	}
	fixtureGit(t, of, "branch", "-M", "main")
	br, sha, err = BaseBranch(context.Background(), g, of)
	if err != nil || br != "main" || sha != ofRev(t, of, "main") {
		t.Fatalf("got (%q,%q,%v); want main", br, sha, err)
	}
	fixtureGit(t, of, "branch", "-M", "trunk")
	br, sha, err = BaseBranch(context.Background(), g, of)
	if err != nil || br != "" || sha != "" {
		t.Fatalf("got (%q,%q,%v); want none, nil", br, sha, err)
	}
}

func TestStaleSHAsAfterRewrite(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	root := ofRev(t, of, "main~1")
	oldTip := ofRev(t, of, "main")
	ctx := context.Background()

	// A normal advance: nothing recorded is stale.
	commitIn(t, of, "c")
	got, err := StaleSHAs(ctx, g, of, "main", []string{root, oldTip})
	if err != nil || len(got) != 0 {
		t.Fatalf("normal advance: stale=%v err=%v; want none", got, err)
	}

	// A rewrite: main goes back to root and forks. oldTip (and the commit "c")
	// are no longer reachable from main.
	cTip := ofRev(t, of, "main")
	fixtureGit(t, of, "reset", "-q", "--hard", root)
	commitIn(t, of, "d")
	got, err = StaleSHAs(ctx, g, of, "main", []string{root, oldTip})
	if err != nil || !slices.Equal(got, []string{oldTip}) {
		t.Fatalf("rewrite: stale=%v err=%v; want [oldTip]", got, err)
	}
	// Record order is kept, duplicates collapse, and a missing SHA is stale.
	missing := strings.Repeat("a", 40)
	got, err = StaleSHAs(ctx, g, of, "main", []string{cTip, root, missing, cTip, oldTip})
	if err != nil || !slices.Equal(got, []string{cTip, missing, oldTip}) {
		t.Fatalf("stale=%v err=%v; want [cTip missing oldTip]", got, err)
	}
	// Only missing SHAs: no rev-list call is needed and the answer still holds.
	got, err = StaleSHAs(ctx, g, of, "main", []string{missing})
	if err != nil || !slices.Equal(got, []string{missing}) {
		t.Fatalf("only missing: stale=%v err=%v", got, err)
	}
	if got, err := StaleSHAs(ctx, g, of, "main", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty record: stale=%v err=%v", got, err)
	}
	if _, err := StaleSHAs(ctx, g, of, "-bad", []string{root}); err == nil {
		t.Error("an invalid base name was accepted")
	}
	if _, err := StaleSHAs(ctx, g, of, "main", []string{"zz"}); err == nil {
		t.Error("a malformed SHA was fed to git")
	}
}

func TestStaleSHAsCallsAreBounded(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	shas := []string{ofRev(t, of, "main~1"), ofRev(t, of, "main")}
	g.reset()
	if _, err := StaleSHAs(context.Background(), g, of, "main", shas); err != nil {
		t.Fatal(err)
	}
	if n := len(g.calls); n != 2 {
		t.Errorf("%d git calls for %d SHAs, want 2 (cat-file, rev-list)", n, len(shas))
	}
}

func TestHeadInHost(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	ctx := context.Background()
	want := func(name string, got *bool, w bool) {
		t.Helper()
		if got == nil || *got != w {
			t.Errorf("%s: got %v, want %v", name, got, w)
		}
	}
	want("main tip", HeadInHost(ctx, g, of, ofRev(t, of, "main")), true)

	// A commit only on a box/ branch still counts as in the host.
	fixtureGit(t, of, "checkout", "-q", "-b", "box/feat")
	commitIn(t, of, "f")
	featOnly := ofRev(t, of, "HEAD")
	fixtureGit(t, of, "checkout", "-q", "main")
	want("box/feat-only", HeadInHost(ctx, g, of, featOnly), true)

	// An object that exists but is on no branch: false.
	fixtureGit(t, of, "branch", "-D", "box/feat")
	want("orphaned", HeadInHost(ctx, g, of, featOnly), false)
	want("unknown", HeadInHost(ctx, g, of, strings.Repeat("a", 40)), false)

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := HeadInHost(cctx, g, of, ofRev(t, of, "main")); got != nil {
		t.Errorf("cancelled ctx: got %v, want nil (unknown)", *got)
	}
	if got := HeadInHost(ctx, g, of, "not-a-sha"); got != nil {
		t.Errorf("malformed sha: got %v, want nil", *got)
	}
}

func TestHostChecksAreHardenedInOf(t *testing.T) {
	g := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	ctx := context.Background()
	g.reset()
	_, sha, err := BaseBranch(ctx, g, of)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StaleSHAs(ctx, g, of, "main", []string{sha}); err != nil {
		t.Fatal(err)
	}
	HeadInHost(ctx, g, of, sha)
	if len(g.calls) < 4 {
		t.Fatalf("only %d calls recorded", len(g.calls))
	}
	for _, c := range g.calls {
		if c.dir != of {
			t.Errorf("git %v ran in %q, want %q", c.args, c.dir, of)
		}
		if !slices.Equal(c.env, CleanEnv()) {
			t.Errorf("git %v env = %v, want CleanEnv()", c.args, c.env)
		}
		if !slices.Equal(c.args[:min(len(c.args), len(hardenedPrefix))], hardenedPrefix) {
			t.Errorf("git %v does not start with %v", c.args, hardenedPrefix)
		}
	}
}
