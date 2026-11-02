//go:build dockerit

package clone

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
)

// citRefreshEnv is a host repo whose branch is main, a clone of it and a base
// record that holds the host's tip: the state right after a clone is created.
type citRefreshEnv struct {
	of, dir, data, home string
	c                   config.Clone
	g                   Git
	d                   ExecDocker
	image               string
	oldTip              string // the host's main when the clone was made
}

func citNewRefreshEnv(t *testing.T) citRefreshEnv {
	t.Helper()
	bin, image := citDocker(t)
	citRemoveContainers(bin)
	t.Cleanup(func() { citRemoveContainers(bin) })

	g, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	e := citRefreshEnv{g: g, d: ExecDocker{Bin: bin}, image: image}
	e.of = citOf(t)
	citMustGit(t, e.of, "branch", "-M", "main")
	e.dir = filepath.Join(citRealTemp(t), "clone")
	e.home = citRealTemp(t)
	e.data = citRealTemp(t)
	e.c = config.Clone{Of: e.of, Dir: e.dir}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := EnsureClone(ctx, g, e.c, e.home); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	e.oldTip = citMustGit(t, e.of, "rev-parse", "main")
	if err := ResetBaseRecord(e.data, e.dir, e.oldTip); err != nil {
		t.Fatal(err)
	}
	return e
}

// hostCommit adds a commit with a new file to the host's main.
func (e citRefreshEnv) hostCommit(t *testing.T, file string) string {
	t.Helper()
	citWrite(t, filepath.Join(e.of, file), "host "+file+"\n")
	citMustGit(t, e.of, "add", "-A")
	citMustGit(t, e.of, "commit", "-q", "-m", "host "+file)
	return citMustGit(t, e.of, "rev-parse", "main")
}

func (e citRefreshEnv) refresh(t *testing.T) RefreshResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res := Refresh(ctx, e.g, e.d, RefreshRequest{Clone: e.c, Image: e.image, Instance: citInstance, DataDir: e.data})
	t.Logf("Refresh: %+v", res)
	return res
}

func citWantRefreshed(t *testing.T, res RefreshResult) {
	t.Helper()
	if !res.Refreshed || res.Error != "" || res.Skipped != "" {
		t.Fatalf("not refreshed: %+v", res)
	}
}

