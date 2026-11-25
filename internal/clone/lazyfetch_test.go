package clone

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

// lazyEnv is CleanEnv without GIT_NO_LAZY_FETCH: the environment of a git that
// predates the variable, or of a caller that forgot it.
func lazyEnv() []string {
	return slices.DeleteFunc(CleanEnv(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_NO_LAZY_FETCH=") })
}

// promisorFixture builds clone_of as a box that once wrote its .git could leave
// it (#371 S1): the config declares a promisor remote whose upload-pack is a
// command that writes marker, the blob of HEAD's file is missing, and
// refs/heads/main names a commit that is not there. A host git that
// lazy-fetches a missing object runs that command on the host. missing is a
// commit SHA the repo does not have. The test is skipped when this git cannot
// be made to run the command: the fixture is the proof that the class exists.
func promisorFixture(t *testing.T) (of, marker, missing string) {
	t.Helper()
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	base := t.TempDir()
	of = filepath.Join(base, "of")
	marker = filepath.Join(base, "MARKER")
	missing = strings.Repeat("2", 40)
	if strings.Contains(marker, "'") {
		t.Skip("the temp dir has a quote in it")
	}
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, of, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(of, "f.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, of, "add", "f.txt")
	fixtureGit(t, of, "commit", "-q", "-m", "f")
	blob := strings.TrimSpace(string(mustRun(t, g, of, "rev-parse", "HEAD:f.txt")))
	if err := os.Remove(filepath.Join(of, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Skipf("the blob is not a loose object (%v): cannot build the fixture", err)
	}
	if err := os.WriteFile(filepath.Join(of, ".git", "refs", "heads", "main"), []byte(missing+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", filepath.Join(base, "nowhere")},
		{"remote.origin.promisor", "true"},
		{"remote.origin.uploadpack", "touch '" + marker + "'; false"},
	} {
		fixtureGit(t, of, "config", kv[0], kv[1])
	}
	// The class is real on this git: an unhardened read of a missing object
	// runs the box's command.
	_, _ = g.Run(context.Background(), of, lazyEnv(), "cat-file", "-e", missing)
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not lazy-fetch through the fixture's promisor remote: cannot build the fixture")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	return of, marker, missing
}

// listOnlyDocker answers the return's branch listing with a fixed text and
// refuses every other container (the bundle): the host calls before and between
// them are what is under test.
type listOnlyDocker struct{ listing string }

func (d listOnlyDocker) RunStream(_ context.Context, args []string, out io.Writer) error {
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, LabelCloneList+"=") }) {
		_, err := io.WriteString(out, d.listing)
		return err
	}
	return errors.New("no container in this test")
}

func (listOnlyDocker) Remove(context.Context, string) error { return nil }

// No host git call in clone_of lazy-fetches, so a promisor remote a box
// declared there never runs its command on the host: not the copy estimate,
// not the refresh's checks (BaseBranch, HeadInHost, StaleSHAs), not a return.
// Each of the two layers (GIT_NO_LAZY_FETCH in CleanEnv, protocol.allow=never in
// the hardened flags) holds on its own, for a git that knows only one of them.
func TestHostGitNeverLazyFetches(t *testing.T) {
	of, marker, missing := promisorFixture(t)
	g, _ := NewExecGit()
	ctx := context.Background()
	noMarker := func(t *testing.T) {
		t.Helper()
		if _, err := os.Stat(marker); err == nil {
			os.Remove(marker)
			t.Fatal("host git lazy-fetched: the promisor remote's upload-pack command ran on the host")
		}
	}

	t.Run("EstimateCopy", func(t *testing.T) {
		e, err := EstimateCopy(ctx, g, of, t.TempDir())
		noMarker(t)
		if err != nil {
			t.Fatal(err)
		}
		if !e.Partial || e.TreeBytes != 0 {
			t.Errorf("estimate of a promisor repo = %+v, want partial with no tree size", e)
		}
	})
	t.Run("treeBytes", func(t *testing.T) {
		_, partial, err := treeBytes(ctx, g, of)
		noMarker(t)
		if err != nil || !partial {
			t.Errorf("treeBytes over a missing blob: partial=%v err=%v, want partial", partial, err)
		}
	})
	t.Run("BaseBranch", func(t *testing.T) {
		b, _, err := BaseBranch(ctx, g, of)
		noMarker(t)
		if err != nil || b != "master" {
			t.Errorf("BaseBranch = %q, %v; want master (main names a missing commit)", b, err)
		}
	})
	t.Run("HeadInHost", func(t *testing.T) {
		in := HeadInHost(ctx, g, of, missing)
		noMarker(t)
		if in == nil || *in {
			t.Errorf("HeadInHost of a missing commit = %v, want false", in)
		}
	})
	t.Run("StaleSHAs", func(t *testing.T) {
		stale, err := StaleSHAs(ctx, g, of, "master", []string{missing})
		noMarker(t)
		if err != nil || !slices.Equal(stale, []string{missing}) {
			t.Errorf("StaleSHAs = %v, %v; want the missing SHA", stale, err)
		}
	})
	t.Run("Return", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "home")
		dir := filepath.Join(t.TempDir(), "copy")
		for _, d := range []string{home, dir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		res := Return(ctx, g, listOnlyDocker{listing: missing + " refs/heads/feat\n#culibox-status\n"}, ReturnRequest{
			Clone: config.Clone{Of: of, Dir: dir, Profile: "p"}, Mode: ReturnAuto,
			Image: "img", Instance: "T", TempDir: t.TempDir(), Home: home,
		})
		noMarker(t)
		if res.Base != "master" || len(res.NotHome) != 1 {
			t.Errorf("the return did not reach its host checks: %+v", res)
		}
	})
	t.Run("the hardened flags alone", func(t *testing.T) {
		_, _ = g.Run(ctx, of, lazyEnv(), hardened("cat-file", "-e", missing)...)
		noMarker(t)
	})
	t.Run("the environment alone", func(t *testing.T) {
		_, _ = g.Run(ctx, of, CleanEnv(), "cat-file", "-e", missing)
		noMarker(t)
	})
}

// The one host call that must read a file through git's transport, the bundle
// fetch into clone_of, gets the file transport back and nothing more.
func TestHardenedFlagsAllowNoTransport(t *testing.T) {
	h := strings.Join(hardenFlags, " ")
	if !strings.Contains(h, "-c protocol.allow=never") {
		t.Errorf("hardenFlags %q lack protocol.allow=never", h)
	}
	if !slices.Contains(CleanEnv(), "GIT_NO_LAZY_FETCH=1") {
		t.Errorf("CleanEnv %q lacks GIT_NO_LAZY_FETCH=1", CleanEnv())
	}
}
