package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/agent-box/internal/launcher"
)

// Usage is a per-session snapshot read from the agent's JSONL transcript.
type Usage struct {
	Model         string `json:"model,omitempty"`
	Effort        string `json:"effort,omitempty"`
	ContextTokens int    `json:"contextTokens"`
	ContextMax    int    `json:"contextMax,omitempty"`
	Pct           *int   `json:"pct,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
}

const usageTailBytes = 256 << 10

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

// transcriptPath finds the newest *.jsonl for cwd under <claudeDir>/projects/<slug>/
// whose mtime >= created; falls back to the newest overall. ok=false if none.
// claudeDir is the .claude directory itself (the default ~/.claude, an agent
// account's config dir, or a box's per-box .claude), not a home.
func transcriptPath(cwd, claudeDir string, created time.Time) (string, bool) {
	dir := filepath.Join(claudeDir, "projects", projectSlug(cwd))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var best, newest string
	var bestT, newestT time.Time
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if fi.ModTime().After(newestT) {
			newestT, newest = fi.ModTime(), p
		}
		if !fi.ModTime().Before(created) && fi.ModTime().After(bestT) {
			bestT, best = fi.ModTime(), p
		}
	}
	if best != "" {
		return best, true
	}
	if newest != "" {
		return newest, true
	}
	return "", false
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
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var rec struct {
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
		if rec.Message.Role == "assistant" && rec.Message.Usage != nil {
			u := rec.Message.Usage
			return Usage{
				Model:         rec.Message.Model,
				Effort:        effort,
				ContextTokens: u.Input + u.CacheRead + u.CacheWrite,
				Cwd:           cwd,
			}, true
		}
	}
	return Usage{}, false
}

// contextMax maps a model id to its context window. opus-4.8 is 1M (measured:
// real sessions read ~597k, above any 200k). Unknown → ok=false (raw-only display).
func contextMax(model string) (int, bool) {
	switch model {
	case "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5":
		return 1_000_000, true
	case "claude-haiku-4-5-20251001":
		return 200_000, true
	default:
		return 0, false
	}
}

// transcriptPathIn is transcriptPath for a boxed session. It never trusts what
// is in the box dir, and unlike the host heuristic it never guesses across
// sessions:
//   - sid known: only <sid>.jsonl under projects/<slug(c)>/ for each cwd form c.
//     Not found means no transcript. The registry is skipped for an exited
//     session, so this is what keeps a fresh or exited session from showing
//     another session's file from the same box.
//   - sid unknown: the newest regular *.jsonl whose mtime is not before
//     created (a file older than the session cannot be its own), and whose stem
//     is not the conversation id of another session in the same box (exclude):
//     a busy sibling's transcript is newer than a fresh session's Created. There
//     is no "newest overall" fallback.
//
// cwds are the session's spawn-dir forms (cwdFormsOf): the box runs claude with
// -w <resolved>, so the slug may be of the symlink-resolved path. One os.Root
// on boxDir serves the whole call; candidates are ranked with root.Lstat
// (confined, no final-symlink follow, never blocks, never DirEntry.Info), and
// only the winner is opened later, through boxread (openTranscript), which
// re-checks it with fstat. The returned path is absolute under boxDir.
func transcriptPathIn(boxDir string, cwds []string, sid string, exclude map[string]bool, created time.Time) (string, bool) {
	if sid != "" && !sessionIDRe.MatchString(sid) {
		return "", false
	}
	root, err := os.OpenRoot(boxDir)
	if err != nil {
		return "", false
	}
	defer root.Close()
	var best string
	var bestT time.Time
	for _, c := range cwds {
		relDir := filepath.Join(".claude", "projects", projectSlug(filepath.Clean(c)))
		// A slug dir that is itself a symlink (even one staying inside the box)
		// is refused: Lstat does not follow the final component.
		if fi, err := root.Lstat(relDir); err != nil || !fi.IsDir() {
			continue
		}
		if sid != "" {
			rel := filepath.Join(relDir, sid+".jsonl")
			if fi, err := root.Lstat(rel); err == nil && fi.Mode().IsRegular() {
				return filepath.Join(boxDir, rel), true
			}
			continue
		}
		d, err := root.OpenFile(relDir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		ents, err := d.ReadDir(4096)
		d.Close()
		if err != nil && len(ents) == 0 {
			continue
		}
		for _, e := range ents {
			name := e.Name()
			stem, isJSONL := strings.CutSuffix(name, ".jsonl")
			if e.IsDir() || !isJSONL || !sessionIDRe.MatchString(stem) || exclude[stem] {
				continue
			}
			rel := filepath.Join(relDir, name)
			fi, err := root.Lstat(rel)
			if err != nil || !fi.Mode().IsRegular() || fi.ModTime().Before(created) {
				continue
			}
			if fi.ModTime().After(bestT) {
				bestT, best = fi.ModTime(), filepath.Join(boxDir, rel)
			}
		}
	}
	return best, best != ""
}

// UsageFor reads the latest transcript usage for a claude session. ok=false for
// a non-claude session, one whose transcript can't be located/read, or one whose
// tail carries no usage line. Same locator as PeekFor (registry, then boxstate,
// then cwd+mtime).
func (m *Manager) UsageFor(id string) (Usage, bool) {
	l := m.locate(id)
	if !l.claude || l.path == "" {
		return Usage{}, false
	}
	f, fi, ok := l.openTranscript()
	if !ok {
		return Usage{}, false
	}
	defer f.Close()
	b, _, err := readUsageTail(f, fi)
	if err != nil {
		return Usage{}, false
	}
	u, ok := parseUsageTail(b)
	if !ok {
		return Usage{}, false
	}
	spawn := []string{l.cwd}
	if l.boxed && len(l.cwds) > 0 {
		spawn = l.cwds // the raw spawn dir and its resolved form
	}
	u.Cwd = liveCwd(u.Cwd, spawn, l.boxed)
	if max, ok := contextMax(u.Model); ok && u.ContextTokens <= max {
		u.ContextMax = max
		pctv := u.ContextTokens * 100 / max
		u.Pct = &pctv
	}
	return u, true
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
	size := fi.Size()
	off := int64(0)
	if size > usageTailBytes {
		off = size - usageTailBytes
		cut = true
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, false, err
	}
	data, err = io.ReadAll(f)
	return data, cut, err
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
	_, root, ok := checkoutOf(dir)
	return root, ok && root != ""
}

// checkoutOf walks up from dir to the nearest .git and reports the repository's
// common git dir, plus the checkout root when that checkout is a linked worktree
// (a .git FILE whose gitdir is <common>/worktrees/<name>; its PARENT is named
// worktrees, so a submodule of a worktree — …/worktrees/wt/modules/sub — merely
// contains the word). A .git directory is a main checkout; a submodule's .git
// file names its own gitdir as the common dir. ok=false: no .git found, or one
// that is neither a directory nor a readable regular file.
func checkoutOf(dir string) (common, wtRoot string, ok bool) {
	for d := dir; ; {
		g := filepath.Join(d, ".git")
		if fi, err := os.Lstat(g); err == nil {
			if fi.IsDir() {
				return g, "", true
			}
			gitdir, ok := readGitFile(g)
			if !ok || gitdir == "" {
				return "", "", false
			}
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(d, gitdir)
			}
			gitdir = filepath.Clean(gitdir)
			if filepath.Base(filepath.Dir(gitdir)) == "worktrees" {
				return filepath.Dir(filepath.Dir(gitdir)), d, true
			}
			return gitdir, "", true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", false
		}
		d = parent
	}
}

// sameRepository reports whether a and b are checkouts (or dirs inside
// checkouts) of one repository, by common git dir.
func sameRepository(a, b string) bool {
	ca, _, okA := checkoutOf(a)
	cb, _, okB := checkoutOf(b)
	return okA && okB && ca == cb
}

// readGitFile returns the gitdir a .git FILE points at; ok=false for anything
// that is not a regular file of the form "gitdir: <path>".
func readGitFile(p string) (string, bool) {
	f, ok := openRegular(p)
	if !ok {
		return "", false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	gitdir, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	return strings.TrimSpace(gitdir), found
}

// openRegular opens p only if it is a regular file, deciding on the opened fd
// rather than a prior Lstat: under a box mount the agent owns these files and can
// swap one for a FIFO between the check and the open, and a plain open of a FIFO
// blocks with no timeout (one pinned thread per poll). O_NONBLOCK makes that open
// return at once; O_NOFOLLOW refuses a symlink.
func openRegular(p string) (*os.File, bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, false
	}
	return f, true
}
