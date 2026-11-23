package box

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
