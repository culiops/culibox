package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

var (
	errBadPath     = errors.New("bad path")
	errOutsideHome = errors.New("outside home")
	errDenied      = errors.New("denied tree") // agent-box's own data dir / box state
	errReadOnly    = errors.New("read only")   // a mutation under a scratch root
	errLinkEscape  = errors.New("link escapes the workspace")
	errBlocked     = errors.New("blocked")
	errPermWrite   = errors.New("no permission (mutation)")
)

// imageExt maps a lowercase extension to the MIME type served raw for <img>.
var imageExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
}

// isBinary reports whether head (the first bytes of a file) looks binary, using
// the git heuristic: a NUL byte in the sniff window means binary. UTF-8 validity
// is deliberately NOT checked (a head truncated mid-rune would false-positive a
// valid text file).
func isBinary(head []byte) bool {
	return bytes.IndexByte(head, 0) >= 0
}

type fsEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// writeFsErr maps a resolveFs (or openParent) sentinel error to an HTTP status.
func writeFsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadPath):
		writeErr(w, http.StatusBadRequest, "bad path")
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, errNotDir):
		writeErr(w, http.StatusBadRequest, "not a directory")
	case errors.Is(err, errOutsideHome):
		writeErr(w, http.StatusForbidden, msgOutside)
	case errors.Is(err, errDenied):
		writeErr(w, http.StatusForbidden, msgDenied)
	case errors.Is(err, errReadOnly):
		writeErr(w, http.StatusForbidden, msgReadOnly)
	case errors.Is(err, errLinkEscape):
		writeErr(w, http.StatusForbidden, msgEscape)
	case errors.Is(err, errBlocked):
		writeErr(w, http.StatusForbidden, msgBlocked)
	case errors.Is(err, errPermWrite):
		writeErr(w, http.StatusForbidden, msgPermWrite)
	case errors.Is(err, os.ErrPermission):
		// A root-owned directory mid-path (Docker leaves them in scratch
		// folders) fails the resolver's Lstat: a refusal, never a 500.
		writeErr(w, http.StatusForbidden, msgPerm)
	default:
		writeErr(w, http.StatusInternalServerError, "error")
	}
}

// writeOpenErr maps an error from opening t through its os.Root. A missing
// path is 404; everything else is 403 with the reason, never a 500 that would
// invite a retry. A permission and a socket (ENXIO; EOPNOTSUPP on darwin) are
// told apart; any other failure inside a boxed workspace is os.Root refusing a
// link that leaves it (or a swap raced in by the box), and a failure through a
// host root says only "blocked" rather than claim a reason nobody checked.
func writeOpenErr(w http.ResponseWriter, err error, t fsTarget) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, os.ErrPermission):
		writeErr(w, http.StatusForbidden, msgPerm)
	case errors.Is(err, syscall.ENXIO), errors.Is(err, syscall.EOPNOTSUPP):
		writeErr(w, http.StatusForbidden, msgNotReg)
	case t.Strict:
		writeErr(w, http.StatusForbidden, msgEscape)
	default:
		writeErr(w, http.StatusForbidden, msgBlocked)
	}
}

// openFsTarget opens t for reading through its root. Strict targets carry an
// unresolved remainder, so this open is the boundary that refuses a box-planted
// link out of the workspace; a path-based os.Open would follow it. The root is
// closed before returning — a file opened through an os.Root stays valid.
// O_NONBLOCK keeps the open of a FIFO from waiting for a writer; callers must
// Fstat the result and refuse anything that is not what they expect.
func openFsTarget(t fsTarget, flag int) (*os.File, error) {
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.OpenFile(t.Rel, os.O_RDONLY|flag|syscall.O_NONBLOCK, 0)
}

