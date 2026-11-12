package box

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// maxClaudeStateSize caps how much of a box home's .claude.json the daemon reads.
// The agent grows that file (one entry per project, usage counters): measured
// 2026-10-23 at ~50 KB in box homes and 184 KB for a months-old host one. A file
// past the cap is left alone, and the dialog shows as before. The merge runs
// under the box layer's opMu, so the cap also bounds how long a box that pads
// its own file can hold up other spawns.
const maxClaudeStateSize = 8 << 20

// trustTempPrefix names the temp file a merge writes before renaming it over
// .claude.json. A random suffix follows, so a stray one from a crash never
// blocks the next merge.
const trustTempPrefix = ".claude.json.ab-trust-"

// trustWorkspace marks workspace as trusted in the box home's .claude.json, so
// Claude Code skips its "Do you trust this folder?" dialog there. That dialog
// defaults to "No, exit": in the web terminal one stray Enter killed a freshly
// spawned boxed session. It is called only for boxes whose sessions run with the
// config bundle (BoxKey.PreTrust): its --setting-sources user already keeps the
// repo's own settings and hooks from loading, so the dialog guards nothing more
// there. Every other box, and every host-tier home, keeps the dialog.
//
// The key is projects[<in-box cwd>].hasTrustDialogAccepted, measured on Claude
// Code 2.1.292. The workspace is mounted at its own path, so the resolved host
// path is the in-box one.
//
// It merges: every other key and value is kept with the same meaning (the file
// is re-encoded, so key order and whitespace may change). Anything it does not
// fully understand (not a regular file, too large, not JSON, a projects value or entry
// that is not an object) is left alone and reported. The caller logs that and
// spawns anyway. The rename is atomic, so the agent never sees half a file.
// Callers run it only while the box's container is absent, so no agent can
// rewrite the file underneath it.
func trustWorkspace(root *os.Root, workspace string) error {
	return mergeClaudeState(root, func(top map[string]json.RawMessage) (bool, error) {
		projects := map[string]json.RawMessage{}
		if p, ok := top["projects"]; ok {
			if err := unmarshalObject(p, &projects); err != nil {
				return false, fmt.Errorf("%s: projects: %w", claudeStateName, err)
			}
		}
		entry := map[string]json.RawMessage{}
		if e, ok := projects[workspace]; ok {
			if err := unmarshalObject(e, &entry); err != nil {
				return false, fmt.Errorf("%s: projects[%q]: %w", claudeStateName, workspace, err)
			}
		}
		var accepted bool
		if json.Unmarshal(entry["hasTrustDialogAccepted"], &accepted) == nil && accepted {
			return false, nil // already trusted: leave the agent's file untouched
		}

		var err error
		entry["hasTrustDialogAccepted"] = json.RawMessage("true")
		if projects[workspace], err = encodeJSON(entry, ""); err != nil {
			return false, err
		}
		if top["projects"], err = encodeJSON(projects, ""); err != nil {
			return false, err
		}
		return true, nil
	})
}

// claudeStateName is the agent's state file in a box home.
const claudeStateName = ".claude.json"

// mergeClaudeState reads the box home's .claude.json through root, hands its
// top-level object to mutate, and, when mutate reports a change, writes it back
// atomically. It is the one place that decides what a state file the daemon
// will touch has to look like: a regular file of at most maxClaudeStateSize
// bytes holding a JSON object. Anything else is left alone and reported, and
// the caller logs it and spawns anyway. Every key mutate does not touch keeps
// its value (the file is re-encoded, so key order and whitespace may change).
// Callers run it only while the box's container is absent.
func mergeClaudeState(root *os.Root, mutate func(top map[string]json.RawMessage) (changed bool, err error)) error {
	const name = claudeStateName
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (%v)", name, fi.Mode().Type())
	}
	// O_NONBLOCK, and the type checked again on the open file: a FIFO swapped in
	// after the Lstat would otherwise block this open, and opMu with it, forever.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	raw, err := readRegular(f)
	f.Close()
	if err != nil {
		return err
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if top == nil {
		return fmt.Errorf("%s is not a JSON object", name)
	}
	changed, err := mutate(top)
	if err != nil || !changed {
		return err
	}
	out, err := encodeJSON(top, "  ")
	if err != nil {
		return err
	}
	return replaceFile(root, name, append(out, '\n'))
}

// acceptBypassPermissions sets bypassPermissionsModeAccepted in the box home's
// .claude.json, so Claude Code skips its one-time "Bypass Permissions mode"
// warning. That dialog defaults to "No, exit" and nothing answers it in an
// unattended boxed session, which then waits forever with no transcript.
//
// It is called only for a box created for a session that runs with the bypass
// option (BoxKey.PreAcceptBypass). The consent is that option, which the user
// chose for the profile or the spawn; the dialog's own condition, a sandboxed
// container with restricted network access, is what a box is. Host-tier
// sessions never reach it and keep the dialog.
//
// Measured on Claude Code 2.1.311 (the image's pin): with the key the dialog
// does not show, and the agent moves the key on its first start into the box
// home's settings.json as skipDangerousModePermissionPrompt. It merges like
// trustWorkspace, and a value of false is replaced: the session's own option
// is the stronger statement.
func acceptBypassPermissions(root *os.Root) error {
	return mergeClaudeState(root, func(top map[string]json.RawMessage) (bool, error) {
		var accepted bool
		if json.Unmarshal(top["bypassPermissionsModeAccepted"], &accepted) == nil && accepted {
			return false, nil // already accepted: leave the agent's file untouched
		}
		top["bypassPermissionsModeAccepted"] = json.RawMessage("true")
		return true, nil
	})
}

// encodeJSON is json.Marshal (indented when indent != "") that leaves <, > and &
// as they are rather than rewriting them as \u003c and friends in the agent's
// values. Every level goes through it: an escape made at an inner level would
// survive the outer one.
func encodeJSON(v any, indent string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// readRegular reads f, which must be a regular file of at most
// maxClaudeStateSize bytes.
func readRegular(f *os.File) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%v)", fi.Name(), fi.Mode().Type())
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxClaudeStateSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxClaudeStateSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", fi.Name(), maxClaudeStateSize)
	}
	return raw, nil
}

// unmarshalObject decodes raw into dst, refusing anything but a JSON object
// (null included, which would otherwise decode to an empty map).
func unmarshalObject(raw json.RawMessage, dst *map[string]json.RawMessage) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m == nil {
		return errors.New("not a JSON object")
	}
	*dst = m
	return nil
}

// replaceFile writes body to a fresh temp file inside root and renames it over
// name, so a reader sees the old file or the new one, never a partial write.
func replaceFile(root *os.Root, name string, body []byte) error {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := trustTempPrefix + hex.EncodeToString(rnd[:])
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, name)
	}
	if werr != nil {
		_ = root.Remove(tmp)
	}
	return werr
}

// preAcceptBypass opens the box home through an os.Root, as ensureStateDir does,
// and accepts the bypass-permissions warning in it.
func (m *Manager) preAcceptBypass(boxName string) error {
	root, err := os.OpenRoot(m.stateDir(boxName))
	if err != nil {
		return err
	}
	defer root.Close()
	return acceptBypassPermissions(root)
}
