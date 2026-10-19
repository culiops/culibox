package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/culiops/agent-box/internal/box"
)

// Drop kinds and the fixed reasons the plugin and bundle paths record. The
// strings are user-facing and pinned by tests, so they live here as constants
// the session layer reuses instead of retyping.
const (
	KindPlugins = "plugins"
	KindBundle  = "bundle"

	ReasonPluginsIncomplete = "the host plugin dir is incomplete; plugins load only with installed_plugins.json, known_marketplaces.json, cache/ and marketplaces/ all present"
	ReasonPluginSymlink     = "is a symlink on the host; agent-box mounts only real files and directories"
	ReasonPluginCredentials = "a plugin marketplace URL carries credentials; agent-box never mounts credentials into a box"
	ReasonBundleFailed      = "the config bundle could not be built; this box runs without your CLAUDE.md, skills, MCP servers, settings and plugins"
)

// pluginEntry is one part of the host plugins/ dir a box needs.
type pluginEntry struct {
	name string
	dir  bool
}

// pluginEntries are exactly the parts of plugins/ that resolve which plugins
// load, in mount order. data/, store/ and the catalog caches are deliberately
// absent: they are host state a box has no business reading.
var pluginEntries = []pluginEntry{
	{"installed_plugins.json", false},
	{"known_marketplaces.json", false},
	{"cache", true},
	{"marketplaces", true},
}

// credentialFiles are the two small json files that can name a marketplace URL.
var credentialFiles = []string{"installed_plugins.json", "known_marketplaces.json"}

// Caps on what a snapshot may hold. Vars so a test can shrink them. Exceeding
// either fails closed: nothing is published, and the set is dropped as
// incomplete, so a snapshot is never partial.
var (
	pluginCapEntries       = 100_000
	pluginCapBytes   int64 = 512 << 20
)

var errPluginCap = errors.New("bundle: plugin set exceeds its size cap")

// pluginSet is the one verdict about the host plugin set. The bundle's source
// hash and settings and the provider's mounts and drops all take it from here,
// so they cannot disagree.
type pluginSet struct {
	mounts []box.Mount
	drops  []Drop
	// token fingerprints the snapshot the mounts point into; "" when nothing is
	// mounted. It feeds the bundle hash.
	token string
}

// pluginSnapshot decides whether the plugin set is mounted into a box and, if
// so, publishes a content-addressed COPY of it under root and points the mounts
// there.
//
// All or nothing. Measured 2026-10-18: a partial set "failed to load", and only
// all four entries enable plugins. And nothing at the top may be a symlink:
// Docker resolves a symlinked bind source on the HOST, as root, so a planted
// cache -> ~/.ssh would publish its target into the box. That is why this uses
// Lstat and refuses rather than following.
//
// No installed_plugins.json (or no plugins/ at all) means the user simply has
// no plugins: no mounts and no drop. Present-but-unusable is a drop, so the
// loss is a named decision rather than silence. Any Lstat failure other than
// "does not exist" (EACCES, EIO) fails closed.
//
// Why a copy and not a live bind of the host dirs: marketplaces are git clones,
// and a clone made with a token keeps it in .git/config (a remote URL, an
// http.extraheader, a credential line, a submodule's config). The set goes into
// a box that has network egress, which the bundle rule "allowlisted,
// secret-free" forbids. A scan for such credentials is a blocklist with gaps
// (depth, submodules, URL spellings, non-URL secrets) and cannot even be
// trusted for the next second, since a live mount shows whatever the host
// writes later. So the snapshot omits every .git dir and .git file outright,
// and what is mounted is exactly what was copied.
//
// Each entry is mounted ONCE, at the box home. claude rebases the absolute
// installPath recorded in installed_plugins.json onto its own config dir
// (measured 2026-10-19), so a second mount at the host path is unneeded.
//
// There is no garbage collection: like the bundle dirs, old snapshots stay. A
// plugin change costs one new copy (about 38 MB on this host).
func pluginSnapshot(src, root string) pluginSet {
	pdir := filepath.Join(src, "plugins")
	incomplete := pluginSet{drops: []Drop{{Kind: KindPlugins, Name: "*", Reason: ReasonPluginsIncomplete}}}

	fi, err := os.Lstat(pdir)
	if errors.Is(err, fs.ErrNotExist) {
		return pluginSet{} // no plugins
	}
	if err != nil {
		return incomplete
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return pluginSet{drops: []Drop{{Kind: KindPlugins, Name: "plugins", Reason: ReasonPluginSymlink}}}
	}
	if !fi.IsDir() {
		return pluginSet{}
	}
	if _, err := os.Lstat(filepath.Join(pdir, pluginEntries[0].name)); errors.Is(err, fs.ErrNotExist) {
		return pluginSet{} // no installed_plugins.json: the user has no plugins
	}

	var symlinks []Drop
	wrong := false
	for _, e := range pluginEntries {
		fi, err := os.Lstat(filepath.Join(pdir, e.name))
		switch {
		case err != nil:
			wrong = true
		case fi.Mode()&os.ModeSymlink != 0:
			symlinks = append(symlinks, Drop{Kind: KindPlugins, Name: e.name, Reason: ReasonPluginSymlink})
		case e.dir && !fi.IsDir(), !e.dir && !fi.Mode().IsRegular():
			// A bind of the wrong type is a docker mount error; a FIFO would also
			// block a reader. Either way the set is unusable.
			wrong = true
		}
	}
	if len(symlinks) > 0 {
		return pluginSet{drops: symlinks} // a symlink names itself; the rest is moot
	}
	if wrong {
		return incomplete
	}

	snap, token, err := publishSnapshot(pdir, root)
	if err != nil {
		return incomplete
	}
	// Checked in the COPY, which no one can change, rather than the host source.
	if entry, found, err := snapshotCredentials(snap); err != nil {
		return incomplete
	} else if found {
		return pluginSet{drops: []Drop{{Kind: KindPlugins, Name: entry, Reason: ReasonPluginCredentials}}}
	}
	var mounts []box.Mount
	for _, e := range pluginEntries {
		mounts = append(mounts, box.Mount{
			Host:     filepath.Join(snap, e.name),
			Box:      path.Join(boxHome, ".claude", "plugins", e.name),
			Optional: true,
		})
	}
	return pluginSet{mounts: mounts, token: token}
}