// handleFsList serves GET /api/fs/list?path=<abs-dir> → the directory's entries,
// directories first then files, each sorted by name. A listing that touched the
// scratch folder also says "readonly": true (the key is absent otherwise).
func (s *Server) handleFsList(w http.ResponseWriter, r *http.Request) {
	t, err := s.resolveFs(r.URL.Query().Get("path"))
	if err != nil {
		writeFsErr(w, err)
		return
	}
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		writeOpenErr(w, err, t)
		return
	}
	defer root.Close()
	d, err := root.OpenFile(t.Rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			writeErr(w, http.StatusBadRequest, "not a directory")
			return
		}
		writeOpenErr(w, err, t)
		return
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	out := make([]fsEntry, 0, len(ents))
	for _, e := range ents {
		var size int64
		// e.Info() is safe here: an entry read from a directory opened in a Root is
		// lstat'ed relative to that directory's fd, not by path string.
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		out = append(out, fsEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	resp := map[string]any{"path": t.Abs, "entries": out}
	// Same wording and omit-when-false as fsRead.ReadOnly: the Files tree hides
	// its mutation buttons from it. The server refuses those mutations anyway
	// (openParent), so this field only spares the user a dead button.
	if t.ReadOnly {
		resp["readonly"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

const maxTextBytes = 2 << 20 // 2 MiB preview cap for text files

type fsRead struct {
	Kind    string `json:"kind"`           // "text" | "image" | "binary"
	Lang    string `json:"lang,omitempty"` // highlight.js hint (bare extension)
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"`
	Version string `json:"version,omitempty"` // sha256 of Content; text only, for edit locking
	// ReadOnly: the file is served but can never be saved (scratch folder, box).
	ReadOnly bool `json:"readonly,omitempty"`
}

// handleFsRead serves GET /api/fs/read?path=<abs-file>. Images are streamed raw
// (so <img src> works); text is returned as JSON with a language hint; binary
// returns metadata only (never raw bytes); text over maxTextBytes → 413.
func (s *Server) handleFsRead(w http.ResponseWriter, r *http.Request) {
	t, err := s.resolveFs(r.URL.Query().Get("path"))
	if err != nil {
		writeFsErr(w, err)
		return
	}
	f, err := openFsTarget(t, 0)
	if err != nil {
		writeOpenErr(w, err, t)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stat error")
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	// A FIFO, socket or device is never a preview: reading a FIFO waits for a
	// writer a box can simply never provide. This applies to host paths too.
	if !info.Mode().IsRegular() {
		writeErr(w, http.StatusForbidden, msgNotReg)
		return
	}
	serveFsFile(w, r, f, info, filepath.Base(t.Abs), t.ReadOnly)
}

// serveFsFile writes the response for an already-opened regular file: raw
// download, raw image, or the text / binary / too-large JSON preview. It is
// the shared tail of /api/fs/read and the box file endpoint, so the two cannot
// drift. name is the file's base name (it picks the extension and the download
// filename); the caller has already authorised and opened f and closes it.
// readOnly marks the JSON preview so the viewer offers no Edit.
func serveFsFile(w http.ResponseWriter, r *http.Request, f *os.File, info os.FileInfo, name string, readOnly bool) {
	ext := strings.ToLower(filepath.Ext(name))

	// download=1 is a byte pipe, not a preview: it deliberately bypasses both
	// preview guards below (the binary-metadata branch and the maxTextBytes
	// cap), because a binary or an oversized log is exactly the file you most
	// need to fetch, and refusing to hand over something the tree already lists
	// makes the viewer a dead end. Always octet-stream + nosniff so the browser
	// saves it instead of trying to render it.
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDisposition(name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}

	if ctype, ok := imageExt[ext]; ok {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		// Defense-in-depth: don't let the browser MIME-sniff, and sandbox the
		// response so an SVG opened as a top-level navigation can't run script.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}

	head := make([]byte, 8000)
	n, _ := io.ReadFull(f, head)
	if isBinary(head[:n]) {
		writeJSON(w, http.StatusOK, fsRead{Kind: "binary", Size: info.Size(), ReadOnly: readOnly})
		return
	}
	if info.Size() > maxTextBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large to preview")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "seek error")
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, maxTextBytes))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	writeJSON(w, http.StatusOK, fsRead{
		Kind: "text", Lang: strings.TrimPrefix(ext, "."), Size: info.Size(),
		Content: string(content), Version: hashText(content), ReadOnly: readOnly,
	})
}

// contentDisposition builds an attachment header for name. The quoted form is
// escaped so a filename containing a quote cannot inject header syntax, and the
// RFC 5987 filename* carries a non-ASCII name through intact.
func contentDisposition(name string) string {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, q, url.PathEscape(name))
}
