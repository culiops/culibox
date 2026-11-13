package boxvm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/culiops/culibox/internal/vmshares"
)

// Mount is one host folder the VM shares.
type Mount struct {
	Location string
	Writable bool
}

// DataMounts are the only parts of the data root the VM sees, in this order.
// vm/, docker/ and profiles.json are never VM-visible.
func DataMounts(root string) []Mount {
	return []Mount{
		{filepath.Join(root, "boxstate"), true},
		{filepath.Join(root, "bundle"), false},
		{filepath.Join(root, "broker"), false},
		{filepath.Join(root, "ca"), false},
	}
}

// Mounts are what the VM shares: the four data-root subfolders, then the
// user's shares sorted by path. The home is not mounted.
func (c Config) Mounts() []Mount {
	out := DataMounts(c.Root)
	shares := append([]vmshares.Share(nil), c.Shares...)
	sort.Slice(shares, func(i, j int) bool { return shares[i].Path < shares[j].Path })
	for _, s := range shares {
		out = append(out, Mount{s.Path, s.Mode == vmshares.RW})
	}
	return out
}

// EnsureDataMountDirs creates the four data-root subfolders: virtiofs needs the
// source to exist before the VM starts.
func EnsureDataMountDirs(root string) error {
	for _, m := range DataMounts(root) {
		if err := os.MkdirAll(m.Location, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// ReadMounts parses the mounts: block renderYAML wrote. It is a strict line
// parser, not a YAML library: the writer refuses `"` and `\`, so the exact
// shape `  - location: "<p>"` / `    writable: <bool>` is all there is. Any
// other shape is an error, so a hand-edited file never reads as a smaller
// mount list than the VM really has.
func ReadMounts(yamlPath string) ([]Mount, error) {
	b, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}
	bad := func(n int, why string) ([]Mount, error) {
		return nil, fmt.Errorf("%s line %d: %s", yamlPath, n, why)
	}
	lines := strings.Split(string(b), "\n")
	i := 0
	for i < len(lines) && lines[i] != "mounts:" {
		i++
	}
	if i == len(lines) {
		return nil, errors.New(yamlPath + ": no mounts: block")
	}
	var out []Mount
	for i++; i < len(lines); i++ {
		l := lines[i]
		if l == "provision:" {
			// colima reads an empty list as "mount the home folder read-write".
			if len(out) == 0 {
				return bad(i, "empty mounts: block")
			}
			return out, nil
		}
		loc, ok := strings.CutPrefix(l, `  - location: "`)
		if !ok || !strings.HasSuffix(loc, `"`) || strings.ContainsAny(strings.TrimSuffix(loc, `"`), `"\`) {
			return bad(i+1, "not a quoted mount location")
		}
		loc = strings.TrimSuffix(loc, `"`)
		if loc == "" {
			return bad(i+1, "empty mount location")
		}
		// What colima would env-expand or lima template-render is not the
		// folder the VM got: never read it back as applied.
		if err := vmshares.YAMLSafe(loc); err != nil {
			return bad(i+1, err.Error())
		}
		if i+1 >= len(lines) {
			return bad(i+1, "missing writable line")
		}
		i++
		var w bool
		switch lines[i] {
		case "    writable: true":
			w = true
		case "    writable: false":
		default:
			return bad(i+1, "expected writable: true or false")
		}
		out = append(out, Mount{loc, w})
	}
	return nil, errors.New(yamlPath + ": mounts: block does not end with provision:")
}

// ErrMountsIncomplete means the written colima.yaml is not one CuliBox wrote:
// it lacks the four data mounts and does not share the home folder either.
var ErrMountsIncomplete = errors.New("the box VM's colima.yaml does not hold the folders CuliBox always shares; it was edited or cut short, so its folder list is not trusted")

// checkApplied is nil when ms is a list CuliBox could have written: the four
// data mounts (and maybe shares), or, for a VM set up by an older CuliBox, a
// mount that covers the home folder. Anything else, a truncated or hand-edited
// file, must never read as an applied list.
func checkApplied(c Config, ms []Mount) error {
	for _, m := range ms {
		if vmshares.Within(c.Home, m.Location, c.Fold) {
			return nil
		}
	}
	for _, d := range DataMounts(c.Root) {
		found := false
		for _, m := range ms {
			if vmshares.Within(m.Location, d.Location, c.Fold) && vmshares.Within(d.Location, m.Location, c.Fold) {
				found = true
				break
			}
		}
		if !found {
			return ErrMountsIncomplete
		}
	}
	return nil
}
