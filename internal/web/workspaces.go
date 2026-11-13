package web

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/culiops/culibox/internal/box"
)

// Workspace is a selectable working directory (home + its subfolders).
type Workspace struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	// Boxable reports whether this directory can be the workspace of a contained
	// session. It is not decoration: the workspace is a box's only writable
	// mount, so choosing one IS choosing the containment boundary. Offering a
	// choice that always fails is what made the picker confusing.
	Boxable bool   `json:"boxable"`
	Reason  string `json:"reason,omitempty"` // why not, when Boxable is false
}

// UnboxableDefaultCwd reports why a directory cannot be a contained workspace.
// It mirrors the daemon-side refusal in the box package (including
// box.MountUnsafe and box.ProtectedOverlap, shared so the two cannot drift) so
// the UI and the startup check explain the same rule. protected are the
// directories a workspace must not overlap (data root, config, profiles).
// shares is the live VM folder list (macOS), nil without a VM: a directory the
// VM does not share read & write cannot be a workspace, so it is refused too.
func UnboxableDefaultCwd(dir, home string, protected []box.ProtectedDir, shares box.SharesFunc) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if reason := box.MountUnsafe(abs); reason != "" {
		return reason, true
	}
	h := filepath.Clean(home)
	if real, err := filepath.EvalSymlinks(h); err == nil {
		h = real
	}
	if abs == string(filepath.Separator) {
		return "it is the filesystem root", true
	}
	if abs == h {
		return "a box here would mount your whole home, including ~/.ssh and your secrets", true
	}
	if rel, err := filepath.Rel(abs, h); err == nil && rel != "." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return "it contains your home directory", true
	}
	if reason := box.ProtectedOverlap(abs, protected); reason != "" {
		return reason, true
	}
	if shares != nil {
		v, err := shares()
		if err != nil {
			return "cannot read the box VM's folder list (" + err.Error() + "); run `culibox doctor`", true
		}
		if err := v.Check(abs, true); err != nil {
			var ns *box.NotSharedError
			if errors.As(err, &ns) && !ns.Pending && !ns.ReadOnly {
				return notSharedReason, true
			}
			return err.Error(), true
		}
	}
	return "", false
}

// notSharedReason is the short why of a folder the box VM does not share.
const notSharedReason = "not shared with the box VM: add it in Settings › VM folders"

// listWorkspaces returns home first, then each non-hidden subdirectory of home
// (depth 1, symlinked dirs included), sorted by name. Only home's children are
// listed — no arbitrary path traversal is exposed. A directory overlapping a
// protected directory is listed but not boxable.
func listWorkspaces(home string, protected []box.ProtectedDir, shares box.SharesFunc) []Workspace {
	// Home stays listed — it is a legitimate trusted-host workspace — but it can
	// never be boxed, so it is labelled rather than silently offered.
	out := []Workspace{{
		Label:   "~ (home)",
		Path:    home,
		Boxable: false,
		Reason:  "a box here would mount your whole home, including ~/.ssh and your secrets",
	}}
	entries, err := os.ReadDir(home)
	if err != nil {
		return out // still offer home even if it can't be listed
	}
	var dirs []Workspace
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // hidden
		}
		full := filepath.Join(home, name)
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		reason, bad := UnboxableDefaultCwd(full, home, protected, shares)
		dirs = append(dirs, Workspace{Label: name, Path: full, Boxable: !bad, Reason: reason})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Label < dirs[j].Label })
	return append(out, dirs...)
}
