package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/statusline"
)

// boxStatusLineCommand runs the bundled script, pointing it at the bundled
// delegate file. A box strips CLAUDE_CONFIG_DIR and mounts the bundle read-only,
// so the per-account delegate file the host-account flow uses cannot exist here.
const boxStatusLineCommand = "AGENTBOX_STATUSLINE_DELEGATE_FILE=" + BoxBundleDir + "/" + delegateName +
	" " + BoxBundleDir + "/" + statusline.Name

// applyStatusLine makes every box run agent-box's statusLine.
//
// Why not pass the host's through: its command names a host-only path, so in a
// box it printed nothing AND never wrote the rate-limits file the sidebar reads.
// The bundled script writes $HOME/.claude/agent-box-rate-limits.json inside the
// box and calls the host command as a delegate; when that command does not work
// here the script prints its own footer instead of a blank one.
//
// settings is the keep-list-filtered map and is edited in place. The delegate is
// written only for a command-type host statusLine that is not already our
// script (a delegate pointing at the script would only recurse).
func (m *Manager) applyStatusLine(tmp string, settings map[string]any, lock *Lock) error {
	host, had := settings["statusLine"]
	settings["statusLine"] = map[string]any{"type": "command", "command": boxStatusLineCommand}

	sum, err := writeBundleFile(filepath.Join(tmp, statusline.Name), statusline.Script, 0o755)
	if err != nil {
		return err
	}
	lock.Files[statusline.Name] = sum

	if !had {
		return nil
	}
	lock.Dropped = append(lock.Dropped, Drop{Kind: "settings", Name: "statusLine",
		Reason: "replaced: the box runs agent-box's statusLine, which calls yours if it works in the box"})
	cmd := delegateCommand(host)
	if cmd == "" {
		return nil
	}
	sum, err = writeBundleFile(filepath.Join(tmp, delegateName), []byte(cmd), 0o644)
	if err != nil {
		return err
	}
	lock.Files[delegateName] = sum
	return nil
}

// delegateCommand is the host statusLine command worth calling from the box, or
// "" for a statusLine of another type, a blank command, or our own script.
func delegateCommand(host any) string {
	sl, ok := host.(map[string]any)
	if !ok || sl["type"] != "command" {
		return ""
	}
	cmd, _ := sl["command"].(string)
	if strings.TrimSpace(cmd) == "" || statusline.IsOwnCommand(cmd) {
		return ""
	}
	return cmd
}

// writeBundleFile writes one bundle file at an exact mode, whatever the umask,
// and returns its sha256 for Lock.Files. The destination is a fresh temp dir
// this process just created and nothing else writes into it.
func writeBundleFile(path string, data []byte, mode os.FileMode) (string, error) {
	if err := os.WriteFile(path, data, mode); err != nil {
		return "", err
	}
	if err := os.Chmod(path, mode); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
