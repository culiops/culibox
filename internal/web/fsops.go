package web

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// File mutations: create a directory, rename, delete. Read-only browsing lives
// in fsapi.go, transfer in fsupload.go / fsarchive.go.

// maxOpBody bounds the three JSON mutation bodies. None is more than a few
// hundred bytes; the upload path already established that an unbounded request
// body is not acceptable on this surface.
const maxOpBody = 8 << 10

// Bounds for the delete measurement. Generous — they only decide when the
// dialog says "over N files" instead of a number, exactly as the archive probe
// already does when it short-circuits.
const (
	deleteMeasureMaxBytes = 1 << 40
	deleteMeasureMaxFiles = 100000
)

// checkMutationGuard applies the method + CSRF checks every mutation endpoint
// shares (decodeOpBody's small-body mutations and fswrite.go's write, which
// needs a much larger body cap and so cannot share decodeOpBody's
// MaxBytesReader call). It writes the response itself on failure and reports
// whether to continue.
//
// sameOrigin is checked here even though csrfGuard already covers the mux: the
// same belt-and-braces handleFsUpload uses, so a future routing mistake
// downgrades the guard instead of removing it.
func checkMutationGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	// Bearer credentials are never auto-attached by a browser, so they carry no
	// CSRF risk and skip the same-origin check (mirrors csrfGuard). Cookie/none
	// still require same-origin.
	if credKindOf(r) != credBearer && !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

// decodeOpBody applies checkMutationGuard, then bounds and decodes the body for
// the small JSON mutations (mkdir/rename/delete). Write's own decode lives in
// fswrite.go's decodeWriteBody, which needs a much larger body cap.
func decodeOpBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !checkMutationGuard(w, r) {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOpBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

// resolveParent is THE rule for every mutation: resolve the PARENT directory and
// keep the base name UNRESOLVED.
//
// resolveFs follows links in the whole path, so using it on a mutation
// target hands back the symlink's TARGET — a delete would destroy a tree the
// user never pointed at and leave the link behind, and a rename would rename the
// target and leave the link dangling. It also makes a link pointing outside home
// undeletable (403) and a dangling one undeletable (404), while /api/fs/list
// shows both.
//
// The returned root must be closed by the caller.
func (s *Server) resolveParent(abs string) (*os.Root, string, string, fsPolicy, error) {
	if abs == "" || !filepath.IsAbs(abs) {
		return nil, "", "", fsPolicy{}, errBadPath
	}
	clean := filepath.Clean(abs)
	base := filepath.Base(clean)
	// safeName is deliberately NOT used here: a file created outside the console
	// may legally carry a name it rejects, and refusing to delete it would be a
	// trap. Only the structural cases are refused.
	if base == "." || base == ".." || base == string(os.PathSeparator) {
		return nil, "", "", fsPolicy{}, errBadPath
	}
	// One snapshot serves the parent resolution and, through the returned policy,
	// the caller's guardsTree check on the target.
	pol, err := s.fsPolicy()
	if err != nil {
		return nil, "", "", fsPolicy{}, err
	}
	_, root, relParent, err := pol.openParent(filepath.Dir(clean))
	if err != nil {
		return nil, "", "", fsPolicy{}, err
	}
	return root, relParent, base, pol, nil
}

// openParent resolves an existing directory through resolveFs and opens the root
// it lives under, returning the directory's absolute path and its root-relative
// path. resolveParent calls it after splitting a target; mkdir and upload call it
// directly because their parent arrives as its own field. All of them go through
// the same containment sequence, so a change to it cannot reach one handler and
// miss another.
//
// For a host path the root is $HOME and the directory is already fully resolved.
// For a path inside a boxed workspace the root is the workspace and the directory
// is NOT resolved: the Stat below, like every later operation, goes through the
// os.Root, which refuses a box-planted link that leaves the workspace — so a
// mutation can never be steered into the host tree.
func (s *Server) openParent(dir string) (string, *os.Root, string, error) {
	p, err := s.fsPolicy()
	if err != nil {
		return "", nil, "", err
	}
	return p.openParent(dir)
}

func (p fsPolicy) openParent(dir string) (string, *os.Root, string, error) {
	t, err := p.resolve(dir)
	if err != nil {
		return "", nil, "", err
	}
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		return "", nil, "", err
	}
	fi, err := root.Stat(t.Rel)
	if err != nil {
		root.Close()
		switch {
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			return "", nil, "", os.ErrNotExist
		default:
			// A link that leaves the root, a permission, a swap: the caller
			// learns only "forbidden", as writeOpenErr does for reads.
			return "", nil, "", errOutsideHome
		}
	}
	if !fi.IsDir() {
		root.Close()
		return "", nil, "", errNotDir
	}
	return t.Abs, root, t.Rel, nil
}

