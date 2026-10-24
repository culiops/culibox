package web

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

var (
	errArchiveTooBig  = errors.New("directory is too large to archive")
	errArchiveTooMany = errors.New("directory has too many files to archive")
)

const (
	defaultMaxArchiveBytes int64 = 512 << 20
	defaultMaxArchiveFiles       = 20000
)

// archiveBudget is the file-count and byte caps, charged one regular file at a
// time. The measure pass and the stream both charge it, because the measure is
// only a snapshot: a box can grow a (sparse) file or add files before the
// stream walks the tree, so the stream must not trust it.
type archiveBudget struct {
	maxBytes int64
	maxFiles int
	bytes    int64
	files    int
}

// take charges one regular file of the given size and reports which cap, if
// any, it pushed over (files first, as the measure pass always did).
func (b *archiveBudget) take(size int64) error {
	b.files++
	b.bytes += size
	if b.files > b.maxFiles {
		return errArchiveTooMany
	}
	if b.bytes > b.maxBytes {
		return errArchiveTooBig
	}
	return nil
}

// walkArchiveTree walks start inside root, calling fn for every entry with the
// root it must be opened through and its tar name (prefix, then the path below
// start).
//
// A strict walk (root is a boxed workspace, so os.Root already confines it) needs
// nothing more. A host walk may descend into a tree it must not trust, and
// os.Root($HOME) would follow any link a box plants beneath it, so the walk
// tests every directory's identity against p: a denied tree is skipped outright,
// and a boxed workspace is entered through its OWN root (outer.OpenRoot), whose
// identity must match the dirent's or the subtree is skipped. Descending into a
// boxed tree from a host walk counts as entering it. skipStart omits the start
// entry itself (the caller already emitted it as the outer walk's directory).
//
// A directory that turns out to be a link by the time it is inspected (a box
// swapped it after the listing) is skipped, never descended.
func (p fsPolicy) walkArchiveTree(root *os.Root, start, prefix string, strict, skipStart bool,
	fn func(root *os.Root, path, name string, d fs.DirEntry) error) error {
	return fs.WalkDir(root.FS(), start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		below, err := filepath.Rel(start, path)
		if err != nil {
			return nil
		}
		name := prefix
		if below != "." {
			name += "/" + filepath.ToSlash(below)
		}
		if !d.IsDir() {
			return fn(root, path, name, d)
		}
		fi, err := d.Info()
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return fs.SkipDir
		}
		denied, contained := p.treeOf(fi)
		if denied {
			return fs.SkipDir
		}
		if skipStart && path == start {
			return nil
		}
		if strict || !contained {
			return fn(root, path, name, d)
		}
		if err := fn(root, path, name, d); err != nil {
			return err
		}
		sub, err := root.OpenRoot(path)
		if err != nil {
			return fs.SkipDir
		}
		defer sub.Close()
		si, err := sub.Stat(".")
		if err != nil {
			return fs.SkipDir
		}
		if k, kerr := keyOf(si); kerr != nil {
			return fs.SkipDir
		} else if want, werr := keyOf(fi); werr != nil || k != want {
			return fs.SkipDir
		}
		if err := p.walkArchiveTree(sub, ".", name, true, true, fn); err != nil {
			return err
		}
		return fs.SkipDir
	})
}

