package session

import (
	"strings"
	"testing"
)

const attrUse = `{"message":{"role":"assistant","usage":{"output_tokens":3},"content":[{"type":"tool_use","id":"a","name":"Read"}]}}` + "\n"
const attrRes = `{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"hello"}]}}` + "\n"

// A line over the cap is skipped unparsed; the lines around it still count.
func TestAttributionOverlongLine(t *testing.T) {
	long := `{"message":{"role":"assistant","usage":{"output_tokens":1000},"x":"` + strings.Repeat("a", 17<<20) + `"}}` + "\n"
	a := attributeTools(strings.NewReader(attrUse + long + attrRes))
	if a.OutputTokens != 3 {
		t.Fatalf("output tokens %d want 3 (long line parsed?)", a.OutputTokens)
	}
	if len(a.Tools) != 1 || a.Tools[0].Name != "Read" || a.Tools[0].Calls != 1 || a.Tools[0].CtxBytes == 0 {
		t.Fatalf("lines around the long one lost: %+v", a.Tools)
	}
	// An unterminated over-long tail is skipped too.
	a = attributeTools(strings.NewReader(attrUse + strings.Repeat("a", 17<<20)))
	if a.OutputTokens != 3 || len(a.Tools) != 1 {
		t.Fatalf("over-long tail: %+v", a)
	}
}

// Past the whole-file cap only what was read is attributed.
func TestAttributionCapped(t *testing.T) {
	in := attrUse + attrRes + attrUse + attrRes
	a := attributeCapped(strings.NewReader(in), int64(len(attrUse+attrRes)))
	if a.OutputTokens != 3 || len(a.Tools) != 1 || a.Tools[0].Calls != 1 {
		t.Fatalf("cap not applied: %+v", a)
	}
	a = attributeCapped(strings.NewReader(in), int64(len(in)))
	if a.OutputTokens != 6 || a.Tools[0].Calls != 2 {
		t.Fatalf("whole file under the cap lost data: %+v", a)
	}
}
