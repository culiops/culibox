// Package bundle materializes the slice of a user's agent configuration that is
// safe and useful to hand a contained session.
//
// The governing rule is an ALLOWLIST. A real agent config directory sits beside
// credentials, shell history and gigabytes of transcripts; a bundle is mounted
// into the box, so anything that lands in one is handed to the contained agent.
// Copying a directory wholesale would deliver exactly the secrets containment
// exists to withhold. Nothing is copied because it happened to be there.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	lockName = "bundle.lock"
	mcpName  = "mcp.json"
	// maxLinkDepth bounds symlink following. A cycle must terminate rather than
	// hang the daemon before a spawn.
	maxLinkDepth = 8
	// Bounds on what a link may drag in. Skills legitimately symlink into
	// sibling repositories, so "outside the config directory" cannot be the
	// rejection rule — it would skip the very case dereferencing exists for.
	// These caps bound the damage instead.
	maxBundleFiles = 4000
	maxBundleBytes = 64 << 20
)

// allowlist is every entry that may enter a bundle, and how it is treated.
// Adding a key is a deliberate act; a new file in the source directory stays out
// by default rather than leaking by default.
var allowlist = []entry{
	{rel: "CLAUDE.md", dir: false},
	{rel: "skills", dir: true},
	{rel: "agents", dir: true},
	{rel: "commands", dir: true},
}

type entry struct {
	rel string
	dir bool
}

// Drop records something deliberately left out, so losing it is a named
// decision rather than a silent degradation.
type Drop struct {
	Kind   string `json:"kind"` // "mcp" | "settings" | "symlink"
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Lock is the manifest: a hash per file plus everything dropped and why.
type Lock struct {
	Hash    string            `json:"hash"`
	Files   map[string]string `json:"files"`
	Dropped []Drop            `json:"dropped,omitempty"`
}

func (l Lock) hasDrop(kind, name string) bool {
	for _, d := range l.Dropped {
		if d.Kind == kind && d.Name == name {
			return true
		}
	}
	return false
}

// Built is a materialized bundle.
type Built struct {
	Dir    string
	Hash   string
	Lock   Lock
	Reused bool
}

// Options configures a Manager.
type Options struct {
	Source string // agent config dir
	Root   string // where bundles are materialized
	// MCPFile is the file holding MCP server definitions. It lives BESIDE the
	// config directory, not inside it, so it is named separately.
	MCPFile string
	Prober  Prober // decides whether a command exists in the box image
}

// Manager builds and caches bundles.
type Manager struct{ opts Options }

func New(opts Options) (*Manager, error) {
	if opts.Source == "" {
		return nil, fmt.Errorf("bundle: a source directory is required")
	}
	if opts.Root == "" {
		return nil, fmt.Errorf("bundle: a root directory is required")
	}
	if opts.Prober == nil {
		opts.Prober = DenyAllProber{}
	}
	return &Manager{opts: opts}, nil
}

// Ensure returns a bundle for the current source, building it if the source
// changed. Reuse requires the lock file, which is written last — so a build that
// died half-way cannot be mistaken for a complete one.
func (m *Manager) Ensure() (*Built, error) {
	hash, err := m.sourceHash()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.opts.Root, hash)
	if lock, ok := readLock(dir); ok {
		return &Built{Dir: dir, Hash: hash, Lock: lock, Reused: true}, nil
	}
	built, err := m.materialize(hash, dir)
	if err != nil {
		return nil, err
	}
	return built, nil
}

