package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func txRec(role string, blocks ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": role, "message": map[string]any{"role": role, "content": blocks}})
	return string(b)
}
func txBash(cmd string) map[string]any {
	return map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": cmd}}
}
func txText(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func txThinking() map[string]any     { return map[string]any{"type": "thinking", "thinking": ""} }
func txUser(s string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": s}})
	return string(b)
}
func txBytes(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

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

func TestParsePeekTailAskFullForASingleCall(t *testing.T) {
	p, ok := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txThinking()), txRec("assistant", txBash("touch /tmp/a"))))
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
		{map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "internal/web/app.js", "old_string": "a", "new_string": "b"}}, "Edit(internal/web/app.js)"},
		{map[string]any{"type": "tool_use", "name": "Write", "input": map[string]any{"file_path": "internal/web/app.js", "content": "x"}}, "Write(internal/web/app.js)"},
		{map[string]any{"type": "tool_use", "name": "NotebookEdit", "input": map[string]any{"notebook_path": "a.ipynb"}}, "NotebookEdit(a.ipynb)"},
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
// asks about the FIRST while the transcript ends on the SECOND.
func TestParsePeekTailAskFullEmptyForParallelCalls(t *testing.T) {
	p, _ := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txThinking()),
		txRec("assistant", txBash("touch /tmp/e1")), txRec("assistant", txBash("touch /tmp/e2"))))
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
