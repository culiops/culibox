//go:build dockerit

package gitview

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/clone"
)

// hostileVectors are the config keys a hostile .git/config can use to make git
// run a command. Each one touches its own marker, so a failure names the vector.
//
// Which of these can fire at all under the current guards (measured with
// writable mounts, see TestHostileConfigControl): filter.x.clean always, since
// git has no flag to stop it and it legitimately runs inside the container
// (spec S1); core.fsmonitor only if -c core.fsmonitor=false is dropped. The rest
// (core.pager, core.sshCommand, filter.x.smudge, diff.external, diff.x.textconv)
// are belt and braces: nothing in status or diff reaches them today, so a
// passing run says little about them beyond "still contained".
//
// filter.x.process is kept apart: it is a long-running filter that must speak
// git's pkt-line handshake, so a bare `touch` makes git die ("the remote end
// hung up") and the whole status fails. That is a harmless "unavailable", but it
// would hide whether the other vectors still let status and diff work.
var hostileVectors = []string{
	"core.fsmonitor", "core.pager", "core.sshCommand",
	"filter.x.clean", "filter.x.smudge",
	"diff.external", "diff.x.textconv",
}

const processVector = "filter.x.process"

// rwMounts runs the same argv with the bind mounts writable. It is the test's
// positive control: it proves a hostile command CAN create a marker in this
// setup, so "no marker" under the real read-only mounts means something.
type rwMounts struct{ clone.Docker }

func (r rwMounts) RunStream(ctx context.Context, args []string, out io.Writer) error {
	a := slices.Clone(args)
	for i, s := range a {
		if strings.HasPrefix(s, "type=bind,") {
			a[i] = strings.TrimSuffix(s, ",readonly")
		}
	}
	return r.Docker.RunStream(ctx, a, out)
}

func hostileDocker(t *testing.T) (clone.ExecDocker, string) {
	t.Helper()
	image := os.Getenv("AB_GITVIEW_IMAGE")
	if image == "" {
		t.Skip("AB_GITVIEW_IMAGE is not set: point it at an already pulled box image, e.g. $(jq -r .box_image config.json)")
	}
	bin, err := box.LookDocker("")
	if err != nil {
		t.Fatalf("AB_GITVIEW_IMAGE is set but docker is unavailable: %v", err)
	}
	d := clone.ExecDocker{Bin: bin}
	if err := exec.Command(bin, "image", "inspect", image).Run(); err != nil {
		t.Fatalf("AB_GITVIEW_IMAGE is set but image %s is not present locally (the runner uses --pull=never): %v", image, err)
	}
	return d, image
}

// hostileRepo makes a repository in a fresh dir and returns it. The repository
// is built by git INSIDE the image (the host never execs git), from a trusted
// config; only afterwards are the hostile config and attributes written, so
// setup itself never runs them. Every vector touches <dir>/MARKER_<vector>.
func hostileRepo(t *testing.T, d clone.ExecDocker, image string, only ...string) string {
	t.Helper()
	dir := t.TempDir()
	script := `set -e
cd "$1"
git init -q -b main
git config user.name t && git config user.email t@example.invalid
mkdir sub
printf 'one\ntwo\nthree\n' > a.txt
printf 'x\ny\n' > sub/b.txt
git add -A
git commit -q -m base`
	args := []string{"run", "--rm", "--network", "none", "--pull=never",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"-e", "HOME=/tmp", "-e", "GIT_CONFIG_GLOBAL=/dev/null", "-e", "GIT_CONFIG_NOSYSTEM=1",
		"--mount", "type=bind,src=" + dir + ",dst=" + dir,
		"--entrypoint", "sh", image, "-c", script, "sh", dir}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.RunStream(ctx, args, &out); err != nil {
		t.Fatalf("building the repo in the image: %v", err)
	}

	cmd := func(v string) string { return "sh -c 'touch " + filepath.Join(dir, "MARKER_"+v) + "'" }
	on := func(v string) bool { return slices.Contains(only, v) }
	cfg := "\n[core]\n"
	for _, k := range []string{"fsmonitor", "pager", "sshCommand"} {
		if on("core." + k) {
			cfg += "\t" + k + " = \"" + cmd("core."+k) + "\"\n"
		}
	}
	cfg += "[filter \"x\"]\n"
	for _, k := range []string{"clean", "smudge", "process"} {
		if on("filter.x." + k) {
			cfg += "\t" + k + " = \"" + cmd("filter.x."+k) + "\"\n"
		}
	}
	cfg += "[diff]\n"
	if on("diff.external") {
		cfg += "\texternal = \"" + cmd("diff.external") + "\"\n"
	}
	cfg += "[diff \"x\"]\n"
	if on("diff.x.textconv") {
		cfg += "\ttextconv = \"" + cmd("diff.x.textconv") + "\"\n"
	}
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	write := func(rel, s string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitattributes", "* filter=x diff=x\n")
	write("a.txt", "one\nTWO\nthree\nfour\n") // tracked, modified
	write("new.txt", "brand new\n")           // untracked
	return dir
}

func markers(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "MARKER_*"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range m {
		m[i] = filepath.Base(m[i])
	}
	return m
}

func runHostile(t *testing.T, r *Runner, tg Target) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	st := r.Status(ctx, tg)
	t.Logf("status: %+v", st)
	if st.State != "ok" {
		t.Fatalf("status state = %q (%s), want ok", st.State, st.Reason)
	}
	want := map[string]string{"a.txt": "M", "new.txt": "U", ".gitattributes": "U"}
	got := map[string]string{}
	for _, e := range st.Entries {
		got[e.P] = e.X
	}
	for p, x := range want {
		if got[p] != x {
			t.Errorf("status entry %s = %q, want %q (entries %v)", p, got[p], x, st.Entries)
		}
	}

	df := r.Diff(ctx, tg, "a.txt", false)
	t.Logf("diff a.txt: state=%s kind=%s reason=%q\n%s", df.State, df.Kind, df.Reason, df.Diff)
	if df.State != "ok" || df.Kind != "text" || !strings.Contains(df.Diff, "+TWO") {
		t.Errorf("tracked diff = state %q kind %q reason %q", df.State, df.Kind, df.Reason)
	}

	un := r.Diff(ctx, tg, "new.txt", true)
	t.Logf("diff new.txt (untracked): state=%s kind=%s reason=%q\n%s", un.State, un.Kind, un.Reason, un.Diff)
	if un.State != "ok" || un.Kind != "text" || !strings.Contains(un.Diff, "+brand new") {
		t.Errorf("untracked diff = state %q kind %q reason %q", un.State, un.Kind, un.Reason)
	}

	// The repo has one commit on main and no remote, so main is the base and
	// HEAD is its own merge base. for-each-ref and merge-base read the hostile
	// config like status does, and must neither run it nor fail on it.
	bi := r.Base(ctx, tg)
	t.Logf("base: %+v", bi)
	if st.Head == "" || st.Branch != "main" {
		t.Errorf("status branch %q head %q, want main and a commit", st.Branch, st.Head)
	}
	if bi.State != "ok" || bi.Branch != "main" || bi.BaseRef != "refs/heads/main" || bi.BaseName != "main" || bi.MergeBase != st.Head || bi.Ahead {
		t.Errorf("base = %+v, want ok on main against refs/heads/main with merge base %q", bi, st.Head)
	}
}

