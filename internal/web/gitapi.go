package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/gitrepo"
	"github.com/culiops/agent-box/internal/gitview"
)

// GitView runs git status and diff for the file tree (gitview.Runner). Nil on
// the Server means no box runtime: both endpoints answer "unavailable".
type GitView interface {
	Status(context.Context, gitview.Target) gitview.Status
	Diff(context.Context, gitview.Target, string, bool) gitview.Diff
}

// Every reason a client sees is one of these fixed messages. The runner's own
// reason can carry git's stderr (agent-shaped paths and refs), so it is only
// logged.
const (
	msgGitNoRuntime    = "Git status needs the box runtime (docker)"
	msgGitOutside      = "repository is outside the viewable tree"
	msgGitNotAvailable = "Git status is not available for this folder"
	msgGitTimeout      = "Git took too long"
)

var (
	errGitOutside = errors.New("git: repository outside the viewable tree")
	errGitNotRepo = errors.New("git: not a repository")
	errGitUnsafe  = errors.New("git: path cannot be mounted")
)

// gitBody is the body of both endpoints.
type gitBody struct {
	Path string `json:"path"`
}

// gitTarget decides which host directories a git container may mount for the
// request path, and returns them with the request's resolved absolute path.
//
// Repo discovery compares paths lexically, so it is fed a resolved path: the
// request is resolved by the fs policy (fs errors pass through), then by
// EvalSymlinks, and that result must still land where the request did (same
// tree, see inGitTree). Top and Common from the checkout must each be free of
// links, pass the fs policy in that same tree, and not hold a denied tree:
// mounted whole, such a directory would carry agent-box's data dir or box
// state into the container. For a boxed request Target.Root is the contained
// root, the only mount: agent-box chose it and the box cannot swap it, while
// Top and Common inside it could be swapped for links after these checks. Any
// refusal is errGitOutside; no .git is errGitNotRepo; a path docker cannot
// take as a --mount value is errGitUnsafe.
func (s *Server) gitTarget(path string) (t gitview.Target, abs string, err error) {
	p, err := s.fsPolicy()
	if err != nil {
		return gitview.Target{}, "", err
	}
	req, err := p.resolve(path)
	if err != nil {
		return gitview.Target{}, "", err
	}
	abs, err = filepath.EvalSymlinks(req.Abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return gitview.Target{}, "", os.ErrNotExist
		}
		return gitview.Target{}, "", errGitOutside
	}
	if !p.inGitTree(req, abs) {
		return gitview.Target{}, "", errGitOutside
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return gitview.Target{}, "", errGitOutside
	}
	dir := abs
	if !fi.IsDir() {
		dir = filepath.Dir(abs)
	}
	repo, ok := gitrepo.Locate(dir)
	if !ok {
		return gitview.Target{}, "", errGitNotRepo
	}
	// Locate walks up from dir, so dir is within Top; rootRel relies on it.
	if rel, err := filepath.Rel(repo.Top, dir); err != nil || !(rel == "." || filepath.IsLocal(rel)) {
		return gitview.Target{}, "", errGitOutside
	}
	for _, x := range []string{repo.Top, repo.Common} {
		if !p.inGitTree(req, x) || p.holdsDenied(x) {
			return gitview.Target{}, "", errGitOutside
		}
	}
	t = gitview.Target{Top: repo.Top, Common: repo.Common}
	if req.Strict {
		if r, err := filepath.EvalSymlinks(req.Root); err != nil || r != req.Root {
			return gitview.Target{}, "", errGitOutside
		}
		t.Root = req.Root
	}
	if t.Unsafe() != "" {
		return gitview.Target{}, "", errGitUnsafe
	}
	return t, abs, nil
}

// inGitTree reports whether x is a link-free path that the fs policy serves in
// the same tree as req: for a boxed request, inside the same contained root; for
// a host request, outside every contained root. A path EvalSymlinks would
// change has a link in it and is refused, whoever planted the link.
func (p fsPolicy) inGitTree(req fsTarget, x string) bool {
	if r, err := filepath.EvalSymlinks(x); err != nil || r != x {
		return false
	}
	got, err := p.resolve(x)
	if err != nil {
		return false
	}
	if req.Strict {
		return got.Strict && got.Root == req.Root
	}
	return !got.Strict
}

// holdsDenied reports whether dir is a denied tree or an ancestor of one.
func (p fsPolicy) holdsDenied(dir string) bool {
	k, ok := statKey(dir)
	if !ok {
		return true
	}
	_, held := p.deniedOrAbove[k]
	return held
}

// decodeGitBody applies the POST + CSRF guard, bounds and decodes the body,
// and requires an absolute path. It writes the response itself on failure.
func decodeGitBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body gitBody
	if !decodeOpBody(w, r, &body) {
		return "", false
	}
	if !filepath.IsAbs(body.Path) {
		writeErr(w, http.StatusBadRequest, "bad path")
		return "", false
	}
	return body.Path, true
}