const snapshotMarker = ".complete"

// budget enforces the snapshot caps while walking.
type budget struct {
	entries int
	bytes   int64
}

func (b *budget) add(fi fs.FileInfo) error {
	b.entries++
	if fi.Mode().IsRegular() {
		b.bytes += fi.Size()
	}
	if b.entries > pluginCapEntries || b.bytes > pluginCapBytes {
		return errPluginCap
	}
	return nil
}

// walkPluginTree visits the four entries and everything below them, parents
// before children, in name order. It never follows a symlink: every node is
// Lstat'd, and a symlink is visited as a symlink. Any directory or file named
// .git is skipped, as is anything that is not a regular file, directory or
// symlink (FIFO, socket, device).
func walkPluginTree(r *os.Root, b *budget, visit func(rel string, fi fs.FileInfo) error) error {
	for _, e := range pluginEntries {
		fi, err := r.Lstat(e.name)
		if err != nil {
			return err
		}
		if e.dir != fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle: plugin entry %s changed type", e.name)
		}
		if err := walkNode(r, e.name, fi, b, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkNode(r *os.Root, rel string, fi fs.FileInfo, b *budget, visit func(string, fs.FileInfo) error) error {
	if err := b.add(fi); err != nil {
		return err
	}
	if !fi.IsDir() {
		return visit(rel, fi)
	}
	if err := visit(rel, fi); err != nil {
		return err
	}
	d, err := r.Open(rel)
	if err != nil {
		return err
	}
	defer d.Close()
	if st, err := d.Stat(); err != nil || !os.SameFile(st, fi) {
		return fmt.Errorf("bundle: %s changed while it was read", rel)
	}
	ents, err := d.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, ent := range ents {
		if ent.Name() == ".git" {
			continue // dir, or the gitlink file of a worktree or submodule
		}
		child := filepath.Join(rel, ent.Name())
		cfi, err := r.Lstat(child)
		if err != nil {
			return err
		}
		if m := cfi.Mode(); !m.IsRegular() && !m.IsDir() && m&os.ModeSymlink == 0 {
			continue
		}
		if err := walkNode(r, child, cfi, b, visit); err != nil {
			return err
		}
	}
	return nil
}

// describe folds one node into the fingerprint: path, kind, and what changes
// when the node does. A symlink contributes its target string, not its target.
func describe(h io.Writer, r *os.Root, rel string, fi fs.FileInfo) error {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := r.Readlink(rel)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00l\x00%s\n", rel, target)
	case fi.IsDir():
		fmt.Fprintf(h, "%s\x00d\n", rel)
	default:
		fmt.Fprintf(h, "%s\x00f\x00%d\x00%d\x00%t\n", rel, fi.Size(), fi.ModTime().UnixNano(), fi.Mode()&0o100 != 0)
	}
	return nil
}

func fingerprint(h interface{ Sum([]byte) []byte }) string {
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// publishSnapshot returns the snapshot dir for the current plugin tree and its
// fingerprint, copying the tree first if no complete snapshot for it exists.
//
// The directory is keyed by a fingerprint of the plugin tree ONLY, so editing a
// skill never recopies it. Publishing follows materialize: build under .tmp-*
// in root, write the completion marker last, rename into place.
func publishSnapshot(pdir, root string) (dir, token string, err error) {
	src, err := os.OpenRoot(pdir)
	if err != nil {
		return "", "", err
	}
	defer src.Close()

	h := sha256.New()
	if err := walkPluginTree(src, &budget{}, func(rel string, fi fs.FileInfo) error {
		return describe(h, src, rel, fi)
	}); err != nil {
		return "", "", err
	}
	token = fingerprint(h)
	if final := filepath.Join(root, "plugins-"+token); snapshotComplete(final, token) {
		return final, token, nil
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	tmp, err := os.MkdirTemp(root, ".tmp-plugins-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	dst, err := os.OpenRoot(tmp)
	if err != nil {
		return "", "", err
	}
	defer dst.Close()

	// The copy walk computes its own fingerprint, and the snapshot is named for
	// THAT: if the host changed between the two walks, the name still describes
	// exactly what was copied.
	h2 := sha256.New()
	if err := walkPluginTree(src, &budget{}, func(rel string, fi fs.FileInfo) error {
		if err := describe(h2, src, rel, fi); err != nil {
			return err
		}
		return copyNode(src, dst, rel, fi)
	}); err != nil {
		return "", "", err
	}
	token = fingerprint(h2)
	if err := dst.WriteFile(snapshotMarker, []byte(token+"\n"), 0o600); err != nil {
		return "", "", err
	}
	final := filepath.Join(root, "plugins-"+token)
	if snapshotComplete(final, token) { // a concurrent Ensure published it first
		return final, token, nil
	}
	_ = os.RemoveAll(final) // an incomplete leftover
	if err := os.Rename(tmp, final); err != nil {
		return "", "", fmt.Errorf("bundle: publishing %s: %w", final, err)
	}
	return final, token, nil
}

func snapshotComplete(dir, token string) bool {
	b, err := os.ReadFile(filepath.Join(dir, snapshotMarker))
	return err == nil && strings.TrimSpace(string(b)) == token
}

// copyNode recreates one node in dst. Every write goes through os.Root, so a
// planted symlink cannot redirect it (plain MkdirAll/WriteFile follow links).
func copyNode(src, dst *os.Root, rel string, fi fs.FileInfo) error {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		// Recreated as a link with the same target string, not followed. A link
		// inside the tree resolves in the box's mount namespace, where the host's
		// home does not exist, so it cannot reach a host file.
		target, err := src.Readlink(rel)
		if err != nil {
			return err
		}
		return dst.Symlink(target, rel)
	case fi.IsDir():
		return dst.Mkdir(rel, 0o755)
	}
	in, err := src.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	if st, err := in.Stat(); err != nil || !os.SameFile(st, fi) || !st.Mode().IsRegular() {
		return fmt.Errorf("bundle: %s changed while it was copied", rel)
	}
	// Permission bits are reduced to 0644/0755: setuid, setgid and sticky never
	// cross, and the read-only mount enforces the rest.
	perm := os.FileMode(0o644)
	if fi.Mode()&0o100 != 0 {
		perm = 0o755
	}
	out, err := dst.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, fi.Size()+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != fi.Size() {
		return fmt.Errorf("bundle: %s changed while it was copied", rel)
	}
	return nil
}

// maxCredFile bounds the json files read for the credential check.
const maxCredFile = 1 << 20

// snapshotCredentials reads the two json files that can name a marketplace URL,
// in the snapshot. entry is the file the credential was found in, never the URL.
// A file that cannot be vouched for (not a regular file, unreadable, too big) is
// an error, so the caller fails closed.
func snapshotCredentials(snap string) (entry string, found bool, err error) {
	for _, name := range credentialFiles {
		p := filepath.Join(snap, name)
		fi, err := os.Lstat(p)
		if err != nil {
			return "", false, err
		}
		if !fi.Mode().IsRegular() {
			return "", false, fmt.Errorf("bundle: %s is not a regular file", name)
		}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return "", false, err
		}
		b, err := io.ReadAll(io.LimitReader(f, maxCredFile+1))
		f.Close()
		if err != nil || len(b) > maxCredFile {
			return "", false, fmt.Errorf("bundle: %s cannot be checked", name)
		}
		if hasURLCredentials(b) {
			return name, true, nil
		}
	}
	return "", false, nil
}

// urlAuthority matches scheme://authority@ where the authority runs to the first
// / ? # quote or space, so an @ later in a path or query is not userinfo.
var urlAuthority = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*)://([^/?#\s"'\\<>]*)@`)

// hasURLCredentials is the rule for a URL in a json file. Any non-empty userinfo
// is a credential for EVERY scheme (http, https, git+https, ftp, ...: a token is
// spelled user@ or user:pass@ and an allowlist of "http-like" schemes has
// differentials), with one exception: ssh and git+ssh with no password, where
// git@ is just the account name. So https://tok@h, git+https://tok@h, ftp://u@h
// and ssh://git:pw@h are refused; ssh://git@h/o/r, scp-style git@h:o/r.git (no
// scheme) and an empty https://@h are not.
func hasURLCredentials(b []byte) bool {
	for _, m := range urlAuthority.FindAllSubmatch(b, -1) {
		scheme, userinfo := strings.ToLower(string(m[1])), string(m[2])
		if userinfo == "" {
			continue
		}
		if (scheme == "ssh" || scheme == "git+ssh") && !strings.Contains(userinfo, ":") {
			continue
		}
		return true
	}
	return false
}
