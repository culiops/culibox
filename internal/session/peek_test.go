package session

import "testing"

func TestParsePeekTailFindsLastAssistantText(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}
{"message":{"role":"user","content":"go on"}}
{"message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if p.Text != "second answer" {
		t.Errorf("Text=%q want %q", p.Text, "second answer")
	}
	if p.Pending {
		t.Error("Pending=true, want false: the last turn ended on text")
	}
}

func TestParsePeekTailPendingWhenLastBlockIsToolUse(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"text","text":"let me check"},{"type":"tool_use","name":"Bash"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if !p.Pending {
		t.Error("Pending=false, want true")
	}
	if p.Text != "let me check" {
		t.Errorf("Text=%q want %q", p.Text, "let me check")
	}
}

func TestParsePeekTailNotPendingOnceToolResultArrives(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","name":"Bash"}]}}
{"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
`
	p, _ := parsePeekTail([]byte(tail))
	if p.Pending {
		t.Error("Pending=true, want false: a tool_result followed the tool_use")
	}
}

func TestParsePeekTailSkipsCorruptLeadingLine(t *testing.T) {
	// A 256KiB tail almost always starts mid-record. That line must be skipped.
	tail := `role":"assistant","content":[{"type":"text","text":"truncated"}]}}
{"message":{"role":"assistant","content":[{"type":"text","text":"good"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok || p.Text != "good" {
		t.Errorf("Text=%q ok=%v want %q true", p.Text, ok, "good")
	}
}

func TestParsePeekTailEmpty(t *testing.T) {
	if _, ok := parsePeekTail(nil); ok {
		t.Error("ok=true on empty input, want false")
	}
	if _, ok := parsePeekTail([]byte("\n  \n")); ok {
		t.Error("ok=true on blank lines, want false")
	}
}

func TestParsePeekTailToolOnlyTurnHasNoText(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok {
		t.Fatal("ok=false, want true: a record WAS parsed, there is just no text")
	}
	if p.Text != "" {
		t.Errorf("Text=%q want empty", p.Text)
	}
	if !p.Pending {
		t.Error("Pending=false, want true")
	}
}

func TestSummaryLine(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	cases := []struct{ in, want string }{
		{"", ""},
		{"   \n\t\n", ""},
		{"hello", "hello"},
		{"\n\n  first real line  \nsecond", "first real line"},
		{long, long[:summaryMax-1] + "…"},
		// Rune-safe: cutting a multi-byte character in half renders as a
		// replacement glyph, so truncation counts runes, not bytes.
		{"héllo", "héllo"},
	}
	for _, c := range cases {
		if got := summaryLine(c.in); got != c.want {
			t.Errorf("summaryLine(%.20q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestSummaryLineTruncatesByRune(t *testing.T) {
	in := ""
	for i := 0; i < 200; i++ {
		in += "é" // 2 bytes each
	}
	got := summaryLine(in)
	if r := []rune(got); len(r) != summaryMax {
		t.Errorf("len=%d runes want %d (%q)", len(r), summaryMax, got)
	}
}
