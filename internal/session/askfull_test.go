package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// txMsgSeq/txToolSeq back the auto-id defaults below (txRec, txBash): a
// fresh id per call, since the common case in these tests is that
// consecutive records are separate turns/messages.
var txMsgSeq, txToolSeq int

// txRec builds one JSONL record with blocks as message.content, under a
// FRESH message.id. A test that needs several records to represent ONE
// split API message — parallel tool calls, or one streamed turn split across
// thinking/text/tool_use, which is exactly the shape AskFull's
// message.id-bound "pending run" exists for (fix round 1, 2026-09-29) — uses
// txRecID directly with one id shared across the calls.
func txRec(role string, blocks ...map[string]any) string {
	txMsgSeq++
	return txRecID(role, fmt.Sprintf("msg_%d", txMsgSeq), blocks...)
}

// txRecID is txRec with an explicit message.id, for binding several records
// to one pending run.
func txRecID(role, id string, blocks ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": role, "message": map[string]any{"role": role, "id": id, "content": blocks}})
	return string(b)
}

// txBash gives each call its own tool_use id (Claude Code writes
// "id":"toolu_…" on every tool_use block), so two Bash calls in one test are
// never accidentally deduped as "the same call".
func txBash(cmd string) map[string]any {
	txToolSeq++
	return map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d", txToolSeq), "name": "Bash", "input": map[string]any{"command": cmd}}
}
func txText(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func txThinking() map[string]any     { return map[string]any{"type": "thinking", "thinking": ""} }
func txUser(s string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": s}})
	return string(b)
}
func txBytes(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

// txToolUse builds a tool_use block with an EXPLICIT id (unlike txBash's
// auto-generated one) — fix round 2's streaming/dedup/reappearance tests need
// to reference a specific call's id from a separate txResult line, or make
// two records deliberately share one.
func txToolUse(name, key, val, id string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{key: val}}
}

// txResult is a user record resolving one tool_use id — the shape Claude
// Code's streaming execution writes between records of the SAME message.id
// once an earlier call in the run finishes (fix round 2, measured 1,546
// times in 400 real transcripts).
func txResult(toolUseID string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{
		{"type": "tool_result", "tool_use_id": toolUseID, "content": "ok"},
	}}})
	return string(b)
}

