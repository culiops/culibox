package account

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/bundle"
)

//go:embed statusline-ratelimits.sh
var statuslineScript []byte

const (
	manifestName = ".agentbox-manifest.json"
	scriptName   = "statusline-ratelimits.sh"
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
}

// ScriptPath is where the embedded rate-limits statusLine script lives.
func (s *Store) ScriptPath() string { return filepath.Join(s.dataDir, binDir, scriptName) }

// provision makes r's config dir ready. It never clobbers: only entries listed
// in the manifest are ever removed, and every write goes through the Store's
// dataDir root so a planted symlink cannot redirect it. Called with s.mu held.
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
	return s.syncInherit(cr, r.InheritPersonal)
}

// writeScript extracts the statusLine script, rewriting it only when the
// on-disk bytes differ (a running session may be executing it).
func (s *Store) writeScript() error {
	rel := filepath.Join(binDir, scriptName)
	if cur, err := s.root.ReadFile(rel); err != nil || !bytes.Equal(cur, statuslineScript) {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := writeFileAtomic(s.root, rel, statuslineScript, 0o755); err != nil {
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
		return writeManifest(cr, manifest{})
	}
	for _, n := range bundle.AllowlistNames() {
		src := filepath.Join(s.defaultDir, n)
		if _, err := os.Stat(src); err != nil {
			continue // nothing to inherit
		}
		if _, err := cr.Lstat(n); err == nil {
			continue // never replace an existing entry
		}
		if err := cr.Symlink(src, n); err != nil {
			return err
		}
		man.Links = appendUnique(man.Links, n)
	}
	// Same shape filter the boxed bundle uses: a server with literal env values
	// or ${VAR} substitution is dropped rather than copied onto disk.
	servers, _ := bundle.FilterMCP(bundle.DefaultMCPFile(s.defaultDir), hostProber{})
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(cr, MCPFileName, b, 0o600); err != nil {
		return err
	}
	man.MCP = true
	if err := s.syncDelegate(cr, &man); err != nil {
		return err
	}
	return writeManifest(cr, man)
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
