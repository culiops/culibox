package wsroots

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/culiops/culibox/internal/datacompat"
)

// FileName is the root list, under the data root. Only CuliBox writes it.
const FileName = "workspace-roots.json"

const fileVersion = 1

// ErrCorrupt means workspace-roots.json exists but cannot be trusted. It is
// never rewritten: the user fixes or removes it.
var ErrCorrupt = errors.New("workspace-roots.json is damaged")

// newerError is the Load error for a file from a newer CuliBox. It is still
// ErrCorrupt, so callers that keep the list read-only on ErrCorrupt are
// unchanged, but its text is the datacompat message, not "damaged".
type newerError struct{ err error }

func (e newerError) Error() string { return e.err.Error() }
func (e newerError) Unwrap() error { return e.err }
func (e newerError) Is(target error) bool {
	return target == ErrCorrupt
}

// Root is one folder, beyond home, that a box workspace and Files may reach.
type Root struct {
	// Path is absolute and symlink-resolved at add time.
	Path string `json:"path"`
	// Added is when the root was added.
	Added time.Time `json:"added"`
	// AddedBy is the audit actor string of whoever added it.
	AddedBy string `json:"added_by"`
	// LastUsed is when a session last started under the root; zero = never.
	LastUsed time.Time `json:"last_used,omitzero"`
}

type fileFormat struct {
	Version int    `json:"version"`
	Roots   []Root `json:"roots"`
}

// Load reads the list. A missing file is (nil, false, nil). A bad file is
// (nil, true, ErrCorrupt) wrapped with its path; a file from a newer
// CuliBox is also datacompat.ErrNewer, with the newer message.
func Load(dataDir string) (list []Root, exists bool, err error) {
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
	// Probe the version before the typed decode: a newer CuliBox may have
	// changed the other fields' types, and that must read as "newer", not damaged.
	var probe struct{ Version int }
	if json.Unmarshal(b, &probe) == nil && probe.Version > fileVersion {
		return nil, true, newerError{datacompat.Newer(path, fmt.Sprintf("version %d, this build understands %d", probe.Version, fileVersion))}
	}
	var ff fileFormat
	if err := json.Unmarshal(b, &ff); err != nil {
		return nil, true, corrupt(err.Error())
	}
	if ff.Version != fileVersion {
		return nil, true, corrupt(fmt.Sprintf("unknown version %d", ff.Version))
	}
	for _, r := range ff.Roots {
		if !filepath.IsAbs(r.Path) {
			return nil, true, corrupt(fmt.Sprintf("path %q is not absolute", r.Path))
		}
	}
	if ff.Roots == nil {
		ff.Roots = []Root{}
	}
	return ff.Roots, true, nil
}

// Save writes the list atomically. It goes through os.Root with an O_EXCL temp
// file so a symlink planted in the data dir is never followed (same pattern as
// vmshares.Save).
func Save(dataDir string, list []Root) error {
	if list == nil {
		list = []Root{}
	}
	b, err := json.Marshal(fileFormat{Version: fileVersion, Roots: list})
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
