package session

import (
	"strings"
	"testing"
)

func TestAttributeTools(t *testing.T) {
	big := strings.Repeat("x", 70000) // > bufio.Scanner's 64KiB cap — long-line safety
	// Note: no trailing newline on the last line — it must still be counted.
	lines := `{"message":{"role":"assistant","usage":{"output_tokens":10},"content":[{"type":"tool_use","id":"a","name":"Read"},{"type":"tool_use","id":"b","name":"mcp__playwright__browser_evaluate"},{"type":"tool_use","id":"c","name":"Skill"}]}}
{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"` + big + `"},{"type":"tool_result","tool_use_id":"b","content":"hi"},{"type":"tool_result","tool_use_id":"c","content":"yo"}]}}
{"message":{"role":"assistant","content":[{"type":"tool_use","id":"d","name":"mcp__playwright__browser_navigate"}]}}
{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"d","content":"nav"}]}}`
	a := attributeTools(strings.NewReader(lines))
	if a.OutputTokens != 10 {
		t.Fatalf("output tokens %+v", a)
	}
	// The final line (playwright navigate, no trailing newline) must be counted:
	// 2 playwright sub-tools grouped → 2 calls.
	if len(a.Tools) < 3 || a.Tools[0].Name != "Read" || a.Tools[0].Kind != "tool" || a.Tools[0].Calls != 1 {
		t.Fatalf("read row %+v", a.Tools)
	}
	if a.Tools[0].CtxBytes < 70000 {
		t.Fatalf("read bytes %d (long line lost?)", a.Tools[0].CtxBytes)
	}
	var pw *ToolStat
	for i := range a.Tools {
		if a.Tools[i].Name == "playwright" {
			pw = &a.Tools[i]
		}
	}
	if pw == nil || pw.Kind != "mcp" || pw.Calls != 2 {
		t.Fatalf("playwright group %+v", a.Tools)
	}
	var sk *ToolStat
	for i := range a.Tools {
		if a.Tools[i].Name == "Skill" {
			sk = &a.Tools[i]
		}
	}
	if sk == nil || sk.Kind != "skill" {
		t.Fatalf("skill %+v", a.Tools)
	}
}

func TestAttributeTools_unmatchedAndEmpty(t *testing.T) {
	if a := attributeTools(strings.NewReader("")); len(a.Tools) != 0 {
		t.Fatalf("empty → no tools, got %+v", a.Tools)
	}
	// a tool_result with no preceding tool_use → "?" bucket, not dropped.
	one := `{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"orphan"}]}}` + "\n"
	a := attributeTools(strings.NewReader(one))
	if len(a.Tools) != 1 || a.Tools[0].Name != "?" || a.Tools[0].CtxBytes == 0 {
		t.Fatalf("orphan result → ? bucket, got %+v", a.Tools)
	}
}
