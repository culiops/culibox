package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Prober answers whether a command would resolve inside the box image. It is an
// interface so classification is unit-testable without a docker daemon.
type Prober interface {
	Resolves(cmds []string) map[string]bool
}

// DenyAllProber is the safe default: with no way to check, no stdio server is
// assumed to work. Failing closed keeps a broken server out of the bundle
// instead of producing a session that dies confusingly.
type DenyAllProber struct{}

func (DenyAllProber) Resolves(cmds []string) map[string]bool {
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		out[c] = false
	}
	return out
}

// ImageProber asks the box image once per build whether each command exists,
// and which MCP servers the image declares (imagemcp.go).
type ImageProber struct {
	Docker string
	// Args are global docker flags (--host, --config) placed before the
	// subcommand, so the probe reaches the same daemon as every other call.
	Args  []string
	Image string
}

// Resolves keeps the plain Prober contract: every command false when the probe
// fails. The Manager uses Probe instead, so a failure there is an error.
func (p ImageProber) Resolves(cmds []string) map[string]bool {
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		out[c] = false
	}
	if len(cmds) == 0 || p.Docker == "" || p.Image == "" {
		return out
	}
	f, err := p.Probe(p.Image, cmds)
	if err != nil {
		return out
	}
	return f.Resolves
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// unresolvedVar matches ${VAR} / ${VAR:-default} substitution, a documented MCP
// config feature. A boxed session gets a curated environment, so such a variable
// is usually absent — and an unresolved one can invalidate the whole config file
// rather than just its own server.
var unresolvedVar = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(:-[^}]*)?\}`)

type mcpServer map[string]any

// FilterMCP is filterMCP for callers outside the bundle (agent accounts that
// inherit the default account's MCP servers): same shape rules, same drops.
func FilterMCP(path string, prober Prober) (map[string]any, []Drop) {
	return filterMCP(path, prober)
}

// filterMCP classifies servers by SHAPE. No server is ever matched by name: the
// same rule has to work for configs this project has never seen.
func filterMCP(path string, prober Prober) (map[string]any, []Drop) {
	return filterMCPSource(readMCPSource(path), prober)
}

// mcpSource is the MCP file as read once per Ensure, so the hash, the image
// probe and the filter all see the same bytes: a content hash computed from one
// read and a bundle built from another would label a dir with servers it does
// not hold, and a later identical file would reuse it.
type mcpSource struct {
	path string
	raw  []byte
	err  error
}

func readMCPSource(path string) mcpSource {
	raw, err := os.ReadFile(path)
	return mcpSource{path: path, raw: raw, err: err}
}

func filterMCPSource(src mcpSource, prober Prober) (map[string]any, []Drop) {
	raw, err := src.raw, src.err
	if err != nil {
		return map[string]any{}, []Drop{{Kind: "mcp", Name: "*",
			Reason: "no MCP configuration file at " + src.path}}
	}
	var doc struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return map[string]any{}, []Drop{{Kind: "mcp", Name: "*", Reason: "config file is not valid JSON"}}
	}

	names := make([]string, 0, len(doc.MCPServers))
	for n := range doc.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)

	// Collect stdio commands first so the image is probed once.
	var cmds []string
	for _, n := range names {
		if c, ok := doc.MCPServers[n]["command"].(string); ok && c != "" {
			cmds = append(cmds, c)
		}
	}
	resolves := prober.Resolves(cmds)

	kept := map[string]any{}
	var drops []Drop
	for _, n := range names {
		srv := doc.MCPServers[n]
		if reason, ok := rejectReason(srv, resolves); ok {
			drops = append(drops, Drop{Kind: "mcp", Name: n, Reason: reason})
			continue
		}
		kept[n] = srv
	}
	return kept, drops
}

// rejectReason returns why a server cannot enter the bundle, or ok=false to keep
// it. Ordered so the most specific reason wins.
func rejectReason(srv mcpServer, resolves map[string]bool) (string, bool) {
	// Converted to the unnamed map type on purpose: a Go type switch matches
	// exact types, so `case map[string]any` would not match the named type and
	// every substitution would slip through unchecked.
	if hasUnresolvedVar(map[string]any(srv)) {
		return "uses ${VAR} substitution; a boxed session has a curated environment where that variable is absent", true
	}
	if env, ok := srv["env"].(map[string]any); ok && len(env) > 0 {
		return "declares literal env values; materializing them would write a secret into the bundle", true
	}
	if reason, ok := credentialReason(srv); ok {
		return reason, true
	}
	if url, _ := srv["url"].(string); url != "" {
		return "", false // remote transport works in a box (subject to egress)
	}
	cmd, _ := srv["command"].(string)
	if strings.TrimSpace(cmd) == "" {
		return "has neither a url nor a command", true
	}
	if !resolves[cmd] {
		// Only the first word: the reason lands in the lock file the box mounts,
		// and the rest of a command string could hold a secret.
		return "command " + commandName(cmd) + " does not exist inside the box image", true
	}
	return "", false
}

// commandName is the base name of a command's first word, withheld unless it
// is a plain name: the reason is written to the lock file the box mounts.
func commandName(cmd string) string {
	return echoName(filepath.Base(strings.Fields(cmd)[0]))
}

func hasUnresolvedVar(v any) bool {
	switch t := v.(type) {
	case string:
		return unresolvedVar.MatchString(t)
	case []any:
		for _, e := range t {
			if hasUnresolvedVar(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range t {
			if hasUnresolvedVar(e) {
				return true
			}
		}
	}
	return false
}
