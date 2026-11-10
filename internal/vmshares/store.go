package vmshares

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the share list, under the data root. Only agent-box writes it.
const FileName = "vm-shares.json"

const fileVersion = 1

// ErrCorrupt means vm-shares.json exists but cannot be trusted. It is never
// rewritten: the user fixes or removes it.
var ErrCorrupt = errors.New("vm-shares.json is damaged")

type fileFormat struct {
	Version int     `json:"version"`
	Shares  []Share `json:"shares"`
}

// Load reads the list. A missing file is (nil, false, nil). A bad file is
// (nil, true, ErrCorrupt) wrapped with its path.
func Load(dataDir string) (list []Share, exists bool, err error) {
	path := filepath.Join(dataDir, FileName)
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer root.Close()
	f, err := root.Open(FileName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, true, err
	}
	corrupt := func(why string) error { return fmt.Errorf("%w: %s: %s", ErrCorrupt, path, why) }
	var ff fileFormat
	if err := json.Unmarshal(b, &ff); err != nil {
		return nil, true, corrupt(err.Error())
	}
	if ff.Version != fileVersion {
		return nil, true, corrupt(fmt.Sprintf("unknown version %d", ff.Version))
	}
	for _, s := range ff.Shares {
		if !filepath.IsAbs(s.Path) {
			return nil, true, corrupt(fmt.Sprintf("path %q is not absolute", s.Path))
		}
		if s.Mode != RW && s.Mode != RO {
			return nil, true, corrupt(fmt.Sprintf("mode %q for %q is not rw or ro", s.Mode, s.Path))
		}
	}
	if ff.Shares == nil {
		ff.Shares = []Share{}
	}
	return ff.Shares, true, nil
}

// Save writes the list atomically. It goes through os.Root with an O_EXCL temp
// file so a symlink planted in the data dir is never followed (same pattern as
// boxedws).
func Save(dataDir string, list []Share) error {
	if list == nil {
		list = []Share{}
	}
	b, err := json.Marshal(fileFormat{Version: fileVersion, Shares: list})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := fmt.Sprintf("%s.tmp.%d", FileName, os.Getpid())
	_ = root.Remove(tmp) // leftover from a crashed write by this pid
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, FileName); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}
