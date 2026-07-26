package web

import (
	"errors"
	"fmt"
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

// containedNewFile resolves dir under home, requires it to be a directory, and
// validates rawName as a bare filename. It creates nothing — createExclusive
// does that — so it is safe to call for a file that does not exist yet, which
// is exactly the case containedPath cannot serve: that helper EvalSymlinks the
// full path and returns ENOENT for a file that is not there yet.
func containedNewFile(home, dir, rawName string) (string, string, error) {
	d, err := containedPath(home, dir)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(d)
	if err != nil {
		return "", "", os.ErrNotExist
	}
	if !info.IsDir() {
		return "", "", errNotDir
	}
	name, err := safeName(rawName)
	if err != nil {
		return "", "", err
	}
	return d, name, nil
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
