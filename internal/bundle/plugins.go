package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"sync"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/box"
)

// Drop kinds and the fixed reasons the plugin and bundle paths record. The
// strings are user-facing and pinned by tests, so they live here as constants
// the session layer reuses instead of retyping.
const (
	KindPlugins = "plugins"
	KindBundle  = "bundle"

	ReasonPluginsIncomplete = "the host plugin dir is incomplete; plugins load only with installed_plugins.json, known_marketplaces.json, cache/ and marketplaces/ all present"
	ReasonPluginSymlink     = "is a symlink on the host; CuliBox mounts only real files and directories"
	ReasonPluginCredentials = "a plugin marketplace URL carries credentials; CuliBox never mounts credentials into a box"
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

// pluginSlot is one snapshot dir: entries copied, keyed and reused together.
type pluginSlot struct {
	name    string
	entries []pluginEntry
}

// pluginSlots splits the set so each part is recopied only when IT changes.
// Host claude rewrites known_marketplaces.json (its lastUpdated) on routine
// marketplace checks even when nothing else moved; with one fingerprint over
// the whole set, each rewrite recopied cache/ and marketplaces/ (tens of MB)
// into a new dir. cache/ and marketplaces/ therefore each get their own slot.
// The two json files share one: together they are a few KB, so copying both
// when either changes costs nothing, they are what the credential check reads,
// and one dir per json pair halves the dirs that churn leaves behind.
var pluginSlots = []pluginSlot{
	{"json", pluginEntries[:2]},
	{"cache", pluginEntries[2:3]},
	{"marketplaces", pluginEntries[3:4]},
}

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
// so they cannot disagree. Only whether mounts is empty reaches the bundle hash.
type pluginSet struct {
	mounts []box.Mount
	drops  []Drop
}

// pluginSnapshot decides whether the plugin set is mounted into a box and, if
// so, publishes a content-addressed COPY of each part of it under root and
// points the mounts there.
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
// Each slot (see pluginSlots) is its own snapshot, keyed by its own
// fingerprint, so a json rewrite costs a few KB and only a change under cache/
// or marketplaces/ recopies that tree. The set is still all or nothing: every
// slot must publish and pass, or nothing is mounted.
//
// A change to cache/ or marketplaces/ costs one new copy of that tree (about 40
// MB together on this host); a json rewrite costs one small dir. Old ones go by
// Manager.GC. No bundle dir names a snapshot: every Ensure gets its snapshots from
// here, by the host's fingerprint, so removing an old one breaks no kept bundle.
func pluginSnapshot(src, root string, guard snapshotGuard) pluginSet {
	pdir, fi, verdict, ok := pluginGate(src)
	if !ok {
		return verdict
	}
	incomplete := pluginSet{drops: []Drop{{Kind: KindPlugins, Name: "*", Reason: ReasonPluginsIncomplete}}}
	hosts, err := publishSnapshot(pdir, fi, root, guard)
	if err != nil {
		return incomplete
	}
	// Checked in the COPY, which no one can change, rather than the host source.
	if entry, found, err := snapshotCredentials(hosts); err != nil {
		return incomplete
	} else if found {
		return pluginSet{drops: []Drop{{Kind: KindPlugins, Name: entry, Reason: ReasonPluginCredentials}}}
	}
	var mounts []box.Mount
	for _, e := range pluginEntries {
		mounts = append(mounts, box.Mount{
			Host:     hosts[e.name],
			Box:      path.Join(boxHome, ".claude", "plugins", e.name),
			Optional: true,
		})
	}
	return pluginSet{mounts: mounts}
}

// snapshotGuard keeps a snapshot from Manager.GC between a spawn taking it and
// the spawn's container existing, the same way Manager.reuse does for a bundle.
type snapshotGuard struct {
	mu    *sync.Mutex
	touch func(path string, t time.Time) error
}

// pluginGate is the part of the verdict that needs no copy. ok means the host set
// is usable, with pdir and fi to fingerprint it from; otherwise verdict is final.
func pluginGate(src string) (pdir string, fi fs.FileInfo, verdict pluginSet, ok bool) {
	pdir = filepath.Join(src, "plugins")
	incomplete := pluginSet{drops: []Drop{{Kind: KindPlugins, Name: "*", Reason: ReasonPluginsIncomplete}}}

	fi, err := os.Lstat(pdir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, pluginSet{}, false // no plugins
	}
	if err != nil {
		return "", nil, incomplete, false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", nil, pluginSet{drops: []Drop{{Kind: KindPlugins, Name: "plugins", Reason: ReasonPluginSymlink}}}, false
	}
	if !fi.IsDir() {
		return "", nil, pluginSet{}, false
	}
	if _, err := os.Lstat(filepath.Join(pdir, pluginEntries[0].name)); errors.Is(err, fs.ErrNotExist) {
		return "", nil, pluginSet{}, false // no installed_plugins.json: the user has no plugins
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
		return "", nil, pluginSet{drops: symlinks}, false // a symlink names itself; the rest is moot
	}
	if wrong {
		return "", nil, incomplete, false
	}
	return pdir, fi, pluginSet{}, true
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

// walkPluginTree visits entries and everything below them, parents before
// children, in name order. It never follows a symlink: every node is
// Lstat'd, and a symlink is visited as a symlink. Any directory or file named
// .git is skipped, as is anything that is not a regular file, directory or
// symlink (FIFO, socket, device).
func walkPluginTree(r *os.Root, entries []pluginEntry, b *budget, visit func(rel string, fi fs.FileInfo) error) error {
	for _, e := range entries {
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
		if strings.EqualFold(ent.Name(), ".git") { // APFS/NTFS fold case
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

// slotState is one slot's way through publishSnapshot.
type slotState struct {
	slot  pluginSlot
	token string // the fingerprint, then the copy's own
	used  budget // what the fingerprint walk counted for this slot
	final string
	tmp   string // "" when a complete snapshot already exists
}

func (s *slotState) name(root string) string {
	return filepath.Join(root, "plugins-"+s.slot.name+"-"+s.token)
}

// publishSnapshot returns, for each of the four entries, the path of its copy
// in the snapshot of its slot, copying a slot first if no complete snapshot
// for its current fingerprint exists.
//
// Each slot is keyed by a fingerprint of its own entries ONLY, so editing a
// skill never recopies anything and a json rewrite never recopies cache/ or
// marketplaces/. Publishing follows materialize: build under .tmp-* in root,
// write the completion marker last, rename into place.
//
// All or nothing across slots: every slot to copy is built first, and only when
// all of them built (and the caps held over the whole set) is any renamed into
// place; on any failure every temp dir is removed and nothing new is published.
func publishSnapshot(pdir string, checked fs.FileInfo, root string, guard snapshotGuard) (map[string]string, error) {
	src, states, err := fingerprintSlots(pdir, checked, root)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	// The copy budget starts with what the reused slots hold, so the published
	// set as a whole stays under the caps even if the host grows between walks.
	var cp budget
	var todo []*slotState
	for _, st := range states {
		reused, err := reuseSnapshot(st, guard)
		if err != nil {
			return nil, err
		}
		if reused {
			cp.entries += st.used.entries
			cp.bytes += st.used.bytes
			continue
		}
		todo = append(todo, st)
	}
	if len(todo) > 0 {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
	}
	defer func() {
		for _, st := range todo {
			if st.tmp != "" {
				_ = os.RemoveAll(st.tmp) // a no-op once renamed away
			}
		}
	}()
	for _, st := range todo {
		if st.tmp, err = os.MkdirTemp(root, ".tmp-plugins-"); err != nil {
			return nil, err
		}
		if err := copySlot(src, st, &cp); err != nil {
			return nil, err
		}
		st.final = st.name(root)
	}
	if len(todo) > 0 {
		guard.mu.Lock()
		defer guard.mu.Unlock()
	}
	for _, st := range todo {
		if err := publishSlot(st.tmp, st.final, st.token); err != nil {
			return nil, err
		}
	}

	hosts := map[string]string{}
	for _, st := range states {
		for _, e := range st.slot.entries {
			hosts[e.name] = filepath.Join(st.final, e.name)
		}
	}
	return hosts, nil
}

// fingerprintSlots opens the host set and fingerprints every slot, writing
// nothing. The caller closes the returned root.
func fingerprintSlots(pdir string, checked fs.FileInfo, root string) (*os.Root, []*slotState, error) {
	src, err := os.OpenRoot(pdir)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			src.Close()
		}
	}()
	// pdir was Lstat'd as a real directory before this open; if it was swapped
	// for another directory in between, the verdict no longer describes it.
	if st, err := src.Stat("."); err != nil || !os.SameFile(st, checked) {
		return nil, nil, fmt.Errorf("bundle: %s changed between the check and the open", pdir)
	}

	// Fingerprint every slot under ONE budget, so the caps bound the whole set
	// before anything is copied.
	var fp budget
	states := make([]*slotState, len(pluginSlots))
	for i, sl := range pluginSlots {
		before := fp
		h := sha256.New()
		if err := walkPluginTree(src, sl.entries, &fp, func(rel string, fi fs.FileInfo) error {
			return describe(h, src, rel, fi)
		}); err != nil {
			return nil, nil, err
		}
		st := &slotState{slot: sl, token: fingerprint(h),
			used: budget{fp.entries - before.entries, fp.bytes - before.bytes}}
		st.final = st.name(root)
		states[i] = st
	}
	ok = true
	return src, states, nil
}

// reuseSnapshot reports whether the slot's complete snapshot exists, refreshing
// its date if so: that date is all that keeps GC away until the spawn's container
// exists. A date that cannot be written is an error, because the snapshot would go
// out unprotected.
func reuseSnapshot(st *slotState, guard snapshotGuard) (bool, error) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if !snapshotComplete(st.final, st.token) {
		return false, nil
	}
	if err := guard.touch(st.final, time.Now()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("bundle: refreshing the date of %s: %w", st.final, err)
	}
	return true, nil
}

// copySlot copies a slot's entries into st.tmp and writes its marker. The copy
// walk computes its own fingerprint, and the snapshot is named for THAT
// (st.token is replaced): if the host changed between the two walks, the name
// still describes exactly what was copied.
func copySlot(src *os.Root, st *slotState, b *budget) error {
	dst, err := os.OpenRoot(st.tmp)
	if err != nil {
		return err
	}
	defer dst.Close()
	h := sha256.New()
	if err := walkPluginTree(src, st.slot.entries, b, func(rel string, fi fs.FileInfo) error {
		if err := describe(h, src, rel, fi); err != nil {
			return err
		}
		return copyNode(src, dst, rel, fi)
	}); err != nil {
		return err
	}
	st.token = fingerprint(h)
	return dst.WriteFile(snapshotMarker, []byte(st.token+"\n"), 0o600)
}

// publishSlot renames a built slot into place. Rename onto an existing
// non-empty dir fails, so a sibling's complete snapshot is never replaced. Only
// an incomplete leftover (no matching marker, e.g. from a crash) is cleared,
// and a dir whose marker matches is never removed.
func publishSlot(tmp, final, token string) error {
	err := os.Rename(tmp, final)
	if err != nil && !snapshotComplete(final, token) {
		_ = os.RemoveAll(final)
		err = os.Rename(tmp, final)
	}
	if err != nil && !snapshotComplete(final, token) {
		return fmt.Errorf("bundle: publishing %s: %w", final, err)
	}
	return nil
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
		// Never followed. Recreated, with the same target string, only when it
		// stays inside its own entry. On Linux any link would resolve in the
		// box's mount namespace, where the host's home does not exist; but on
		// macOS the colima file-sharing layer was never checked for following
		// links on the HOST side, so a link that leaves its entry is skipped
		// rather than trusted to it. A skipped link is not a drop: the set still
		// mounts, and the box just lacks that link.
		target, err := src.Readlink(rel)
		if err != nil {
			return err
		}
		if !linkStaysInEntry(src, rel, target) {
			return nil
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

// linkStaysInEntry is true when the link at rel, with target, resolves inside
// the entry rel belongs to (rel's first element): the target is relative, every
// step of it resolved lexically from the link's dir stays inside that entry,
// and no step before the last is itself a link. That last rule is what makes
// the lexical answer the real one: cache/a/hop -> .. is inside, but a link
// "hop/../../x" through it would climb from where hop really points, not from
// where the text says. The source tree is the copy's source and is walked as is
// (static plugin content), so checking it there is checking what is copied.
func linkStaysInEntry(src *os.Root, rel, target string) bool {
	if target == "" || filepath.IsAbs(target) {
		return false
	}
	entry, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	inside := func(p string) bool { return p == entry || strings.HasPrefix(p, entry+string(filepath.Separator)) }
	cur := filepath.Dir(rel)
	steps := strings.Split(filepath.ToSlash(target), "/")
	for i, step := range steps {
		switch step {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
		default:
			cur = filepath.Join(cur, step)
		}
		if !inside(cur) {
			return false
		}
		if i < len(steps)-1 {
			if fi, err := src.Lstat(cur); err != nil || !fi.IsDir() {
				return false // a link, or nothing there: cannot be vouched for
			}
		}
	}
	return true
}

// maxCredFile bounds the json files read for the credential check.
const maxCredFile = 1 << 20

// snapshotCredentials reads the two json files that can name a marketplace URL,
// in the snapshot (hosts maps each entry to its copy). entry is the file the
// credential was found in, never the URL. A file that cannot be vouched for (not
// a regular file, unreadable, too big) is an error, so the caller fails closed.
func snapshotCredentials(hosts map[string]string) (entry string, found bool, err error) {
	for _, name := range credentialFiles {
		p := hosts[name]
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
		if jsonHasURLCredentials(b) {
			return name, true, nil
		}
	}
	return "", false, nil
}

// jsonHasURLCredentials applies hasURLCredentials to doc, failing closed. It is
// true when doc is not valid json (it cannot be vouched for), when the RAW bytes
// carry a credentialed URL (the raw file is what the box can read), or when any
// DECODED string token does: json escapes (https:\/\/tok@h, \u0040, \u002f)
// decode to a credentialed URL that a regex over the raw text never sees. Every
// token is checked, object keys and duplicate keys included, so a value shadowed
// by a later duplicate is not lost the way it is when decoding into a map.
func jsonHasURLCredentials(doc []byte) bool {
	if !json.Valid(doc) || hasURLCredentials(doc) {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		if s, ok := tok.(string); ok && hasURLCredentials([]byte(s)) {
			return true
		}
	}
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
