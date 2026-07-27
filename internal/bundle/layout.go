package bundle

import "path/filepath"

// Mount is a read-only bind mount a bundle needs inside the box. Declared here
// rather than reusing the box package's type so the two packages stay
// independent; the caller converts.
type Mount struct{ Host, Box string }

// Layout describes how one agent binary consumes a bundle: what must be mounted
// where, and what flags point the agent at the rest.
type Layout struct {
	Bin    string
	Mounts func(dir, home string) []Mount
	Flags  func(dir string) []string
}

// claudeHome is where the agent's config directory lives inside the box.
const claudeHome = "/home/agent/.claude"

// layouts is the registry. Only the Claude CLI is implemented; another agent
// means another entry, not a redesign.
var layouts = []Layout{
	{
		Bin: "claude",
		Mounts: func(dir, home string) []Mount {
			cfg := filepath.Join(home, ".claude")
			return []Mount{
				{Host: filepath.Join(dir, "CLAUDE.md"), Box: filepath.Join(cfg, "CLAUDE.md")},
				{Host: filepath.Join(dir, "skills"), Box: filepath.Join(cfg, "skills")},
				{Host: filepath.Join(dir, "agents"), Box: filepath.Join(cfg, "agents")},
				{Host: filepath.Join(dir, "commands"), Box: filepath.Join(cfg, "commands")},
			}
		},
		Flags: func(dir string) []string {
			// --mcp-config is variadic: it would swallow a following positional,
			// so it must never be last. --strict-mcp-config confines MCP loading
			// to the pinned file. --setting-sources user is the control that
			// stops a settings.json inside the MOUNTED WORKSPACE from running its
			// hooks — arbitrary code from an untrusted repo, which matters more
			// than the MCP listing does.
			return []string{
				"--mcp-config", filepath.Join(dir, mcpName),
				"--settings", filepath.Join(dir, "settings.json"),
				"--setting-sources", "user",
				"--strict-mcp-config",
			}
		},
	},
}

// LayoutFor returns the layout for an agent binary, matched on basename so a
// configured absolute path still resolves — and so the quick-shell profile
// (bash/zsh) never receives agent flags.
func LayoutFor(bin string) (Layout, bool) {
	base := filepath.Base(bin)
	for _, l := range layouts {
		if l.Bin == base {
			return l, true
		}
	}
	return Layout{}, false
}

// MountsFor returns only the mounts whose source actually exists, so an absent
// optional entry (no agents/ directory, say) does not make docker fail the whole
// box on a missing bind source.
func (l Layout) MountsFor(dir, home string, exists func(string) bool) []Mount {
	var out []Mount
	for _, m := range l.Mounts(dir, home) {
		if exists(m.Host) {
			out = append(out, m)
		}
	}
	return out
}
