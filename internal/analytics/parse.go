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
	CacheCreation int64 // total of the 5m and 1h cache writes
	CacheWrite1h  int64 // the 1h part of CacheCreation (the rest is 5m)
	Prompts       int64
	Cost          float64 // USD: the sum of MessageCostUSD per counted message
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
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			// CacheSplit is absent in transcripts written before Claude Code
			// split cache writes by TTL; then the whole total is a 5m write.
			CacheSplit *struct {
				E1h int64 `json:"ephemeral_1h_input_tokens"`
				E5m int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
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
	p, sid, consumed, _, _, err := parseTranscript(r, nil)
	return p, sid, consumed, err
}

// parseTranscript is ParseTranscript plus a flag reporting an unterminated tail
// longer than boxread.MaxLineBytes. Such a tail is consumed (it can never become
// a parseable line, and must not be re-read every pass); the caller logs it.
//
// Lines longer than the cap are skipped, not parsed: a box can create a sparse
// file with no newline, and an unbounded read would buffer all of it.
//
// Claude Code writes one record per content block (thinking, text, tool_use) of
// an assistant message, each repeating the message's full usage, so usage is
// counted once per message id. seen holds the ids counted so far (last
// maxSeenIDs, oldest first) so a message split across two ingest batches is
// still counted once; the updated list is returned. A record without an id is
// always counted (old transcripts).
func parseTranscript(r io.Reader, seen []string) (*Parsed, string, int64, bool, []string, error) {
	p := newParsed()
	dd := newDedupe(seen)
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
				processLine(line, p, dd, pending, &cur, &sessionID, flushPending)
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
	return p, sessionID, consumed, overlongTail, dd.list(), nil
}

// processLine folds one complete JSONL line into the rollup. cur/pending carry the
// prompt→answering-model state across lines; flushPending assigns held prompts;
// sessionID captures the first non-empty session id seen.
func processLine(line []byte, p *Parsed, dd *dedupe, pending map[string]int64, cur, sessionID *string, flushPending func(string)) {
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
		if !dd.first(rec.Message.ID) {
			// Another content block of a message already counted: no tokens or
			// cost, but it is still the answering model for held prompts.
			*cur = rec.Message.Model
			flushPending(*cur)
			return
		}
		u := rec.Message.Usage
		// Transcripts are untrusted (a contained agent writes them): a negative
		// count must never lower the shared totals or the stored cost.
		in, out, cr := u.Input, u.Output, u.CacheRead
		if in < 0 {
			in = 0
		}
		if out < 0 {
			out = 0
		}
		if cr < 0 {
			cr = 0
		}
		cc := u.CacheCreation
		if cc < 0 {
			cc = 0
		}
		w1h := int64(0)
		if u.CacheSplit != nil {
			w1h = u.CacheSplit.E1h
			if w1h < 0 {
				w1h = 0
			}
			if w1h > cc {
				w1h = cc // the record's total is the authority; never count more
			}
		}
		t := p.tok(day, rec.Message.Model)
		t.Input += in
		t.Output += out
		t.CacheRead += cr
		t.CacheCreation += cc
		t.CacheWrite1h += w1h
		t.Cost += MessageCostUSD(rec.Message.Model, MsgUsage{
			Input: in, Output: out, CacheRead: cr,
			CacheWrite5m: cc - w1h, CacheWrite1h: w1h,
		})
		*cur = rec.Message.Model
		flushPending(*cur)
		return
	}
	if isHumanPrompt(&rec) {
		pending[day]++
	}
}

// maxSeenIDs bounds the remembered message ids per transcript. A message's
// content-block records are adjacent, so a small window covers any batch seam.
const maxSeenIDs = 64

// dedupe remembers recently counted message ids: a set for lookup and a ring
// (oldest first) for eviction.
type dedupe struct {
	set  map[string]struct{}
	ring []string
}

func newDedupe(seen []string) *dedupe {
	if len(seen) > maxSeenIDs {
		seen = seen[len(seen)-maxSeenIDs:]
	}
	d := &dedupe{set: make(map[string]struct{}, maxSeenIDs+1)}
	for _, id := range seen {
		// Defensive: persisted state is ours, but never trust an entry's shape.
		if !safeMsgID(id) {
			continue
		}
		if _, dup := d.set[id]; !dup {
			d.set[id] = struct{}{}
			d.ring = append(d.ring, id)
		}
	}
	return d
}

// maxMsgIDLen bounds a remembered message id. Transcripts can be written by a
// contained, untrusted agent, so ids are checked before they reach the seen
// set or the persisted state.
const maxMsgIDLen = 128

// safeMsgID reports whether id matches ^[A-Za-z0-9_-]{1,128}$ (a byte loop,
// no regexp). Real Claude ids (msg_...) match; anything else is treated like
// an empty id.
func safeMsgID(id string) bool {
	if len(id) == 0 || len(id) > maxMsgIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// first reports whether a record with this message id should be counted, and
// remembers the id. An empty or unsafe id is always counted and never remembered.
func (d *dedupe) first(id string) bool {
	if !safeMsgID(id) {
		return true
	}
	if _, dup := d.set[id]; dup {
		return false
	}
	d.set[id] = struct{}{}
	d.ring = append(d.ring, id)
	if len(d.ring) > maxSeenIDs {
		delete(d.set, d.ring[0])
		d.ring = d.ring[1:]
	}
	return true
}

func (d *dedupe) list() []string { return d.ring }
