package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// One conversations folder per (instance, workspace, account, profile), shared by
// every box of that profile in that folder (#330). The box name keys a home by the
// whole scope (bindings, egress, an elevated session's id), so a scope change or an
// elevatable connection gives a session a new home; Claude Code's /resume lists
// only the conversations in its own projects dir. Mounting this folder over that
// dir lets the conversations follow the profile while the rest of the home (caches,
// credential files) stays per scope.

// conversationsDirName holds the folders, beside the homes in boxstate/. It is not
// a box name, so reclaim never considers it: conversations are never reclaimed.
const conversationsDirName = ".conversations"

// ConversationsBox is where a box's conversations folder is mounted: the projects
// dir of the box home, read-write.
const ConversationsBox = boxHome + "/" + conversationsRel

// ConversationsKey names the conversations folder of a profile in a workspace:
// hex(sha256(instance \0 workspace \0 account \0 profile))[:12]. workspace is the
// real (symlink-resolved) path, as for the box name. The account is normalised
// like nameFor's: "" and "default" are the same account. Nothing else enters it,
// so a binding, egress or elevate change keeps the folder.
func ConversationsKey(instance, workspace, account, profile string) string {
	if account == "default" { // account.Default; box does not import account
		account = ""
	}
	sum := sha256.Sum256([]byte(instance + "\x00" + workspace + "\x00" + account + "\x00" + profile))
	return hex.EncodeToString(sum[:6])
}

// ValidConversationsKey reports whether key has ConversationsKey's shape: 12
// lowercase hex digits, so it can be joined into a path.
func ValidConversationsKey(key string) bool {
	if len(key) != 12 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ConversationsDir is the host folder of key under stateRoot. key must pass
// ValidConversationsKey.
func ConversationsDir(stateRoot, key string) string {
	return filepath.Join(stateRoot, stateDirName, conversationsDirName, key)
}

// ConversationsRoot is the folder holding every conversations folder.
func ConversationsRoot(stateRoot string) string {
	return filepath.Join(stateRoot, stateDirName, conversationsDirName)
}

// ensureConversationsDir creates key's folder (and .conversations) as 0700
// directories owned by the daemon user, and returns its path. Both are made and
// checked through an os.Root on boxstate with Lstat, so a symlink or a file where
// either belongs is refused, never followed: the leaf is mounted read-write into
// boxes, and a box writes in it freely. What is already in the folder stays.
func ensureConversationsDir(stateRoot, key string) (string, error) {
	if !ValidConversationsKey(key) {
		return "", fmt.Errorf("box: conversations key %q is not 12 lowercase hex digits", key)
	}
	state := filepath.Join(stateRoot, stateDirName)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return "", fmt.Errorf("box: creating %q: %w", state, err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return "", fmt.Errorf("box: opening %q: %w", state, err)
	}
	defer root.Close()
	for _, rel := range []string{conversationsDirName, conversationsDirName + "/" + key} {
		// Lstat first: Mkdir through a symlinked parent that stays inside the
		// root would be followed.
		fi, err := root.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			if err = root.Mkdir(rel, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("box: creating conversations folder %q: %w", rel, err)
			}
			fi, err = root.Lstat(rel)
		}
		if err != nil {
			return "", fmt.Errorf("box: checking conversations folder %q: %w", rel, err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("box: conversations folder %q is not a plain directory (%s)", rel, fi.Mode().Type())
		}
	}
	return ConversationsDir(stateRoot, key), nil
}

// ConversationsKeyOf is the key of the conversations folder box name mounts, as
// the last ensure of it found or made it: "" for a box that mounts none (one
// from before #330, a spawn with no profile) or one this process does not know.
// A session reads it right after Wrap: its conversations are written there, and
// the box is counted live then, so nothing replaces it in between.
func (m *Manager) ConversationsKeyOf(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conv[name]
}

// conversationsKeyAt is the key of the conversations folder at host path src
// under stateRoot, or "" when src is not one.
func conversationsKeyAt(stateRoot, src string) string {
	key := filepath.Base(src)
	if !ValidConversationsKey(key) || !sameDir(filepath.Dir(src), ConversationsRoot(stateRoot)) {
		return ""
	}
	return key
}

// splitUnder splits ms into the mounts outside the in-box dir base and those
// inside it (below it, not base itself). New slices, in order.
func splitUnder(ms []Mount, base string) (outside, inside []Mount) {
	for _, mt := range ms {
		rel, err := filepath.Rel(base, filepath.Clean(mt.Box))
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inside = append(inside, mt)
			continue
		}
		outside = append(outside, mt)
	}
	return outside, inside
}