func writeTxLines(t *testing.T, home, cwd, sid string, mtime time.Time, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, txBytes(lines...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestAskRenderIsUntruncatedAndClean(t *testing.T) {
	long := strings.Repeat("x", 300)
	full, has := askRender("Bash", json.RawMessage(`{"command":"echo `+long+`"}`))
	if !has || full != "Bash(echo "+long+")" {
		t.Fatalf("askRender = %q,%v", full, has)
	}
	if r := []rune(askLine("Bash", json.RawMessage(`{"command":"echo `+long+`"}`))); len(r) != askMax {
		t.Fatalf("askLine is no longer elided: %d runes", len(r))
	}
	in, _ := json.Marshal(map[string]string{"command": "rm -rf build\n&& echo 'x'\x1b[31m‮\u009b"})
	full, _ = askRender("Bash", in)
	if full != "Bash(rm -rf build && echo 'x' [31m)" {
		t.Fatalf("control/format characters survived: %q", full)
	}
}

func TestAskRenderNeedsAPrimaryArgument(t *testing.T) {
	for _, c := range []struct{ name, input string }{
		{"WebFetch", `{"url":"https://example.com"}`},
		{"AskUserQuestion", `{"questions":[]}`},
		{"mcp__x__y", `{}`},
		{"", `{"command":"ls"}`},
	} {
		if _, has := askRender(c.name, json.RawMessage(c.input)); has {
			t.Errorf("%s: reported a primary argument", c.name)
		}
	}
}

// Controller ruling (2026-09-29, amends spec §1.2): AskFull is set ONLY for
// Bash (command) and Read (file_path) — tools whose primary argument IS the
// whole action. Edit/Write/NotebookEdit's real content (a diff or a body of
// text) is not visible in the one-line rendering, so they must never be
// answerable Yes from the sidebar even though the short Ask line still shows
// their path.
func TestAskRenderExcludesEditWriteNotebookEdit(t *testing.T) {
	for _, c := range []struct{ name, input string }{
		{"Edit", `{"file_path":"internal/web/app.js","old_string":"a","new_string":"b"}`},
		{"Write", `{"file_path":"internal/web/app.js","content":"x"}`},
		{"NotebookEdit", `{"notebook_path":"a.ipynb","new_source":"x"}`},
	} {
		full, has := askRender(c.name, json.RawMessage(c.input))
		if has || full != "" {
			t.Errorf("%s: askRender = %q,%v, want empty/false (not in the Bash/Read allowlist)", c.name, full, has)
		}
		if line := askLine(c.name, json.RawMessage(c.input)); line == c.name || line == "" {
			t.Errorf("%s: askLine=%q, want the short Ask line to still render its path", c.name, line)
		}
	}
}

// A positive Read case, plus wrong-key rows: an allowed tool called with a
// key that isn't its own primary argument (not a shape Claude Code
// produces) must not be rendered either.
func TestAskRenderReadAndWrongKeys(t *testing.T) {
	if full, has := askRender("Read", json.RawMessage(`{"file_path":"internal/session/peek.go"}`)); !has || full != "Read(internal/session/peek.go)" {
		t.Fatalf("Read askRender = %q,%v", full, has)
	}
	for _, c := range []struct{ name, input string }{
		{"Read", `{"command":"ls"}`},
		{"Bash", `{"file_path":"a.go"}`},
	} {
		if full, has := askRender(c.name, json.RawMessage(c.input)); has {
			t.Errorf("%s: a key that isn't its primary argument still reported one: %q", c.name, full)
		}
	}
}

func TestParsePeekTailAskFullForASingleCall(t *testing.T) {
	id := "msg_single"
	p, ok := parsePeekTail(txBytes(txUser("go"), txRecID("assistant", id, txThinking()), txRecID("assistant", id, txBash("touch /tmp/a"))))
	if !ok || !p.Pending || p.AskFull != "Bash(touch /tmp/a)" {
		t.Fatalf("peek = %+v", p)
	}
}

// Controller ruling: parsePeekTail's AskFull must be empty for a pending
// Edit/Write/NotebookEdit even though there is exactly one pending call and
// Ask still renders a line for it.
func TestParsePeekTailAskFullEmptyForEditWriteNotebookEdit(t *testing.T) {
	for _, c := range []struct {
		block   map[string]any
		wantAsk string
	}{
		{map[string]any{"type": "tool_use", "id": "toolu_e1", "name": "Edit", "input": map[string]any{"file_path": "internal/web/app.js", "old_string": "a", "new_string": "b"}}, "Edit(internal/web/app.js)"},
		{map[string]any{"type": "tool_use", "id": "toolu_e2", "name": "Write", "input": map[string]any{"file_path": "internal/web/app.js", "content": "x"}}, "Write(internal/web/app.js)"},
		{map[string]any{"type": "tool_use", "id": "toolu_e3", "name": "NotebookEdit", "input": map[string]any{"notebook_path": "a.ipynb"}}, "NotebookEdit(a.ipynb)"},
	} {
		p, ok := parsePeekTail(txBytes(txUser("go"), txRec("assistant", c.block)))
		name, _ := c.block["name"].(string)
		if !ok || !p.Pending {
			t.Fatalf("%s: ok=%v pending=%v", name, ok, p.Pending)
		}
		if p.Ask != c.wantAsk {
			t.Errorf("%s: Ask=%q want %q", name, p.Ask, c.wantAsk)
		}
		if p.AskFull != "" {
			t.Errorf("%s: AskFull=%q want empty (not in the Bash/Read allowlist)", name, p.AskFull)
		}
	}
}

// Measured 2026-09-29 (probe E): two parallel calls are two records; the dialog
// asks about the FIRST while the transcript ends on the SECOND. Both records
// come from ONE split API message, so they share a message.id.
func TestParsePeekTailAskFullEmptyForParallelCalls(t *testing.T) {
	id := "msg_parallel"
	p, _ := parsePeekTail(txBytes(txUser("go"), txRecID("assistant", id, txThinking()),
		txRecID("assistant", id, txBash("touch /tmp/e1")), txRecID("assistant", id, txBash("touch /tmp/e2"))))
	if !p.Pending || p.Ask != "Bash(touch /tmp/e2)" || p.AskFull != "" {
		t.Fatalf("peek = %+v: AskFull must be empty with two pending calls", p)
	}
}

func TestParsePeekTailAskFullEmptyForMultiBlockRecord(t *testing.T) {
	p, _ := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txText("ok"), txBash("touch a"), txBash("touch b"))))
	if !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v", p)
	}
}

