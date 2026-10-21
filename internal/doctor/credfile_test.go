package doctor

import (
	"strings"
	"testing"
)

func TestCheckCredFileFailsOnErrors(t *testing.T) {
	r := checkCredFile(Input{CredErrors: []string{"token bad: env is required", "aws x: nope"}})
	if r.Status != Fail || r.Check != "credentials file" {
		t.Fatalf("got %+v, want a FAIL", r)
	}
	for _, want := range []string{"token bad: env is required", "aws x: nope"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail %q missing %q", r.Detail, want)
		}
	}
	if r := checkCredFile(Input{}); r.Status != Pass {
		t.Errorf("no errors = %+v, want PASS", r)
	}
}
