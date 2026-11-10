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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/box"
)

const (
	lockName = "bundle.lock"
	mcpName  = "mcp.json"
	// imageMCPName holds the image's own servers on their own, beside the merged
	// mcp.json: a session's config needs them apart to rank image > entry > host.
	imageMCPName = "image-mcp.json"
	// delegateName holds the host's statusLine command for the bundled script to
	// call (see statusline.go).
	delegateName = "statusline-delegate"
	// layoutMarker is folded into the source hash. v2 added image-mcp.json, v3
	// refuses MCP servers carrying credentials in headers/args/url, v4 ships
	// agent-box's statusLine script and replaces the host statusLine, so every
	// bundle built before it rebuilds once. v5 puts the files CLAUDE.md imports
	// beside the bundle (<dir>.imports) and lists them in the lock. Bundle hashes
	// are not part of the box key, so no box is recreated by that.
	layoutMarker = "layout\x00v5\n"
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

// AllowlistNames is the personal-config allowlist, shared with agent accounts
// that inherit the default account's config.
func AllowlistNames() []string {
	out := make([]string, len(allowlist))
	for i, e := range allowlist {
		out[i] = e.rel
	}
	return out
}

type entry struct {
	rel string
	dir bool
}

// Drop records something deliberately left out, so losing it is a named
// decision rather than a silent degradation.
type Drop struct {
	Kind   string `json:"kind"` // "mcp" | "settings" | "symlink" | "plugins" | "bundle" | "import"
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Lock is the manifest: a hash per file plus everything dropped and why.
type Lock struct {
	Hash    string            `json:"hash"`
	Files   map[string]string `json:"files"`
	Dropped []Drop            `json:"dropped,omitempty"`
	ImageID string            `json:"image_id,omitempty"`
	// EntryResolves is the probe's answer for Options.EntryCommands (agentbox-env
	// and each token entry's MCP command). Absent when none were asked.
	EntryResolves map[string]bool `json:"entry_resolves,omitempty"`
	// Imports are the files CLAUDE.md imports that were copied to importsDir, by
	// in-box path. Their bytes are not in Files: they live beside the bundle, so
	// the dir mounted at BoxBundleDir never holds them.
	Imports []LockImport `json:"imports,omitempty"`
}

// LockImport is one bundled import: where it appears in the box and the hash of
// the bytes that were copied.
type LockImport struct {
	Box    string `json:"box"`
	SHA256 string `json:"sha256"`
}

// importsDir is where a bundle's imports are copied: beside the bundle dir, so
// the dir mounted at BoxBundleDir never holds them. Like the bundle dir it is
// removed only by GC, which keeps it for as long as any container mounts it, so
// a live box keeps the imports it was created with.
func importsDir(bundleDir string) string { return bundleDir + ".imports" }

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

	// The plugin verdict this bundle was built for. Not part of the lock: the
	// plugin snapshot lives beside the bundle dirs, so the verdict is recomputed
	// on every Ensure and carried here so the provider hands out exactly the
	// mounts the bundle's settings were decided against.
	pluginMounts []box.Mount
	pluginDrops  []Drop
}

// Options configures a Manager.
type Options struct {
	Source string // agent config dir
	Root   string // where bundles are materialized
	// MCPFile is the file holding MCP server definitions. It lives BESIDE the
	// config directory, not inside it, so it is named separately.
	MCPFile string
	Prober  Prober // decides whether a command exists in the box image
	// EntryCommands are the commands token entries launch in the box
	// (agentbox-env and each entry's MCP command). They are probed with the
	// user's MCP commands and folded into the hash, so a new entry rebuilds the
	// bundle and a session's MCP config can name what the image lacks.
	EntryCommands []string
	// HostHome is the home directory CLAUDE.md imports are resolved against.
	// Empty means os.UserHomeDir(), symlinks resolved.
	HostHome string
	// ImportDeny are host directories no import may come from (agent-box's own
	// data and state). Imports are also denied by the resolver's built-in list
	// and Claude Code's credentials file.
	ImportDeny []string
}

// Manager builds and caches bundles.
type Manager struct {
	opts Options
	// gcMu makes Ensure's look-and-refresh of a bundle or snapshot, the publish of
	// a new one and GC's look-and-rename exclude each other. See GC. Per root and not per process
	// on purpose: one Manager exists per root, and one daemon per data dir.
	gcMu sync.Mutex
	// touch refreshes a dir's date; os.Chtimes outside a test of its failing.
	touch func(path string, t time.Time) error
}

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
	return &Manager{opts: opts, touch: func(path string, t time.Time) error { return os.Chtimes(path, t, t) }}, nil
}

