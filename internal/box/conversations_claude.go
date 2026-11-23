package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
func (m *Manager) ensureConversationsMount(name, convDir string, inside []Mount) error {
	home, err := os.OpenRoot(m.stateDir(name))
	if err != nil {
		return fmt.Errorf("box: opening state dir of %s: %w", name, err)
	}
	defer home.Close()
	for _, rel := range []string{".claude", conversationsRel} {
		fi, err := home.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			if err = home.Mkdir(rel, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("box: creating mountpoint %q: %w", rel, err)
			}
			fi, err = home.Lstat(rel)
		}
		if err != nil {
			return fmt.Errorf("box: checking mountpoint %q: %w", rel, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%w: box %s: %s in its home is not a plain directory (%s), so the conversations folder cannot be mounted there; remove it from %s",
				ErrBoxUnavailable, name, rel, fi.Mode().Type(), m.stateDir(name))
		}
	}
	if len(inside) == 0 {
		return nil
	}
	conv, err := os.OpenRoot(convDir)
	if err != nil {
		return fmt.Errorf("box: opening conversations folder: %w", err)
	}
	defer conv.Close()
	return ensureMountpointsUnder(conv, ConversationsBox, inside)
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
