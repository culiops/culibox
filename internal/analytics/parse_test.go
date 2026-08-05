package analytics

import (
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
