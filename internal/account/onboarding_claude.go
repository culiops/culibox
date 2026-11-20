package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

const (
	claudeStateName = ".claude.json"
	// claudeStateMax is the largest .claude.json read, the same cap as the box
	// side's maxClaudeStateSize. Claude Code's own file stays far below it.
	claudeStateMax = 8 << 20
)

// ensureOnboarded marks Claude Code's first-run setup as done in the account's
// own .claude.json (cr is the account's config dir), so a token-only account
// does not open on the welcome screens. It sets hasCompletedOnboarding to true
// and adds theme "dark" only when no theme is set; every other key keeps its
// value. Folder trust is not touched. changed reports whether it wrote.
//
// A running Claude Code rewrites this file all the time, so the file is only
// written when the flag is missing or not true: once the account is set up,
// provision never touches it again. It returns an error for every reason it
// left the file alone (not a regular file, too big, not a JSON object, I/O);
// the caller only logs it.
func ensureOnboarded(cr *os.Root) (changed bool, err error) {
	created, err := createClaudeState(cr)
	if err != nil {
		return false, err
	}
	if created {
		return true, nil
	}
	// state that is already there: merged below, never replaced

	top, err := readClaudeState(cr)
	if err != nil {
		return false, err
	}
	var done bool
	if raw, ok := top["hasCompletedOnboarding"]; ok {
		_ = json.Unmarshal(raw, &done)
	}
	if done {
		return false, nil
	}
	top["hasCompletedOnboarding"] = json.RawMessage("true")
	if _, ok := top["theme"]; !ok {
		top["theme"] = json.RawMessage(`"dark"`)
	}
	b, err := encodeClaudeState(top)
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(cr, claudeStateName, b, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// createClaudeState writes the seed as a new .claude.json and reports whether
// it did. The seed goes to a temp file first (0600, O_EXCL, fsynced) and is then
// hard-linked into place, so a crash or a reader never sees an empty or partial
// .claude.json. link(2) fails with EEXIST when the name exists in any form, a
// symlink included, so a file Claude Code created a moment ago is never
// replaced: created is false then, with no error. The temp file is always
// removed.
func createClaudeState(cr *os.Root) (created bool, err error) {
	b, err := encodeClaudeState(map[string]json.RawMessage{
		"hasCompletedOnboarding": json.RawMessage("true"),
		"theme":                  json.RawMessage(`"dark"`),
	})
	if err != nil {
		return false, err
	}
	const tmp = claudeStateName + ".seed.tmp"
	_ = cr.Remove(tmp) // a leftover of a crash would fail the O_EXCL open below
	f, err := cr.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	defer cr.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync() // durable before the link
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if err := cr.Link(tmp, claudeStateName); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// readClaudeState reads .claude.json as a JSON object. It accepts only a
// regular file of at most claudeStateMax bytes, the rules of the box side's
// mergeClaudeState.
func readClaudeState(cr *os.Root) (map[string]json.RawMessage, error) {
	fi, err := cr.Lstat(claudeStateName)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%v)", claudeStateName, fi.Mode().Type())
	}
	// O_NONBLOCK, and the type checked again on the open file: a FIFO swapped in
	// after the Lstat would otherwise block this open forever.
	f, err := cr.OpenFile(claudeStateName, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err = f.Stat(); err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%v)", claudeStateName, fi.Mode().Type())
	}
	raw, err := io.ReadAll(io.LimitReader(f, claudeStateMax+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > claudeStateMax {
		return nil, fmt.Errorf("%s is larger than %d bytes", claudeStateName, claudeStateMax)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("%s: %w", claudeStateName, err)
	}
	if top == nil {
		return nil, fmt.Errorf("%s is not a JSON object", claudeStateName)
	}
	return top, nil
}

func encodeClaudeState(top map[string]json.RawMessage) ([]byte, error) {
	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
