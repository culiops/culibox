package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/gitrepo"
	"github.com/culiops/culibox/internal/launcher"
)

// Usage is a per-session snapshot read from the agent's JSONL transcript.
type Usage struct {
	Model         string `json:"model,omitempty"`
	Effort        string `json:"effort,omitempty"`
	ContextTokens int    `json:"contextTokens"`
	ContextMax    int    `json:"contextMax,omitempty"`
	Pct           *int   `json:"pct,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	// PostCompact marks ContextTokens as the size the transcript recorded right
	// after a compaction (no reply has been counted since), not a reply's usage.
	PostCompact bool `json:"postCompact,omitempty"`
}

const usageTailBytes = 256 << 10

// usageWideTailBytes is the one-off wider read UsageFor retries with when the
// normal tail holds no usage line (a long run of attachment records can exceed
// usageTailBytes after the last reply). Bounded: never the whole transcript.
const usageWideTailBytes = 4 << 20

// slugNonAlnum matches Claude Code's transcript-dir encoder: every non-alphanumeric
// character (including "/", ".", "_") becomes "-". An earlier version replaced only
// "/", so a cwd like /home/x/my.app_v2 produced "-home-x-my.app_v2" while Claude's
// real dir is "-home-x-my-app-v2" — the transcript was never found and usage silently
// stayed blank. Existing "-" chars are non-alnum too and map to themselves.
var slugNonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// projectSlug mirrors Claude Code's ~/.claude/projects/<slug> naming.
func projectSlug(cwd string) string { return slugNonAlnum.ReplaceAllString(cwd, "-") }

// isClaudeBin reports whether a resolved agent binary is Claude Code — the only
// agent that writes the ~/.claude/projects transcripts this reads. Gating on it
// stops a shell (or codex) in a cwd that ALSO has claude history from being
// mislabelled with that history's usage.
func isClaudeBin(bin string) bool { return launcher.IsClaudeBin(bin) }

// isAgentBin reports whether a resolved binary is an interactive coding agent
// whose silence can mean "your turn". Anything else (a shell, a REPL) that goes
// quiet is simply idle.
func isAgentBin(bin string) bool {
	if bin == "" {
		return false
	}
	switch filepath.Base(bin) {
	case "claude", "codex":
		return true
	}
	return false
}

// parseUsageTail scans a byte tail from the end for the last assistant message
// carrying message.usage. Sums TOP-LEVEL usage fields only (iterations double-count).
// effort is the most recent top-level `effort` seen anywhere in the tail, and cwd
// the most recent absolute top-level `cwd`: Claude Code's EnterWorktree moves the
// agent without a chdir of the process, so the transcript is the only record of
// where it is working now.
func parseUsageTail(b []byte) (Usage, bool) {
	lines := strings.Split(string(b), "\n")
	var effort, cwd string
	for _, ln := range lines {
		var probe struct {
			Effort string `json:"effort"`
			Cwd    string `json:"cwd"`
		}
		if json.Unmarshal([]byte(ln), &probe) != nil {
			continue
		}
		if probe.Effort != "" {
			effort = probe.Effort
		}
		if filepath.IsAbs(probe.Cwd) {
			cwd = probe.Cwd
		}
	}
	postTokens := -1 // postTokens of a compact_boundary newer than any usage line
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var rec struct {
			Type            string `json:"type"`
			Subtype         string `json:"subtype"`
			IsAPIError      bool   `json:"isApiErrorMessage"`
			CompactMetadata struct {
				PostTokens int `json:"postTokens"`
			} `json:"compactMetadata"`
			Message struct {
				Role  string `json:"role"`
				Model string `json:"model"`
				Usage *struct {
					Input      int `json:"input_tokens"`
					CacheRead  int `json:"cache_read_input_tokens"`
					CacheWrite int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(ln), &rec) != nil {
			continue
		}
		if rec.Type == "system" && rec.Subtype == "compact_boundary" && postTokens < 0 {
			postTokens = rec.CompactMetadata.PostTokens
			continue
		}
		if rec.Message.Role == "assistant" && rec.Message.Usage != nil {
			u := rec.Message.Usage
			tokens := u.Input + u.CacheRead + u.CacheWrite
			// Claude Code skips its own placeholders (a failed API call leaves a
			// <synthetic> zero-usage message): so do we, keeping the last real reply.
			if rec.IsAPIError || rec.Message.Model == "<synthetic>" || tokens == 0 {
				continue
			}
			if postTokens >= 0 {
				tokens = postTokens
			}
			return Usage{
				Model:         rec.Message.Model,
				Effort:        effort,
				ContextTokens: tokens,
				Cwd:           cwd,
				PostCompact:   postTokens >= 0,
			}, true
		}
	}
	if postTokens >= 0 { // the tail starts after the last reply: the boundary is all there is
		return Usage{Effort: effort, ContextTokens: postTokens, Cwd: cwd, PostCompact: true}, true
	}
	return Usage{}, false
}

// contextWindows lists model-id prefixes and their context window (max input
// tokens). First match wins. A model matching no prefix is unknown: no guessed
// number, so no percentage. opus-4-6 and sonnet-4-6 are deliberately absent. Basis
// (inferred, not from a primary source: Anthropic's docs were unreachable): issue
// #79 reports Claude Code's catalog gives them 200k unless the [1m] variant is
// used, and the transcript's model id does not say which, so either number would
// be a guess.
var contextWindows = []struct {
	prefix string
	max    int
}{
	{"claude-opus-5", 1_000_000},
	{"claude-opus-4-8", 1_000_000},
	{"claude-opus-4-7", 1_000_000},
	{"claude-sonnet-5", 1_000_000},
	{"claude-haiku-5", 1_000_000},
	{"claude-fable-5", 1_000_000},
	{"claude-mythos-5", 1_000_000},
	{"claude-haiku-4-5", 200_000},
}

// contextMax maps a model id to its context window by prefix. Unknown →
// ok=false (raw-only display, never a guessed percentage).
func contextMax(model string) (int, bool) {
	for _, w := range contextWindows {
		if strings.HasPrefix(model, w.prefix) {
			return w.max, true
		}
	}
	return 0, false
}

// transcriptPathIn finds a boxed session's transcript by its conversation id.
// It never trusts what is in the box dir, and it never guesses across sessions:
// only <sid>.jsonl under projects/<slug(c)>/ for each cwd form c, and sid must
// be a valid session id. Not found means no transcript. The registry is
// skipped for an exited session, so this is what keeps a fresh or exited
// session from showing another session's file from the same box. It stats at
// most two paths per cwd form and never lists a directory.
//
// cwds are the session's spawn-dir forms (cwdFormsOf): the box runs claude with
// -w <resolved>, so the slug may be of the symlink-resolved path. One os.Root
// on boxDir serves the whole call; both checks use root.Lstat (confined, no
// final-symlink follow, never blocks), and the winner is opened later, through
// boxread (openTranscript), which re-checks it with fstat. The returned path
// is absolute under boxDir.
func transcriptPathIn(boxDir string, cwds []string, sid string) (string, bool) {
	if !sessionIDRe.MatchString(sid) {
		return "", false
	}
	root, err := os.OpenRoot(boxDir)
	if err != nil {
		return "", false
	}
	defer root.Close()
	for _, c := range cwds {
		relDir := filepath.Join(".claude", "projects", projectSlug(filepath.Clean(c)))
		// A slug dir that is itself a symlink (even one staying inside the box)
		// is refused: Lstat does not follow the final component.
		if fi, err := root.Lstat(relDir); err != nil || !fi.IsDir() {
			continue
		}
		rel := filepath.Join(relDir, sid+".jsonl")
		if fi, err := root.Lstat(rel); err == nil && fi.Mode().IsRegular() {
			return filepath.Join(boxDir, rel), true
		}
	}
	return "", false
}

// UsageFor reads the latest transcript usage for a claude session. ok=false for
// a non-claude session, one whose transcript can't be located/read, or one whose
// tail carries no usage line. Reads only a transcript bound to this session
// (boundTranscript): never the newest-by-cwd guess, which can be a sibling's.
func (m *Manager) UsageFor(id string) (Usage, bool) {
	l, ok := m.boundTranscript(id)
	if !ok {
		return Usage{}, false
	}
	f, fi, ok := l.openTranscript()
	if !ok {
		return Usage{}, false
	}
	defer f.Close()
	b, cut, err := readUsageTail(f, fi)
	if err != nil {
		return Usage{}, false
	}
	u, ok := parseUsageTail(b)
	if cut && (!ok || u.Model == "") { // no usage, or only a compact boundary: the model is further back
		u, ok = m.usageWidened(id, l.path, f, fi, u, ok)
	}
	if !ok {
		return Usage{}, false
	}
	spawn := []string{l.cwd}
	if l.boxed && len(l.cwds) > 0 {
		spawn = l.cwds // the raw spawn dir and its resolved form
	}
	u.Cwd = liveCwd(u.Cwd, spawn, l.boxed)
	if win, ok := contextMax(u.Model); ok && u.ContextTokens > 0 && u.ContextTokens <= win {
		u.ContextMax = win
		// Rounded and clamped like Claude Code's statusline (it uses Math.round).
		pctv := min(100, max(0, (u.ContextTokens*100+win/2)/win))
		u.Pct = &pctv
	}
	return u, true
}

// usageWideEntry is the cached result of one widened read, valid while the
// transcript still has the same path, size and mtime.
type usageWideEntry struct {
	path  string
	mtime time.Time
	size  int64
	u     Usage
	ok    bool
}

// usageWidened parses the last usageWideTailBytes of a transcript whose normal
// tail was not enough. The read is ~100 ms and tens of MB, and the sidebar polls
// every few seconds, so the answer (including "none") is reused while the file is
// unchanged. One entry per session; peekForget drops it with the session. If the
// wide read fails, narrow (the normal tail's result) is returned as is.
func (m *Manager) usageWidened(id, path string, f *os.File, fi os.FileInfo, narrow Usage, narrowOK bool) (Usage, bool) {
	m.usageMu.Lock()
	e, hit := m.usageWide[id]
	m.usageMu.Unlock()
	if hit && e.path == path && e.size == fi.Size() && e.mtime.Equal(fi.ModTime()) {
		return e.u, e.ok
	}
	b, err := readTail(f, fi, usageWideTailBytes)
	if err != nil {
		return narrow, narrowOK // a read error is not cached (the next poll retries); the normal tail's answer stands
	}
	u, ok := parseUsageTail(b)
	m.usageMu.Lock()
	if m.usageWide == nil {
		m.usageWide = map[string]usageWideEntry{}
	}
	m.usageWide[id] = usageWideEntry{path: path, mtime: fi.ModTime(), size: fi.Size(), u: u, ok: ok}
	m.usageMu.Unlock()
	return u, ok
}

// ResolveBins fills in Bin for any session that still lacks it. Reconcile runs at
// startup BEFORE runtime profiles are registered with the launcher, so a
// profile-based session (e.g. a custom "my-project" agent) reattaches with an
// empty Bin and BinFor(name) would have failed. Call this once profiles are
// loaded so claude-only features (usage) work for reconciled profile sessions.
func (m *Manager) ResolveBins() {
	br, ok := m.launcher.(BinResolver)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.Bin != "" {
			continue
		}
		if b, err := br.BinFor(launcher.SpawnSpec{Agent: s.Agent}); err == nil {
			s.Bin = b
			// Lock order m.mu -> s.mu, the same as List's call into Info.
			s.mu.Lock()
			s.agentKind = isAgentBin(b)
			s.mu.Unlock()
		}
	}
}

// readUsageTail reads the last usageTailBytes of a file (or the whole file if
// smaller) — never the whole of a multi-MB transcript. cut reports whether the
// read actually started partway through the file (size > usageTailBytes):
// fix round 3 (2026-09-29) — parsePeekTailCut uses it to fail AskFull closed
// when a byte cut, even one that happens to land exactly on a newline (so no
// line fails to parse), could have hidden context just outside the window.
// The caller opens the file (host: os.Open; boxed: boxread.Open) and closes it.
func readUsageTail(f *os.File, fi os.FileInfo) (data []byte, cut bool, err error) {
	data, err = readTail(f, fi, usageTailBytes)
	return data, fi.Size() > usageTailBytes, err
}

// readTail reads the last n bytes of a file (all of it if smaller).
func readTail(f *os.File, fi os.FileInfo, n int64) ([]byte, error) {
	off := int64(0)
	if size := fi.Size(); size > n {
		off = size - n
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, n)) // the file may have grown since fi was taken
}

// liveCwd turns the transcript's cwd into the base the browser should join
// relative terminal paths onto, or "" to keep the spawn cwd. That cwd moves on
// every Bash `cd`, not only on Claude Code's EnterWorktree (which moves the agent
// without a chdir of the process), while a path the agent prints is relative to
// its checkout. So the base changes only when the cwd sits in a linked worktree
// the spawn cwd is not already inside, and it becomes that worktree's root.
// Boxed tier: the cwd must also be at or under the spawn cwd, the one mount that
// names the same files in the container and on the host (box/argv.go: cwd:cwd).
// spawn holds every form the spawn dir is known by (the raw dir and, for a
// symlinked one, its resolved path, which is what the box runs claude in); the
// host tier passes just the one.
func liveCwd(raw string, spawn []string, boxed bool) string {
	if raw == "" || !filepath.IsAbs(raw) {
		return ""
	}
	raw = filepath.Clean(raw)
	forms := make([]string, len(spawn))
	for i, sp := range spawn {
		forms[i] = filepath.Clean(sp)
	}
	if boxed && !withinAny(raw, forms) {
		return ""
	}
	root, ok := worktreeRoot(raw)
	if !ok {
		return ""
	}
	for _, sp := range forms {
		if within(sp, root) {
			return ""
		}
	}
	return root
}

// withinAny reports whether p is at or below any of dirs (all already Clean).
func withinAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if within(p, d) {
			return true
		}
	}
	return false
}

// within reports whether p is dir or below it (both already Clean).
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// worktreeRoot reports dir's checkout root only if it is a linked worktree.
func worktreeRoot(dir string) (string, bool) {
	_, root, ok := gitrepo.CheckoutOf(dir)
	return root, ok && root != ""
}

// sameRepository reports whether a and b are checkouts (or dirs inside
// checkouts) of one repository, by common git dir.
func sameRepository(a, b string) bool {
	ca, _, okA := gitrepo.CheckoutOf(a)
	cb, _, okB := gitrepo.CheckoutOf(b)
	return okA && okB && ca == cb
}
