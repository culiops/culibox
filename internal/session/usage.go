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

// transcriptPath finds the newest *.jsonl for cwd under <home>/.claude/projects/<slug>/
// whose mtime >= created; falls back to the newest overall. ok=false if none.
func transcriptPath(cwd, home string, created time.Time) (string, bool) {
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
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

// UsageFor reads the latest transcript usage for a claude session. ok=false for
// a non-claude session, one whose transcript can't be located/read, or one whose
// tail carries no usage line. Same locator as PeekFor (registry, then boxstate,
// then cwd+mtime).
func (m *Manager) UsageFor(id string) (Usage, bool) {
	l := m.locate(id)
	if !l.claude || l.path == "" {
		return Usage{}, false
	}
	b, _, err := readUsageTail(l.path)
	if err != nil {
		return Usage{}, false
	}
	u, ok := parseUsageTail(b)
	if !ok {
		return Usage{}, false
	}
	u.Cwd = liveCwd(u.Cwd, l.cwd, l.boxed)
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
func readUsageTail(path string) (data []byte, cut bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
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
func liveCwd(raw, spawn string, boxed bool) string {
	if raw == "" || !filepath.IsAbs(raw) {
		return ""
	}
	raw, spawn = filepath.Clean(raw), filepath.Clean(spawn)
	if boxed && !within(raw, spawn) {
		return ""
	}
	root, ok := worktreeRoot(raw)
	if !ok || within(spawn, root) {
		return ""
	}
	return root
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
