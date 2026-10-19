package bundle

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/culiops/agent-box/internal/box"
)

const (
	// BoxSessionMCPDir is where the directory holding a session's personal-server
	// MCP config is mounted (read-only) inside a box.
	BoxSessionMCPDir = "/opt/agent-session-mcp"
	// sessionMCPDir sits beside the bundle dirs under Options.Root. It is not a
	// bundle dir and never part of one: a bundle's identity is its content hash.
	sessionMCPDir = "session-mcp"
)

var sessionMCPKeyRe = regexp.MustCompile(`^[0-9a-z-]{1,64}$`)

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

// A config that carries the user's personal servers must never be readable from
// a box whose account did not opt into personal config. The binding dir cannot
// hold it: it is keyed by the binding alone and mounted into every account's box
// with that binding. So the personal config gets a directory of its own, keyed
// by the binding, that only a box which already receives the personal bundle
// mounts (the session layer gates both on the same account test). A config with
// only entry and image servers is not personal and stays in the binding dir.

// SessionMCPMount ensures the personal-config directory for a binding and
// returns the mount that exposes it. The caller adds it only to boxes of an
// account that inherits the personal config, for every agent bin: a box is
// shared by the bins of one workspace and its mounts are fixed at start.
func (p *Provider) SessionMCPMount(bindingKey string) (box.Mount, error) {
	dir, root, err := p.ensureSessionMCPDir(bindingKey)
	if err != nil {
		return box.Mount{}, err
	}
	_ = root.Close()
	return box.Mount{Host: dir, Box: BoxSessionMCPDir}, nil
}

// WriteSessionMCP writes one personal-server config into that directory and
// returns where the box sees it. The write goes through os.Root and replaces
// atomically: a symlink planted at the name is replaced, never followed.
func (p *Provider) WriteSessionMCP(bindingKey, key string, doc []byte) (string, error) {
	if !sessionMCPKeyRe.MatchString(key) {
		return "", fmt.Errorf("bundle: bad session key %q", key)
	}
	dir, root, err := p.ensureSessionMCPDir(bindingKey)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := "mcp-" + key + ".json"
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("bundle: %w", err)
	}
	rel, err := filepath.Rel(p.m.opts.Root, dir)
	if err != nil {
		return "", fmt.Errorf("bundle: %w", err)
	}
	tmp := filepath.Join(rel, "."+name+"."+hex.EncodeToString(nonce[:])+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("bundle: writing %s: %w", name, err)
	}
	_, err = f.Write(doc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, filepath.Join(rel, name))
	}
	if err != nil {
		_ = root.Remove(tmp)
		return "", fmt.Errorf("bundle: writing %s: %w", name, err)
	}
	return BoxSessionMCPDir + "/" + name, nil
}

// ensureSessionMCPDir creates the binding's directory (0700) under Root through
// os.Root and returns its path with the open root, which the caller closes.
func (p *Provider) ensureSessionMCPDir(bindingKey string) (string, *os.Root, error) {
	if !sessionMCPKeyRe.MatchString(bindingKey) {
		return "", nil, fmt.Errorf("bundle: bad binding key %q", bindingKey)
	}
	if err := os.MkdirAll(p.m.opts.Root, 0o700); err != nil {
		return "", nil, fmt.Errorf("bundle: creating root: %w", err)
	}
	root, err := os.OpenRoot(p.m.opts.Root)
	if err != nil {
		return "", nil, fmt.Errorf("bundle: opening root: %w", err)
	}
	rel := filepath.Join(sessionMCPDir, bindingKey)
	for _, d := range []string{sessionMCPDir, rel} {
		if err := root.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			_ = root.Close()
			return "", nil, fmt.Errorf("bundle: %w", err)
		}
	}
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		_ = root.Close()
		return "", nil, fmt.Errorf("bundle: %s is not a plain directory", rel)
	}
	return filepath.Join(p.m.opts.Root, rel), root, nil
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
