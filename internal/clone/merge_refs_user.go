package clone

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

// The user's own Claude Code config runs repo files too (#370): the servers and
// hooks it names are resolved against the project, and a box commit that
// changes what they run is as trusted as a change to the config. This file reads
// that config with the same scanner as the project's (relative paths resolve
// against the repo top, hooks run with the project as their working folder):
//
//   - <home>/.claude.json: mcpServers (user scope) and projects[<clone_of>].mcpServers;
//   - <home>/.claude/settings.json and settings.local.json;
//   - the managed settings (mergeManagedSettings), its managed-settings.d/*.json
//     drop-ins and the managed-mcp.json beside it.
//
// Unlike the host's work tree, these are the user's files and the merge cannot
// do without them, so it fails closed: a file that is there and cannot be read,
// is past its size limit or does not parse refuses the merge. A file that is
// not there is nothing. The config dir Claude Code reads from
// $CLAUDE_CONFIG_DIR, and a home that is not the daemon's, are not followed.

const (
	mergeUserJSONBytes     = 32 << 20 // ~/.claude.json, which keeps a project's history, is often over 1 MiB
	mergeUserSettingsBytes = mergeImportBytes
)

// mergeManagedSettings is the managed Claude Code settings file of this OS. A
// var so a test can point it elsewhere.
var mergeManagedSettings = managedSettingsPath(runtime.GOOS)

// managedSettingsPath is where Claude Code reads managed settings on goos; ""
// for an OS it has no managed path on here.
func managedSettingsPath(goos string) string {
	switch goos {
	case "linux":
		return "/etc/claude-code/managed-settings.json"
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	}
	return ""
}

// scanUser reads the user's Claude Code config into the sink. home == "" skips
// the files in the home; the managed settings are read either way.
func (s *refSink) scanUser(home string) error {
	if home != "" {
		if err := s.userClaudeJSON(filepath.Join(home, ".claude.json")); err != nil {
			return err
		}
		for _, f := range []string{"settings.json", "settings.local.json"} {
			if err := s.userSettings(filepath.Join(home, ".claude", f)); err != nil {
				return err
			}
		}
	}
	if mergeManagedSettings != "" {
		if err := s.userSettings(mergeManagedSettings); err != nil {
			return err
		}
		dropIns, mcp := managedSiblings(mergeManagedSettings)
		if err := s.userManagedDropIns(dropIns); err != nil {
			return err
		}
		return s.userMCPFile(mcp)
	}
	return nil
}

// managedSiblings are the managed drop-in folder and managed-mcp.json that sit
// in the same folder as the managed settings file.
func managedSiblings(settings string) (dropIns, mcp string) {
	dir := filepath.Dir(settings)
	return filepath.Join(dir, "managed-settings.d"), filepath.Join(dir, "managed-mcp.json")
}

// userManagedDropIns reads the managed-settings.d/*.json drop-ins in name order.
// A folder that is not there is nothing; one that is there and cannot be read is
// an error, as it is to Claude Code. Past mergeRefFiles entries the scan fails.
func (s *refSink) userManagedDropIns(dir string) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	} else if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) > mergeRefFiles {
		return errScanCap
	}
	sort.Strings(names)
	for _, n := range names {
		if err := s.userSettings(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}

// userMCPFile reads managed-mcp.json: a JSON object whose mcpServers are read
// like a project's .mcp.json.
func (s *refSink) userMCPFile(p string) error {
	v, found, err := readUserJSON(p)
	if !found || err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: not a JSON object", p)
	}
	s.mcpServers(m["mcpServers"])
	return nil
}

// openUserFile opens p for reading without blocking. A file that is not there
// is (nil, nil); one that is there and is not a regular file within limit bytes
// is an error.
func openUserFile(p string, limit int64) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	switch {
	case err != nil:
		f.Close()
		return nil, err
	case !fi.Mode().IsRegular():
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", p)
	case fi.Size() > limit:
		f.Close()
		return nil, fmt.Errorf("%s is over %d MiB", p, limit>>20)
	}
	return f, nil
}

// readUserJSON reads and parses a user file; found is false when it is not there.
func readUserJSON(p string) (v any, found bool, err error) {
	f, err := openUserFile(p, mergeUserSettingsBytes)
	if f == nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, mergeUserSettingsBytes+1))
	if err != nil {
		return nil, true, err
	}
	if len(data) > mergeUserSettingsBytes {
		return nil, true, fmt.Errorf("%s is over %d MiB", p, mergeUserSettingsBytes>>20)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, true, fmt.Errorf("%s: %w", p, err)
	}
	return v, true, nil
}

// userSettings reads a Claude Code settings file like the project's.
func (s *refSink) userSettings(p string) error {
	v, _, err := readUserJSON(p)
	if m, ok := v.(map[string]any); ok {
		scanClaudeSettingsDoc(s, m)
	}
	return err
}

// userClaudeJSON reads the servers of ~/.claude.json: the user-scope ones and
// those of this repo's project entry. The file is decoded as a stream and only
// those two members are kept, so the rest of a large file is never held.
func (s *refSink) userClaudeJSON(p string) error {
	f, err := openUserFile(p, mergeUserJSONBytes)
	if f == nil {
		return err
	}
	defer f.Close()
	keys := projectKeys(s.of)
	dec := json.NewDecoder(io.LimitReader(f, mergeUserJSONBytes+1))
	wrap := func(err error) error { return fmt.Errorf("%s: %w", p, err) }
	if t, err := dec.Token(); err != nil {
		return wrap(err)
	} else if d, ok := t.(json.Delim); !ok || d != '{' {
		return wrap(errors.New("not a JSON object"))
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return wrap(err)
		}
		switch key, _ := t.(string); key {
		case "mcpServers":
			var v any
			if err := dec.Decode(&v); err != nil {
				return wrap(err)
			}
			s.mcpServers(v)
		case "projects":
			if err := s.userProjects(dec, keys); err != nil {
				return wrap(err)
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return wrap(err)
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace: a cut-off file is not one
		return wrap(err)
	}
	return nil
}

// userProjects reads the "projects" member: the servers of the entries whose
// key is one of keys.
func (s *refSink) userProjects(dec *json.Decoder, keys []string) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return skipRest(dec, t) // null, an array: no project entries
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		if !matchesProject(key, keys) {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return err
			}
			continue
		}
		var entry struct {
			McpServers any `json:"mcpServers"`
		}
		if err := dec.Decode(&entry); err != nil {
			return err
		}
		s.mcpServers(entry.McpServers)
	}
	_, err = dec.Token()
	return err
}

// skipRest reads the rest of the value that began with t.
func skipRest(dec *json.Decoder, t json.Token) error {
	if d, ok := t.(json.Delim); !ok || (d != '{' && d != '[') {
		return nil
	}
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// projectKeys are the spellings of the repo's path a project entry may be
// keyed by: the path as given and the one it resolves to.
func projectKeys(of string) []string {
	keys := []string{filepath.Clean(of)}
	if r, err := filepath.EvalSymlinks(of); err == nil && r != keys[0] {
		keys = append(keys, filepath.Clean(r))
	}
	return keys
}

// matchesProject reports whether a project key names one of keys, ignoring a
// trailing slash and case (more refusals on a case-insensitive file system, never
// fewer).
func matchesProject(key string, keys []string) bool {
	k := filepath.Clean(strings.TrimSpace(key))
	for _, want := range keys {
		if strings.EqualFold(k, want) {
			return true
		}
	}
	return false
}
