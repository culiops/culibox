package bundle

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

// ImageProber asks the box image once per build whether each command exists.
type ImageProber struct {
	Docker string
	Image  string
}

func (p ImageProber) Resolves(cmds []string) map[string]bool {
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		out[c] = false
	}
	if len(cmds) == 0 || p.Docker == "" || p.Image == "" {
		return out
	}
	// One container for the whole candidate set, not one per server.
	var sb strings.Builder
	for _, c := range cmds {
		sb.WriteString("if command -v " + shellQuote(c) + " >/dev/null 2>&1; then echo OK " + shellQuote(c) + "; fi\n")
	}
	cmd := exec.CommandContext(context.Background(), p.Docker, "run", "--rm", "--entrypoint", "sh", p.Image, "-c", sb.String())
	res, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(res), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "OK "); ok {
			out[name] = true
		}
	}
	return out
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// unresolvedVar matches ${VAR} / ${VAR:-default} substitution, a documented MCP
// config feature. A boxed session gets a curated environment, so such a variable
// is usually absent — and an unresolved one can invalidate the whole config file
// rather than just its own server.
var unresolvedVar = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(:-[^}]*)?\}`)

type mcpServer map[string]any

// filterMCP classifies servers by SHAPE. No server is ever matched by name: the
// same rule has to work for configs this project has never seen.
func filterMCP(path string, prober Prober) (map[string]any, []Drop) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}, []Drop{{Kind: "mcp", Name: "*",
			Reason: "no MCP configuration file at " + path}}
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
	if url, _ := srv["url"].(string); url != "" {
		return "", false // remote transport works in a box (subject to egress)
	}
	cmd, _ := srv["command"].(string)
	if cmd == "" {
		return "has neither a url nor a command", true
	}
	if !resolves[cmd] {
		return "command " + cmd + " does not exist inside the box image", true
	}
	return "", false
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