// gitReason maps the runner's reason to a fixed message; a timeout is told
// apart, everything else is "not available".
func gitReason(raw string) string {
	if strings.Contains(raw, "timed out") {
		return msgGitTimeout
	}
	return msgGitNotAvailable
}

// targetReason is the fixed message for a gitTarget refusal.
func targetReason(err error) string {
	if errors.Is(err, errGitOutside) {
		return msgGitOutside
	}
	return msgGitNotAvailable
}

func isGitRefusal(err error) bool {
	return errors.Is(err, errGitOutside) || errors.Is(err, errGitNotRepo) || errors.Is(err, errGitUnsafe)
}

func unavailableGitStatus(reason string) gitview.Status {
	return gitview.Status{State: "unavailable", Reason: reason, Entries: []gitview.Entry{}}
}

func unavailableGitDiff(reason string) gitview.Diff {
	return gitview.Diff{State: "unavailable", Reason: reason}
}

// handleGitStatus is POST /api/git/status {"path": abs}: the change set of the
// checkout holding path. ok/notrepo/toomany/unavailable are all 200.
func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	path, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	if s.Git == nil {
		writeJSON(w, http.StatusOK, unavailableGitStatus(msgGitNoRuntime))
		return
	}
	t, abs, err := s.gitTarget(path)
	switch {
	case errors.Is(err, errGitNotRepo):
		writeJSON(w, http.StatusOK, gitview.Status{State: "notrepo", Entries: []gitview.Entry{}})
		return
	case isGitRefusal(err):
		writeJSON(w, http.StatusOK, unavailableGitStatus(targetReason(err)))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	// st is this request's own copy (Status returns a value with cloned
	// entries), so RootRel never reaches the runner's shared cache.
	st := s.Git.Status(r.Context(), t)
	st.RootRel = rootRel(t.Top, abs)
	if st.State == "unavailable" {
		log.Printf("git status %q: %q", t.Top, st.Reason)
		st.Reason = gitReason(st.Reason)
	} else {
		st.Reason = ""
	}
	if st.Entries == nil {
		st.Entries = []gitview.Entry{}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleGitDiff is POST /api/git/diff {"path": abs file}: one file's diff
// against HEAD, or against nothing when the file is untracked.
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	path, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	if s.Git == nil {
		writeJSON(w, http.StatusOK, unavailableGitDiff(msgGitNoRuntime))
		return
	}
	t, abs, err := s.gitTarget(path)
	switch {
	case isGitRefusal(err):
		writeJSON(w, http.StatusOK, unavailableGitDiff(targetReason(err)))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		writeFsErr(w, err)
		return
	}
	if !fi.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, "not a file")
		return
	}
	rel, err := filepath.Rel(t.Top, abs)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		writeJSON(w, http.StatusOK, unavailableGitDiff(msgGitOutside))
		return
	}
	rel = filepath.ToSlash(rel)

	st := s.Git.Status(r.Context(), t)
	untracked := false
	switch st.State {
	case "ok":
		untracked = untrackedIn(st, rel)
	case "toomany":
		// No entries to look in: diff against HEAD; an untracked file reads
		// as unchanged rather than failing.
	default:
		log.Printf("git diff %q: status: %q", t.Top, st.Reason)
		writeJSON(w, http.StatusOK, unavailableGitDiff(gitReason(st.Reason)))
		return
	}
	d := s.Git.Diff(r.Context(), t, rel, untracked)
	if d.State != "ok" {
		log.Printf("git diff %q: %q", t.Top, d.Reason)
		d = unavailableGitDiff(gitReason(d.Reason))
	} else {
		d.Reason = ""
	}
	writeJSON(w, http.StatusOK, d)
}

// rootRel is where the request's resolved dir (abs, or its parent for a file)
// sits inside top, slash-separated, "." for top itself. gitrepo.Locate walked
// up from that dir to top, so it always lies within top.
func rootRel(top, abs string) string {
	dir := abs
	if fi, err := os.Lstat(abs); err == nil && !fi.IsDir() {
		dir = filepath.Dir(abs)
	}
	rel, err := filepath.Rel(top, dir)
	if err != nil || !(rel == "." || filepath.IsLocal(rel)) {
		return "."
	}
	return filepath.ToSlash(rel)
}

// untrackedIn reports whether rel (slash-separated, relative to Top) is
// untracked: listed as U itself, under a collapsed untracked dir ("dir/"), or
// anywhere in a repository with no commits.
func untrackedIn(st gitview.Status, rel string) bool {
	if st.NoHead {
		return true
	}
	for _, e := range st.Entries {
		if e.X != "U" {
			continue
		}
		if e.P == rel || (strings.HasSuffix(e.P, "/") && strings.HasPrefix(rel, e.P)) {
			return true
		}
	}
	return false
}
