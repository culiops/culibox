package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/culibox/internal/gitrepo"
	"github.com/culiops/culibox/internal/gitview"
)

// GitView runs git status, diff, base lookup and line counts for the file tree
// (gitview.Runner). Nil on the Server means no box runtime: every endpoint
// answers "unavailable". Base takes no ref: the runner picks the base branch
// from the repository's own refs, so a request can never name one. The
// Scope.BaseRef a handler gives Numstat is the one Base returned.
type GitView interface {
	Status(context.Context, gitview.Target) gitview.Status
	StatusFresh(context.Context, gitview.Target) gitview.Status
	Diff(context.Context, gitview.Target, string, bool) gitview.Diff
	Base(context.Context, gitview.Target) gitview.BaseInfo
	Numstat(context.Context, gitview.Target, gitview.Scope) gitview.Numstat
}

// Every reason a client sees is one of these fixed messages. The runner's own
// reason can carry git's stderr (agent-shaped paths and refs), so it is only
// logged.
const (
	msgGitNoRuntime    = "Git status needs the box runtime (docker)"
	msgGitOutside      = "repository is outside the viewable tree"
	msgGitNotAvailable = "Git status is not available for this folder"
	msgGitTimeout      = "Git took too long"
	msgGitNoImage      = "Git status needs the box image (start a boxed session once)"
	msgGitTooMany      = "Too many changes to tell what changed in this file"
	msgGitNoBase       = "no base branch"
)

var (
	errGitOutside = errors.New("git: repository outside the viewable tree")
	errGitNotRepo = errors.New("git: not a repository")
	errGitUnsafe  = errors.New("git: path cannot be mounted")
)

