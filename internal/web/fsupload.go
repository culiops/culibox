package web

import (
	"errors"
	"fmt"
	"io"
	"log"
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

const (
	// maxNameAttempts bounds the collision-suffix search (name-1 … name-100).
	maxNameAttempts = 100
	// maxUploadParts bounds one request. Without it, per-file caps still allow
	// an unbounded number of files, which is a disk-fill in slow motion.
	maxUploadParts = 50
)

// safeName reduces a browser-supplied part filename to a bare, safe base name.
// Browsers are allowed to send a path, and a Windows browser sends a backslash
// one, so this strips directories from both conventions before validating.
//
// The character rules are the load-bearing part: this name is typed into a live
// PTY after upload. C0 controls would submit the line (\n) or corrupt it (\r);
// C1 controls (0x80–0x9F) are parsed as escape sequences by xterm once the shell
// echoes them back; bidi overrides let a name render as something other than
// what it is, in both the toast and the terminal.
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
	if hasUnsafeRunes(n) {
		return "", errBadName
	}
	// A collision suffix may add up to 4 bytes; leave room under NAME_MAX so the
	// rename path cannot turn a legal upload into ENAMETOOLONG.
	if len(n) > 250 {
		return "", errBadName
	}
	return n, nil
}

// hasUnsafeRunes reports whether s contains anything that must never reach a
// terminal: C0 controls, DEL, C1 controls, or a bidi override.
func hasUnsafeRunes(s string) bool {
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f:
			return true
		case r >= 0x80 && r <= 0x9f:
			return true
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return true
		}
	}
	return false
}

// containedDir resolves an upload destination: contained under home, existing,
// and a directory. The write path needs this rather than containedPath on the
// full target, because containedPath EvalSymlinks the whole path and returns
// ENOENT for a file that does not exist yet — which is every upload.
//
// This resolution is a snapshot, so it is NOT the security boundary on its own;
// see openHomeRoot.
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

// openHomeRoot opens $HOME as an os.Root and returns it with the resolved home
// path. Every upload write goes through this root.
//
// This is the actual containment boundary. os.Root resolves each path component
// with openat/O_NOFOLLOW semantics, so a symlink swapped in for an ANCESTOR
// directory between containedDir's EvalSymlinks and the open cannot be used to
// escape. O_EXCL alone only guards the final component and loses that race.
func openHomeRoot(home string) (*os.Root, string, error) {
	rhome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(rhome)
	if err != nil {
		return nil, "", err
	}
	return root, rhome, nil
}

// createExclusive creates a new file at relDir/name inside root, never
// overwriting and never following a symlink at any component. O_EXCL supplies
// the final-component guarantees — it fails when the name exists at all,
// including when it is a symlink — and the root supplies them for every
// ancestor. On collision it walks name-1, name-2, … and returns the name it
// actually used.
func createExclusive(root *os.Root, relDir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	// filepath.Ext(".bashrc") is the whole name, which would leave an empty stem
	// and produce "-1.bashrc". A dotfile has no extension to preserve.
	if stem == "" {
		stem, ext = name, ""
	}
	for i := 0; i <= maxNameAttempts; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		f, err := root.OpenFile(filepath.Join(relDir, candidate),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
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
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
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
	root, rhome, err := openHomeRoot(s.Home)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot open home")
		return
	}
	defer root.Close()
	relDir, err := filepath.Rel(rhome, dir)
	if err != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(os.PathSeparator)) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	limit := s.MaxUploadBytes
	if limit <= 0 {
		limit = defaultMaxUploadBytes
	}
	// Bound the whole request, not just each part: per-file caps alone still let
	// an unbounded number of files through in one POST. The slack covers
	// multipart framing (boundaries and per-part headers), which is not file
	// content and must not be charged against the file cap.
	r.Body = http.MaxBytesReader(w, r.Body, limit*maxUploadParts+maxUploadParts*8192)

	// MultipartReader streams; ParseMultipartForm would buffer whole files.
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}

	uploaded := make([]uploadedFile, 0, 4)
	failures := make([]uploadFailure, 0)
	seen := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A malformed or over-long body mid-stream: report what landed
			// rather than discarding the parts that already succeeded.
			failures = append(failures, uploadFailure{Name: "", Error: "upload stream ended early"})
			break
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		seen++
		if seen > maxUploadParts {
			part.Close()
			failures = append(failures, uploadFailure{
				Name:  "",
				Error: fmt.Sprintf("more than %d files in one upload", maxUploadParts),
			})
			break
		}
		u, ferr := storePart(root, relDir, dir, part.FileName(), part, limit)
		if ferr != nil {
			// The client message deliberately carries no path, so the server log
			// is the only place a disk-full or EACCES leaves any trace at all.
			log.Printf("upload %s: %v", dir, ferr)
			failures = append(failures, uploadFailure{Name: displayName(part.FileName()), Error: ferr.Error()})
		} else {
			uploaded = append(uploaded, u)
		}
		part.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploaded": uploaded, "errors": failures})
}

