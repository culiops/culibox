package account

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/statusline"
)

const (
	manifestName = ".agentbox-manifest.json"
	scriptName   = statusline.Name
	// DelegateFileName holds the default account's statusLine command; the
	// embedded script runs it (after harvesting rate limits) so every account's
	// footer matches the default's.
	DelegateFileName = ".agentbox-statusline-delegate"
)

// manifest records what agent-box itself created in an account's config dir.
// Only listed entries are ever removed.
type manifest struct {
	Links []string `json:"links,omitempty"` // symlink names agent-box created
	MCP   bool     `json:"mcp,omitempty"`   // agent-box wrote MCPFileName
	// Delegate: agent-box wrote DelegateFileName (the default account's statusLine command).
	Delegate bool `json:"delegate,omitempty"`
	// Settings: the settings.json keys copied from the default account. Only
	// these are ever removed again.
	Settings []string `json:"settings,omitempty"`
}

// ScriptPath is where the embedded rate-limits statusLine script lives.
func (s *Store) ScriptPath() string { return filepath.Join(s.dataDir, binDir, scriptName) }

// provision makes r's config dir ready. It never clobbers: only entries listed
// in the manifest are ever removed, and every write goes through the Store's
// dataDir root so a planted symlink cannot redirect it. Called with s.mu held,
// or from Open before the Store is shared.
func (s *Store) provision(r Resolved) error {
	if err := s.root.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	cfgRel := filepath.Join(accountsDir, r.Name, "claude")
	if err := s.root.MkdirAll(cfgRel, 0o700); err != nil {
		return err
	}
	if err := s.writeScript(); err != nil {
		return err
	}
	cr, err := s.root.OpenRoot(cfgRel)
	if err != nil {
		return err
	}
	defer cr.Close()
	if err := s.mergeSettings(cr); err != nil {
		return err
	}
	if r.Provider == ProviderClaude {
		// Best effort: without it Claude Code just shows its first-run screens.
		if _, err := ensureOnboarded(cr); err != nil {
			log.Printf("account %q: .claude.json left as it is: %v", r.Name, err)
		}
	}
	return s.syncInherit(cr, r.InheritPersonal)
}

// writeScript extracts the statusLine script, rewriting it only when the
// on-disk bytes differ (a running session may be executing it).
func (s *Store) writeScript() error {
	rel := filepath.Join(binDir, scriptName)
	if cur, err := s.root.ReadFile(rel); err != nil || !bytes.Equal(cur, statusline.Script) {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := writeFileAtomic(s.root, rel, statusline.Script, 0o755); err != nil {
			return err
		}
	}
	return s.root.Chmod(rel, 0o755) // the umask may have narrowed the create mode
}

