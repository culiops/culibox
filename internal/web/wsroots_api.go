package web

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/gitrepo"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/wsroots"
)

const (
	rootsBrowserOnlyMsg = "changing workspace roots needs this page's signed-in browser session"
	rootsFailedMsg      = "Could not change the workspace roots. The full error is in the CuliBox log."
	rootsCorruptMsg     = "The workspace roots list is damaged, so it cannot be changed. Fix or delete workspace-roots.json in CuliBox's data folder."
	rootsKind           = "workspace_root"
)

// RootsAudit adapts a credential-audit recorder to the AuditFunc the
// workspace-roots service calls before every save: one row per added or
// removed root, the root's path as the name.
func RootsAudit(rec CredRecorder) wsroots.AuditFunc {
	return func(action, path, actor string) error {
		return rec.RecordCred(broker.CredEvent{Action: action, Kind: rootsKind, Name: path, Actor: actor})
	}
}

// rootRow is one entry of GET /api/workspace-roots.
type rootRow struct {
	Path      string     `json:"path"`
	Added     *time.Time `json:"added,omitempty"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
	Available bool       `json:"available"`
	Always    bool       `json:"always,omitempty"`
}

// rootsView is the reply of GET (and of a successful POST or DELETE): home
// first, then each added root.
func (s *Server) rootsView() map[string]any {
	// Home is shown as the resolved spelling Covering and the check use, so the
	// client sees one spelling of it.
	rows := []rootRow{{Path: resolvedOrClean(s.Home), Always: true, Available: isDir(s.Home)}}
	list, err := s.Roots.List()
	for _, r := range list {
		row := rootRow{Path: r.Path, Available: s.rootAvailable(r.Path)}
		if !r.Added.IsZero() {
			a := r.Added
			row.Added = &a
		}
		if !r.LastUsed.IsZero() {
			l := r.LastUsed
			row.LastUsed = &l
		}
		rows = append(rows, row)
	}
	return map[string]any{
		"roots": rows,
		// The project folders outside home and every root; the scan is a later slice.
		"elsewhere": []any{},
		"corrupt":   err != nil,
	}
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// rootAvailable is true when the file API would serve this root now: still a
// directory, not redirected through a symlink since it was added, and not a
// forbidden folder, CuliBox's own data folder included (a hand-edited file can name one). It mirrors the fs
// policy's drop rules, so "not available" is what the Files view shows.
func (s *Server) rootAvailable(path string) bool {
	if !isDir(path) {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != filepath.Clean(path) {
		return false
	}
	return wsroots.Refusal(real, wsroots.Env{Home: resolvedOrClean(s.Home), DataDir: resolvedOrClean(s.DataDir)}) == ""
}

func resolvedOrClean(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// writeRootsErr maps a root-service error to the coded reply the page acts on.
func writeRootsErr(w http.ResponseWriter, err error) {
	var ref *wsroots.RefusedError
	switch {
	case errors.As(err, &ref):
		writeErrCode(w, http.StatusBadRequest, ref.Reason, "refused")
	case errors.Is(err, wsroots.ErrCovered):
		writeErrCode(w, http.StatusConflict, err.Error(), "covered")
	case errors.Is(err, wsroots.ErrCap):
		writeErrCode(w, http.StatusConflict, err.Error(), "cap")
	case errors.Is(err, wsroots.ErrCorrupt):
		log.Printf("workspace roots: %v", err)
		writeErrCode(w, http.StatusConflict, rootsCorruptMsg, "list_corrupt")
	case errors.Is(err, wsroots.ErrNotDir):
		writeErrCode(w, http.StatusBadRequest, err.Error(), "not_dir")
	case errors.Is(err, wsroots.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, err.Error(), "not_found")
	default:
		// Audit and OS errors carry data-dir paths: the log has them, the page
		// gets fixed copy.
		log.Printf("workspace roots: %v", err)
		writeErr(w, http.StatusInternalServerError, rootsFailedMsg)
	}
}

// handleWorkspaceRoots serves /api/workspace-roots (GET list, POST add, DELETE
// remove). Writes are browser-only: a token holder, and so any agent, must not
// widen what the web UI can open.
func (s *Server) handleWorkspaceRoots(w http.ResponseWriter, r *http.Request) {
	if s.Roots == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && !browserOnly(w, r, rootsBrowserOnlyMsg) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.rootsView())
	case http.MethodPost, http.MethodDelete:
		var req struct {
			Path string `json:"path"`
		}
		if !decodeVMBody(w, r, &req) {
			return
		}
		var err error
		if r.Method == http.MethodPost {
			_, err = s.Roots.Add(req.Path, storeActor)
		} else {
			err = s.Roots.Remove(req.Path, storeActor)
		}
		if err != nil {
			writeRootsErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.rootsView())
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleWorkspaceCheck serves GET /api/workspace/check?path=: what starting a
// session in one path would do to the workspace roots. It is an existence check
// on a single path and never lists contents.
func (s *Server) handleWorkspaceCheck(w http.ResponseWriter, r *http.Request) {
	if s.Roots == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	in := r.URL.Query().Get("path")
	if in == "" || strings.ContainsRune(in, 0) || !filepath.IsAbs(in) {
		writeErr(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	path := gitrepo.Normalize(in)
	fi, statErr := os.Stat(path)
	exists := statErr == nil
	isDir := exists && fi.IsDir()

	root := s.Roots.Covering(path)
	addRoot := path
	if isDir {
		if top := gitrepo.WorkspaceRoot(path); top != "" {
			addRoot = top
		}
	}
	// A covered path is already reachable: the refusal line applies only to a
	// folder that would have to become a root.
	refused := ""
	if root == "" {
		refused = wsroots.Refusal(addRoot, wsroots.Env{Home: resolvedOrClean(s.Home), DataDir: resolvedOrClean(s.DataDir)})
	}
	reason, code, bad := unboxableCwdCode(path, s.Home, s.Protected, s.Shares)
	if bad && code == "" {
		code = "unboxable"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":          path,
		"exists":        exists,
		"is_dir":        isDir,
		"root":          root,
		"would_add":     isDir && root == "" && refused == "",
		"add_root_path": addRoot,
		"refused":       refused,
		"box":           map[string]any{"ok": !bad, "reason": reason, "code": code},
		"vm_shared":     s.vmShared(addRoot),
	})
}

// vmShared reports whether the box VM can see p: always true without a VM
// (Linux), else p must lie inside a folder the running VM shares.
func (s *Server) vmShared(p string) bool {
	if s.Shares == nil {
		return true
	}
	v, err := s.Shares()
	if err != nil {
		return false
	}
	return v.Check(p, false) == nil
}

// rootAdd is the "root" member of the 201 body of POST /api/sessions: whether
// the Start made its folder a workspace root, and why not when it did not.
type rootAdd struct {
	Added  bool   `json:"added"`
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

// rootAfterSpawn is the add_root step of a successful Start (#343 spec 6.3). Only
// for the signed-in browser (a cookie and a strict same-origin request; a bearer
// token or an agent never widens what the page reaches) it adds the session's repo
// top, or its folder when it is in no repo. It then notes the use of the covering
// root, after the add so a root just added counts. A clone-mode spawn is skipped: its folder is the box's own
// copy (R3). Nil means nothing to report: no service, not asked, a folder home
// or a root already reaches, or a clone.
func (a *API) rootAfterSpawn(r *http.Request, info session.Info, addRoot, cloneAsked bool) *rootAdd {
	if a.Roots == nil {
		return nil
	}
	res := a.addRootAfterSpawn(r, info, addRoot, cloneAsked)
	// After the add, so a root this Start just added is also this Start's use.
	a.Roots.Touch(info.Cwd)
	return res
}

func (a *API) addRootAfterSpawn(r *http.Request, info session.Info, addRoot, cloneAsked bool) *rootAdd {
	if !addRoot || credKindOf(r) != credCookie || !strictSameOrigin(r) || cloneAsked || info.CloneOf != "" {
		return nil
	}
	target := info.WorkspaceRoot
	if target == "" {
		target = info.Cwd
	}
	if target == "" || !filepath.IsAbs(target) {
		return nil
	}
	// The folder itself decides "already reachable": a repo top above a root
	// must not widen the list to the top.
	if a.Roots.Covering(info.Cwd) != "" || a.Roots.Covering(target) != "" {
		return nil
	}
	root, err := a.Roots.Add(target, storeActor)
	if err == nil {
		return &rootAdd{Added: true, Path: root.Path}
	}
	var refused *wsroots.RefusedError
	switch {
	case errors.As(err, &refused):
		return &rootAdd{Path: target, Reason: refused.Reason}
	case errors.Is(err, wsroots.ErrCovered):
		return nil
	case errors.Is(err, wsroots.ErrCap):
		return &rootAdd{Path: target, Reason: "the " + strconv.Itoa(wsroots.MaxRoots) + "-root limit is reached"}
	case errors.Is(err, wsroots.ErrCorrupt):
		return &rootAdd{Path: target, Reason: wsroots.FileName + " is damaged"}
	case errors.Is(err, wsroots.ErrNotDir):
		return &rootAdd{Path: target, Reason: "that folder is not a directory"}
	}
	// Anything else came from the audit callback. The raw error stays in the
	// log; the body gets a fixed phrase.
	log.Printf("web: workspace root %q not added after a start: %v", target, err)
	return &rootAdd{Path: target, Reason: "the audit log could not record it"}
}
