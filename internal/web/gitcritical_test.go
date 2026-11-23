package web

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/gitview"
)

// The critical path of the Changes list (design P2.7): how many container runs deep each scenario is, through the real
// handlers and a real gitview.Runner over a docker whose every run takes runDelay. depth = elapsed / runDelay, rounded.

const runDelay = 100 * time.Millisecond

const (
	cpHead = "1111111111111111111111111111111111111111"
	cpBase = "2222222222222222222222222222222222222222"
	cpTip  = "4444444444444444444444444444444444444444"
)

// delayDocker answers like a repository on branch feat at cpHead, ahead of origin/main (origin/HEAD's target), with
// a.go modified and new.txt untracked; every run takes runDelay.
type delayDocker struct {
	mu   sync.Mutex
	runs []string
}

func (d *delayDocker) RunStream(ctx context.Context, args []string, w io.Writer) error {
	sub := ""
	if i := slices.Index(args, "safe.directory=*"); i >= 0 && i+1 < len(args) {
		sub = args[i+1]
	}
	d.mu.Lock()
	d.runs = append(d.runs, sub)
	d.mu.Unlock()
	select {
	case <-time.After(runDelay):
	case <-ctx.Done():
		return ctx.Err()
	}
	var out string
	switch sub {
	case "status":
		out = "# branch.oid " + cpHead + "\x00# branch.head feat\x00" + "1 .M N... 100644 100644 100644 a b a.go\x00? new.txt\x00"
	case "for-each-ref":
		out = "refs/remotes/origin/HEAD\x00refs/remotes/origin/main\x00" + cpTip + "\x00\n" + "refs/remotes/origin/main\x00\x00" + cpTip + "\x00\n"
	case "merge-base":
		out = cpBase + "\n"
	case "diff":
		out = ":100644 100644 aaaaaaa bbbbbbb M\x00a.go\x00" + "3\t1\ta.go\x00"
	}
	_, err := io.WriteString(w, out)
	return err
}

func (d *delayDocker) Remove(context.Context, string) error { return nil }

// critServer is a server whose git is a real Runner over delayDocker, on a temp repository, with a clock the test can
// move forward (real time plus an offset).
func critServer(t *testing.T) (s *Server, repo string, off *atomic.Int64) {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(repo, "new.txt"), "one\ntwo\n")
	off = &atomic.Int64{}
	r := &gitview.Runner{Docker: &delayDocker{}, Image: "img", UID: 1, GID: 1, Now: func() time.Time { return time.Now().Add(time.Duration(off.Load())) }}
	return &Server{Home: h, Git: r}, repo, off
}

type critReq struct {
	name string
	h    http.HandlerFunc
	body string
}

// depthOf sends every request at once and returns how many runs deep the slowest answer was, and the answers by name.
func depthOf(t *testing.T, reqs ...critReq) (int, map[string]string) {
	t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]string{}
	t0 := time.Now()
	for _, q := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := gitPost(q.h, "/api/git/x", q.body)
			if rr.Code != http.StatusOK {
				t.Errorf("%s: code %d %s", q.name, rr.Code, rr.Body)
			}
			mu.Lock()
			got[q.name] = rr.Body.String()
			mu.Unlock()
		}()
	}
	wg.Wait()
	return int((time.Since(t0) + runDelay/2) / runDelay), got
}

func TestCriticalPathUncommittedCold(t *testing.T) {
	s, repo, _ := critServer(t)
	d, got := depthOf(t,
		critReq{"status", s.handleGitStatus, pathBody(repo)},
		critReq{"numstat", s.handleGitNumstat, numstatBody(repo, "head")})
	if d > 1 {
		t.Fatalf("Uncommitted cold is %d runs deep, want <= 1 (%v)", d, got)
	}
}

func TestCriticalPathBranchPickCold(t *testing.T) {
	s, repo, _ := critServer(t)
	d, got := depthOf(t,
		critReq{"status", s.handleGitStatus, pathBody(repo)},
		critReq{"numstat", s.handleGitNumstat, numstatBody(repo, "branch")},
		critReq{"base", s.handleGitBase, pathBody(repo)})
	if d > 2 {
		t.Fatalf("Branch (a remembered pick) cold is %d runs deep, want <= 2 (%v)", d, got)
	}
}

func TestCriticalPathNoPickCold(t *testing.T) {
	s, repo, _ := critServer(t)
	d, got := depthOf(t,
		critReq{"status", s.handleGitStatus, pathBody(repo)},
		critReq{"numstat", s.handleGitNumstat, numstatBody(repo, "auto")},
		critReq{"base", s.handleGitBase, pathBody(repo)})
	if d > 2 {
		t.Fatalf("no remembered scope, cold, is %d runs deep, want <= 2 (%v)", d, got)
	}
	if !strings.Contains(got["numstat"], `"scope":"branch"`) {
		t.Fatalf("auto on a branch ahead of its base answered %s, want scope branch", got["numstat"])
	}
}

func TestCriticalPathBranchWarmPastBaseReuse(t *testing.T) {
	s, repo, off := critServer(t)
	depthOf(t,
		critReq{"status", s.handleGitStatus, pathBody(repo)},
		critReq{"numstat", s.handleGitNumstat, numstatBody(repo, "branch")},
		critReq{"base", s.handleGitBase, pathBody(repo)})
	off.Add(int64(31 * time.Second))
	fresh := func(scope string) string {
		if scope == "" {
			return `{"path":"` + repo + `","fresh":true}`
		}
		return `{"path":"` + repo + `","scope":"` + scope + `","fresh":true}`
	}
	d, got := depthOf(t,
		critReq{"status", s.handleGitStatus, fresh("")},
		critReq{"numstat", s.handleGitNumstat, fresh("branch")},
		critReq{"base", s.handleGitBase, pathBody(repo)})
	if d > 1 {
		t.Fatalf("Branch warm past the 30 s base reuse is %d runs deep, want <= 1 (%v)", d, got)
	}
}