// Imports resolves the files CLAUDE.md imports, fresh from the host. It writes
// nothing.
//
// Besides Options.ImportDeny and the resolver's own list, it denies by identity
// every host file the bundle sanitizes or replaces (the MCP file, settings.json,
// settings.local.json) and the credentials the box must not hold. The resolver's
// reserved-path check sees only the in-box spelling, so an absolute or linked
// import of one of these files would otherwise keep its host path in the box and
// hand over the unsanitized original. Claude Code's OAuth credentials need an
// explicit entry too: the leading dot keeps them out of the credentials* glob.
func (m *Manager) Imports() ImportSet {
	home := m.opts.HostHome
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ImportSet{Files: []ImportFile{}}
		}
		if r, err := filepath.EvalSymlinks(h); err == nil {
			h = r
		}
		home = h
	}
	deny := slices.Clone(m.opts.ImportDeny)
	if m.opts.MCPFile != "" {
		deny = append(deny, m.opts.MCPFile)
	}
	for _, f := range []string{"settings.json", "settings.local.json", ".credentials.json"} {
		deny = append(deny, filepath.Join(m.opts.Source, f))
	}
	deny = append(deny, filepath.Join(home, ".claude", ".credentials.json"))
	for _, p := range homeCredentialPaths {
		deny = append(deny, filepath.Join(home, p))
	}
	return resolveImports(importOpts{Source: m.opts.Source, HostHome: home, Deny: deny})
}

// homeCredentialPaths are credential stores under the home that are not already
// covered by box.HostSocketDirs (which holds .ssh, .docker and the like).
var homeCredentialPaths = []string{".aws", ".config/gh", ".kube", ".gnupg", ".netrc", ".git-credentials"}

// Ensure returns a bundle for the current source, building it if the source
// changed. Reuse requires the lock file, which is written last — so a build that
// died half-way cannot be mistaken for a complete one.
func (m *Manager) Ensure() (*Built, error) {
	// Decided once per Ensure so the hash, the settings and the mounts agree
	// even if the host plugin dir changes mid-call.
	ps := pluginSnapshot(m.opts.Source, m.opts.Root, m.snapshotGuard())
	mounted := len(ps.mounts) > 0
	mcp := readMCPSource(m.opts.MCPFile)
	imports := m.Imports()
	hash, imageID, err := m.sourceHash(mounted, mcp, imports)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.opts.Root, hash)
	built, err := m.reuse(hash, dir)
	if err != nil {
		return nil, err
	}
	if built == nil {
		if built, err = m.materialize(hash, imageID, dir, mounted, mcp, imports); err != nil {
			return nil, err
		}
	}
	built.pluginMounts, built.pluginDrops = ps.mounts, ps.drops
	return built, nil
}

// snapshotGuard is this Manager's lock and touch, for the plugin snapshots.
func (m *Manager) snapshotGuard() snapshotGuard {
	return snapshotGuard{mu: &m.gcMu, touch: m.touch}
}

// reuse returns the existing complete bundle at dir, or nil to build one. Handing
// it out refreshes its date: the spawn's container does not exist yet, so that
// date is all that keeps GC away until it does. A date that cannot be written is
// an error, because the dir would go out unprotected.
func (m *Manager) reuse(hash, dir string) (*Built, error) {
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	lock, ok := readLock(dir)
	if !ok || !importsPresent(dir, lock) {
		return nil, nil
	}
	if err := m.touch(dir, time.Now()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("bundle: refreshing the date of %s: %w", dir, err)
	}
	return &Built{Dir: dir, Hash: hash, Lock: lock, Reused: true}, nil
}