// ensureConversationsMount prepares box name's home and the conversations
// folder convDir for the folder's mount, before the container exists:
//   - the mountpoint, the home's projects dir, is a plain directory made by the
//     daemon user (docker would make it as root). A symlink or a file there, on
//     it or on .claude, is refused, like a rigged mountpoint in ensureMountpoints:
//     docker would resolve the mount target through it.
//   - inside are the mounts that land inside the folder (clone mode's memory
//     dir): their mountpoints are made in the folder, through an os.Root on it.
//     Other live boxes of the profile write in that folder, so what is found at
//     a mountpoint is not trusted: every component of its path must be a real
//     directory and the mountpoint itself of the bind's type (a directory, or a
//     regular file for a file bind), all read with Lstat. One that is not is
//     never handed to docker: an Optional mount is dropped (returned, and
//     logged), a required one refuses the spawn.
//
// What is left: another live box of the profile can still swap a checked
// mountpoint for a link between this check and docker's mount. Docker resolves
// the mount target inside the new container's own root, so the worst case is a
// mount at another path inside that container, never on the host.
func (m *Manager) ensureConversationsMount(name, convDir string, inside []Mount) ([]Mount, error) {
	home, err := os.OpenRoot(m.stateDir(name))
	if err != nil {
		return nil, fmt.Errorf("box: opening state dir of %s: %w", name, err)
	}
	defer home.Close()
	for _, rel := range []string{".claude", conversationsRel} {
		fi, err := home.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			if err = home.Mkdir(rel, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, fmt.Errorf("box: creating mountpoint %q: %w", rel, err)
			}
			fi, err = home.Lstat(rel)
		}
		if err != nil {
			return nil, fmt.Errorf("box: checking mountpoint %q: %w", rel, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%w: box %s: %s in its home is not a plain directory (%s), so the conversations folder cannot be mounted there; remove it from %s",
				ErrBoxUnavailable, name, rel, fi.Mode().Type(), m.stateDir(name))
		}
	}
	if len(inside) == 0 {
		return nil, nil
	}
	conv, err := os.OpenRoot(convDir)
	if err != nil {
		return nil, fmt.Errorf("box: opening conversations folder: %w", err)
	}
	defer conv.Close()
	var dropped []Mount
	for _, mt := range inside {
		err := ensureMountpointsUnder(conv, ConversationsBox, []Mount{mt})
		if err == nil {
			err = plainMountpoint(conv, mt)
		}
		if err == nil {
			continue
		}
		if !mt.Optional {
			return nil, fmt.Errorf("%w: box %s: %v", ErrBoxUnavailable, name, err)
		}
		log.Printf("box: %s: optional mount at %s left out: %v", name, mt.Box, err)
		dropped = append(dropped, mt)
	}
	return dropped, nil
}

// plainMountpoint checks, through root (mounted at ConversationsBox), that every
// component of mt's mountpoint is a real directory and the mountpoint itself is
// of mt's type: a directory for a directory bind, else a regular file. Lstat
// throughout, so a link is never taken for either.
func plainMountpoint(root *os.Root, mt Mount) error {
	rel, err := filepath.Rel(ConversationsBox, filepath.Clean(mt.Box))
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(p)
		if err != nil {
			return fmt.Errorf("mountpoint %q: %w", p, err)
		}
		last := i == len(parts)-1
		wantDir := !last
		if last {
			hfi, herr := os.Stat(mt.Host)
			wantDir = herr == nil && hfi.IsDir()
		}
		if wantDir && !fi.IsDir() || !wantDir && !fi.Mode().IsRegular() {
			return fmt.Errorf("mountpoint %q is not a plain %s (%s)", p, map[bool]string{true: "directory", false: "file"}[wantDir], fi.Mode().Type())
		}
	}
	return nil
}