// A single trailing tool_use, preceded by a text block in the SAME record,
// still counts as exactly one pending call.
func TestParsePeekTailAskFullSetForOneCallAfterText(t *testing.T) {
	p, ok := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txText("checking"), txBash("touch a"))))
	if !ok || !p.Pending || p.AskFull != "Bash(touch a)" {
		t.Fatalf("peek = %+v", p)
	}
}

// Fix round 1: the pending run is bound to message.id, and a text/thinking
// record between two tool_use calls does NOT end the run — it is still part
// of the same split API message. Two distinct tool_use ids under one
// message.id must still read as "more than one pending call", even though
// the immediately-preceding record (walking backward) is plain text.
func TestParsePeekTailAskFullEmptyAcrossTextSplitSameMessage(t *testing.T) {
	id := "msg_split"
	p, _ := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", id, txText("first")),
		txRecID("assistant", id, txBash("touch /tmp/a")),
		txRecID("assistant", id, txText("second")),
		txRecID("assistant", id, txBash("touch /tmp/b"))))
	if !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: two tool_use ids under one message.id must not offer AskFull", p)
	}
}

// The mirror image: one message.id, exactly ONE tool_use, with thinking and
// text records on either side of it in the same run — AskFull must be set.
func TestParsePeekTailAskFullSetAcrossTextSplitSameMessage(t *testing.T) {
	id := "msg_split_single"
	p, _ := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", id, txThinking()),
		txRecID("assistant", id, txText("about to run it")),
		txRecID("assistant", id, txBash("touch /tmp/a"))))
	if !p.Pending || p.AskFull != "Bash(touch /tmp/a)" {
		t.Fatalf("peek = %+v", p)
	}
}

// A missing message.id on the pending record fails AskFull closed
// regardless of how many calls are actually pending — with no id to bind
// the run to, "exactly one" cannot be trusted.
func TestParsePeekTailAskFullEmptyWithoutAMessageID(t *testing.T) {
	rec := `{"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Bash","input":{"command":"touch a"}}]}}`
	p, ok := parsePeekTail(txBytes(txUser("go"), rec))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: a record with no message.id must fail AskFull closed", p)
	}
}

// Fix round 2 (2026-09-29): measured on real transcripts — Claude Code's
// streaming tool execution writes a tool_result USER record BETWEEN two
// tool_use records of the SAME message.id (1,546 times in the last 400
// transcripts). Fix round 1's "a non-assistant role ends the run" rule ended
// the run at u{result A}, saw only C, and would have offered AskFull for C —
// but B (an earlier call in the SAME run) is still unresolved, so the dialog
// may actually be asking about B. Walking back through user records
// (collecting their tool_result ids, never ending the run on them) and
// counting every unresolved tool_use id under runID fixes this: A resolved,
// B and C both unresolved → 2 unresolved → AskFull must stay empty.
func TestParsePeekTailAskFullEmptyStreamingToolResultBetweenSameMessage(t *testing.T) {
	id := "msg_stream1"
	p, ok := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", id, txToolUse("Read", "file_path", "A", "toolu_A1")),
		txRecID("assistant", id, txToolUse("Bash", "command", "B", "toolu_B1")),
		txResult("toolu_A1"),
		txRecID("assistant", id, txToolUse("Bash", "command", "C", "toolu_C1"))))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: B is still unresolved under the same message.id, AskFull must stay empty", p)
	}
}

