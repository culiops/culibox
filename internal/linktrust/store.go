// Package linktrust is the per-person list of hosts whose links CuliBox opens
// from an agent without the "Open this link?" dialog (#348). A host is trusted
// only by an explicit tick in that dialog, and each row belongs to one actor
// (the audit actor string), so a second user of one server keeps a list of
// their own. The list is a plain file under the data root that only CuliBox
// writes; every change is audited before it is saved.
package linktrust

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

// FileName is the trust list, under the data root. Only CuliBox writes it.
const FileName = "link-trust.json"

const fileVersion = 1

// ErrCorrupt means link-trust.json exists but cannot be trusted. It is never
// rewritten: the user fixes or removes it.
var ErrCorrupt = errors.New("link-trust.json is damaged")

// newerError is the Load error for a file from a newer CuliBox. It is still
// ErrCorrupt, so callers that keep the list read-only on ErrCorrupt are
// unchanged, but its text is the datacompat message, not "damaged".
type newerError struct{ err error }

func (e newerError) Error() string { return e.err.Error() }
func (e newerError) Unwrap() error { return e.err }
func (e newerError) Is(target error) bool {
	return target == ErrCorrupt
}

// Host is one trusted host of one actor.
type Host struct {
	// Actor is the audit actor string of whoever trusted the host; the row is
	// theirs alone.
	Actor string `json:"actor"`
	// Host is lowercase ASCII (punycode never trusted), with no port or dot at
	// the end; see Validate.
	Host string `json:"host"`
	// Subdomains: the host's subdomains are trusted too.
	Subdomains bool `json:"subdomains"`
	// Added is when the host was first trusted.
	Added time.Time `json:"added"`
}

type fileFormat struct {
	Version int    `json:"version"`
	Hosts   []Host `json:"hosts"`
}

// Load reads the list, every actor's rows. A missing file is (nil, false, nil).
// A bad file is (nil, true, ErrCorrupt) wrapped with its path; a file from a
// newer CuliBox is also datacompat.ErrNewer, with the newer message. A row
// with no actor, a host Validate refuses, or a second row for the same actor
// and host makes the file damaged: a row that skips the confirm dialog is
// never half-trusted.
func Load(dataDir string) (list []Host, exists bool, err error) {
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
	type key struct{ actor, host string }
	seen := make(map[key]bool, len(ff.Hosts))
	for _, h := range ff.Hosts {
		if h.Actor == "" {
			return nil, true, corrupt("a row has no actor")
		}
		if err := Validate(h.Host); err != nil {
			return nil, true, corrupt(fmt.Sprintf("host %q: %v", h.Host, err))
		}
		k := key{h.Actor, h.Host}
		if seen[k] {
			return nil, true, corrupt(fmt.Sprintf("host %q is listed twice for one actor", h.Host))
		}
		seen[k] = true
	}
	if ff.Hosts == nil {
		ff.Hosts = []Host{}
	}
	return ff.Hosts, true, nil
}

// Save writes the list atomically. It goes through os.Root with an O_EXCL temp
// file so a symlink planted in the data dir is never followed (same pattern as
// wsroots.Save).
func Save(dataDir string, list []Host) error {
	if list == nil {
		list = []Host{}
	}
	b, err := json.Marshal(fileFormat{Version: fileVersion, Hosts: list})
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