// mergeSettings sets statusLine to our script only when none is configured, or
// the configured one is already ours. Claude Code runs exactly one statusLine,
// so any other value (another tool's command, or a non-object) is left alone.
func (s *Store) mergeSettings(cr *os.Root) error {
	m := map[string]any{}
	if b, err := cr.ReadFile("settings.json"); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return errors.New("account: settings.json is not valid JSON; fix or remove it")
		}
		if m == nil { // the file held a literal null
			m = map[string]any{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if cur, present := m["statusLine"]; present {
		sl, isObj := cur.(map[string]any)
		if !isObj || sl["command"] != s.ScriptPath() {
			return nil
		}
		if sl["type"] == "command" {
			return nil // already wired
		}
	}
	m["statusLine"] = map[string]any{"type": "command", "command": s.ScriptPath()}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(cr, "settings.json", b, 0o600)
}

func (s *Store) syncInherit(cr *os.Root, on bool) error {
	var man manifest
	if b, err := cr.ReadFile(manifestName); err == nil {
		_ = json.Unmarshal(b, &man)
	}
	if !on {
		for _, n := range man.Links {
			if fi, err := cr.Lstat(n); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				if err := cr.Remove(n); err != nil {
					return err
				}
			}
		}
		if man.MCP {
			if err := cr.Remove(MCPFileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if man.Delegate {
			if err := cr.Remove(DelegateFileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := s.syncSettings(cr, false, &man); err != nil {
			return err
		}
		return writeManifest(cr, manifest{})
	}
	for _, n := range inheritLinkNames(cr) {
		src := filepath.Join(s.defaultDir, n)
		if _, err := os.Stat(src); err != nil {
			continue // nothing to inherit
		}
		if _, err := cr.Lstat(n); err == nil {
			continue // never replace an existing entry
		}
		if err := cr.MkdirAll(filepath.Dir(n), 0o700); err != nil {
			return err
		}
		if err := cr.Symlink(src, n); err != nil {
			return err
		}
		man.Links = appendUnique(man.Links, n)
	}
	// Same shape filter the boxed bundle uses: a server with literal env values,
	// ${VAR} substitution, or a credential in its headers/args/url is dropped
	// rather than copied onto disk.
	src := bundle.DefaultMCPFile(s.defaultDir)
	servers, drops := bundle.FilterMCP(src, hostProber{})
	if mcpSourceUnreadable(src, drops) && man.MCP {
		// The source exists but could not be read or parsed (e.g. mid-write at a
		// restart): keep the last good copy rather than wiping every server.
	} else {
		b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFileAtomic(cr, MCPFileName, b, 0o600); err != nil {
			return err
		}
		man.MCP = true
	}
	if err := s.syncDelegate(cr, &man); err != nil {
		return err
	}
	if err := s.syncSettings(cr, true, &man); err != nil {
		return err
	}
	return writeManifest(cr, man)
}

// pluginEntries are the parts of the default's plugins/ dir that resolve which
// plugins are installed: installed_plugins.json names each plugin's absolute
// install path under cache/, and a plugin only loads if its marketplace is
// known (measured with `claude plugin list`: without the marketplace entries
// it reports "failed to load"). Without these enabledPlugins loads nothing.
var pluginEntries = []string{"installed_plugins.json", "cache", "known_marketplaces.json", "marketplaces"}

// inheritLinkNames is what an inheriting host account links from the default:
// the bundle allowlist plus plugins. plugins is linked for host accounts only (a
// box gets plugins through the bundle provider's read-only mounts, never through
// this path). Linked as a whole so Claude Code's own writes (atomic renames of
// installed_plugins.json) land in the default's dir, not in a diverging copy. If
// the account already has a real plugins dir (marketplaces it fetched itself),
// that dir is kept and only the pluginEntries it lacks are linked into it.
func inheritLinkNames(cr *os.Root) []string {
	names := bundle.AllowlistNames()
	if fi, err := cr.Lstat("plugins"); err == nil && fi.IsDir() {
		for _, e := range pluginEntries {
			names = append(names, filepath.Join("plugins", e))
		}
		return names
	}
	return append(names, "plugins")
}

// defaultStatusLineCommand returns the default account's statusLine command,
// or "" when there is none worth delegating to (missing/invalid settings, not
// a command-type statusLine, empty, or our own script).
func (s *Store) defaultStatusLineCommand() string {
	b, err := os.ReadFile(filepath.Join(s.defaultDir, "settings.json"))
	if err != nil {
		return ""
	}
	var m struct {
		StatusLine struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	c := m.StatusLine.Command
	if m.StatusLine.Type != "command" || strings.TrimSpace(c) == "" || c == s.ScriptPath() {
		return ""
	}
	return c
}

// syncDelegate writes (or, if agent-box wrote it earlier, removes) the delegate
// file for an account that inherits personal config.
func (s *Store) syncDelegate(cr *os.Root, man *manifest) error {
	if cmd := s.defaultStatusLineCommand(); cmd != "" {
		if err := writeFileAtomic(cr, DelegateFileName, []byte(cmd), 0o600); err != nil {
			return err
		}
		man.Delegate = true
		return nil
	}
	if man.Delegate {
		if err := cr.Remove(DelegateFileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		man.Delegate = false
	}
	return nil
}

// hostProber resolves stdio MCP commands against the host PATH: an account
// session runs on the host, so that is where its servers must start.
type hostProber struct{}

func (hostProber) Resolves(cmds []string) map[string]bool {
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		_, err := exec.LookPath(c)
		out[c] = err == nil
	}
	return out
}

func writeManifest(cr *os.Root, man manifest) error {
	b, err := json.Marshal(man)
	if err != nil {
		return err
	}
	return writeFileAtomic(cr, manifestName, b, 0o600)
}

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}

// mcpSourceUnreadable reports whether FilterMCP dropped the whole file ("*")
// although it exists: unreadable or invalid JSON, as opposed to absent.
func mcpSourceUnreadable(path string, drops []bundle.Drop) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	for _, d := range drops {
		if d.Name == "*" {
			return true
		}
	}
	return false
}

// RewriteStatusLinePrefix points each account's statusLine back at the data
// root after the root moved (#255): for every
// <dataDir>/accounts/*/claude/settings.json whose statusLine.command is oldRoot
// or lies below it (a whole path component: <oldRoot>-prod is not touched), it
// rewrites that one field to the same place under newRoot. Other keys and the
// file's mode are kept; a command elsewhere (the user's own) is left alone.
// Idempotent. A missing accounts dir is a no-op. One unreadable account does
// not stop the others; their errors are joined.
func RewriteStatusLinePrefix(dataDir, oldRoot, newRoot string) error {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	d, err := root.Open(accountsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		if !e.IsDir() { // a symlink or file is not an account dir agent-box made
			continue
		}
		cfgRel := filepath.Join(accountsDir, e.Name(), "claude")
		if fi, err := root.Lstat(cfgRel); err != nil || !fi.IsDir() {
			continue
		}
		if err := rewriteStatusLine(root, cfgRel, oldRoot, newRoot); err != nil {
			errs = append(errs, fmt.Errorf("account %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func rewriteStatusLine(root *os.Root, cfgRel, oldRoot, newRoot string) error {
	cr, err := root.OpenRoot(cfgRel)
	if err != nil {
		return err
	}
	defer cr.Close()
	fi, err := cr.Lstat("settings.json")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	b, err := cr.ReadFile("settings.json")
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return errors.New("settings.json is not valid JSON")
	}
	var sl map[string]json.RawMessage
	if raw, ok := m["statusLine"]; !ok || json.Unmarshal(raw, &sl) != nil || sl == nil {
		return nil
	}
	var cmd string
	if raw, ok := sl["command"]; !ok || json.Unmarshal(raw, &cmd) != nil {
		return nil
	}
	if cmd != oldRoot && !strings.HasPrefix(cmd, oldRoot+"/") {
		return nil
	}
	if sl["command"], err = json.Marshal(newRoot + cmd[len(oldRoot):]); err != nil {
		return err
	}
	if m["statusLine"], err = json.Marshal(sl); err != nil {
		return err
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	perm := fi.Mode().Perm()
	if err := writeFileAtomic(cr, "settings.json", out, perm); err != nil {
		return err
	}
	return cr.Chmod("settings.json", perm) // the umask may have narrowed the create mode
}
