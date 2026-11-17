package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type FileState int

const (
	Absent  FileState = iota
	Ours              // a regular file carrying Marker
	Foreign           // anything else at the path: never written over, never removed
)

func (f FileState) String() string {
	return [...]string{"absent", "ours", "foreign"}[f]
}

// ErrForeign: the path holds something install did not write.
var ErrForeign = errors.New("not written by culibox service install")

// markerWindow is how far into the file the marker may sit.
const markerWindow = 512

const noFollow = syscall.O_NOFOLLOW

// Inspect says whether path is absent, ours or foreign. A symlink is foreign
// whatever it points at: Lstat, and the read itself refuses to follow one.
func Inspect(path string) (FileState, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Absent, nil
	}
	if err != nil {
		return Foreign, err
	}
	if !fi.Mode().IsRegular() {
		return Foreign, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return Foreign, err
	}
	defer f.Close()
	buf := make([]byte, markerWindow)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Foreign, err
	}
	if strings.Contains(string(buf[:n]), Marker) {
		return Ours, nil
	}
	return Foreign, nil
}

// WriteUnit writes data at path when nothing is there or the file is ours,
// through a temp file and a rename so a crash never leaves half a unit. The
// rename replaces a name, never what a symlink points at.
func WriteUnit(path string, data []byte) error {
	st, err := Inspect(path)
	if err != nil {
		return err
	}
	if st == Foreign {
		return ErrForeign
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".culibox-service-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RemoveUnit removes path only when it is ours. removed is false when nothing
// was there.
func RemoveUnit(path string) (removed bool, err error) {
	st, err := Inspect(path)
	if err != nil {
		return false, err
	}
	switch st {
	case Absent:
		return false, nil
	case Foreign:
		return false, ErrForeign
	}
	return true, os.Remove(path)
}
