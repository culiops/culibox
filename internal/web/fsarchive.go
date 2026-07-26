package web

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

var (
	errArchiveTooBig  = errors.New("directory is too large to archive")
	errArchiveTooMany = errors.New("directory has too many files to archive")
)

const (
	defaultMaxArchiveBytes int64 = 512 << 20
	defaultMaxArchiveFiles       = 20000
)

// measureTree walks root counting regular files and their total size, without
// following symlinks. It runs BEFORE any bytes are written so an over-cap
// directory can be refused with a status code: once the gzip stream starts the
// status is already 200, and aborting mid-stream hands the user a truncated
// .tar.gz that is indistinguishable from a successful download.
//
// It is best-effort against concurrent writes — the tree can grow between the
// measurement and the stream — which is an accepted race.
func measureTree(root string, maxBytes int64, maxFiles int) error {
	var total int64
	var count int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		count++
		total += info.Size()
		if count > maxFiles {
			return errArchiveTooMany
		}
		if total > maxBytes {
			return errArchiveTooBig
		}
		return nil
	})
	return err
}

// handleFsArchive serves GET /api/fs/archive?path=<abs-dir> as a .tar.gz.
//
// Symlinks are skipped entirely — neither followed nor recorded. One rule
// removes both symlink loops and any escape from $HOME; the cost, documented
// for the user, is that symlinked content is absent from the tarball.
func (s *Server) handleFsArchive(w http.ResponseWriter, r *http.Request) {
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

	maxBytes := s.MaxArchiveBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxArchiveBytes
	}
	maxFiles := s.MaxArchiveFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxArchiveFiles
	}
	if err := measureTree(p, maxBytes, maxFiles); err != nil {
		if errors.Is(err, errArchiveTooBig) || errors.Is(err, errArchiveTooMany) {
			writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "walk error")
		return
	}

	base := filepath.Base(p)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", contentDisposition(base+".tar.gz"))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	parent := filepath.Dir(p)
	// Errors mid-stream cannot be reported as a status (the header is already
	// sent), so the walk stops rather than emitting a partial entry.
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil // sockets, fifos, devices
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(parent, path)
		if err != nil {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		// Bound the copy by the size in the header: a file that grew since the
		// header was written would otherwise desynchronise the tar stream.
		_, err = io.Copy(tw, io.LimitReader(f, fi.Size()))
		return err
	})
}