// TestHostileConfigNeverRunsOnHost: an agent-shaped .git/config that tries
// every way to make git run a command must leave no trace on the host. Git
// runs in a container whose mounts of the checkout are read-only, so even a
// command that does run in there cannot create the marker.
func TestHostileConfigNeverRunsOnHost(t *testing.T) {
	d, image := hostileDocker(t)
	for _, shape := range []struct {
		name string
		root bool // the boxed R8 shape: Root = the workspace holding Top
	}{{"host checkout", false}, {"boxed workspace root", true}} {
		t.Run(shape.name, func(t *testing.T) {
			dir := hostileRepo(t, d, image, hostileVectors...)
			tg := Target{Top: dir, Common: filepath.Join(dir, ".git")}
			if shape.root {
				tg.Root = dir
			}
			if why := tg.Unsafe(); why != "" {
				t.Fatalf("target refused: %s", why)
			}
			r := &Runner{Docker: d, Image: image, UID: os.Getuid(), GID: os.Getgid()}
			runHostile(t, r, tg)
			if m := markers(t, dir); len(m) != 0 {
				t.Errorf("hostile config executed and left %v", m)
			}
		})
	}
}

// TestHostileConfigControl is the positive control for the test above: with the
// mounts made writable a hostile command does leave its marker, so the markers
// are a working detector and the read-only mount is what holds.
func TestHostileConfigControl(t *testing.T) {
	d, image := hostileDocker(t)
	dir := hostileRepo(t, d, image, hostileVectors...)
	tg := Target{Top: dir, Common: filepath.Join(dir, ".git")}
	r := &Runner{Docker: rwMounts{d}, Image: image, UID: os.Getuid(), GID: os.Getgid()}
	// Not runHostile: a filter that really runs rewrites the content, so the
	// diffs no longer look like the read-only ones. Only the markers matter here.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r.Status(ctx, tg)
	r.Diff(ctx, tg, "a.txt", false)
	r.Diff(ctx, tg, "new.txt", true)
	m := markers(t, dir)
	t.Logf("with writable mounts the hostile config left: %v", m)
	// filter.x.clean is the one vector that legitimately fires inside the
	// container (spec S1). Any other marker means an in-container guard
	// (-c core.fsmonitor=false, --no-ext-diff, --no-textconv, --no-pager,
	// core.attributesFile) regressed. No marker at all means the control is inert.
	if want := []string{"MARKER_filter.x.clean"}; !slices.Equal(m, want) {
		t.Fatalf("markers with writable mounts = %v, want exactly %v", m, want)
	}
}

// TestHostileProcessFilterNeverRunsOnHost covers filter.x.process on its own:
// git may fail the run when the filter breaks the handshake, so only the host
// side is asserted.
func TestHostileProcessFilterNeverRunsOnHost(t *testing.T) {
	d, image := hostileDocker(t)
	dir := hostileRepo(t, d, image, processVector)
	tg := Target{Top: dir, Common: filepath.Join(dir, ".git"), Root: dir}
	r := &Runner{Docker: d, Image: image, UID: os.Getuid(), GID: os.Getgid()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st := r.Status(ctx, tg)
	t.Logf("status: state=%s reason=%q entries=%v", st.State, st.Reason, st.Entries)
	df := r.Diff(ctx, tg, "a.txt", false)
	t.Logf("diff a.txt: state=%s kind=%s reason=%q", df.State, df.Kind, df.Reason)
	un := r.Diff(ctx, tg, "new.txt", true)
	t.Logf("diff new.txt: state=%s kind=%s reason=%q", un.State, un.Kind, un.Reason)
	if m := markers(t, dir); len(m) != 0 {
		t.Errorf("hostile config executed and left %v", m)
	}
}
