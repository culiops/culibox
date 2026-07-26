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
)

var (
	errBadPath     = errors.New("bad path")
	errOutsideHome = errors.New("outside home")
)

// containedPath resolves reqPath (which must be absolute) and guarantees it
// lives inside home after resolving symlinks. It returns the resolved absolute
// path, or a sentinel error: errBadPath (empty/relative), os.ErrNotExist
// (missing), errOutsideHome (escapes home, including via a symlink).
func containedPath(home, reqPath string) (string, error) {
	rhome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	return containedUnder(rhome, reqPath)
}

// containedUnder is containedPath with the home directory already resolved, for
// callers checking many paths in a row (see handleFsSearch) that would otherwise
// re-resolve $HOME on every one.
func containedUnder(rhome, reqPath string) (string, error) {
	if reqPath == "" || !filepath.IsAbs(reqPath) {
		return "", errBadPath
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(reqPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	if resolved != rhome && !strings.HasPrefix(resolved, rhome+string(os.PathSeparator)) {
		return "", errOutsideHome
	}
	return resolved, nil
}

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

// writeFsErr maps a containedPath sentinel error to an HTTP status.
func writeFsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadPath):
		writeErr(w, http.StatusBadRequest, "bad path")
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, errNotDir):
		writeErr(w, http.StatusBadRequest, "not a directory")
	case errors.Is(err, errOutsideHome):
		writeErr(w, http.StatusForbidden, "forbidden")
	default:
		writeErr(w, http.StatusInternalServerError, "error")
	}
}

// handleFsList serves GET /api/fs/list?path=<abs-dir> → the directory's entries,
// directories first then files, each sorted by name.
func (s *Server) handleFsList(w http.ResponseWriter, r *http.Request) {
	p, err := containedPath(s.Home, r.URL.Query().Get("path"))
	if err != nil {
		writeFsErr(w, err)
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "not a directory")
		return
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	out := make([]fsEntry, 0, len(ents))
	for _, e := range ents {
		var size int64
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
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": out})
}

const maxTextBytes = 2 << 20 // 2 MiB preview cap for text files

type fsRead struct {
	Kind    string `json:"kind"`           // "text" | "image" | "binary"
	Lang    string `json:"lang,omitempty"` // highlight.js hint (bare extension)
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"`
}

// handleFsRead serves GET /api/fs/read?path=<abs-file>. Images are streamed raw
// (so <img src> works); text is returned as JSON with a language hint; binary
// returns metadata only (never raw bytes); text over maxTextBytes → 413.
func (s *Server) handleFsRead(w http.ResponseWriter, r *http.Request) {
	p, err := containedPath(s.Home, r.URL.Query().Get("path"))
	if err != nil {
		writeFsErr(w, err)
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "open error")
		return
	}
	defer f.Close()

	// download=1 is a byte pipe, not a preview: it deliberately bypasses both
	// preview guards below (the binary-metadata branch and the maxTextBytes
	// cap), because a binary or an oversized log is exactly the file you most
	// need to fetch, and refusing to hand over something the tree already lists
	// makes the viewer a dead end. Always octet-stream + nosniff so the browser
	// saves it instead of trying to render it.
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDisposition(filepath.Base(p)))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f)
		return
	}

	if ctype, ok := imageExt[ext]; ok {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		// Defense-in-depth: don't let the browser MIME-sniff, and sandbox the
		// response so an SVG opened as a top-level navigation can't run script.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f)
		return
	}

	head := make([]byte, 8000)
	n, _ := io.ReadFull(f, head)
	if isBinary(head[:n]) {
		writeJSON(w, http.StatusOK, fsRead{Kind: "binary", Size: info.Size()})
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
		Kind: "text", Lang: strings.TrimPrefix(ext, "."), Size: info.Size(), Content: string(content),
	})
}

// contentDisposition builds an attachment header for name. The quoted form is
// escaped so a filename containing a quote cannot inject header syntax, and the
// RFC 5987 filename* carries a non-ASCII name through intact.
func contentDisposition(name string) string {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, q, url.PathEscape(name))
}
