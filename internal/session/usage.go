package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
func isClaudeBin(bin string) bool { return bin != "" && filepath.Base(bin) == "claude" }

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
// effort is the most recent top-level `effort` seen anywhere in the tail.
func parseUsageTail(b []byte) (Usage, bool) {
	lines := strings.Split(string(b), "\n")
	var effort string
	for _, ln := range lines {
		var probe struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal([]byte(ln), &probe) == nil && probe.Effort != "" {
			effort = probe.Effort
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
// tail carries no usage line. Immutable session fields (Cwd/Box/Created/Bin) are
// read without the lock; only the map lookup and paths are guarded.
func (m *Manager) UsageFor(id string) (Usage, bool) {
	// Capture everything under the lock: Bin can be filled in post-reconcile by
	// ResolveBins (a locked write), so reading it lock-free would race. Cwd/Box/
	// Created are immutable but captured here too for one clean critical section.
	m.mu.Lock()
	s := m.sessions[id]
	home, stateDir := m.home, m.stateDir
	var bin, cwd, box string
	var created time.Time
	if s != nil {
		bin, cwd, box, created = s.Bin, s.Cwd, s.Box, s.Created
	}
	m.mu.Unlock()
	if s == nil || !isClaudeBin(bin) {
		return Usage{}, false
	}
	base := home
	if box != "" && stateDir != "" {
		base = filepath.Join(stateDir, "boxstate", box)
	}
	if base == "" {
		return Usage{}, false
	}
	p, ok := transcriptPath(cwd, base, created)
	if !ok {
		return Usage{}, false
	}
	b, err := readUsageTail(p)
	if err != nil {
		return Usage{}, false
	}
	u, ok := parseUsageTail(b)
	if !ok {
		return Usage{}, false
	}
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
// smaller) — never the whole of a multi-MB transcript.
func readUsageTail(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	off := int64(0)
	if size > usageTailBytes {
		off = size - usageTailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
