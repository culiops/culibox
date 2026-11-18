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
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/gitrepo"
	"github.com/culiops/culibox/internal/gitview"
)

// GitView runs git status, diff, base lookup, line counts and file-at-revision
// reads for the file tree and the editor (gitview.Runner). Nil on the Server
// means no box runtime: every endpoint answers "unavailable". Base takes no
// ref: the runner picks the base branch from the repository's own refs, so a
// request can never name one. The Scope.BaseRef a handler gives Numstat and the
// rev it gives Show are the ones Base returned (or HEAD). The Fresh
// variants (StatusFresh, NumstatFresh) are for a request that has just changed
// something on disk.
type GitView interface {
	Status(context.Context, gitview.Target) gitview.Status
	StatusFresh(context.Context, gitview.Target) gitview.Status
	Diff(context.Context, gitview.Target, string, bool) gitview.Diff
	Base(context.Context, gitview.Target) gitview.BaseInfo
	Numstat(context.Context, gitview.Target, gitview.Scope) gitview.Numstat
	NumstatFresh(context.Context, gitview.Target, gitview.Scope) gitview.Numstat
	Show(ctx context.Context, t gitview.Target, rev, rel string) gitview.Blob
	ShowRaw(ctx context.Context, t gitview.Target, rev, rel string) ([]byte, gitview.Blob)
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
	msgGitNotFile      = "not a file"
)

var (
	errGitOutside = errors.New("git: repository outside the viewable tree")
	errGitNotRepo = errors.New("git: not a repository")
	errGitUnsafe  = errors.New("git: path cannot be mounted")
	errGitNotFile = errors.New("git: not a regular file")
)

// gitBody is the body of every git endpoint.
type gitBody struct {
	Path string `json:"path"`
	// Fresh (status and numstat) asks for a result read after this request
	// arrived, not a reused or already-running one: a client sets it when it has
	// just changed something on disk.
	Fresh bool `json:"fresh,omitempty"`
	// Scope (numstat and show) is "head" or "branch", nothing else: the base
	// branch of "branch" is derived by the server, no client field can name a ref.
	Scope string `json:"scope,omitempty"`
	// Raw (show only) asks for the bytes of an image, not the JSON text.
	Raw bool `json:"raw,omitempty"`
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
// take as a --mount value is errGitUnsafe. A path that is gone is
// os.ErrNotExist.
func (s *Server) gitTarget(path string) (t gitview.Target, abs string, err error) {
	return s.gitTargetFor(path, false)
}

// gitTargetShow is gitTarget for POST /api/git/show only: it also accepts a
// vanished leaf, a file deleted from the working tree, whose base text the
// Changes view shows as all removed. The leaf is accepted only when its own
// Lstat says it does not exist (a dangling link is not that) and its PARENT
// directory passes every check an existing path does: the fs policy, a
// link-free EvalSymlinks result, inGitTree. The returned abs is that resolved
// parent plus the leaf's single local name. Nothing reads or stats the leaf
// itself afterwards (see gitShowRel); git only reads an object of the
// repository by that name. Every other outcome is gitTarget's.
func (s *Server) gitTargetShow(path string) (t gitview.Target, abs string, err error) {
	return s.gitTargetFor(path, true)
}

// gitTargetFor is the one body of gitTarget and gitTargetShow; vanishedOK is the latter's.
func (s *Server) gitTargetFor(path string, vanishedOK bool) (t gitview.Target, abs string, err error) {
	p, err := s.fsPolicy()
	if err != nil {
		return gitview.Target{}, "", err
	}
	req, err := p.resolve(path)
	if err == nil {
		abs, err = filepath.EvalSymlinks(req.Abs)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return gitview.Target{}, "", errGitOutside
		}
	}
	// gone: the request names nothing that exists. The host walk says so in
	// resolve, a boxed one (whose resolve stops at the contained root) in
	// EvalSymlinks.
	gone := errors.Is(err, os.ErrNotExist)
	if err != nil && !(gone && vanishedOK) {
		if gone {
			err = os.ErrNotExist
		}
		return gitview.Target{}, "", err
	}
	// checked is the path inGitTree vouches for and Repo discovery starts from:
	// abs, or for a vanished leaf its parent (EvalSymlinks of a leaf that is not
	// there is impossible, and the leaf is a local name below that parent).
	checked := abs
	if gone {
		if req, checked, abs, err = vanishedLeaf(p, path); err != nil {
			return gitview.Target{}, "", err
		}
	}
	if !p.inGitTree(req, checked) {
		return gitview.Target{}, "", errGitOutside
	}
	dir := checked
	if !gone {
		fi, err := os.Lstat(abs)
		if err != nil {
			return gitview.Target{}, "", errGitOutside
		}
		if !fi.IsDir() {
			dir = filepath.Dir(abs)
		}
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

// vanishedLeaf resolves path, whose resolution failed with "does not exist",
// as a leaf under an existing directory: it returns the fs target of the PARENT
// (what inGitTree must compare against), the resolved parent, and abs, that
// parent plus the leaf's name. Refusals: os.ErrNotExist for everything gitTarget
// already refuses as not there (a missing or non-directory parent, a leaf that
// does exist, as a dangling link does, a name that is not one local name),
// errGitOutside for a parent EvalSymlinks sends elsewhere, and the fs policy's
// own error for a parent it refuses (a denied tree, outside $HOME).
func vanishedLeaf(p fsPolicy, path string) (preq fsTarget, parent, abs string, err error) {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if base == "." || base == ".." || !filepath.IsLocal(base) {
		return fsTarget{}, "", "", os.ErrNotExist
	}
	if preq, err = p.resolve(filepath.Dir(clean)); err != nil {
		return fsTarget{}, "", "", err
	}
	if parent, err = filepath.EvalSymlinks(preq.Abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fsTarget{}, "", "", os.ErrNotExist
		}
		return fsTarget{}, "", "", errGitOutside
	}
	if fi, err := os.Lstat(parent); err != nil {
		return fsTarget{}, "", "", errGitOutside
	} else if !fi.IsDir() {
		return fsTarget{}, "", "", os.ErrNotExist
	}
	abs = filepath.Join(parent, base)
	switch _, err := os.Lstat(abs); {
	case err == nil:
		// Something is there (a dangling link): not a vanished file.
		return fsTarget{}, "", "", os.ErrNotExist
	case !errors.Is(err, os.ErrNotExist):
		return fsTarget{}, "", "", errGitOutside
	}
	return preq, parent, abs, nil
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

// gitFileRel is the slash-form path of abs, the file a request named, relative
// to t.Top: the path git is asked about. It is the one check of what lies inside
// the repository for every endpoint that takes a single file (diff, show), so a
// fix to it reaches them all. abs must be gitTarget's resolved path for t. A
// path that is gone gives the Lstat error (the caller maps it with writeFsErr);
// one that is not a regular file (a directory, a link, a device) gives
// errGitNotFile (so the checkout directory itself is that, not an "outside");
// a regular file that is not strictly below t.Top gives errGitOutside.
func gitFileRel(t gitview.Target, abs string) (string, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errGitNotFile
	}
	rel, err := filepath.Rel(t.Top, abs)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errGitOutside
	}
	return filepath.ToSlash(rel), nil
}

