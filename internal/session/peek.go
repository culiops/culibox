package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Peek is a per-session snapshot of what the agent last said, read from the
// JSONL transcript. It answers the two questions the sidebar cannot answer from
// PTY output alone: what is this session saying, and is it mid-tool.
type Peek struct {
	Text string `json:"text"`
	// Pending reports that the last record in the transcript is an assistant
	// turn ending on a tool_use with no result yet — the session is inside a
	// tool call, which includes sitting on a permission prompt. This is ground
	// truth from the transcript, unlike activityOf's silence heuristic.
	Pending bool `json:"pending,omitempty"`
}

// summaryMax is the rune budget for the one-line sidebar summary. Measured at
// the real 279px sidebar width; longer text is elided rather than wrapped.
const summaryMax = 120

// peekRec is the subset of a transcript record we decode. content is kept raw
// because it is an array of blocks on assistant records but may be a bare
// string on user records — decoding it eagerly would reject the latter.
type peekRec struct {
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// contentBlocks decodes message.content as a block array. A bare string (or any
// other shape) yields no blocks rather than an error: callers only ever ask
// "does this record contain blocks of interest".
func contentBlocks(raw json.RawMessage) []contentBlock {
	var bs []contentBlock
	if json.Unmarshal(raw, &bs) == nil {
		return bs
	}
	return nil
}

// parsePeekTail scans a byte tail backwards for the last assistant text block,
// and reports whether the final record is an unresolved tool_use.
//
// ok reports that at least one record parsed — i.e. the transcript is readable
// and non-empty. Text may still be empty ("no message yet"), which the UI shows
// differently from "transcript unreadable". A line that fails to unmarshal is
// skipped: the tail almost always begins mid-record.
func parsePeekTail(b []byte) (Peek, bool) {
	lines := strings.Split(string(b), "\n")
	var p Peek
	var parsedAny, sawLast, foundText bool
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var rec peekRec
		if json.Unmarshal([]byte(ln), &rec) != nil {
			continue
		}
		parsedAny = true
		bs := contentBlocks(rec.Message.Content)
		// The first record we reach walking backwards IS the last record in the
		// file. If a tool_result had arrived, it would be that record instead,
		// so "last block is tool_use" is sufficient — no forward scan needed.
		if !sawLast {
			sawLast = true
			if rec.Message.Role == "assistant" && len(bs) > 0 && bs[len(bs)-1].Type == "tool_use" {
				p.Pending = true
			}
		}
		if !foundText && rec.Message.Role == "assistant" {
			for j := len(bs) - 1; j >= 0; j-- {
				if bs[j].Type == "text" && strings.TrimSpace(bs[j].Text) != "" {
					p.Text = bs[j].Text
					foundText = true
					break
				}
			}
		}
		if foundText {
			break
		}
	}
	return p, parsedAny
}

// summaryLine reduces peek text to the single line the sidebar shows: the first
// non-empty line, elided to summaryMax runes. Runes, not bytes — a byte-sliced
// multi-byte character renders as a replacement glyph.
func summaryLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		r := []rune(ln)
		if len(r) <= summaryMax {
			return ln
		}
		return string(r[:summaryMax-1]) + "…"
	}
	return ""
}

// peekEntry is one cached parse, valid while the transcript's mtime is unchanged.
type peekEntry struct {
	mtime time.Time
	peek  Peek
}

// peekCached returns the cached parse when it still matches mtime.
func (m *Manager) peekCached(id string, mtime time.Time) (Peek, bool) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	e, ok := m.peekCache[id]
	if !ok || !e.mtime.Equal(mtime) {
		return Peek{}, false
	}
	return e.peek, true
}

func (m *Manager) peekStore(id string, mtime time.Time, p Peek) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	if m.peekCache == nil {
		m.peekCache = map[string]peekEntry{}
	}
	m.peekCache[id] = peekEntry{mtime: mtime, peek: p}
}

// PeekFor reads the latest transcript peek for a claude session. ok=false for a
// non-claude session, one whose transcript cannot be located or read, or one
// whose tail holds no parseable record.
//
// The sidebar polls this for every session, so the hot path is one os.Stat: the
// tail is re-read only when mtime moved. A waiting session's transcript is
// static, so the sessions whose summaries anyone reads are served from cache.
//
// Locking mirrors UsageFor: capture Bin/Cwd/Box/Created in one critical section
// (Bin is backfilled post-reconcile by ResolveBins, so a lock-free read races),
// then work without the lock.
func (m *Manager) PeekFor(id string) (Peek, bool) {
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
		return Peek{}, false
	}
	base := home
	if box != "" && stateDir != "" {
		base = filepath.Join(stateDir, "boxstate", box)
	}
	if base == "" {
		return Peek{}, false
	}
	path, ok := transcriptPath(cwd, base, created)
	if !ok {
		return Peek{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Peek{}, false
	}
	if p, ok := m.peekCached(id, fi.ModTime()); ok {
		return p, true
	}
	b, err := readUsageTail(path)
	if err != nil {
		return Peek{}, false
	}
	p, ok := parsePeekTail(b)
	if !ok {
		return Peek{}, false
	}
	m.peekStore(id, fi.ModTime(), p)
	return p, true
}