// relOf joins a resolved parent with a base name for use against the root.
// filepath.Join cleans away the "." a top-level entry's parent produces.
func relOf(relParent, base string) string { return filepath.Join(relParent, base) }

// typedName validates a name a HUMAN typed, which is not the same problem as a
// filename a browser sent. safeName STRIPS directories — correct for an upload,
// where a Windows browser legitimately sends "C:\dir\file.txt" — but here it
// would silently turn a typed "sub/evil" into "evil" and put the folder
// somewhere the user did not ask for. Refuse instead, then reuse safeName for
// the character and length rules.
func typedName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || n == "." || n == ".." ||
		strings.ContainsRune(n, '/') || strings.ContainsRune(n, '\\') {
		return "", errBadName
	}
	return safeName(n)
}

type mkdirReq struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

// handleFsMkdir serves POST /api/fs/mkdir — a new directory inside an existing
// one. root.Mkdir fails when the name exists in any form, symlink included, so
// there is no check-then-create window.
func (s *Server) handleFsMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirReq
	if !decodeOpBody(w, r, &req) {
		return
	}
	name, err := typedName(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad name")
		return
	}
	dir, root, relParent, err := s.openParent(req.Dir)
	if err != nil {
		writeFsErr(w, err)
		return
	}
	defer root.Close()
	if err := root.Mkdir(relOf(relParent, name), 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeErr(w, http.StatusConflict, "name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "cannot create directory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": filepath.Join(dir, name)})
}

type renameReq struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	ConfirmName string `json:"confirm_name"`
}

// handleFsRename serves POST /api/fs/rename — a new base name in the SAME
// directory. typedName is what keeps it in place: os.Root would rename across
// directories happily, so "sub/evil" must never reach it.
func (s *Server) handleFsRename(w http.ResponseWriter, r *http.Request) {
	var req renameReq
	if !decodeOpBody(w, r, &req) {
		return
	}
	name, err := typedName(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad name")
		return
	}
	root, relParent, base, pol, err := s.resolveParent(req.Path)
	if err != nil {
		writeFsErr(w, err)
		return
	}
	defer root.Close()
	fi, err := root.Lstat(relOf(relParent, base))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if pol.guardsTree(fi) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	// The same gate delete uses. Without it the two entries in one ⋯ menu compose
	// into a bypass: rename .ssh to anything, then delete it with no typed name.
	// Renaming ~/.ssh or a .git is independently destructive anyway.
	if protectedDirs[base] && req.ConfirmName != base {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        "this folder needs its name typed to confirm",
			"confirm_name": base,
		})
		return
	}
	newPath := filepath.Join(filepath.Dir(filepath.Clean(req.Path)), name)
	if name == base {
		// A no-op is not a conflict: the user typed the name it already has.
		writeJSON(w, http.StatusOK, map[string]string{"path": newPath})
		return
	}
	// Never overwrite. root.Rename already refuses when the destination is a
	// directory (it lstats and returns EEXIST); this closes the file-onto-file
	// and file-onto-symlink cases, up to the microseconds between this Lstat and
	// the Rename — the race the spec accepts.
	if _, err := root.Lstat(relOf(relParent, name)); err == nil {
		writeErr(w, http.StatusConflict, "name already exists")
		return
	}
	if err := root.Rename(relOf(relParent, base), relOf(relParent, name)); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeErr(w, http.StatusConflict, "name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "cannot rename")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": newPath})
}

// protectedDirs are directories whose loss is unrecoverable or catastrophic, so
// deleting one asks the user to TYPE its name instead of clicking OK.
//
// This is NOT a security boundary — the console spawns shells, so anyone who can
// reach it can rm -rf anyway — and it deliberately mirrors skipDirs in
// fssearch.go: it stops an accident, not an attacker. .git earns its place
// because several of this user's repositories have no remote at all, so deleting
// one destroys the only copy of their history.
var protectedDirs = map[string]bool{
	".ssh": true, ".gnupg": true, ".aws": true, ".kube": true,
	".git": true, ".claude": true,
}

type deleteReq struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	// Entries is the entry count the client showed the user. The recursive
	// delete re-measures and refuses if the tree has grown well past it.
	Entries     int    `json:"entries"`
	ConfirmName string `json:"confirm_name"`
}

