package clone

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

// safeDirsOf is every safe.directory value an argv gives the container's git:
// the GIT_CONFIG_COUNT/KEY_n/VALUE_n env (command scope) and any -c on a git
// command line. It fails the test on a malformed env set.
func safeDirsOf(t *testing.T, args []string) []string {
	t.Helper()
	env := map[string]string{}
	var dirs []string
	for i := 0; i+1 < len(args); i++ {
		switch {
		case args[i] == "-e":
			k, v, _ := strings.Cut(args[i+1], "=")
			env[k] = v
		case args[i] == "-c" && strings.HasPrefix(strings.ToLower(args[i+1]), "safe.directory="):
			dirs = append(dirs, args[i+1][len("safe.directory="):])
		}
	}
	if c, ok := env["GIT_CONFIG_COUNT"]; ok {
		n, err := strconv.Atoi(c)
		if err != nil || n < 0 {
			t.Fatalf("GIT_CONFIG_COUNT=%q", c)
		}
		for i := range n {
			k, ok := env["GIT_CONFIG_KEY_"+strconv.Itoa(i)]
			v, vok := env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
			if !ok || !vok {
				t.Fatalf("GIT_CONFIG_COUNT=%d but entry %d is missing", n, i)
			}
			if strings.EqualFold(k, "safe.directory") {
				dirs = append(dirs, v)
			}
		}
	}
	return dirs
}

// mountDst is the container path of the bind mount whose source is src.
func mountDst(t *testing.T, args []string, src string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--mount" {
			continue
		}
		var s, d string
		for _, f := range strings.Split(args[i+1], ",") {
			if v, ok := strings.CutPrefix(f, "src="); ok {
				s = v
			}
			if v, ok := strings.CutPrefix(f, "dst="); ok {
				d = v
			}
		}
		if s == src {
			return d
		}
	}
	t.Fatalf("no mount of %s in %q", src, args)
	return ""
}

// Every one-shot container over a clone lets its git open exactly the clone,
// by the path git sees in the container (the mount target), and nothing else:
// never "*", never clone_of's work tree (#294).
func TestOneShotSafeDirectoryIsExactlyTheClone(t *testing.T) {
	for _, c := range []config.Clone{
		{Of: "/src/of", Dir: "/work/clone"},
		{Of: "/Users/macuser/ab-r1-upg/k/app", Dir: "/Users/macuser/ab-r1-upg/k/copy"}, // a darwin layout
	} {
		sha := strings.Repeat("ab", 20)
		runs := map[string][]string{
			"list":    ListArgs(ReturnRequest{Clone: c, Image: "img:1", Instance: "T"}, "n"),
			"bundle":  BundleArgs(FetchRequest{Clone: c, Branch: "feat", Base: "main", Image: "img:1", Instance: "T"}, sha, "n"),
			"refresh": RefreshArgs(RefreshRequest{Clone: c, Image: "img:1", Instance: "T"}, "main", nil, "n"),
		}
		for name, args := range runs {
			got := safeDirsOf(t, args)
			if want := []string{c.Dir}; !slices.Equal(got, want) {
				t.Errorf("%s %s: safe.directory = %q, want exactly %q", c.Dir, name, got, want)
			}
			if dst := mountDst(t, args, c.Dir); !slices.Contains(got, dst) {
				t.Errorf("%s %s: safe.directory %q is not the clone's path in the container %q", c.Dir, name, got, dst)
			}
			for _, a := range args {
				if strings.Contains(a, "safe.directory=*") || strings.HasSuffix(a, "=*") {
					t.Errorf("%s %s: a wildcard reaches the container: %q", c.Dir, name, a)
				}
			}
		}
	}
}

// The refresh fetches from clone_of's .git through git-upload-pack, which git
// starts without the command-scope config (GIT_CONFIG_* is in its local_repo_env
// list), so the script hands upload-pack its own safe.directory: clone_of's .git,
// from the positional path, never a wildcard and never in the script text.
func TestRefreshScriptScopesUploadPackToCloneOfGitDir(t *testing.T) {
	for _, want := range []string{
		`export CULIBOX_SAFE_OF="$of/.git"`,
		`up='git -c safe.directory="$CULIBOX_SAFE_OF" upload-pack'`,
	} {
		if strings.Count(refreshScript, want) != 1 {
			t.Errorf("refreshScript lacks %q", want)
		}
	}
	n := strings.Count(refreshScript, "--upload-pack=")
	if n == 0 || n != strings.Count(refreshScript, `--upload-pack="$up"`) {
		t.Errorf("%d --upload-pack options, not all of them scoped", n)
	}
	if strings.Contains(refreshScript, "safe.directory=*") || strings.Contains(refreshScript, "safe.directory '*'") {
		t.Error("refreshScript has a safe.directory wildcard")
	}
}

// assumeForeignOwner makes git treat every path as owned by another user (git's
// own test knob, the check is_path_owned_by_current_uid makes): what a
// container's git sees on macOS, where the clone and clone_of reach the box VM
// over virtiofs (#294). Only the in-container git gets it, never host git.
var assumeForeignOwner = []string{"GIT_TEST_ASSUME_DIFFERENT_OWNER=1"}

// The return (listing + bundle containers) brings a box branch home although
// the container's git sees a foreign-owned clone (#294 row K4).
func TestReturnWorksOnAForeignOwnedClone(t *testing.T) {
	x := newReturnFx(t)
	x.dock.env = assumeForeignOwner
	res := x.ret(ReturnAuto)
	if res.Error != "" || len(res.NotHome) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if want := []string{"box/feat", "box/main"}; !slices.Equal(res.Home, want) {
		t.Errorf("Home = %q, want %q", res.Home, want)
	}
}

// `clone fetch` (the bundle container alone) works on a foreign-owned clone.
func TestFetchWorksOnAForeignOwnedClone(t *testing.T) {
	x := newFetchFx(t)
	x.dock.env = assumeForeignOwner
	if _, err := Fetch(context.Background(), x.rec, x.dock, x.req); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := x.ofRev("refs/heads/box/feat"); err != nil {
		t.Errorf("box/feat not in clone_of: %v", err)
	}
}

// The refresh fast-forwards a foreign-owned clone from a foreign-owned
// clone_of (#294 row K5): the clone needs its safe.directory, and upload-pack
// on clone_of's .git its own.
func TestRefreshWorksOnAForeignOwnedClone(t *testing.T) {
	x := newRefreshFx(t)
	x.dock.env = assumeForeignOwner
	commitIn(t, x.of, "c")
	tip := ofRev(t, x.of, "main")
	res := x.refresh()
	if !res.Refreshed || res.Error != "" {
		t.Fatalf("res = %+v (stderr %q)", res, x.dock.stderr)
	}
	if res.Report.FFTo != tip || res.Report.FFRefused {
		t.Errorf("report = %+v, want a fast-forward to %s", res.Report, tip)
	}
}
