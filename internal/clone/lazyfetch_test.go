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
// commit SHA the repo does not have. With viaInclude the remote is declared in a
// file .git/config pulls in with [include] (no extensions.partialClone), which
// no check of CuliBox's reads. The test is skipped when this git cannot be made
// to run the command: the fixture is the proof that the class exists.
func promisorFixture(t *testing.T, viaInclude bool) (of, marker, missing string) {
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
	remote := [][2]string{
		{"remote.origin.url", filepath.Join(base, "nowhere")},
		{"remote.origin.promisor", "true"},
		{"remote.origin.uploadpack", "touch '" + marker + "'; false"},
	}
	if viaInclude {
		inc := filepath.Join(base, "included.cfg")
		for _, kv := range remote {
			fixtureGit(t, of, "config", "--file", inc, kv[0], kv[1])
		}
		fixtureGit(t, of, "config", "include.path", inc)
	} else {
		for _, kv := range append([][2]string{{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"}}, remote...) {
			fixtureGit(t, of, "config", kv[0], kv[1])
		}
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

// No host git call in clone_of that needs no transport lazy-fetches, so a
// promisor remote a box declared there never runs its command on the host: not
// the copy estimate, not the refresh's and the return's checks (BaseBranch,
// HeadInHost, StaleSHAs). Each of the two layers (GIT_NO_LAZY_FETCH in CleanEnv,
// protocol.allow=never in the hardened flags) holds on its own, for a git that
// knows only one of them. The two calls that read clone_of through a transport
// (the first copy's git clone, which may use any transport, and the bundle
// import, the file transport only) rely on GIT_NO_LAZY_FETCH alone; where a box
// may have written clone_of's config, the daemon refuses a partial clone before
// either runs (cmd/culibox TestCheckOfRefusesAPartialCloneABoxMayHaveWritten).
func TestHostGitNeverLazyFetches(t *testing.T) {
	of, marker, missing := promisorFixture(t, false)
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

// oldGit is the real git with lazy fetching left on, as a git that predates
// GIT_NO_LAZY_FETCH (or ignores it) runs: =0 also keeps git upload-pack, which
// a local clone runs in the source repo, from turning it off by itself.
type oldGit struct{ g fullGit }

// fullGit is a Git that can also feed stdin and stream, as ExecGit can.
type fullGit interface {
	StdinGit
	StreamGit
}

func oldEnv(env []string) []string {
	out := slices.Clone(env)
	for i, kv := range out {
		if strings.HasPrefix(kv, "GIT_NO_LAZY_FETCH=") {
			out[i] = "GIT_NO_LAZY_FETCH=0"
		}
	}
	return out
}

func (o oldGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return o.g.Run(ctx, dir, oldEnv(env), args...)
}

func (o oldGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	return o.g.RunStdin(ctx, dir, oldEnv(env), stdin, args...)
}

func (o oldGit) Stream(ctx context.Context, dir string, env []string, stdin io.Reader, stdout io.Writer, args ...string) error {
	return o.g.Stream(ctx, dir, oldEnv(env), stdin, stdout, args...)
}

// anyTransport drops the hardened flags' transport rules (protocol.allow=never
// and the file transport given back) from every call: what is left between a
// promisor remote and its command is GIT_NO_LAZY_FETCH alone.
type anyTransport struct{ g fullGit }

func dropTransportRules(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" && i+1 < len(args) && (args[i+1] == "protocol.allow=never" || args[i+1] == "protocol.file.allow=always") {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func (a anyTransport) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return a.g.Run(ctx, dir, env, dropTransportRules(args)...)
}

func (a anyTransport) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	return a.g.RunStdin(ctx, dir, env, stdin, dropTransportRules(args)...)
}

func (a anyTransport) Stream(ctx context.Context, dir string, env []string, stdin io.Reader, stdout io.Writer, args ...string) error {
	return a.g.Stream(ctx, dir, env, stdin, stdout, dropTransportRules(args)...)
}

// A promisor remote declared through an [include] passes every check of
// CuliBox's by design (a stated limit), so it reaches a return's host calls.
// There GIT_NO_LAZY_FETCH alone stops it: the same return with the transport
// rules dropped from the flags writes no marker, and with the variable off too
// it does (which proves the return reaches the lazy fetch at all).
func TestReturnIncludedPromisorStoppedByTheEnvironment(t *testing.T) {
	of, marker, missing := promisorFixture(t, true)
	g, _ := NewExecGit()
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckOf(of, home, nil); err != nil {
		t.Fatalf("an [include]d promisor is allowed by design: CheckOf = %v", err)
	}
	ret := func(t *testing.T, gg Git) ReturnResult {
		dir := filepath.Join(t.TempDir(), "copy")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return Return(ctx, gg, listOnlyDocker{listing: missing + " refs/heads/feat\n#culibox-status\n"}, ReturnRequest{
			Clone: config.Clone{Of: of, Dir: dir, Profile: "p"}, Mode: ReturnAuto,
			Image: "img", Instance: "T", TempDir: t.TempDir(), Home: home,
		})
	}
	t.Run("fixture: with neither layer the return fires", func(t *testing.T) {
		ret(t, anyTransport{oldGit{g}})
		if _, err := os.Stat(marker); err != nil {
			t.Fatal("the return wrote no marker with lazy fetching on and every transport allowed: the subtest below would prove nothing")
		}
		os.Remove(marker)
	})
	t.Run("GIT_NO_LAZY_FETCH alone", func(t *testing.T) {
		res := ret(t, anyTransport{g})
		if _, err := os.Stat(marker); err == nil {
			os.Remove(marker)
			t.Fatal("host git lazy-fetched with GIT_NO_LAZY_FETCH=1: the promisor remote's upload-pack command ran on the host")
		}
		if res.Error != "" || res.Base != "master" || len(res.NotHome) != 1 {
			t.Errorf("the return did not reach its host checks: %+v", res)
		}
	})
}
