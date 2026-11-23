//go:build dockerit

package gitview

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// TestBlameRealRepo blames a two-author file through a real container: the last commit and the line runs, the
// .git-blame-ignore-revs file looked through, and a broken one retried without (host-only: needs docker and
// AB_GITVIEW_IMAGE).
func TestBlameRealRepo(t *testing.T) {
	d, image := hostileDocker(t)
	dir := t.TempDir()
	script := `set -e
cd "$1"
git init -q -b main
c() { n=$1; shift; git -c user.name="$n" -c user.email="$n@example.invalid" commit -q "$@"; }
printf 'a\nb\nc\n' > f.tf
git add f.tf && c Minh -m "add f (#12)"
printf 'a\nB\nc\nd\n' > f.tf
c Lan -am "edit"
printf 'a \nB\nc\nd\n' > f.tf
c Fmt -am "format"
git rev-parse HEAD > .git-blame-ignore-revs
printf 'a \nB\nc\nd\ne\n' > f.tf`
	args := []string{"run", "--rm", "--network", "none", "--pull=never",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"-e", "HOME=/tmp", "-e", "GIT_CONFIG_GLOBAL=/dev/null", "-e", "GIT_CONFIG_NOSYSTEM=1",
		"--mount", "type=bind,src=" + dir + ",dst=" + dir,
		"--entrypoint", "sh", image, "-c", script, "sh", dir}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	if err := d.RunStream(ctx, args, &out); err != nil {
		t.Fatalf("building the repo in the image: %v", err)
	}
	tg := Target{Top: dir, Common: filepath.Join(dir, ".git")}
	r := &Runner{Docker: d, Image: image, UID: os.Getuid(), GID: os.Getgid()}
	yes := func() bool { return true }
	authors := func(b Blame) []string {
		var a []string
		for _, run := range b.Lines {
			for range run[1] {
				a = append(a, b.Commits[run[0]].Author)
			}
		}
		return a
	}

	plain := r.Blame(ctx, tg, "f.tf", BlameKey{Version: "1"}, yes)
	if plain.State != "ok" || plain.Last == nil || plain.Last.Author != "Fmt" || plain.IgnoredRevs {
		t.Fatalf("blame = %+v", plain)
	}
	if got := authors(plain); !slices.Equal(got, []string{"Fmt", "Lan", "Minh", "Lan", "Not Committed Yet"}) {
		t.Errorf("authors %v", got)
	}
	ign := r.Blame(ctx, tg, "f.tf", BlameKey{Version: "1", IgnoreRevs: true}, yes)
	if ign.State != "ok" || !ign.IgnoredRevs {
		t.Fatalf("blame with ignore revs = %+v", ign)
	}
	if got := authors(ign); got[0] == "Fmt" {
		t.Errorf("the formatting commit was not looked through: %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, IgnoreRevsName), []byte("zzz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := r.Blame(ctx, tg, "f.tf", BlameKey{Version: "2", IgnoreRevs: true}, yes)
	if bad.State != "ok" || bad.IgnoredRevs {
		t.Fatalf("blame with a broken ignore-revs file = %+v", bad)
	}
	// The repository's own config names a missing list: every plain blame fails, the retry without any list does not.
	cfg, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.WriteString("[blame]\n\tignoreRevsFile = nosuch\n"); err != nil {
		t.Fatal(err)
	}
	cfg.Close()
	if b := r.Blame(ctx, tg, "f.tf", BlameKey{Version: "3"}, yes); b.State != "ok" || b.IgnoredRevs {
		t.Fatalf("blame with a broken blame.ignoreRevsFile = %+v", b)
	}
}