// measureTree counts the regular files under start and their total size, walking
// as walkArchiveTree does so it measures exactly what the stream will include. It
// runs BEFORE any bytes are written so an over-cap directory can be refused with
// a status code: once the gzip stream starts the status is already 200, and
// aborting mid-stream hands the user a truncated .tar.gz that is
// indistinguishable from a successful download.
//
// It is best-effort against concurrent writes — the tree can change between the
// measurement and the stream — so streamArchive charges the same caps again.
func measureTree(p fsPolicy, root *os.Root, start string, strict bool, maxBytes int64, maxFiles int) (int64, int, error) {
	bud := archiveBudget{maxBytes: maxBytes, maxFiles: maxFiles}
	err := p.walkArchiveTree(root, start, "", strict, false, func(_ *os.Root, _, _ string, d fs.DirEntry) error {
		// IsRegular() is already false for symlinks, sockets, fifos and devices,
		// so this single test covers everything the archive will not include.
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		return bud.take(info.Size())
	})
	return bud.bytes, bud.files, err
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
	// One policy snapshot serves the resolve, the measurement and the stream.
	pol, err := s.fsPolicy()
	if err != nil {
		writeFsErr(w, err)
		return
	}
	t, err := pol.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeFsErr(w, err)
		return
	}
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		writeOpenErr(w, err)
		return
	}
	defer root.Close()
	info, err := root.Stat(t.Rel)
	if err != nil {
		writeOpenErr(w, err)
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
	total, count, err := measureTree(pol, root, t.Rel, t.Strict, maxBytes, maxFiles)
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

	base := filepath.Base(t.Abs)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", contentDisposition(base+".tar.gz"))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	bud := archiveBudget{maxBytes: maxBytes, maxFiles: maxFiles}
	_ = streamArchive(r.Context(), w, pol, root, t, base, &bud)
}

// streamArchive writes the .tar.gz of the tree at t to w. The status is
// already 200 by now, so the only way to fail is to end the tarball: a walk
// error (including the caps being passed, see archiveBudget) stops the walk
// and the tar and gzip writers are still closed in order, leaving a tarball
// that ends at a member boundary rather than a truncated one. It returns the
// walk/close errors for the caller and tests; they are logged here already.
func streamArchive(ctx context.Context, w io.Writer, pol fsPolicy, root *os.Root, t fsTarget, base string, bud *archiveBudget) error {
	// BestSpeed, not the default level: this box also runs the user's agents,
	// and a 512 MiB archive at level 6 is a long single-threaded CPU burn for a
	// few percent of size on already-compressed screenshots and logs.
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)

	walkErr := pol.walkArchiveTree(root, t.Rel, base, t.Strict, false, func(root *os.Root, path, name string, d fs.DirEntry) error {
		if ctx.Err() != nil {
			return ctx.Err() // client hung up; stop compressing into a dead socket
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil // sockets, fifos, devices
		}
		if d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			hdr, err := tar.FileInfoHeader(fi, "")
			if err != nil {
				return nil
			}
			hdr.Name = name + "/"
			return tw.WriteHeader(hdr)
		}

		// Open BEFORE writing the header. A header written for a file that then
		// fails to open leaves the tar writer expecting a body that never
		// arrives, and every later entry is corrupt — with the status already
		// sent as 200. Skipping the file outright keeps the stream valid.
		//
		// The dirent said "regular file", but a box can swap it for a link or a
		// FIFO between the listing and here. The os.Root confinement is what stops
		// a swapped link leaving a boxed workspace — O_NOFOLLOW does not, since
		// os.Root follows links that stay inside it — so it only decides when the
		// root is box-owned (walkArchiveTree opens every boxed tree through its own
		// root). O_NONBLOCK keeps a swapped-in FIFO from blocking the open, and the
		// Fstat of what was actually opened, not the stale dirent, picks regular.
		f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return nil
		}
		// Charge the caps before the header: the file may have grown (or others
		// appeared) since measureTree ran. Returning here stops the walk with
		// the tar still at a member boundary, so the tarball ends cleanly.
		if err := bud.take(hdr.Size); err != nil {
			return err
		}
		hdr.Name = name
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
	if errors.Is(walkErr, errArchiveTooBig) || errors.Is(walkErr, errArchiveTooMany) {
		log.Printf("archive %s: the tree outgrew the cap after it was measured (%v); archive ended early",
			t.Abs, walkErr)
	} else if walkErr != nil {
		log.Printf("archive %s: walk=%v", t.Abs, walkErr)
	}
	if tarErr != nil || gzErr != nil {
		log.Printf("archive %s: tar=%v gzip=%v", t.Abs, tarErr, gzErr)
	}
	return errors.Join(walkErr, tarErr, gzErr)
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
