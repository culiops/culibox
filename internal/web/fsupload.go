package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	errBadName           = errors.New("bad filename")
	errTooManyCollisions = errors.New("too many name collisions")
	errNotDir            = errors.New("not a directory")
)

// maxNameAttempts bounds the collision-suffix search (name-1 … name-100).
const maxNameAttempts = 100

// safeName reduces a browser-supplied part filename to a bare, safe base name.
// Browsers are allowed to send a path, and a Windows browser sends a backslash
// one, so this strips directories from both conventions before validating.
func safeName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndexByte(raw, '\\'); i >= 0 {
		raw = raw[i+1:]
	}
	n := strings.TrimSpace(filepath.Base(raw))
	if n == "" || n == "." || n == ".." || n == string(os.PathSeparator) {
		return "", errBadName
	}
	if strings.ContainsRune(n, os.PathSeparator) {
		return "", errBadName
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", errBadName
		}
	}
	return n, nil
}

// containedDir resolves an upload destination: contained under home, existing,
// and a directory. The write path needs this rather than containedPath on the
// full target, because containedPath EvalSymlinks the whole path and returns
// ENOENT for a file that does not exist yet — which is every upload.
func containedDir(home, dir string) (string, error) {
	d, err := containedPath(home, dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(d)
	if err != nil {
		return "", os.ErrNotExist
	}
	if !info.IsDir() {
		return "", errNotDir
	}
	return d, nil
}

// createExclusive creates a new file under dir without ever overwriting and
// without ever following a symlink. O_EXCL supplies both properties atomically:
// it fails when the name exists at all, including when it is a symlink pointing
// somewhere outside $HOME. On collision it walks name-1, name-2, … and returns
// the name it actually used.
func createExclusive(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i <= maxNameAttempts; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, candidate, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errTooManyCollisions
}

// defaultMaxUploadBytes is the per-file cap used when none is configured.
const defaultMaxUploadBytes int64 = 64 << 20

type uploadedFile struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	RenamedFrom string `json:"renamed_from,omitempty"`
}

type uploadFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// handleFsUpload serves POST /api/fs/upload?dir=<abs-dir> (multipart, repeated
// "file" parts).
//
// The status code describes the DESTINATION, never the files: a usable
// destination is always 200 — even when every part failed — with each part's
// outcome in the body. 400/403/404 mean nothing was attempted, so one oversized
// file can never discard its siblings' successful writes.
func (s *Server) handleFsUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dir, err := containedDir(s.Home, r.URL.Query().Get("dir"))
	if err != nil {
		if errors.Is(err, errNotDir) {
			writeErr(w, http.StatusBadRequest, "not a directory")
			return
		}
		writeFsErr(w, err)
		return
	}

	// MultipartReader streams; ParseMultipartForm would buffer whole files.
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	limit := s.MaxUploadBytes
	if limit <= 0 {
		limit = defaultMaxUploadBytes
	}

	uploaded := make([]uploadedFile, 0, 4)
	failures := make([]uploadFailure, 0)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A malformed body mid-stream: report what landed rather than
			// discarding the parts that already succeeded.
			failures = append(failures, uploadFailure{Name: "", Error: "malformed upload body"})
			break
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		orig := part.FileName()
		size, name, ferr := storePart(dir, orig, part, limit)
		if ferr != nil {
			failures = append(failures, uploadFailure{Name: displayName(orig), Error: ferr.Error()})
		} else {
			u := uploadedFile{Name: name, Path: filepath.Join(dir, name), Size: size}
			if name != orig {
				u.RenamedFrom = orig
			}
			uploaded = append(uploaded, u)
		}
		part.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploaded": uploaded, "errors": failures})
}

// displayName is the filename echoed back in an error. It must never be the raw
// browser string, which lands in the UI unescaped-by-intent, so anything that
// fails sanitation reports a placeholder instead.
func displayName(raw string) string {
	if n, err := safeName(raw); err == nil {
		return n
	}
	return "(unnamed)"
}

// storePart writes one multipart part into dir, capped at limit bytes, and
// returns the bytes written and the final name. On any failure the partial file
// is removed, so a rejected upload never leaves a truncated file behind for an
// agent to read as if it were whole.
func storePart(dir, rawName string, src io.Reader, limit int64) (int64, string, error) {
	name, err := safeName(rawName)
	if err != nil {
		return 0, "", err
	}
	f, final, err := createExclusive(dir, name)
	if err != nil {
		return 0, "", err
	}
	full := filepath.Join(dir, final)
	// limit+1: reading one byte past the cap is the only way to tell "exactly
	// at the limit" from "truncated at the limit".
	n, cerr := io.Copy(f, io.LimitReader(src, limit+1))
	closeErr := f.Close()
	switch {
	case cerr != nil:
		os.Remove(full)
		return 0, "", cerr
	case n > limit:
		os.Remove(full)
		return 0, "", fmt.Errorf("file is larger than the %d byte limit", limit)
	case closeErr != nil:
		os.Remove(full)
		return 0, "", closeErr
	}
	return n, final, nil
}
