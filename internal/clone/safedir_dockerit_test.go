//go:build dockerit

package clone

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// foreignOwnerDocker is ExecDocker with git's own test knob in every one-shot
// container: the container's git then reads every path as another uid's, as it
// does on macOS, where the clone and clone_of reach the box VM over virtiofs.
// Host git never gets it.
type foreignOwnerDocker struct{ ExecDocker }

func (d foreignOwnerDocker) RunStream(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "run" {
		return d.ExecDocker.RunStream(ctx, args, stdout)
	}
	a := append([]string{"run", "-e", "GIT_TEST_ASSUME_DIFFERENT_OWNER=1"}, args[1:]...)
	return d.ExecDocker.RunStream(ctx, a, stdout)
}

// TestOneShotsOnAForeignOwnedCloneInARealContainer is #294 rows K4 and K5 in the
// real box image: the return (listing + bundle) and the refresh (fetch from
// clone_of through upload-pack, fast-forward) work although git there sees the
// clone and clone_of as foreign-owned.
func TestOneShotsOnAForeignOwnedCloneInARealContainer(t *testing.T) {
	t.Run("return", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		citMustGit(t, e.dir, "checkout", "-q", "-b", "feat/x")
		citWrite(t, filepath.Join(e.dir, "feat.txt"), "feat\n")
		citMustGit(t, e.dir, "add", "-A")
		citMustGit(t, e.dir, "commit", "-q", "-m", "feat in the box")
		tip := citMustGit(t, e.dir, "rev-parse", "HEAD")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res := Return(ctx, e.g, foreignOwnerDocker{e.d}, ReturnRequest{Clone: e.c, Mode: ReturnAuto, Image: e.image,
			Instance: citInstance, TempDir: filepath.Join(e.data, "clone-fetch"), Home: e.home, DataDir: e.data})
		t.Logf("Return: %+v", res)
		if res.Error != "" || len(res.NotHome) != 0 || !slices.Equal(res.Home, []string{"box/feat/x"}) {
			t.Fatalf("result = %+v", res)
		}
		if got := citMustGit(t, e.of, "rev-parse", "refs/heads/box/feat/x"); got != tip {
			t.Errorf("box/feat/x = %s, want %s", got, tip)
		}
	})
	t.Run("refresh", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		hostTip := e.hostCommit(t, "new.txt")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res := Refresh(ctx, e.g, foreignOwnerDocker{e.d}, RefreshRequest{Clone: e.c, Image: e.image, Instance: citInstance, DataDir: e.data})
		t.Logf("Refresh: %+v", res)
		citWantRefreshed(t, res)
		if res.Report.FFTo != hostTip || res.Report.FFRefused {
			t.Errorf("Report = %+v, want a fast-forward to %s", res.Report, hostTip)
		}
		if got := citMustGit(t, e.dir, "rev-parse", "HEAD"); got != hostTip {
			t.Errorf("clone HEAD = %s, want %s", got, hostTip)
		}
	})
}