// TestCloneRefreshInARealContainer runs the refresh script in a real
// network-less container against real clones: what each state of a clone
// becomes, and that a killed refresh leaves no container behind.
func TestCloneRefreshInARealContainer(t *testing.T) {
	t.Run("behind on main", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		hostTip := e.hostCommit(t, "new.txt")
		res := e.refresh(t)
		citWantRefreshed(t, res)
		if res.Base != "main" || res.Branch != "main" {
			t.Errorf("Base/Branch = %q/%q, want main/main", res.Base, res.Branch)
		}
		if res.Report.FFRefused || res.Report.FFTo != hostTip || res.Report.FFFrom != e.oldTip || res.Report.FFCount != 1 {
			t.Errorf("Report = %+v, want a fast-forward %s..%s of 1", res.Report, e.oldTip, hostTip)
		}
		if res.Report.Ahead != 0 || res.Report.Behind != 0 {
			t.Errorf("ahead/behind = %d/%d, want 0/0", res.Report.Ahead, res.Report.Behind)
		}
		if got := citMustGit(t, e.dir, "rev-parse", "HEAD"); got != hostTip {
			t.Errorf("clone HEAD = %s, want the host tip %s", got, hostTip)
		}
		if _, err := os.Stat(filepath.Join(e.dir, "new.txt")); err != nil {
			t.Errorf("the host's new file is not in the clone's tree: %v", err)
		}
		if res.Rewritten || res.Unverified {
			t.Errorf("Rewritten=%v Unverified=%v, want both false", res.Rewritten, res.Unverified)
		}
		if res.HeadInHost == nil || !*res.HeadInHost {
			t.Errorf("HeadInHost = %v, want true", res.HeadInHost)
		}
	})

	t.Run("feature branch", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		citMustGit(t, e.dir, "checkout", "-q", "-b", "feat")
		citWrite(t, filepath.Join(e.dir, "feat.txt"), "feat\n")
		citMustGit(t, e.dir, "add", "-A")
		citMustGit(t, e.dir, "commit", "-q", "-m", "feat")
		featTip := citMustGit(t, e.dir, "rev-parse", "HEAD")
		hostTip := e.hostCommit(t, "new.txt")
		res := e.refresh(t)
		citWantRefreshed(t, res)
		if got := citMustGit(t, e.dir, "rev-parse", "refs/heads/main"); got != hostTip {
			t.Errorf("clone main = %s, want the host tip %s", got, hostTip)
		}
		if got := citMustGit(t, e.dir, "rev-parse", "HEAD"); got != featTip {
			t.Errorf("clone HEAD = %s, want feat's own commit %s", got, featTip)
		}
		if res.Branch != "feat" || res.Report.Head != "feat" {
			t.Errorf("Branch/Head = %q/%q, want feat", res.Branch, res.Report.Head)
		}
		if res.Report.Ahead != 1 || res.Report.Behind != 1 {
			t.Errorf("ahead/behind = %d/%d, want 1/1", res.Report.Ahead, res.Report.Behind)
		}
		if res.HeadInHost == nil || *res.HeadInHost {
			t.Errorf("HeadInHost = %v, want false: feat's commit is not in the host", res.HeadInHost)
		}
	})

	t.Run("diverged main", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		citWrite(t, filepath.Join(e.dir, "local.txt"), "local\n")
		citMustGit(t, e.dir, "add", "-A")
		citMustGit(t, e.dir, "commit", "-q", "-m", "local on main")
		localTip := citMustGit(t, e.dir, "rev-parse", "main")
		e.hostCommit(t, "new.txt")
		res := e.refresh(t)
		citWantRefreshed(t, res)
		if !res.Report.FFRefused || res.Report.FFTo != "" {
			t.Errorf("Report = %+v, want the fast-forward refused", res.Report)
		}
		if got := citMustGit(t, e.dir, "rev-parse", "main"); got != localTip {
			t.Errorf("local main moved: %s, want %s", got, localTip)
		}
		if res.Report.Ahead != 1 || res.Report.Behind != 1 {
			t.Errorf("ahead/behind = %d/%d, want 1/1", res.Report.Ahead, res.Report.Behind)
		}
	})

	t.Run("rewritten host", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		citMustGit(t, e.dir, "checkout", "-q", "-b", "feat")
		citWrite(t, filepath.Join(e.dir, "feat.txt"), "feat\n")
		citMustGit(t, e.dir, "add", "-A")
		citMustGit(t, e.dir, "commit", "-q", "-m", "feat")

		// The host drops its last commit (a scrubbed secret) and goes on.
		citMustGit(t, e.of, "reset", "-q", "--hard", "main~1")
		e.hostCommit(t, "after-rewrite.txt")

		res := e.refresh(t)
		citWantRefreshed(t, res)
		if !res.Rewritten {
			t.Errorf("Rewritten = false, want true")
		}
		got := map[string]bool{}
		for _, s := range res.Report.Stale {
			got[s] = true
		}
		if !got["feat"] || !got["main"] {
			t.Errorf("Report.Stale = %v, want it to hold feat and main", res.Report.Stale)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, err := Fetch(ctx, e.g, e.d, FetchRequest{
			Clone: e.c, Branch: "feat", Base: "main", Image: e.image, Instance: citInstance,
			TempDir: filepath.Join(citRealTemp(t), "tmp"), Home: e.home, DataDir: e.data,
		})
		if err == nil || !strings.Contains(err.Error(), "no longer has") {
			t.Fatalf("Fetch err = %v, want a refusal that names history the host no longer has", err)
		}
		refs := citRefs(t, e.of)
		if _, ok := refs["refs/heads/box/feat"]; ok {
			t.Errorf("box/feat was created in clone_of: %v", refs)
		}
		for ref := range refs {
			if strings.HasPrefix(ref, "refs/agent-box/") {
				t.Errorf("a quarantine ref is left in clone_of: %s", ref)
			}
		}
	})

	t.Run("dirty tree", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		edited := "package main\n// edited in the box\n"
		citWrite(t, filepath.Join(e.dir, "main.go"), edited)
		hostTip := e.hostCommit(t, "new.txt")
		res := e.refresh(t)
		citWantRefreshed(t, res)
		if res.Report.FFTo != hostTip || res.Report.FFRefused {
			t.Errorf("Report = %+v, want a fast-forward to %s", res.Report, hostTip)
		}
		if got := citMustGit(t, e.dir, "rev-parse", "HEAD"); got != hostTip {
			t.Errorf("clone HEAD = %s, want %s", got, hostTip)
		}
		b, err := os.ReadFile(filepath.Join(e.dir, "main.go"))
		if err != nil || string(b) != edited {
			t.Errorf("the uncommitted edit is gone: %q, %v", b, err)
		}
	})

	t.Run("killed on timeout", func(t *testing.T) {
		e := citNewRefreshEnv(t)
		old := refreshScript
		refreshScript = "sleep 600"
		t.Cleanup(func() { refreshScript = old })
		e.hostCommit(t, "new.txt")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		res := Refresh(ctx, e.g, e.d, RefreshRequest{Clone: e.c, Image: e.image, Instance: citInstance, DataDir: e.data})
		t.Logf("Refresh: %+v after %s", res, time.Since(start))
		if res.Error == "" || res.Refreshed {
			t.Errorf("Refresh = %+v, want an error and not refreshed", res)
		}
		if d := time.Since(start); d > 45*time.Second {
			t.Errorf("Refresh took %s, want it to end soon after its 5s deadline", d)
		}
		bin, _ := citDocker(t)
		if left := citLeftovers(t, bin); len(left) != 0 {
			t.Errorf("containers left behind by the killed refresh: %v", left)
		}
	})
}
