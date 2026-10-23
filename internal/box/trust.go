package box

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// maxClaudeStateSize caps how much of a box home's .claude.json the daemon reads.
// The agent grows that file (one entry per project, usage counters), so the cap
// is generous; a file past it is left alone, and the dialog shows as before.
const maxClaudeStateSize = 32 << 20

// trustTempPrefix names the temp file a merge writes before renaming it over
// .claude.json. A random suffix follows, so a stray one from a crash never
// blocks the next merge.
const trustTempPrefix = ".claude.json.ab-trust-"

// trustWorkspace marks workspace as trusted in the box home's .claude.json, so
// Claude Code skips its "Do you trust this folder?" dialog there. That dialog
// defaults to "No, exit": in the web terminal one stray Enter killed a freshly
// spawned boxed session. Inside a box the repo's hooks and settings run with the
// box's own, contained power, so the dialog protects nothing. Host-tier homes
// never reach this code and keep their dialog.
//
// The key is projects[<in-box cwd>].hasTrustDialogAccepted, measured on Claude
// Code 2.1.292. The workspace is mounted at its own path, so the resolved host
// path is the in-box one.
//
// It merges: every other key stays byte for byte. Anything it does not fully
// understand (not a regular file, too large, not JSON, a projects value or entry
// that is not an object) is left alone and reported. The caller logs that and
// spawns anyway. The rename is atomic, so the agent never sees half a file.
// Callers run it only while the box's container is absent, so no agent can
// rewrite the file underneath it.
func trustWorkspace(root *os.Root, workspace string) error {
	const name = ".claude.json"
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (%v)", name, fi.Mode().Type())
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxClaudeStateSize+1))
	f.Close()
	if err != nil {
		return err
	}
	if len(raw) > maxClaudeStateSize {
		return fmt.Errorf("%s is larger than %d bytes", name, maxClaudeStateSize)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if top == nil {
		return fmt.Errorf("%s is not a JSON object", name)
	}
	projects := map[string]json.RawMessage{}
	if p, ok := top["projects"]; ok {
		if err := unmarshalObject(p, &projects); err != nil {
			return fmt.Errorf("%s: projects: %w", name, err)
		}
	}
	entry := map[string]json.RawMessage{}
	if e, ok := projects[workspace]; ok {
		if err := unmarshalObject(e, &entry); err != nil {
			return fmt.Errorf("%s: projects[%q]: %w", name, workspace, err)
		}
	}
	var accepted bool
	if json.Unmarshal(entry["hasTrustDialogAccepted"], &accepted) == nil && accepted {
		return nil // already trusted: leave the agent's file untouched
	}

	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	if projects[workspace], err = json.Marshal(entry); err != nil {
		return err
	}
	if top["projects"], err = json.Marshal(projects); err != nil {
		return err
	}
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return replaceFile(root, name, append(out, '\n'))
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
