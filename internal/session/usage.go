package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// projectSlug mirrors Claude Code's transcript dir naming: every "/" becomes "-".
// (A leading "/" yields a leading "-", matching ~/.claude/projects/-home-...)
func projectSlug(cwd string) string { return strings.ReplaceAll(cwd, "/", "-") }

// isClaudeBin reports whether a resolved agent binary is Claude Code — the only
// agent that writes the ~/.claude/projects transcripts this reads. Gating on it
// stops a shell (or codex) in a cwd that ALSO has claude history from being
// mislabelled with that history's usage.
func isClaudeBin(bin string) bool { return bin != "" && filepath.Base(bin) == "claude" }

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
