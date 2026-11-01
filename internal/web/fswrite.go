package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"syscall"
)

// errNotRegular is a lock read that found something other than a regular file
// (a FIFO, socket or device a box left in its place).
var errNotRegular = errors.New("not a regular file")

type writeReqBody struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	BaseVersion string `json:"base_version"`
	Overwrite   bool   `json:"overwrite"`
}

// maxWriteBody bounds the raw JSON body for /api/fs/write. decodeOpBody's
// maxOpBody (8 KiB) is sized for the tiny mkdir/rename/delete bodies and is far
// too small here: this body carries a whole file's content, JSON-string-escaped
// on the wire, which can inflate up to ~6x worst case (each control byte becomes
// \u00XX). The cap must clear maxTextBytes by that margin so a legitimate
// maxTextBytes-sized save is never rejected before its own over-cap check runs.
const maxWriteBody = maxTextBytes*6 + 4096

// decodeWriteBody applies the shared checkMutationGuard (fsops.go) — the same
// method/CSRF checks decodeOpBody uses — then bounds and decodes the body with
// a cap sized for file content instead of the tiny JSON mutation bodies. Only
// the size cap is local to this file; the guard logic lives in exactly one
// place so it cannot drift between the two decoders.
func decodeWriteBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !checkMutationGuard(w, r) {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

// handleFsWrite serves POST /api/fs/write: overwrite an EXISTING regular text
// file with new content, atomically and only if it has not changed underneath
// the editor since it was read. The whole point is safe writes — agent-box runs
// live agents that may rewrite the same file — not access control (the console
// spawns shells that can already write anything).
func (s *Server) handleFsWrite(w http.ResponseWriter, r *http.Request) {
	var req writeReqBody
	if !decodeWriteBody(w, r, &req) {
		return
	}
	if len(req.Content) > maxTextBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large to save")
		return
	}
	root, relParent, base, _, err := s.resolveParent(req.Path)
	if err != nil {
		writeFsErr(w, err)
		return
	}
	defer root.Close()
	rel := relOf(relParent, base)

	fi, err := root.Lstat(rel)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "is a directory")
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		writeErr(w, http.StatusBadRequest, "cannot edit a symlink")
		return
	}
	if !fi.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, "not a regular file")
		return
	}

	// Optimistic lock. Bounded: an on-disk file now larger than the cap cannot
	// match any editable base, so it is treated as changed rather than read into
	// memory unbounded.
	if !req.Overwrite {
		if fi.Size() > maxTextBytes {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "file changed on disk", "current_version": "",
			})
			return
		}
		cur, err := readRootFile(root, rel)
		if err != nil {
			if errors.Is(err, errNotRegular) {
				writeErr(w, http.StatusForbidden, msgNotReg)
				return
			}
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		curVer := hashText(cur)
		if curVer != req.BaseVersion {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "file changed on disk", "current_version": curVer,
			})
			return
		}
	}

	// Atomic write: temp sibling created O_EXCL, then rename over the target,
	// which replaces it atomically on POSIX and keeps the original if anything
	// before the rename fails.
	tmpRel, err := writeTemp(root, relParent, req.Content, fi.Mode().Perm())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "write error")
		return
	}
	if err := root.Rename(tmpRel, rel); err != nil {
		_ = root.Remove(tmpRel)
		writeErr(w, http.StatusInternalServerError, "cannot save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"path": req.Path, "version": hashText([]byte(req.Content)),
	})
}

// readRootFile reads a root-relative file, bounded by the text cap. The open is
// O_NONBLOCK and the result must be a regular file: a box can leave a FIFO where
// the editor expects a file, and a blocking open or read of one would hang the
// request until something wrote to it.
func readRootFile(root *os.Root, rel string) ([]byte, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	return io.ReadAll(io.LimitReader(f, maxTextBytes))
}

// writeTemp creates a uniquely named sibling temp file in relParent (via the
// root, so it cannot escape), writes content with the given perm, and returns
// its root-relative path. O_EXCL closes the create-then-write race.
func writeTemp(root *os.Root, relParent, content string, perm os.FileMode) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	name := ".agentbox-tmp-" + hex.EncodeToString(b[:])
	tmpRel := relOf(relParent, name)
	f, err := root.OpenFile(tmpRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		_ = root.Remove(tmpRel)
		return "", err
	}
	// OpenFile's create mode is subject to the process umask, so the file on
	// disk can end up with FEWER bits than perm (e.g. a group-writable 0664
	// original becomes 0644 under umask 022). f.Chmod operates on the open fd
	// and bypasses the umask, forcing the exact original mode.
	if err := f.Chmod(perm); err != nil {
		f.Close()
		_ = root.Remove(tmpRel)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmpRel)
		return "", err
	}
	return tmpRel, nil
}