// sourceHash fingerprints only the allowlisted entries, following symlinks so
// that editing a symlinked skill actually invalidates the cache. With an
// ImageProbe the box image ID is folded in too: the bundle depends on what the
// image declares and contains, so a new image rebuilds it.
//
// pluginsMounted (the plugin VERDICT) is folded in because it decides whether
// the bundle's settings carry enabledPlugins: a cached bundle must not keep the
// key after the plugin set breaks, nor lack it after the set is fixed. Only the
// verdict, never the snapshot fingerprints: the settings depend on nothing
// else, and host claude rewrites known_marketplaces.json on routine checks, so
// folding the snapshots in would make a new bundle dir per marketplace check.
//
// The mounts themselves come from the same Ensure (Built.pluginMounts), so a new
// box always gets settings and snapshot mounts decided together. A live box that
// is reused keeps the bundle dir and the snapshot dirs it was created with:
// reuse compares mount destinations, not host paths, and neither kind of dir is
// removed while a container mounts it, so its settings and its plugins still
// match each other.
// It sees a newer plugin snapshot only once it is recreated.
func (m *Manager) sourceHash(pluginsMounted bool, mcp mcpSource, imports ImportSet) (hash, imageID string, err error) {
	h := sha256.New()
	io.WriteString(h, layoutMarker)
	for _, c := range m.entryCommands() {
		fmt.Fprintf(h, "entrycmd\x00%s\n", c)
	}
	if ip, ok := m.opts.Prober.(ImageProbe); ok {
		if imageID, err = ip.ImageID(); err != nil {
			return "", "", fmt.Errorf("bundle: box image: %w", err)
		}
		fmt.Fprintf(h, "image\x00%s\n", imageID)
	}
	for _, e := range allowlist {
		src := filepath.Join(m.opts.Source, e.rel)
		_ = m.walkAllowed(src, e.rel, 0, map[string]bool{}, func(rel string, fi os.FileInfo) error {
			fmt.Fprintf(h, "%s\x00%d\x00%d\n", rel, fi.Size(), fi.ModTime().UnixNano())
			return nil
		}, nil)
	}
	// The MCP file lives outside the source dir; fold it in so a change there
	// rebuilds too. By content, not mtime: see mcpFingerprint.
	fmt.Fprintf(h, "mcp\x00%s\n", mcpFingerprint(mcp))
	if fi, err := os.Stat(filepath.Join(m.opts.Source, "settings.json")); err == nil {
		fmt.Fprintf(h, "settings\x00%d\x00%d\n", fi.Size(), fi.ModTime().UnixNano())
	}
	fmt.Fprintf(h, "plugins\x00%t\n", pluginsMounted)
	// The imports' content and the refusals both reach the bundle (the files and
	// the lock's drops), so both are folded in.
	fmt.Fprintf(h, "imports\x00%s\n", imports.fingerprint())
	return hex.EncodeToString(h.Sum(nil))[:16], imageID, nil
}

