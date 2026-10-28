package analytics

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestDayOf(t *testing.T) {
	cases := map[string]string{
		"2026-08-05T14:50:54.586Z": "2026-08-05",
		"2026-12-31T23:59:59Z":     "2026-12-31",
		"":                         "",
		"garbage":                  "",
		"2026-08":                  "", // too short to be a date
	}
	for in, want := range cases {
		if got := dayOf(in); got != want {
			t.Errorf("dayOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// A representative transcript: two real prompts, one tool_result (not a prompt),
// a sidechain user turn (subagent, not a prompt), a meta user turn (not a prompt),
// an SDK-injected prompt (not a human prompt), two assistant messages with usage,
// attribution fields, and several non-message record types that must be skipped.
const sampleJSONL = `
{"type":"user","sessionId":"S1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"hello, do a thing"}}
{"type":"assistant","sessionId":"S1","timestamp":"2026-08-05T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}},"attributionSkill":"brainstorming"}
{"type":"user","sessionId":"S1","timestamp":"2026-08-05T10:01:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"result bytes"}]}}
{"type":"assistant","sessionId":"S1","timestamp":"2026-08-05T10:01:05Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":200,"output_tokens":40,"cache_read_input_tokens":7,"cache_creation_input_tokens":0}},"attributionMcpServer":"playwright","attributionMcpTool":"browser_navigate"}
{"type":"user","sessionId":"S1","timestamp":"2026-08-05T10:02:00Z","message":{"role":"user","content":[{"type":"text","text":"another real prompt"}]}}
{"type":"user","sessionId":"S1","timestamp":"2026-08-05T10:02:05Z","isSidechain":true,"message":{"role":"user","content":"subagent internal prompt"}}
{"type":"user","sessionId":"S1","timestamp":"2026-08-05T10:02:06Z","isMeta":true,"message":{"role":"user","content":"meta note"}}
{"type":"user","sessionId":"S1","timestamp":"2026-08-05T10:02:07Z","promptSource":"sdk","message":{"role":"user","content":"headless injected prompt"}}
{"type":"ai-title","sessionId":"S1","timestamp":"2026-08-05T10:02:08Z","aiTitle":"some title"}
{"type":"queue-operation","sessionId":"S1","timestamp":"2026-08-05T10:02:09Z"}
{"type":"attachment","sessionId":"S1","timestamp":"2026-08-05T10:02:10Z","attachment":{"x":1}}
`

func TestParseTranscript(t *testing.T) {
	p, sid, consumed, err := ParseTranscript(strings.NewReader(sampleJSONL))
	if err != nil {
		t.Fatal(err)
	}
	if sid != "S1" {
		t.Errorf("sessionID = %q, want S1", sid)
	}
	// Every line ends with \n, so all bytes are consumed.
	if int(consumed) != len(sampleJSONL) {
		t.Errorf("consumed = %d, want %d (all complete lines)", consumed, len(sampleJSONL))
	}

	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok == nil {
		t.Fatal("no token row for the day/model")
	}
	if tok.Input != 300 || tok.Output != 60 || tok.CacheRead != 12 || tok.CacheCreation != 3 {
		t.Errorf("tokens = %+v, want in=300 out=60 cr=12 cc=3", tok)
	}
	// Two real prompts (string + text). Excluded: tool_result, sidechain, meta, sdk.
	if tok.Prompts != 2 {
		t.Errorf("prompts = %d, want 2", tok.Prompts)
	}

	if got := p.Attrs[AttrKey{Day: "2026-08-05", Kind: "skill", Name: "brainstorming"}]; got != 1 {
		t.Errorf("skill attr = %d, want 1", got)
	}
	if got := p.Attrs[AttrKey{Day: "2026-08-05", Kind: "mcp_server", Name: "playwright"}]; got != 1 {
		t.Errorf("mcp_server attr = %d, want 1", got)
	}
	if got := p.Attrs[AttrKey{Day: "2026-08-05", Kind: "mcp_tool", Name: "browser_navigate"}]; got != 1 {
		t.Errorf("mcp_tool attr = %d, want 1", got)
	}
}

// A trailing partial line (no newline) must NOT be consumed, so the next
// incremental run re-reads it from the returned offset once it completes.
func TestParseTranscriptPartialTrailingLine(t *testing.T) {
	full := `{"sessionId":"S1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"a"}}` + "\n"
	partial := `{"sessionId":"S1","timestamp":"2026-08-05T10:00:05Z","message":{"role":"assis`
	_, _, consumed, err := ParseTranscript(strings.NewReader(full + partial))
	if err != nil {
		t.Fatal(err)
	}
	if int(consumed) != len(full) {
		t.Errorf("consumed = %d, want %d (only the complete first line)", consumed, len(full))
	}
}

// Prompts seen before any assistant model is known are held and attributed to the
// first model that answers, so tokens/prompt has a model to hang off.
func TestPromptsAttributedToAnsweringModel(t *testing.T) {
	j := `{"sessionId":"S1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"first"}}` + "\n" +
		`{"sessionId":"S1","timestamp":"2026-08-05T10:00:05Z","message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	p, _, _, err := ParseTranscript(strings.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-5"}]
	if tok == nil || tok.Prompts != 1 {
		t.Fatalf("prompt not attributed to answering model: %+v", tok)
	}
}

// Subagent (sidechain) assistant usage still counts as tokens (real, billed), but
// a subagent user turn is NOT a human prompt.
func TestSidechainTokensCountPromptsDont(t *testing.T) {
	j := `{"sessionId":"P1","isSidechain":true,"timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"subagent task"}}` + "\n" +
		`{"sessionId":"P1","isSidechain":true,"timestamp":"2026-08-05T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":50,"output_tokens":9}}}` + "\n"
	p, sid, _, err := ParseTranscript(strings.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	if sid != "P1" {
		t.Errorf("sessionID = %q, want P1 (subagent files carry the parent id)", sid)
	}
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok == nil || tok.Input != 50 || tok.Output != 9 {
		t.Fatalf("subagent tokens not counted: %+v", tok)
	}
	if tok.Prompts != 0 {
		t.Errorf("subagent user turn counted as a prompt: %d", tok.Prompts)
	}
}

// Empty / blank lines are skipped without error.
func TestParseTranscriptSkipsBlankLines(t *testing.T) {
	p, _, _, err := ParseTranscript(strings.NewReader("\n\n   \n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 0 || len(p.Attrs) != 0 {
		t.Errorf("blank input produced rows: %+v %+v", p.Tokens, p.Attrs)
	}
}

// Claude Code writes one record per content block (thinking/text/tool_use), each
// repeating the message's full usage. The message must be counted once.
func msgRec(id, ts, usage string) string {
	idField := ""
	if id != "" {
		idField = `"id":"` + id + `",`
	}
	return `{"sessionId":"S1","timestamp":"` + ts + `","message":{` + idField + `"role":"assistant","model":"claude-opus-4-8","usage":` + usage + `}}` + "\n"
}

const splitUsage = `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_1h_input_tokens":40,"ephemeral_5m_input_tokens":0}}`

func TestUsageCountedOncePerMessageID(t *testing.T) {
	j := msgRec("msg_A", "2026-08-05T10:00:01Z", splitUsage) +
		msgRec("msg_A", "2026-08-05T10:00:02Z", splitUsage) +
		msgRec("msg_A", "2026-08-05T10:00:03Z", splitUsage)
	p, _, _, err := ParseTranscript(strings.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok == nil {
		t.Fatal("no row")
	}
	if tok.Input != 10 || tok.Output != 5 || tok.CacheRead != 100 || tok.CacheCreation != 40 || tok.CacheWrite1h != 40 {
		t.Fatalf("tokens = %+v, want in=10 out=5 cr=100 cc=40 1h=40", tok)
	}
	want := MessageCostUSD("claude-opus-4-8", MsgUsage{Input: 10, Output: 5, CacheRead: 100, CacheWrite5m: 0, CacheWrite1h: 40})
	if want == 0 || tok.Cost != want {
		t.Fatalf("cost = %v, want %v", tok.Cost, want)
	}
}

// Transcripts are untrusted: negative counts must contribute nothing, and must
// not drag down the totals or cost of records around them.
func TestNegativeTokenCountsClampedToZero(t *testing.T) {
	neg := `{"input_tokens":-1000,"output_tokens":-500,"cache_read_input_tokens":-2000}`
	norm := `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100}`
	day, model := "2026-08-05", "claude-opus-4-8"

	p, _, _, err := ParseTranscript(strings.NewReader(msgRec("n1", day+"T10:00:01Z", neg)))
	if err != nil {
		t.Fatal(err)
	}
	tok := p.Tokens[TokKey{Day: day, Model: model}]
	if tok == nil {
		t.Fatal("no row for negative record")
	}
	if tok.Input != 0 || tok.Output != 0 || tok.CacheRead != 0 || tok.Cost != MessageCostUSD(model, MsgUsage{}) {
		t.Fatalf("negative record = %+v, want zero tokens and zero-usage cost", tok)
	}

	p, _, _, err = ParseTranscript(strings.NewReader(
		msgRec("n1", day+"T10:00:01Z", neg) + msgRec("n2", day+"T10:00:02Z", norm)))
	if err != nil {
		t.Fatal(err)
	}
	tok = p.Tokens[TokKey{Day: day, Model: model}]
	want := MessageCostUSD(model, MsgUsage{Input: 10, Output: 5, CacheRead: 100})
	if tok.Input != 10 || tok.Output != 5 || tok.CacheRead != 100 || want == 0 || tok.Cost != want {
		t.Fatalf("after normal record = %+v, want in=10 out=5 cr=100 cost=%v", tok, want)
	}
}

// A hostile record claiming MaxInt64 is capped per count, so totals cannot wrap
// and a following normal record still adds correctly.
func TestHugeTokenCountsCappedNoOverflow(t *testing.T) {
	day, model := "2026-08-05", "claude-opus-4-8"
	huge := fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_1h_input_tokens":%d}}`,
		int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MaxInt64))
	norm := `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100}`
	p, _, _, err := ParseTranscript(strings.NewReader(
		msgRec("h1", day+"T10:00:01Z", huge) + msgRec("h2", day+"T10:00:02Z", huge) + msgRec("h3", day+"T10:00:03Z", norm)))
	if err != nil {
		t.Fatal(err)
	}
	tok := p.Tokens[TokKey{Day: day, Model: model}]
	if tok == nil {
		t.Fatal("no row")
	}
	const m = int64(maxTokensPerRecord)
	if tok.Input != 2*m+10 || tok.Output != 2*m+5 || tok.CacheRead != 2*m+100 || tok.CacheCreation != 2*m || tok.CacheWrite1h != 2*m {
		t.Fatalf("tokens = %+v, want each huge count capped at %d", tok, m)
	}
	one, _, _, _ := ParseTranscript(strings.NewReader(msgRec("h1", day+"T10:00:01Z", huge)))
	if got := one.Tokens[TokKey{Day: day, Model: model}].Input; got != m {
		t.Fatalf("single huge record input = %d, want exactly %d", got, m)
	}
	if tok.Cost <= 0 {
		t.Fatalf("cost = %v, want positive (no wrap)", tok.Cost)
	}
}

func TestCacheSplitLegacyAndMixed(t *testing.T) {
	// Legacy: total only -> whole total is a 5m write.
	legacy := `{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":40}`
	p, _, _, _ := ParseTranscript(strings.NewReader(msgRec("m1", "2026-08-05T10:00:01Z", legacy)))
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.CacheCreation != 40 || tok.CacheWrite1h != 0 {
		t.Fatalf("legacy tokens = %+v", tok)
	}
	if want := MessageCostUSD("claude-opus-4-8", MsgUsage{Input: 10, Output: 5, CacheWrite5m: 40}); tok.Cost != want || want == 0 {
		t.Fatalf("legacy cost = %v want %v", tok.Cost, want)
	}
	// Split present, 5m part is total - 1h.
	mixed := `{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_1h_input_tokens":20,"ephemeral_5m_input_tokens":30}}`
	p, _, _, _ = ParseTranscript(strings.NewReader(msgRec("m2", "2026-08-05T10:00:01Z", mixed)))
	tok = p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.CacheCreation != 50 || tok.CacheWrite1h != 20 {
		t.Fatalf("mixed tokens = %+v", tok)
	}
	if want := MessageCostUSD("claude-opus-4-8", MsgUsage{Input: 1, Output: 1, CacheWrite5m: 30, CacheWrite1h: 20}); tok.Cost != want {
		t.Fatalf("mixed cost = %v want %v", tok.Cost, want)
	}
	// 1h larger than the total is clamped; never count more than the total.
	over := `{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":99}}`
	p, _, _, _ = ParseTranscript(strings.NewReader(msgRec("m3", "2026-08-05T10:00:01Z", over)))
	tok = p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.CacheCreation != 10 || tok.CacheWrite1h != 10 {
		t.Fatalf("clamped tokens = %+v", tok)
	}
}

func TestRecordsWithoutIDStillCount(t *testing.T) {
	u := `{"input_tokens":10,"output_tokens":5}`
	j := msgRec("", "2026-08-05T10:00:01Z", u) + msgRec("", "2026-08-05T10:00:02Z", u)
	p, _, _, _ := ParseTranscript(strings.NewReader(j))
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.Input != 20 || tok.Output != 10 {
		t.Fatalf("id-less records not both counted: %+v", tok)
	}
}

func TestAttributionStillPerRecord(t *testing.T) {
	rec := `{"sessionId":"S1","timestamp":"2026-08-05T10:00:01Z","attributionSkill":"brainstorming","message":{"id":"msg_A","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	p, _, _, _ := ParseTranscript(strings.NewReader(rec + rec + rec))
	if n := p.Attrs[AttrKey{Day: "2026-08-05", Kind: "skill", Name: "brainstorming"}]; n != 3 {
		t.Fatalf("attribution = %d, want 3 (per record)", n)
	}
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.Input != 1 {
		t.Fatalf("input = %d want 1", tok.Input)
	}
}

// A duplicate record must still flush a held prompt to its model.
func TestDuplicateRecordStillFlushesPrompt(t *testing.T) {
	u := `{"input_tokens":1,"output_tokens":1}`
	j := msgRec("msg_A", "2026-08-05T10:00:01Z", u) +
		`{"sessionId":"S1","timestamp":"2026-08-05T10:00:02Z","message":{"role":"user","content":"next"}}` + "\n" +
		msgRec("msg_A", "2026-08-05T10:00:03Z", u)
	p, _, _, _ := ParseTranscript(strings.NewReader(j))
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.Prompts != 1 || tok.Input != 1 {
		t.Fatalf("tok = %+v want prompts=1 input=1", tok)
	}
}

func TestSeenRingCappedAt64(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 70; i++ {
		b.WriteString(msgRec("msg_"+string(rune('a'+i%26))+string(rune('A'+i/26)), "2026-08-05T10:00:01Z", `{"input_tokens":1}`))
	}
	_, _, _, _, seen, err := parseTranscript(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 64 {
		t.Fatalf("len(seen) = %d want 64", len(seen))
	}
	// oldest first: the last id counted is last.
	if seen[63] != "msg_"+string(rune('a'+69%26))+string(rune('A'+69/26)) {
		t.Fatalf("seen not oldest-first: last=%q", seen[63])
	}
}

// Transcripts can be written by a contained agent: an id that is too long or has
// a space is treated like an empty id (counted per record, never remembered).
func TestUnsafeMessageIDsCountPerRecordAndAreNotRemembered(t *testing.T) {
	u := `{"input_tokens":1,"output_tokens":1}`
	long := strings.Repeat("a", 200)
	j := msgRec(long, "2026-08-05T10:00:01Z", u) + msgRec(long, "2026-08-05T10:00:02Z", u) +
		msgRec("msg A", "2026-08-05T10:00:03Z", u) + msgRec("msg A", "2026-08-05T10:00:04Z", u) +
		msgRec("msg_ok", "2026-08-05T10:00:05Z", u) + msgRec("msg_ok", "2026-08-05T10:00:06Z", u)
	p, _, _, _, seen, err := parseTranscript(strings.NewReader(j), nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-4-8"}]
	if tok.Input != 5 { // 2 long + 2 spaced + 1 deduped
		t.Fatalf("input = %d want 5", tok.Input)
	}
	if len(seen) != 1 || seen[0] != "msg_ok" {
		t.Fatalf("seen = %v want [msg_ok]", seen)
	}
	// Defensive: unsafe entries loaded from state are dropped.
	_, _, _, _, seen, _ = parseTranscript(strings.NewReader(""), []string{long, "a b", "msg_ok"})
	if len(seen) != 1 || seen[0] != "msg_ok" {
		t.Fatalf("loaded seen = %v want [msg_ok]", seen)
	}
}