// The positive mirror: only ONE call (A) precedes the tail call (C) in the
// run, A resolves via a streamed tool_result, leaving exactly C unresolved.
func TestParsePeekTailAskFullSetAfterStreamingResolvesEarlierCall(t *testing.T) {
	id := "msg_stream2"
	p, ok := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", id, txToolUse("Bash", "command", "A", "toolu_A2")),
		txResult("toolu_A2"),
		txRecID("assistant", id, txToolUse("Bash", "command", "C", "toolu_C2"))))
	if !ok || !p.Pending || p.AskFull != "Bash(C)" {
		t.Fatalf("peek = %+v: A resolved, C is the only unresolved call", p)
	}
}

// A line that fails to parse before the run finds its boundary means the
// 256 KiB tail itself may have started mid-message — a truncated record
// could have held another tool_use, another tool_result, or the true
// boundary — so this fails AskFull closed even though the visible ids alone
// would otherwise look like exactly one unresolved call.
func TestParsePeekTailAskFullEmptyMidTailTruncationWithToolResult(t *testing.T) {
	p, ok := parsePeekTail(txBytes(`{"partial record`,
		txResult("toolu_A3"),
		txRecID("assistant", "msg_trunc", txToolUse("Bash", "command", "C", "toolu_C3"))))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: a tail that may have started mid-message must fail AskFull closed", p)
	}
}

// An assistant record INSIDE the run (before any boundary) with an empty
// message.id can't be told apart from a genuine continuation of the same
// run, so it fails closed too — distinct from
// TestParsePeekTailAskFullEmptyWithoutAMessageID above, where it is the TAIL
// record itself (runID never even gets set) that has no id.
func TestParsePeekTailAskFullEmptyWhenAnEarlierRunRecordHasNoMessageID(t *testing.T) {
	p, ok := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", "", txToolUse("Bash", "command", "B", "toolu_B4")),
		txRecID("assistant", "msg_x4", txToolUse("Bash", "command", "C", "toolu_C4"))))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: an earlier run record with no message.id must fail AskFull closed", p)
	}
}

// A record with a DIFFERENT non-empty message.id ends the run (a boundary) —
// but if runID reappears further back, past that boundary, the message.id
// ordering isn't the simple single-split-turn shape the rule assumes, and
// the count sealed at the boundary (only C, since B sat on the far side of Y)
// cannot be trusted. Must fail closed rather than offer AskFull=Bash(C) with
// B silently unresolved.
func TestParsePeekTailAskFullEmptyWhenRunIDReappearsAcrossABoundary(t *testing.T) {
	p, ok := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", "msg_x5", txToolUse("Bash", "command", "B", "toolu_B5")),
		txRecID("assistant", "msg_y5", txText("unrelated")),
		txRecID("assistant", "msg_x5", txToolUse("Bash", "command", "C", "toolu_C5"))))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: runID reappearing across a different-id boundary must fail AskFull closed", p)
	}
}

// One record can interleave a tool_use with a text block ([tool_use B, text,
// tool_use C]) rather than keeping every tool_use trailing — every tool_use
// block in a run-matching record must count, not only the trailing run, or B
// would be missed and C alone would wrongly read as "exactly one".
func TestParsePeekTailAskFullEmptyForInterleavedBlocksInOneRecord(t *testing.T) {
	p, ok := parsePeekTail(txBytes(txUser("go"),
		txRec("assistant", txToolUse("Bash", "command", "B", "toolu_B6"), txText("narrating"), txToolUse("Bash", "command", "C", "toolu_C6"))))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: every tool_use in the record counts, not only the trailing one", p)
	}
}

