// Package analytics turns Claude Code JSONL transcripts into durable, queryable
// rollups (daily token usage, prompt counts, and skill/plugin/MCP attribution)
// so token/prompt trends across model releases can be examined over time.
//
// parse.go is the pure core: it reads a transcript stream and produces rollup
// deltas with no filesystem or database dependency, so it is fully unit-testable.
package analytics

import (
	"encoding/json"
	"io"

	"github.com/culiops/agent-box/internal/boxread"
)

// TokKey identifies a token/prompt rollup bucket. Prompts have no model of their
// own, so each is attributed to the model that answered it (see ParseTranscript).
type TokKey struct {
	Day   string // "YYYY-MM-DD" (UTC), from the record timestamp
	Model string
}

// Tok is one bucket's summed usage. Input/CacheRead/CacheCreation are summed
// across turns on purpose: each turn re-reads context, and that re-read IS real
// (billed) usage — the number reflects total volume processed, not a snapshot.
type Tok struct {
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Prompts       int64
}

// AttrKey identifies a skill/plugin/MCP attribution bucket. Kind is one of
// "skill", "plugin", "mcp_server", "mcp_tool".
type AttrKey struct {
	Day  string
	Kind string
	Name string
}

// Parsed holds the rollup deltas extracted from one transcript's bytes.
type Parsed struct {
	Tokens map[TokKey]*Tok
	Attrs  map[AttrKey]int64
}

func newParsed() *Parsed {
	return &Parsed{Tokens: map[TokKey]*Tok{}, Attrs: map[AttrKey]int64{}}
}

func (p *Parsed) tok(day, model string) *Tok {
	k := TokKey{Day: day, Model: model}
	t := p.Tokens[k]
	if t == nil {
		t = &Tok{}
		p.Tokens[k] = t
	}
	return t
}

// dayOf extracts the UTC date prefix from an ISO8601 timestamp. It returns "" for
// anything that is not at least a full YYYY-MM-DD, so a malformed record cannot
// produce a garbage day bucket.
func dayOf(ts string) string {
	if len(ts) < 10 {
		return ""
	}
	d := ts[:10]
	// Cheap shape check: dddd-dd-dd.
	for i, c := range d {
		switch i {
		case 4, 7:
			if c != '-' {
				return ""
			}
		default:
			if c < '0' || c > '9' {
				return ""
			}
		}
	}
	return d
}