// gitBody is the body of every git endpoint.
type gitBody struct {
	Path string `json:"path"`
	// Fresh (status only) asks for a result read after this request arrived,
	// not a reused or already-running one: a client sets it when it has just
	// changed something on disk.
	Fresh bool `json:"fresh,omitempty"`
	// Scope (numstat only) is "head" or "branch", nothing else: the base branch
	// of "branch" is derived by the server, no client field can name a ref.
	Scope string `json:"scope,omitempty"`
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
func decodeGitBody(w http.ResponseWriter, r *http.Request) (gitBody, bool) {
	var body gitBody
	if !decodeOpBody(w, r, &body) {
		return gitBody{}, false
	}
	if !filepath.IsAbs(body.Path) {
		writeErr(w, http.StatusBadRequest, "bad path")
		return gitBody{}, false
	}
	return body, true
}

// gitReason maps the runner's reason to a fixed message; a timeout and a
// missing box image are told apart, everything else is "not available".
func gitReason(raw string) string {
	if strings.Contains(raw, gitview.ReasonNoImage) {
		return msgGitNoImage
	}
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

func unavailableGitBase(reason string) gitview.BaseInfo {
	return gitview.BaseInfo{State: "unavailable", Reason: reason}
}

func unavailableGitNumstat(reason string) gitview.Numstat {
	return gitview.Numstat{State: "unavailable", Reason: reason, Entries: []gitview.NumEntry{}}
}

// handleGitStatus is POST /api/git/status {"path": abs}: the change set of the
// checkout holding path. ok/notrepo/toomany/unavailable are all 200.
func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	path := body.Path
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
	var st gitview.Status
	if body.Fresh {
		st = s.Git.StatusFresh(r.Context(), t)
	} else {
		st = s.Git.Status(r.Context(), t)
	}
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
	body, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	path := body.Path
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

	// A plain Status: right after the tree painted it is the reused result (2 s),
	// and past that it is one single-flighted run, not one per click.
	st := s.Git.Status(r.Context(), t)
	untracked := false
	tooMany := false
	switch st.State {
	case "ok":
		untracked = untrackedIn(st, rel)
	case "toomany":
		// No entries to look in, so whether the file is untracked is unknown.
		// Diff against HEAD: a tracked change shows; an empty answer could be
		// an untracked file, so it is reported as unknown, not "no changes".
		tooMany = true
	default:
		log.Printf("git diff %q: status: %q", t.Top, st.Reason)
		writeJSON(w, http.StatusOK, unavailableGitDiff(gitReason(st.Reason)))
		return
	}
	d := s.Git.Diff(r.Context(), t, rel, untracked)
	if d.State != "ok" {
		log.Printf("git diff %q: %q", t.Top, d.Reason)
		d = unavailableGitDiff(gitReason(d.Reason))
	} else if tooMany && d.Kind == "unchanged" {
		d = unavailableGitDiff(msgGitTooMany)
	} else {
		d.Reason = ""
	}
	writeJSON(w, http.StatusOK, d)
}

// handleGitBase is POST /api/git/base {"path": abs}: the branch checked out in
// the checkout holding path, the base branch picked for it and their merge
// base. Nothing in the body names a ref: the base is derived by the runner from
// the repository's refs. ok/nobase/notrepo/unavailable are all 200.
func (s *Server) handleGitBase(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	if s.Git == nil {
		writeJSON(w, http.StatusOK, unavailableGitBase(msgGitNoRuntime))
		return
	}
	t, _, err := s.gitTarget(body.Path)
	switch {
	case errors.Is(err, errGitNotRepo):
		writeJSON(w, http.StatusOK, gitview.BaseInfo{State: "notrepo"})
		return
	case isGitRefusal(err):
		writeJSON(w, http.StatusOK, unavailableGitBase(targetReason(err)))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	bi := s.Git.Base(r.Context(), t)
	switch bi.State {
	case "unavailable":
		log.Printf("git base %q: %q", t.Top, bi.Reason)
		bi.Reason = gitReason(bi.Reason)
	case "nobase":
		bi.Reason = msgGitNoBase
	default:
		bi.Reason = ""
	}
	writeJSON(w, http.StatusOK, bi)
}

// handleGitNumstat is POST /api/git/numstat {"path": abs, "scope": "head" or
// "branch"}: per-file line counts of the changes in the checkout holding path,
// against HEAD or, for "branch", against the merge base with the base branch the
// server picks (Base). Tracked files are counted by git; untracked ones are
// counted here (see withUntracked). Any other scope is a 400. ok/notrepo/toomany/
// unavailable are all 200, and "branch" with no base branch is "unavailable"
// with "no base branch".
func (s *Server) handleGitNumstat(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	var branch bool
	switch body.Scope {
	case "head":
	case "branch":
		branch = true
	default:
		writeErr(w, http.StatusBadRequest, "bad scope")
		return
	}
	if s.Git == nil {
		writeJSON(w, http.StatusOK, unavailableGitNumstat(msgGitNoRuntime))
		return
	}
	t, abs, err := s.gitTarget(body.Path)
	switch {
	case errors.Is(err, errGitNotRepo):
		writeJSON(w, http.StatusOK, gitview.Numstat{State: "notrepo", Entries: []gitview.NumEntry{}})
		return
	case isGitRefusal(err):
		writeJSON(w, http.StatusOK, unavailableGitNumstat(targetReason(err)))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	var sc gitview.Scope
	if branch {
		// The ref is the server's: Base derived it from the repository's refs.
		bi := s.Git.Base(r.Context(), t)
		switch bi.State {
		case "ok":
			sc = gitview.Scope{Branch: true, BaseRef: bi.BaseRef}
		case "nobase":
			writeJSON(w, http.StatusOK, unavailableGitNumstat(msgGitNoBase))
			return
		case "notrepo":
			writeJSON(w, http.StatusOK, gitview.Numstat{State: "notrepo", Entries: []gitview.NumEntry{}})
			return
		default:
			log.Printf("git numstat %q: base: %q", t.Top, bi.Reason)
			writeJSON(w, http.StatusOK, unavailableGitNumstat(gitReason(bi.Reason)))
			return
		}
	}
	// ns is this request's own copy (Numstat returns a value with cloned
	// entries), so RootRel and the appended entries never reach the runner's cache.
	ns := s.Git.Numstat(r.Context(), t, sc)
	ns.RootRel = rootRel(t.Top, abs)
	if ns.State == "unavailable" {
		log.Printf("git numstat %q: %q", t.Top, ns.Reason)
		ns.Reason = gitReason(ns.Reason)
	} else {
		ns.Reason = ""
	}
	if ns.Entries == nil {
		ns.Entries = []gitview.NumEntry{}
	}
	if ns.State == "ok" {
		ns.Entries = s.withUntracked(r.Context(), t, ns.Entries)
	}
	writeJSON(w, http.StatusOK, ns)
}

const (
	// maxCountFiles is how many untracked files one reply looks at.
	maxCountFiles = 200
	// maxCountBytes is the largest untracked file whose lines are counted.
	maxCountBytes = 2 << 20
	// countSniffBytes is the head of a file searched for a NUL byte.
	countSniffBytes = 8 << 10
)

// withUntracked appends a line count for each untracked file the checkout has.
// Git's diff has none (an untracked file is not in it), so they are counted
// here, from Status's untracked entries (the reused result, so usually no new
// run), as "U" entries with every line added. A collapsed untracked folder
// ("dir/") has no count, nor has a file over 2 MiB, one that is not a plain
// file or has gone, and only the first 200 files are looked at. A path numstat
// already lists is not added again, as the two runs can be seconds apart.
func (s *Server) withUntracked(ctx context.Context, t gitview.Target, entries []gitview.NumEntry) []gitview.NumEntry {
	st := s.Git.Status(ctx, t)
	if st.State != "ok" {
		return entries
	}
	p, err := s.fsPolicy()
	if err != nil {
		log.Printf("git numstat %q: fs policy: %v", t.Top, err)
		return entries
	}
	have := make(map[string]bool, len(entries))
	for _, e := range entries {
		have[e.P] = true
	}
	looked := 0
	for _, e := range st.Entries {
		if e.X != "U" || strings.HasSuffix(e.P, "/") || have[e.P] {
			continue
		}
		if looked++; looked > maxCountFiles || ctx.Err() != nil {
			break
		}
		// Status already refuses these; the interface does not promise it.
		rel := filepath.FromSlash(e.P)
		if !filepath.IsLocal(rel) {
			continue
		}
		if lines, bin, ok := p.countFile(filepath.Join(t.Top, rel)); ok {
			entries = append(entries, gitview.NumEntry{P: e.P, X: "U", A: lines, Bin: bin})
		}
	}
	return entries
}

// countFile counts the lines of the plain file at abs, an absolute path built
// from git's output, which an agent controls: it is resolved and opened by the
// same fs policy as /api/fs/read (p.resolve, openFsTarget: a boxed path only
// through its os.Root), never by a path-based open, and never executed. ok is
// false when there is no count: no such file, or one outside the policy, not a
// regular file (a FIFO is never opened for a read that could wait), over 2 MiB,
// or a link. A link is refused because git counts the link's own text, not
// the file it names, so that file's lines would be a wrong number. bin is true
// for a NUL byte in the first 8 KiB, as git sniffs.
func (p fsPolicy) countFile(abs string) (lines int, bin, ok bool) {
	t, err := p.resolve(abs)
	// A host path comes back resolved through every link on its way, so a
	// different one means a link was followed.
	if err != nil || t.Abs != abs || !isPlainFile(t) {
		return 0, false, false
	}
	f, err := openFsTarget(t, 0)
	if err != nil {
		return 0, false, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCountBytes {
		return 0, false, false
	}
	return countLines(io.LimitReader(f, maxCountBytes+1))
}

// isPlainFile reports whether t names a regular file itself, not a link. A boxed
// path is unresolved and os.Root follows a link that stays inside the
// workspace, so the last component is looked at without following it.
func isPlainFile(t fsTarget) bool {
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		return false
	}
	defer root.Close()
	fi, err := root.Lstat(t.Rel)
	return err == nil && fi.Mode().IsRegular()
}

// countLines counts the lines of r as git does: each newline, and one more for
// a last line with none. ok is false when r holds more than maxCountBytes (the
// caller limits it to one byte over), or cannot be read.
func countLines(r io.Reader) (lines int, bin, ok bool) {
	buf := make([]byte, 32<<10)
	total := 0
	var last byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if total < countSniffBytes && bytes.IndexByte(buf[:min(n, countSniffBytes-total)], 0) >= 0 {
				return 0, true, true
			}
			total += n
			if total > maxCountBytes {
				return 0, false, false
			}
			lines += bytes.Count(buf[:n], []byte{'\n'})
			last = buf[n-1]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, false, false
		}
	}
	if total > 0 && last != '\n' {
		lines++
	}
	return lines, false, true
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
