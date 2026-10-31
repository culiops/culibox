package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// importsPresent reports whether a lock's imports can be mounted: a lock that
// lists imports whose dir is gone (someone cleaned the bundle root) is not a
// complete bundle, so it is rebuilt rather than reused.
func importsPresent(dir string, lock Lock) bool {
	if len(lock.Imports) == 0 {
		return true
	}
	fi, err := os.Lstat(importsDir(dir))
	return err == nil && fi.IsDir()
}

// writeImports copies the set's files into a fresh temp dir under root, at their
// in-box paths, and records them in lock. It returns "" when there is nothing to
// write. Every write goes through os.Root, so a path that would leave the temp
// dir fails instead of landing elsewhere on the host. The bytes written are the
// ones the resolver hashed (ImportFile.content), never a second read.
func writeImports(root string, set ImportSet, lock *Lock) (string, error) {
	if len(set.Files) == 0 {
		return "", nil
	}
	tmp, err := os.MkdirTemp(root, ".tmp-imp-")
	if err != nil {
		return "", fmt.Errorf("bundle: imports temp dir: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	r, err := os.OpenRoot(tmp)
	if err != nil {
		return "", fmt.Errorf("bundle: imports root: %w", err)
	}
	defer r.Close()
	for _, f := range set.Files {
		rel := strings.TrimPrefix(f.Box, "/")
		if !filepath.IsLocal(rel) {
			return "", fmt.Errorf("bundle: import %q has no safe in-box path", f.Box)
		}
		if err := r.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
			return "", fmt.Errorf("bundle: import dir for %s: %w", f.Box, err)
		}
		out, err := r.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("bundle: import %s: %w", f.Box, err)
		}
		_, werr := out.Write(f.content)
		if cerr := out.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", fmt.Errorf("bundle: import %s: %w", f.Box, werr)
		}
		lock.Imports = append(lock.Imports, LockImport{Box: f.Box, SHA256: f.SHA256})
	}
	ok = true
	return tmp, nil
}