type record struct {
	Timestamp            string `json:"timestamp"`
	SessionID            string `json:"sessionId"`
	IsSidechain          bool   `json:"isSidechain"`
	IsMeta               bool   `json:"isMeta"`
	PromptSource         string `json:"promptSource"`
	AttributionSkill     string `json:"attributionSkill"`
	AttributionPlugin    string `json:"attributionPlugin"`
	AttributionMcpServer string `json:"attributionMcpServer"`
	AttributionMcpTool   string `json:"attributionMcpTool"`
	Message              *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// isHumanPrompt reports whether a record is a real prompt the user typed — a
// user-role message whose content is plain text (a string, or a content array
// carrying a text block and NO tool_result), which is neither a subagent
// (sidechain) turn nor a system-injected meta message, and which was not injected
// by the SDK (agent-box's own headless spawns record their initial prompt as a
// user message; counting those would distort the model-efficiency signal). It is
// a heuristic: a slash-command expansion is also user text, so it can over-count
// slightly.
func isHumanPrompt(r *record) bool {
	if r.Message == nil || r.Message.Role != "user" || r.IsSidechain || r.IsMeta {
		return false
	}
	if r.PromptSource == "sdk" {
		return false
	}
	return contentIsPromptText(r.Message.Content)
}

// contentIsPromptText: a JSON string is prompt text; a JSON array is prompt text
// only if it has a text block and no tool_result block (a tool_result is the
// engine feeding a tool's output back in, not the user typing).
func contentIsPromptText(raw json.RawMessage) bool {
	s := trimSpace(raw)
	if len(s) == 0 {
		return false
	}
	if s[0] == '"' {
		return true
	}
	if s[0] != '[' {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(s, &blocks) != nil {
		return false
	}
	hasText := false
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false
		}
		if b.Type == "text" {
			hasText = true
		}
	}
	return hasText
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\n' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

// ParseTranscript streams one transcript's bytes and returns the rollup deltas,
// the session id observed in the records (the first non-empty `sessionId` — for a
// subagent file this is the PARENT session, which is what its usage should be
// attributed to), and how many bytes were consumed (up to and including the last
// complete line). A trailing partial line (no newline yet, because a live claude
// process is still writing) is NOT consumed, so the next incremental run re-reads
// it once complete.
//
// boxread.LineReader (never Scanner): a single JSONL line — a big tool_result or
// a thinking block — can exceed Scanner's 64 KiB cap and be silently truncated.
// It is capped at boxread.MaxLineBytes instead of unbounded.
func ParseTranscript(r io.Reader) (*Parsed, string, int64, error) {
	p, sid, consumed, _, err := parseTranscript(r)
	return p, sid, consumed, err
}

// parseTranscript is ParseTranscript plus a flag reporting an unterminated tail
// longer than boxread.MaxLineBytes. Such a tail is consumed (it can never become
// a parseable line, and must not be re-read every pass); the caller logs it.
//
// Lines longer than the cap are skipped, not parsed: a box can create a sparse
// file with no newline, and an unbounded read would buffer all of it.
func parseTranscript(r io.Reader) (*Parsed, string, int64, bool, error) {
	p := newParsed()
	lr := boxread.NewLineReader(r, boxread.MaxLineBytes)

	// Prompts precede the assistant message that answers them, so a prompt is held
	// until the model that answers is known, then attributed to it. pending maps a
	// day to prompts awaiting a model.
	pending := map[string]int64{}
	cur := "" // most recent assistant model seen
	sessionID := ""
	flushPending := func(model string) {
		if model == "" || len(pending) == 0 {
			return
		}
		for day, c := range pending {
			p.tok(day, model).Prompts += c
		}
		pending = map[string]int64{}
	}

	var consumed int64
	overlongTail := false
	for {
		line, n, over, err := lr.Next()
		if err == nil { // newline-terminated: consumed, parsed unless over-long
			consumed += int64(n)
			if !over {
				processLine(line, p, pending, &cur, &sessionID, flushPending)
			}
			continue
		}
		// EOF (or a read error). A short unterminated tail is left unconsumed so
		// the next run re-reads it once complete.
		if over {
			consumed += int64(n)
			overlongTail = true
		}
		break
	}
	// Prompts with no answering assistant in this batch: attribute to the last
	// model seen (or "" if none — a rare unknown bucket, not silently dropped).
	if len(pending) > 0 {
		for day, c := range pending {
			p.tok(day, cur).Prompts += c
		}
	}
	return p, sessionID, consumed, overlongTail, nil
}

// processLine folds one complete JSONL line into the rollup. cur/pending carry the
// prompt→answering-model state across lines; flushPending assigns held prompts;
// sessionID captures the first non-empty session id seen.
func processLine(line []byte, p *Parsed, pending map[string]int64, cur, sessionID *string, flushPending func(string)) {
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if *sessionID == "" && rec.SessionID != "" {
		*sessionID = rec.SessionID
	}
	day := dayOf(rec.Timestamp)

	// Attribution counts (a record can carry several of these at once).
	for _, a := range []struct{ kind, name string }{
		{"skill", rec.AttributionSkill},
		{"plugin", rec.AttributionPlugin},
		{"mcp_server", rec.AttributionMcpServer},
		{"mcp_tool", rec.AttributionMcpTool},
	} {
		if a.name != "" {
			p.Attrs[AttrKey{Day: day, Kind: a.kind, Name: a.name}]++
		}
	}

	if rec.Message == nil {
		return
	}
	if rec.Message.Role == "assistant" && rec.Message.Usage != nil {
		u := rec.Message.Usage
		t := p.tok(day, rec.Message.Model)
		t.Input += u.Input
		t.Output += u.Output
		t.CacheRead += u.CacheRead
		t.CacheCreation += u.CacheCreation
		*cur = rec.Message.Model
		flushPending(*cur)
		return
	}
	if isHumanPrompt(&rec) {
		pending[day]++
	}
}
