package web

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

var (
	errArchiveTooBig  = errors.New("directory is too large to archive")
	errArchiveTooMany = errors.New("directory has too many files to archive")
)

const (
	defaultMaxArchiveBytes int64 = 512 << 20
	defaultMaxArchiveFiles       = 20000
)

// measureTree walks root counting regular files and their total size. It runs
// BEFORE any bytes are written so an over-cap directory can be refused with a
// status code: once the gzip stream starts the status is already 200, and
// aborting mid-stream hands the user a truncated .tar.gz that is
// indistinguishable from a successful download.
//
// It is best-effort against concurrent writes — the tree can change between the
// measurement and the stream — which is an accepted race.
func measureTree(root string, maxBytes int64, maxFiles int) (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		// IsRegular() is already false for symlinks, sockets, fifos and devices,
		// so this single test covers everything the archive will not include.
		if !d.Type().IsRegular() {
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
	return total, count, err
}

// handleFsArchive serves GET /api/fs/archive?path=<abs-dir> as a .tar.gz, or,
// with probe=1, as a JSON measurement instead of the archive.
//
// The probe exists because a download triggered by an <a download> navigation
// swallows the response body on an error status: without it, an over-cap
// directory gives the user a failed download and no reason for it.
//
// Symlinks are skipped entirely — neither followed nor recorded. One rule
// removes both symlink loops and any escape from $HOME; the cost, disclosed in
// the UI, is that symlinked content is absent from the tarball.
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
	total, count, err := measureTree(p, maxBytes, maxFiles)
	if err != nil {
		switch {
		case errors.Is(err, errArchiveTooBig):
			// No measured size in the message: measureTree short-circuits the
			// moment the cap is passed, so `total` is always "just over the
			// limit" and would read as a self-contradiction ("folder is 512 MB,
			// over the 512 MB limit") for a folder that might be 50 GB.
			writeErr(w, http.StatusRequestEntityTooLarge,
				"folder is over the "+humanBytes(maxBytes)+" limit")
		case errors.Is(err, errArchiveTooMany):
			writeErr(w, http.StatusRequestEntityTooLarge,
				"folder has more than "+strconv.Itoa(maxFiles)+" files")
		default:
			writeErr(w, http.StatusInternalServerError, "could not read the folder")
		}
		return
	}
	if r.URL.Query().Get("probe") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": total, "files": count})
		return
	}

	base := filepath.Base(p)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", contentDisposition(base+".tar.gz"))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// BestSpeed, not the default level: this box also runs the user's agents,
	// and a 512 MiB archive at level 6 is a long single-threaded CPU burn for a
	// few percent of size on already-compressed screenshots and logs.
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start the archive")
		return
	}
	tw := tar.NewWriter(gz)

	parent := filepath.Dir(p)
	ctx := r.Context()
	walkErr := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err() // client hung up; stop compressing into a dead socket
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
			return tw.WriteHeader(hdr)
		}

		// Open BEFORE writing the header. A header written for a file that then
		// fails to open leaves the tar writer expecting a body that never
		// arrives, and every later entry is corrupt — with the status already
		// sent as 200. Skipping the file outright keeps the stream valid.
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		n, err := io.Copy(tw, io.LimitReader(f, hdr.Size))
		if err != nil {
			return err
		}
		if n < hdr.Size {
			// The file shrank after it was measured. The header is already
			// committed, so pad to the declared size rather than desynchronise
			// the stream — a zero-padded member beats an unextractable one.
			if _, err := io.CopyN(tw, zeroReader{}, hdr.Size-n); err != nil {
				return err
			}
		}
		return nil
	})

	// Close in order (tar, then gzip) and report failures. A silently broken
	// download is worse than a logged one: nothing else in this response can
	// signal the problem, since the 200 went out with the first byte.
	tarErr := tw.Close()
	gzErr := gz.Close()
	if walkErr != nil || tarErr != nil || gzErr != nil {
		log.Printf("archive %s: walk=%v tar=%v gzip=%v", p, walkErr, tarErr, gzErr)
	}
}

// zeroReader is an endless source of NUL bytes, used to pad a tar member whose
// file shrank between measurement and read.
type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 0
	}
	return len(b), nil
}
