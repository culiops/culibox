//go:build dockerit

package clone

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// citTreeSnapshot is every path under dir with its mode, size and mtime: what a
// host git run in the clone's .git would change.
func citTreeSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(p + " " + fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().UTC().Format(time.RFC3339Nano) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestReturnInARealContainer lists a real clone's branches in a network-less
// container and brings the unhomed ones home, without the host running git in
// the clone.
func TestReturnInARealContainer(t *testing.T) {
	e := citNewRefreshEnv(t)
	citMustGit(t, e.dir, "checkout", "-q", "-b", "feat/x")
	citWrite(t, filepath.Join(e.dir, "feat.txt"), "feat\n")
	citMustGit(t, e.dir, "add", "-A")
	citMustGit(t, e.dir, "commit", "-q", "-m", "feat in the box")
	featTip := citMustGit(t, e.dir, "rev-parse", "HEAD")
	citMustGit(t, e.dir, "branch", "old", "main") // already home

	before := citTreeSnapshot(t, filepath.Join(e.dir, ".git"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := ReturnRequest{Clone: e.c, Mode: ReturnAuto, Image: e.image, Instance: citInstance,
		TempDir: filepath.Join(e.data, "clone-fetch"), Home: e.home, DataDir: e.data}
	res := Return(ctx, e.g, e.d, req)
	t.Logf("Return: %+v", res)
	if res.Error != "" || len(res.NotHome) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if want := []string{"box/feat/x"}; !slices.Equal(res.Home, want) {
		t.Errorf("Home = %q, want %q", res.Home, want)
	}
	if got := citMustGit(t, e.of, "rev-parse", "refs/heads/box/feat/x"); got != featTip {
		t.Errorf("box/feat/x = %s, want the clone's %s", got, featTip)
	}
	if got := citMustGit(t, e.of, "for-each-ref", "--format=%(refname)", "refs/heads/box/"); got != "refs/heads/box/feat/x" {
		t.Errorf("box refs = %q, want only box/feat/x (old is host main)", got)
	}
	if after := citTreeSnapshot(t, filepath.Join(e.dir, ".git")); after != before {
		t.Errorf("the clone's .git changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if left := citLeftovers(t, e.d.Bin); len(left) != 0 {
		t.Errorf("containers left behind: %v", left)
	}

	// A second pass has nothing to do.
	res = Return(ctx, e.g, e.d, req)
	if len(res.Home) != 0 || len(res.NotHome) != 0 || res.Error != "" {
		t.Errorf("second pass = %+v", res)
	}
}