// displayName is the filename echoed back in an error. It must never be the raw
// browser string — that string lands in the UI and in a terminal-adjacent
// toast — so anything failing sanitation reports a placeholder instead.
func displayName(raw string) string {
	if n, err := safeName(raw); err == nil {
		return n
	}
	return "(unnamed)"
}

// storePart writes one multipart part into relDir inside root. absDir is used
// only to report the absolute path back to the client. On any failure the
// partial file is removed, so a rejected upload never leaves a truncated file
// behind for an agent to read as if it were whole.
//
// Errors returned here are shown to the user verbatim, so they are written for
// a human and deliberately carry no filesystem paths.
func storePart(root *os.Root, relDir, absDir, rawName string, src io.Reader, limit int64) (uploadedFile, error) {
	name, err := safeName(rawName)
	if err != nil {
		return uploadedFile{}, errors.New("invalid file name")
	}
	f, final, err := createExclusive(root, relDir, name)
	if err != nil {
		if errors.Is(err, errTooManyCollisions) {
			return uploadedFile{}, errors.New("too many files already have that name")
		}
		return uploadedFile{}, errors.New("could not create the file")
	}
	relPath := filepath.Join(relDir, final)
	// limit+1: reading one byte past the cap is the only way to tell "exactly at
	// the limit" from "truncated at the limit".
	n, cerr := io.Copy(f, io.LimitReader(src, limit+1))
	closeErr := f.Close()
	switch {
	case cerr != nil:
		root.Remove(relPath)
		return uploadedFile{}, errors.New("transfer failed before the file was complete")
	case n > limit:
		root.Remove(relPath)
		return uploadedFile{}, fmt.Errorf("larger than the %s limit", humanBytes(limit))
	case closeErr != nil:
		root.Remove(relPath)
		return uploadedFile{}, errors.New("could not finish writing the file")
	}
	u := uploadedFile{Name: final, Path: filepath.Join(absDir, final), Size: n}
	// Only a real collision counts as a rename. Comparing against the RAW
	// browser name would report "renamed" for every Windows-style path and every
	// name that merely had whitespace trimmed — a false claim about the one
	// promise this feature makes loudest.
	if final != name {
		u.RenamedFrom = name
	}
	return u, nil
}

// humanBytes renders a cap the way the person who hit it thinks about it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.3g GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.3g MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.3g KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// sameOrigin rejects a cross-site request. Basic-auth credentials are cached
// per-origin and re-attached to cross-site requests — there is no SameSite
// equivalent for them — and multipart/form-data is a CORS-safelisted content
// type, so without this any page on the internet could POST a file into $HOME
// on behalf of a logged-in browser. The response would be opaque to the
// attacker, but the write would still happen, and this box runs agents that
// read those files.
//
// The JSON APIs are incidentally protected because application/json forces a
// preflight; multipart does not, which is why this endpoint needs it
// explicitly.
//
// Both headers are checked only when present: a same-origin GET sends no
// Origin, and older browsers send no Sec-Fetch-Site, so an absent header must
// not fail the request.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	if o := r.Header.Get("Origin"); o != "" &&
		o != "http://"+r.Host && o != "https://"+r.Host {
		return false
	}
	return true
}
