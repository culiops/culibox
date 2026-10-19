package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// entryLauncher is the in-box command that fetches an entry's token and runs the
// real server with it: the generated config names it and never a value.
const entryLauncher = "agentbox-env"

// EntryServer is one box-side MCP server a bound token entry declares.
type EntryServer struct {
	Server  string   // the MCP server name
	Entry   string   // the token entry that declares it
	Command string   // the entry's MCP command, as probed
	Args    []string // the full agentbox-env argv: entry, extras, "--", command, args
}

// SessionMCP is one session's MCP config: the document, the key that names its
// file, and the drops (the bundle's own, adjusted for what the entries replaced).
type SessionMCP struct {
	Doc   []byte
	Key   string
	Drops []Drop
}

// ComposeSessionMCP merges the three server sources with the precedence image >
// entry > host. user is the host servers the bundle kept (nil for an account
// that does not inherit them); image is the image's own.
//
// resolves is the probe of the entries' commands, nil when nothing was probed
// (no bundle). Otherwise an entry is left out, with a named reason, when the
// image lacks agentbox-env or the entry's command. An entry that is used
// replaces a host server of the same name, and that is recorded as a drop; an
// image server replaces an entry of the same name, the same way.
//
// The result holds no value: an entry server is the launcher and its argv.
func ComposeSessionMCP(user, image map[string]any, resolves map[string]bool, drops []Drop, entries []EntryServer) (map[string]any, []Drop) {
	out := make(map[string]any, len(user)+len(entries)+len(image))
	for k, v := range user {
		out[k] = v
	}
	drops = slices.Clone(drops)
	for _, e := range entries {
		switch {
		case resolves != nil && !resolves[entryLauncher]:
			drops = append(drops, Drop{Kind: "mcp", Name: e.Server,
				Reason: "the box image has no " + entryLauncher + "; update box_image"})
			continue
		case resolves != nil && !resolves[e.Command]:
			drops = append(drops, Drop{Kind: "mcp", Name: e.Server,
				Reason: "command " + e.Command + " does not exist inside the box image"})
			continue
		}
		if _, ok := image[e.Server]; ok {
			drops = append(drops, Drop{Kind: "mcp", Name: e.Server, Reason: "replaced by the image's " + e.Server})
			continue
		}
		// A host server of this name, kept or dropped, is replaced. Its own drop
		// row goes: the replacement row says what became of it.
		replaced := out[e.Server] != nil
		kept := drops[:0:0]
		for _, d := range drops {
			if d.Kind == "mcp" && d.Name == e.Server && e.Server != "*" {
				replaced = true
				continue
			}
			kept = append(kept, d)
		}
		drops = kept
		out[e.Server] = map[string]any{"type": "stdio", "command": entryLauncher, "args": slices.Clone(e.Args)}
		if replaced {
			drops = append(drops, Drop{Kind: "mcp", Name: e.Server, Reason: "replaced by token entry " + e.Entry})
		}
	}
	for k, v := range image {
		out[k] = v
	}
	return out, drops
}

// SessionMCP builds the MCP config for one boxed session: the shared bundle's
// servers (only when includeUser: the account gate), the entry servers and the
// image's. drops is what the caller already holds (the bundle grant's). The Key
// names the file; it depends on the bundle and on includeUser, and the binding
// dir it lands in is keyed by the token set, so one key is one content.
func (p *Provider) SessionMCP(includeUser bool, drops []Drop, entries []EntryServer) (SessionMCP, error) {
	built, err := p.m.Ensure()
	if err != nil {
		return SessionMCP{}, err
	}
	image, err := readServers(filepath.Join(built.Dir, imageMCPName))
	if err != nil {
		return SessionMCP{}, err
	}
	var user map[string]any
	suffix := "-i"
	if includeUser {
		suffix = "-u"
		if user, err = readServers(filepath.Join(built.Dir, mcpName)); err != nil {
			return SessionMCP{}, err
		}
		for n := range image { // the merged file already holds the image's servers
			delete(user, n)
		}
	}
	servers, drops := ComposeSessionMCP(user, image, built.Lock.EntryResolves, drops, entries)
	doc, err := marshalMCPDoc(servers)
	if err != nil {
		return SessionMCP{}, err
	}
	return SessionMCP{Doc: doc, Key: built.Hash + suffix, Drops: drops}, nil
}

// MarshalSessionMCP renders a composed server set as the file claude reads.
func MarshalSessionMCP(servers map[string]any) ([]byte, error) { return marshalMCPDoc(servers) }

func marshalMCPDoc(servers map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// readServers reads the mcpServers object of one bundle file. A bundle built
// before image-mcp.json existed cannot be reused (the layout version is hashed),
// so a missing file is an error, not an empty set.
func readServers(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("bundle: %s: %w", filepath.Base(path), err)
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]any{}
	}
	return doc.MCPServers, nil
}