// sourceHash fingerprints only the allowlisted entries, following symlinks so
// that editing a symlinked skill actually invalidates the cache.
func (m *Manager) sourceHash() (string, error) {
	h := sha256.New()
	for _, e := range allowlist {
		src := filepath.Join(m.opts.Source, e.rel)
		_ = m.walkAllowed(src, e.rel, 0, map[string]bool{}, func(rel string, fi os.FileInfo) error {
			fmt.Fprintf(h, "%s\x00%d\x00%d\n", rel, fi.Size(), fi.ModTime().UnixNano())
			return nil
		}, nil)
	}
	// The MCP file lives outside the source dir; fold it in so a change there
	// rebuilds too.
	if fi, err := os.Stat(m.opts.MCPFile); err == nil {
		fmt.Fprintf(h, "mcp\x00%d\x00%d\n", fi.Size(), fi.ModTime().UnixNano())
	}
	if fi, err := os.Stat(filepath.Join(m.opts.Source, "settings.json")); err == nil {
		fmt.Fprintf(h, "settings\x00%d\x00%d\n", fi.Size(), fi.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// walkAllowed visits regular files under one allowlisted entry, following
// symlinks with a depth cap and a visited set, and skipping any link that
// resolves outside the source root.
func (m *Manager) walkAllowed(path, rel string, depth int, seen map[string]bool,
	visit func(rel string, fi os.FileInfo) error, drop func(Drop)) error {
	if depth > maxLinkDepth {
		return nil
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if reason, bad := pathologicalTarget(m.opts.Source, target); bad {
			if drop != nil {
				drop(Drop{Kind: "symlink", Name: filepath.ToSlash(rel), Reason: reason})
			}
			return nil
		}
		if seen[target] {
			return nil
		}
		seen[target] = true
		return m.walkAllowed(target, rel, depth+1, seen, visit, drop)
	}
	if !fi.IsDir() {
		return visit(rel, fi)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, ent := range ents {
		_ = m.walkAllowed(filepath.Join(path, ent.Name()), filepath.Join(rel, ent.Name()), depth+1, seen, visit, drop)
	}
	return nil
}

// pathologicalTarget rejects link targets that would pull in a whole home or
// filesystem. A link into a sibling repository is normal and must be followed —
// that is how skills are shared between projects — so the test is not "outside
// the config directory" but "is this an enclosing tree".
func pathologicalTarget(source, target string) (string, bool) {
	clean := filepath.Clean(target)
	if clean == string(filepath.Separator) {
		return "link resolves to the filesystem root", true
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == clean {
		return "link resolves to the home directory; following it would copy the whole home into a directory mounted in the box", true
	}
	src, err := filepath.EvalSymlinks(source)
	if err != nil {
		src = source
	}
	if rel, err := filepath.Rel(clean, src); err == nil && rel != "." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return "link resolves to a directory containing the config directory", true
	}
	return "", false
}

// materialize builds into a temp dir and renames, writing the lock last as the
// completion sentinel.
func (m *Manager) materialize(hash, final string) (*Built, error) {
	if err := os.MkdirAll(m.opts.Root, 0o700); err != nil {
		return nil, fmt.Errorf("bundle: creating root: %w", err)
	}
	tmp, err := os.MkdirTemp(m.opts.Root, ".tmp-")
	if err != nil {
		return nil, fmt.Errorf("bundle: temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	lock := Lock{Hash: hash, Files: map[string]string{}}
	addDrop := func(d Drop) { lock.Dropped = append(lock.Dropped, d) }
	var files int
	var bytes int64
	capped := false

	for _, e := range allowlist {
		src := filepath.Join(m.opts.Source, e.rel)
		if _, err := os.Lstat(src); err != nil {
			continue // absent entries are normal, not errors
		}
		err := m.walkAllowed(src, e.rel, 0, map[string]bool{}, func(rel string, fi os.FileInfo) error {
			files++
			bytes += fi.Size()
			if files > maxBundleFiles || bytes > maxBundleBytes {
				if !capped {
					capped = true
					addDrop(Drop{Kind: "symlink", Name: filepath.ToSlash(e.rel),
						Reason: "bundle exceeded its size cap; a symlink is probably pulling in an unexpected tree"})
				}
				return nil
			}
			sum, err := copyFile(m.resolveIn(rel), filepath.Join(tmp, rel))
			if err != nil {
				return err
			}
			lock.Files[filepath.ToSlash(rel)] = sum
			return nil
		}, addDrop)
		if err != nil {
			return nil, err
		}
	}

	// settings.json: keep-list filtered, never copied verbatim.
	settings, sdrops := filterSettings(filepath.Join(m.opts.Source, "settings.json"))
	lock.Dropped = append(lock.Dropped, sdrops...)
	if sum, err := writeJSON(filepath.Join(tmp, "settings.json"), settings); err == nil {
		lock.Files["settings.json"] = sum
	}

	// mcp.json: shape-classified, never name-matched.
	servers, mdrops := filterMCP(m.opts.MCPFile, m.opts.Prober)
	lock.Dropped = append(lock.Dropped, mdrops...)
	if sum, err := writeJSON(filepath.Join(tmp, mcpName), map[string]any{"mcpServers": servers}); err == nil {
		lock.Files[mcpName] = sum
	}

	// The lock is the completion sentinel and therefore written last.
	if _, err := writeJSON(filepath.Join(tmp, lockName), lock); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return nil, fmt.Errorf("bundle: publishing %s: %w", final, err)
	}
	return &Built{Dir: final, Hash: hash, Lock: lock}, nil
}

// resolveIn maps a bundle-relative path back to the file to read, following any
// symlink in the chain (a dereferenced skill lives outside the config dir).
func (m *Manager) resolveIn(rel string) string {
	p := filepath.Join(m.opts.Source, rel)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// copyFile copies with a fixed 0600/0700 mode: source modes are irrelevant in
// the box and a restrictive host mode would make the file unreadable there.
func copyFile(from, to string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return "", err
	}
	in, err := os.Open(from)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeJSON(path string, v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func readLock(dir string) (Lock, bool) {
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		return Lock{}, false
	}
	var l Lock
	if err := json.Unmarshal(b, &l); err != nil {
		return Lock{}, false
	}
	return l, true
}

// ExpandTilde resolves a leading ~ against the user's home. The daemon had no
// tilde expansion, so a perfectly reasonable "~/.claude" in config resolved to a
// relative path, missed, and degraded to "run bare" without saying why.
func ExpandTilde(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// DefaultSource resolves the agent config directory the way the agent does:
// $CLAUDE_CONFIG_DIR when set, else ~/.claude.
func DefaultSource() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return ExpandTilde(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// DefaultMCPFile is the file holding MCP servers, which sits BESIDE the config
// directory rather than inside it.
func DefaultMCPFile(source string) string {
	return filepath.Join(filepath.Dir(source), ".claude.json")
}

var _ = strconv.Itoa