// mcpFingerprint is what of the MCP file can reach the bundle: its top-level
// mcpServers (filterMCP, mcpCommands), or whether it is absent or invalid. The
// file is ~/.claude.json, which host claude rewrites constantly with tips,
// stats and project state, so folding its mtime in made a new, identical bundle
// dir on nearly every spawn (#3). Marshalling the decoded map sorts its keys, so
// a reordered rewrite fingerprints the same.
func mcpFingerprint(src mcpSource) string {
	raw, err := src.raw, src.err
	if err != nil {
		return "absent"
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "invalid"
	}
	b, err := json.Marshal(doc.MCPServers)
	if err != nil {
		return "invalid"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// entryCommands is the sorted, de-duplicated Options.EntryCommands.
func (m *Manager) entryCommands() []string {
	out := slices.Clone(m.opts.EntryCommands)
	sort.Strings(out)
	return slices.Compact(out)
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
func (m *Manager) materialize(hash, imageID, final string, pluginsMounted bool, mcp mcpSource, imports ImportSet) (*Built, error) {
	// The image is probed before anything is written. A failed probe publishes
	// nothing: a cached bundle would pin "no image servers" and "every stdio
	// server dropped" until the source changed, while an error lets the spawn
	// run without a bundle and the next spawn retry.
	prober := m.opts.Prober
	entryCmds := m.entryCommands()
	var facts ImageFacts
	if ip, ok := m.opts.Prober.(ImageProbe); ok {
		var err error
		cmds := mcpCommands(mcp)
		for _, c := range entryCmds {
			if !slices.Contains(cmds, c) {
				cmds = append(cmds, c)
			}
		}
		if facts, err = ip.Probe(imageID, cmds); err != nil {
			return nil, fmt.Errorf("bundle: probing the box image: %w", err)
		}
		prober = resolvedProber(facts.Resolves)
	}

	if err := os.MkdirAll(m.opts.Root, 0o700); err != nil {
		return nil, fmt.Errorf("bundle: creating root: %w", err)
	}
	tmp, err := os.MkdirTemp(m.opts.Root, ".tmp-")
	if err != nil {
		return nil, fmt.Errorf("bundle: temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	lock := Lock{Hash: hash, Files: map[string]string{}, ImageID: imageID}
	if len(entryCmds) > 0 {
		lock.EntryResolves = prober.Resolves(entryCmds)
	}
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
	if !pluginsMounted {
		// No extra drop row: the plugins drop (or the user's having no plugins)
		// already explains why none load.
		delete(settings, "enabledPlugins")
	}
	lock.Dropped = append(lock.Dropped, sdrops...)
	if err := m.applyStatusLine(tmp, settings, &lock); err != nil {
		return nil, err
	}
	if sum, err := writeJSON(filepath.Join(tmp, "settings.json"), settings); err == nil {
		lock.Files["settings.json"] = sum
	}

	// mcp.json: shape-classified, never name-matched; the image's own servers
	// are merged in after the user filter.
	servers, mdrops := filterMCPSource(mcp, prober)
	servers, mdrops, imageServers := mergeImageMCP(servers, mdrops, facts)
	lock.Dropped = append(lock.Dropped, mdrops...)
	if sum, err := writeJSON(filepath.Join(tmp, mcpName), map[string]any{"mcpServers": servers}); err == nil {
		lock.Files[mcpName] = sum
	}
	// The image's servers alone, for the per-session config (sessionmcp.go).
	if sum, err := writeJSON(filepath.Join(tmp, imageMCPName), map[string]any{"mcpServers": imageServers}); err == nil {
		lock.Files[imageMCPName] = sum
	}

	// Imports are copied from the bytes the resolver hashed, never re-read from
	// the host. They go into a temp dir of their own and are renamed into place
	// before the bundle, so the lock, which lands last, never names imports that
	// are not there.
	impTmp, err := writeImports(m.opts.Root, imports, &lock)
	if err != nil {
		return nil, err
	}
	if impTmp != "" {
		defer os.RemoveAll(impTmp)
	}
	for _, r := range imports.Refused {
		addDrop(Drop{Kind: KindImport, Name: r.Import, Reason: r.Reason})
	}

	// The lock is the completion sentinel and therefore written last.
	if _, err := writeJSON(filepath.Join(tmp, lockName), lock); err != nil {
		return nil, err
	}
	// Under gcMu: a pass that read this hash as old must not rename half a publish.
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	_ = os.RemoveAll(final)
	if impTmp != "" {
		_ = os.RemoveAll(importsDir(final))
		if err := os.Rename(impTmp, importsDir(final)); err != nil {
			return nil, fmt.Errorf("bundle: publishing imports of %s: %w", final, err)
		}
	}
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

// SourceOf is the config bundle's source dir for the bundle_source setting:
// "" for "off", the configured dir (tilde expanded), or DefaultSource().
func SourceOf(setting string) string {
	if setting == "off" {
		return ""
	}
	if src := ExpandTilde(setting); src != "" {
		return src
	}
	return DefaultSource()
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