// gitShowRel is gitFileRel for show, which also takes a file deleted from the
// working tree (gitTargetShow): when abs is not there, its path below t.Top is
// the answer without a stat of the leaf. A leaf that has appeared since is
// checked as any file is.
func gitShowRel(t gitview.Target, abs string) (string, error) {
	if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
		return gitFileRel(t, abs)
	}
	rel, err := filepath.Rel(t.Top, abs)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errGitOutside
	}
	return filepath.ToSlash(rel), nil
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

func unavailableGitBlob(reason string) gitview.Blob {
	return gitview.Blob{State: "unavailable", Reason: reason}
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
	rel, err := gitFileRel(t, abs)
	switch {
	case errors.Is(err, errGitNotFile):
		writeErr(w, http.StatusBadRequest, msgGitNotFile)
		return
	case errors.Is(err, errGitOutside):
		writeJSON(w, http.StatusOK, unavailableGitDiff(msgGitOutside))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}

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

// handleGitShow is POST /api/git/show {"path": abs file, "scope": "head" or
// "branch", "raw": bool}: the file as it is in HEAD or, for "branch", at the
// merge base with the base branch the server picks (Base), for the editor's
// change gutter and inline diff. The rev is the server's: no body field names
// one, and any other scope is a 400. ok/notrepo/unavailable are all 200, and
// "branch" with no base branch is "unavailable" with "no base branch". A path
// the revision does not have (a new file) is ok and "missing".
//
// raw serves the blob's bytes instead, for an image diff: only an image
// extension (the one of the file git reads) is allowed, anything else is a 400
// before git runs. The success reply is the image with the headers the file
// preview has (nosniff, CSP sandbox, the type of the extension); a blob over
// 10 MiB is a 413, since a cut image is no image. Every other outcome of a raw
// request (missing, unavailable, not a repo) is the JSON reply of the text
// form, so a client tells them apart by Content-Type.
func (s *Server) handleGitShow(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusOK, unavailableGitBlob(msgGitNoRuntime))
		return
	}
	t, abs, err := s.gitTargetShow(body.Path)
	switch {
	case errors.Is(err, errGitNotRepo):
		writeJSON(w, http.StatusOK, gitview.Blob{State: "notrepo"})
		return
	case isGitRefusal(err):
		writeJSON(w, http.StatusOK, unavailableGitBlob(targetReason(err)))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	rel, err := gitShowRel(t, abs)
	switch {
	case errors.Is(err, errGitNotFile):
		writeErr(w, http.StatusBadRequest, msgGitNotFile)
		return
	case errors.Is(err, errGitOutside):
		writeJSON(w, http.StatusOK, unavailableGitBlob(msgGitOutside))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	var ctype string
	if body.Raw {
		if ctype, ok = imageExt[strings.ToLower(filepath.Ext(abs))]; !ok {
			writeErr(w, http.StatusBadRequest, "not an image")
			return
		}
	}

	rev := "HEAD"
	if branch {
		// The rev is the server's: Base derived it from the repository's refs.
		bi := s.Git.Base(r.Context(), t)
		switch bi.State {
		case "ok":
			rev = bi.MergeBase
		case "nobase":
			writeJSON(w, http.StatusOK, unavailableGitBlob(msgGitNoBase))
			return
		case "notrepo":
			writeJSON(w, http.StatusOK, gitview.Blob{State: "notrepo"})
			return
		default:
			log.Printf("git show %q: base: %q", t.Top, bi.Reason)
			writeJSON(w, http.StatusOK, unavailableGitBlob(gitReason(bi.Reason)))
			return
		}
	}
	var data []byte
	var b gitview.Blob
	if body.Raw {
		data, b = s.Git.ShowRaw(r.Context(), t, rev, rel)
	} else {
		b = s.Git.Show(r.Context(), t, rev, rel)
	}
	if b.State == "unavailable" {
		log.Printf("git show %q: %q", t.Top, b.Reason)
		b = unavailableGitBlob(gitReason(b.Reason))
	} else {
		b.Reason = ""
	}
	if body.Raw && b.State == "ok" && !b.Missing {
		if b.Truncated {
			writeErr(w, http.StatusRequestEntityTooLarge, "file too large to preview")
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Cache-Control", "no-cache")
		// As for the file preview (serveFsFile): no MIME sniffing, and the
		// response is sandboxed so an SVG opened on its own cannot run script.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Write(data)
		return
	}
	writeJSON(w, http.StatusOK, b)
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
// "branch", "fresh": bool}: per-file line counts of the changes in the checkout
// holding path, against HEAD or, for "branch", against the merge base with the
// base branch the server picks (Base). Tracked files are counted by git;
// untracked ones are counted here (see withUntracked). Any other scope is a 400.
// ok/notrepo/toomany/unavailable are all 200, and "branch" with no base branch
// is "unavailable" with "no base branch". Fresh makes only the numstat run fresh
// (see Runner.NumstatFresh): the status the untracked files come from is the
// reused one, as the client refreshes status on its own.
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
	// The reused status is read alongside the diff, not after it: a diff that
	// takes longer than Status's reuse window would otherwise make the read after
	// it a second cold container run, and one request would wait for two. The
	// buffer lets the goroutine finish unobserved when the numstat is not ok.
	stc := make(chan gitview.Status, 1)
	go func() { stc <- s.Git.Status(r.Context(), t) }()
	// ns is this request's own copy (Numstat returns a value with cloned
	// entries), so RootRel and the appended entries never reach the runner's cache.
	var ns gitview.Numstat
	if body.Fresh {
		ns = s.Git.NumstatFresh(r.Context(), t, sc)
	} else {
		ns = s.Git.Numstat(r.Context(), t, sc)
	}
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
		ns.Entries = s.withUntracked(r.Context(), t, ns.Entries, <-stc)
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
// here, from the untracked entries of st (the reused status), as "U" entries
// with every line added. A collapsed untracked folder
// ("dir/") has no count, nor has a file over 2 MiB, one that is not a plain
// file or has gone, and only the first 200 files are looked at. A path numstat
// already lists is not added again, as the two runs can be seconds apart.
func (s *Server) withUntracked(ctx context.Context, t gitview.Target, entries []gitview.NumEntry, st gitview.Status) []gitview.NumEntry {
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