// deleteMeasure is what a delete needs to know about a directory before anyone
// consents to losing it.
//
// Entries counts EVERYTHING RemoveAll would destroy — directories, symlinks,
// sockets, fifos — not just regular files. measureTree answers a different
// question ("what ends up in the tarball"), and borrowing it here made a tree of
// 500 symlinks and 2 subdirectories report "0 files, 0 bytes", so its
// confirmation dialog was byte-identical to an empty folder's.
type deleteMeasure struct {
	Entries int
	Files   int
	Bytes   int64
	Capped  string // "" | "entries" | "bytes" — which bound stopped the walk
}

// measureForDelete walks INSIDE the os.Root, so a link repointed after resolution
// cannot move the measurement outside the root, as walking the caller's absolute
// path would. The numbers are still an estimate: a box racing the delete can swap
// a directory between this walk and the removal when the root is $HOME. They only
// feed the confirmation dialog; RemoveAll, which works on file descriptors and
// resolves nothing again, is what bounds the damage.
//
// fs.WalkDir takes entry types from the dirent, so it never descends a symlink
// and a loop cannot hang it.
func measureForDelete(root *os.Root, rel string) deleteMeasure {
	var m deleteMeasure
	fs.WalkDir(root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if p == rel {
			return nil // the directory itself is not one of its contents
		}
		m.Entries++
		if d.Type().IsRegular() {
			m.Files++
			if info, err := d.Info(); err == nil {
				m.Bytes += info.Size()
			}
		}
		if m.Entries >= deleteMeasureMaxFiles {
			m.Capped = "entries"
			return errStopMeasure
		}
		if m.Bytes >= deleteMeasureMaxBytes {
			m.Capped = "bytes"
			return errStopMeasure
		}
		return nil
	})
	return m
}

var errStopMeasure = errors.New("measurement bound reached")

// body renders a measurement for the client, always naming the entry count so a
// non-empty directory can never present itself as an empty one.
func (m deleteMeasure) body(extra map[string]any) map[string]any {
	out := map[string]any{
		"entries": m.Entries, "files": m.Files, "bytes": m.Bytes, "capped": m.Capped,
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// handleFsDelete serves POST /api/fs/delete. A file, a symlink (the link, never
// its target) or an empty directory goes immediately; a directory with contents
// is refused with its measured size so the client can say what would be lost.
//
// The two-step gate is a CLIENT affordance: this handler honours recursive on a
// first request. It grants no capability a spawned shell lacks.
func (s *Server) handleFsDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteReq
	if !decodeOpBody(w, r, &req) {
		return
	}
	root, relParent, base, pol, err := s.resolveParent(req.Path)
	if err != nil {
		writeFsErr(w, err)
		return
	}
	defer root.Close()
	rel := relOf(relParent, base)
	info, err := root.Lstat(rel)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if pol.guardsTree(info) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	// A symlink is removed as a link whatever it points at, so it must never take
	// the directory path below — that is the difference between deleting a
	// shortcut and deleting the tree it points at.
	if !info.IsDir() {
		if err := root.Remove(rel); err != nil {
			writeErr(w, http.StatusInternalServerError, "cannot delete")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Measure BEFORE any gate, so every refusal can say what is at stake. The
	// protected-name gate used to answer first and shadow the counts, which left
	// the directories that matter most — .git, .ssh — as the only ones deleted
	// with no idea of what was inside.
	m := measureForDelete(root, rel)
	if protectedDirs[base] && req.ConfirmName != base {
		writeJSON(w, http.StatusConflict, m.body(map[string]any{
			"error":        "this folder needs its name typed to confirm",
			"confirm_name": base,
		}))
		return
	}
	if m.Entries == 0 {
		if err := root.Remove(rel); err != nil {
			writeDeleteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !req.Recursive {
		writeJSON(w, http.StatusConflict, m.body(map[string]any{"error": "directory not empty"}))
		return
	}
	// Consent was given against numbers a human read seconds or minutes ago, on a
	// box whose whole purpose is running agents that write files. If the tree has
	// grown materially since, ask again with the new numbers. Compared by
	// division so a hostile or absurd Entries cannot overflow into "always grew".
	if req.Entries > 0 && m.Entries/2 > req.Entries {
		writeJSON(w, http.StatusConflict, m.body(map[string]any{
			"error": "the folder grew since you confirmed",
		}))
		return
	}
	if err := root.RemoveAll(rel); err != nil {
		writeDeleteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeDeleteErr keeps a refusal honest about its cause: a permission problem is
// the user's to fix and must not be reported as a server fault.
func writeDeleteErr(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrPermission) {
		writeErr(w, http.StatusForbidden, "permission denied")
		return
	}
	writeErr(w, http.StatusInternalServerError, "cannot delete")
}