// reuseConversations judges the conversations folder of a running box, the way
// reuseBrokerDir judges its broker dir: reuseMounts compares destinations only,
// and every profile's folder mounts at the same one. It answers the state ensure
// continues from and the key of the folder the box mounts.
//
//   - No folder mounted (a box from before #330): reuseMounts already removed it
//     when idle and the spawn asks for one; live, it is reused and its sessions
//     keep writing in the home ("").
//   - The folder asked for, or the spawn asks for none: reused.
//   - Another folder (two profiles that share a box) and live: reused, never
//     refused, since sharing the box used to mean sharing the home. The key
//     returned is that folder's, where the session's conversations will be.
//   - Another folder and idle: removed, to be recreated with the asked one.
//
// A source that cannot be read refuses the spawn. Caller holds opMu.
func (m *Manager) reuseConversations(ctx context.Context, name string, have []string, want string) (State, string, error) {
	if !slices.ContainsFunc(have, func(d string) bool { return filepath.Clean(d) == ConversationsBox }) {
		return StateRunning, "", nil
	}
	src, err := m.opts.Client.BindSource(ctx, name, ConversationsBox)
	if err != nil {
		return StateRunning, "", fmt.Errorf("%w: box %s: reading its conversations mount: %v", ErrBoxUnavailable, name, err)
	}
	got := conversationsKeyAt(m.opts.StateRoot, src)
	if want == "" || got == want {
		return StateRunning, got, nil
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live > 0 {
		return StateRunning, got, nil
	}
	if err := m.removeIdle(ctx, name); err != nil {
		return StateRunning, "", fmt.Errorf("box: removing idle %s to recreate it with this profile's conversations folder: %w", name, err)
	}
	return StateMissing, "", nil
}

// AdoptHook is told, before adoptConversations moves a conversation (a file or a
// conversation's folder) out of a home, the host path it is moved from and the
// one it is moved to. An error stops the adoption before that move. The daemon
// carries analytics' ingest offsets this way (internal/box does not import
// analytics), so a moved transcript is not counted again.
type AdoptHook func(from, to string) error

// SetAdoptHook sets the hook adoptConversations calls; nil, the default, moves
// with nobody told.
func (m *Manager) SetAdoptHook(f AdoptHook) {
	m.mu.Lock()
	m.adopt = f
	m.mu.Unlock()
}

// maxAdoptEntries caps how many directory entries one adoption reads, across the
// home's projects dir and its workspace folders (a variable so a test can lower
// it). Past it the rest stays in the home, for the next container created.
var maxAdoptEntries = 10000

// adoptConversations moves what box name's home holds in its projects dir into
// the conversations folder convDir, so the folder's mount does not hide it from
// /resume (#330, D3). ensure calls it only while the box has no container, so
// nothing writes in the home meanwhile; boxes of the same profile in other
// scopes may be writing in the folder, so nothing there is trusted.
//
// Each entry of <projects>/<slug>/ moves to <convDir>/<slug>/ when the folder has
// no such name: a regular file is hard-linked there and then unlinked from the
// home (a link never replaces a name), a real directory (a conversation's
// subagents and tool results) is renamed (a rename never replaces a non-empty
// directory). Both run on directory descriptors, one opened through an os.Root
// on the home and one through an os.Root on the folder, so no path is resolved
// again between the check and the move. Types come from the directory listing,
// which does not follow links: a symlink, a FIFO or anything else stays in the
// home, and so does a slug that is not a real directory. The hook, when set, is
// told before each move; its error stops the adoption there. At most
// maxAdoptEntries entries are read; ctx bounds it too. Nothing here fails the
// spawn: what is not moved stays on disk in the home.
func (m *Manager) adoptConversations(ctx context.Context, name, convDir string) {
	m.mu.Lock()
	hook := m.adopt
	m.mu.Unlock()
	homeDir := m.stateDir(name)
	home, err := os.OpenRoot(homeDir)
	if err != nil {
		return
	}
	defer home.Close()
	conv, err := os.OpenRoot(convDir)
	if err != nil {
		log.Printf("box: %s: conversations not adopted: %v", name, err)
		return
	}
	defer conv.Close()
	a := adoption{ctx: ctx, hook: hook, home: home, conv: conv,
		from: filepath.Join(homeDir, filepath.FromSlash(conversationsRel)), to: convDir}
	var slugs []string
	err = eachEntry(ctx, home, conversationsRel, func(e fs.DirEntry) bool {
		if a.read++; a.read > maxAdoptEntries {
			a.cut = true
			return false
		}
		if e.IsDir() {
			slugs = append(slugs, e.Name())
		} else {
			a.kept++
		}
		return true
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.err = err
	}
	for _, slug := range slugs {
		if a.err != nil || a.cut {
			break
		}
		a.slug(slug)
	}
	if a.err != nil {
		log.Printf("box: %s: adopting conversations into %s stopped: %v", name, convDir, a.err)
	}
	if a.moved > 0 || a.kept > 0 || a.cut {
		more := ""
		if a.cut {
			more = fmt.Sprintf(", and more past the %d-entry cap (moved on a later start)", maxAdoptEntries)
		}
		log.Printf("box: %s: moved %d conversation file(s) or folder(s) from its home into %s; %d left in %s (the name is taken in the folder, or not a plain file or folder)%s",
			name, a.moved, convDir, a.kept, a.from, more)
	}
}

// adoption is one adoptConversations pass.
type adoption struct {
	ctx        context.Context
	hook       AdoptHook
	home, conv *os.Root
	from, to   string // the home's projects dir and the folder, on the host
	read       int    // entries read, against maxAdoptEntries
	moved      int
	kept       int
	cut        bool  // the cap was reached
	err        error // stops the pass
}

// slug moves the entries of one workspace folder of the home.
func (a *adoption) slug(slug string) {
	src, err := a.home.Open(conversationsRel + "/" + slug)
	if err != nil {
		a.kept++
		return
	}
	defer src.Close()
	if fi, err := src.Stat(); err != nil || !fi.IsDir() {
		a.kept++
		return
	}
	if err := a.conv.Mkdir(slug, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		a.kept++
		return
	}
	if fi, err := a.conv.Lstat(slug); err != nil || !fi.IsDir() {
		a.kept++ // a box of this profile put something else there
		return
	}
	dst, err := a.conv.Open(slug)
	if err != nil {
		a.kept++
		return
	}
	defer dst.Close()
	if fi, err := dst.Stat(); err != nil || !fi.IsDir() {
		a.kept++
		return
	}
	// The names first, then the moves: the listing is not read while it changes.
	var ents []fs.DirEntry
	for {
		if err := a.ctx.Err(); err != nil {
			a.err = err
			return
		}
		chunk, err := src.ReadDir(readDirChunk)
		for _, e := range chunk {
			if a.read++; a.read > maxAdoptEntries {
				a.cut = true
				break
			}
			ents = append(ents, e)
		}
		if a.cut || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.kept += len(ents)
			return
		}
	}
	sfd, dfd := int(src.Fd()), int(dst.Fd())
	for _, e := range ents {
		if err := a.ctx.Err(); err != nil {
			a.err = err
			return
		}
		n := e.Name()
		file, dir := e.Type().IsRegular(), e.Type().IsDir()
		var st unix.Stat_t
		if (!file && !dir) || unix.Fstatat(dfd, n, &st, unix.AT_SYMLINK_NOFOLLOW) != unix.ENOENT {
			a.kept++
			continue
		}
		if a.hook != nil {
			if err := a.hook(filepath.Join(a.from, slug, n), filepath.Join(a.to, slug, n)); err != nil {
				a.err = err
				return
			}
		}
		if file {
			if err := unix.Linkat(sfd, n, dfd, n, 0); err != nil {
				a.kept++
				continue
			}
			if err := unix.Unlinkat(sfd, n, 0); err != nil {
				// Moved all the same: the folder's copy is the one the box sees.
				log.Printf("box: conversation %s copied into %s but not removed from the home: %v", n, a.to, err)
			}
		} else if err := unix.Renameat(sfd, n, dfd, n); err != nil {
			a.kept++
			continue
		}
		a.moved++
	}
}

// ConversationsStats is what ConversationsUsage found.
type ConversationsStats struct {
	Folders int   // conversations folders (names with a key's shape)
	Bytes   int64 // their size: regular files only, no link followed
	Sized   bool  // false when the walk did not finish
}

// ConversationsUsage counts the conversations folders under stateRoot and
// their size, for doctor. Read-only; no link is followed (WalkDir does not
// descend one, and a link's own size is not counted) and ctx bounds the walk:
// cut short, it returns what it has with ctx's error and Sized false.
func ConversationsUsage(ctx context.Context, stateRoot string) (ConversationsStats, error) {
	var st ConversationsStats
	dir := ConversationsRoot(stateRoot)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return st, err
		}
		return st, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return st, err
	}
	st.Sized = true
	for _, e := range ents {
		if !e.IsDir() || !ValidConversationsKey(e.Name()) {
			continue
		}
		st.Folders++
		err := filepath.WalkDir(filepath.Join(dir, e.Name()), func(_ string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				st.Bytes += fi.Size()
			}
			return nil
		})
		if err != nil {
			st.Sized = false
			return st, err
		}
	}
	return st, ctx.Err()
}