// Two records can carry the SAME tool_use id (Claude Code rewriting a
// partial record with more content as a turn streams in) — dedup by id means
// this still counts as exactly one call, not two.
func TestParsePeekTailAskFullDuplicateRecordsSameToolUseIDCountedOnce(t *testing.T) {
	id := "msg_dup"
	dup := func() map[string]any { return txToolUse("Bash", "command", "C", "toolu_dup7") }
	p, ok := parsePeekTail(txBytes(txUser("go"),
		txRecID("assistant", id, dup()),
		txRecID("assistant", id, dup())))
	if !ok || !p.Pending || p.AskFull != "Bash(C)" {
		t.Fatalf("peek = %+v: duplicate records sharing one tool_use id must count once", p)
	}
}

func TestParsePeekTailAskFullEmptyWhenTheTailRunsOutMidRun(t *testing.T) {
	p, _ := parsePeekTail(txBytes(`{"partial record`, txRec("assistant", txBash("touch a"))))
	if !p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v: a run with no visible start may hide more calls", p)
	}
}

func TestParsePeekTailAskFullEmptyOverTheCap(t *testing.T) {
	p, _ := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txBash("echo "+strings.Repeat("y", askFullMax)))))
	if !p.Pending || p.Ask == "" || p.AskFull != "" {
		t.Fatalf("peek Ask=%q AskFull len=%d", p.Ask, len(p.AskFull))
	}
}

// Exact cap boundary: a rendering of exactly askFullMax bytes is accepted,
// one byte more is refused.
func TestParsePeekTailAskFullCapBoundary(t *testing.T) {
	overhead := len("Bash()")
	atCap := strings.Repeat("z", askFullMax-overhead)
	p, ok := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txBash(atCap))))
	if !ok || !p.Pending || len(p.AskFull) != askFullMax {
		t.Fatalf("at cap: ok=%v pending=%v AskFull len=%d, want exactly %d", ok, p.Pending, len(p.AskFull), askFullMax)
	}
	p, ok = parsePeekTail(txBytes(txUser("go"), txRec("assistant", txBash(atCap+"z"))))
	if !ok || !p.Pending || p.AskFull != "" {
		t.Fatalf("one byte over cap: ok=%v pending=%v AskFull=%q, want empty", ok, p.Pending, p.AskFull)
	}
}

func TestParsePeekTailAskFullEmptyOnceResolved(t *testing.T) {
	result := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	p, _ := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txBash("touch a")), result))
	if p.Pending || p.AskFull != "" {
		t.Fatalf("peek = %+v", p)
	}
}

func TestPeekJSONNeverCarriesAskFull(t *testing.T) {
	b, _ := json.Marshal(Peek{Text: "x", Pending: true, Ask: "Bash(a)", AskFull: "Bash(a)"})
	if strings.Contains(string(b), "AskFull") || strings.Contains(string(b), "ask_full") {
		t.Fatalf("/peek would ship the full command: %s", b)
	}
}

func TestListShipsAskFullOnlyWhenAnswerable(t *testing.T) {
	m, home := newLocatorManager(t)
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), txUser("go"), txRec("assistant", txBash("touch /tmp/a")))
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	askFullOf := func() Info {
		for _, i := range m.List() {
			if i.ID == "s1" {
				return i
			}
		}
		t.Fatal("s1 not listed")
		return Info{}
	}
	m.sessions["s1"].replyable = true
	if i := askFullOf(); i.Need != "ask" || i.AskFull != "Bash(touch /tmp/a)" {
		t.Fatalf("blocked+replyable: need=%q ask_full=%q", i.Need, i.AskFull)
	}
	m.sessions["s1"].replyable = false
	if i := askFullOf(); i.AskFull != "" {
		t.Fatalf("not replyable, yet ask_full=%q", i.AskFull)
	}
	m.sessions["s1"].replyable = true
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "idle")) // transcript pending, registry not blocked
	if i := askFullOf(); i.Need != "ask" || i.AskFull != "" {
		t.Fatalf("not registry-blocked: need=%q ask_full=%q", i.Need, i.AskFull)
	}
}
