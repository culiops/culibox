package web

import (
	"os"
	"regexp"
	"testing"

	"github.com/culiops/agent-box/internal/awsprobe"
)

// The reason texts are written out in three places: awsprobe.ReasonText (the
// source of truth), the BASE_REASON table in the box's agentbox-env.js and the
// one in the UI's sessionrow.js. A drifted copy would show a different sentence
// on different surfaces, so each literal is checked against the Go function.
func TestBaseReasonLiteralsMatchReasonText(t *testing.T) {
	reasons := []awsprobe.Reason{
		awsprobe.ReasonSSOExpired, awsprobe.ReasonAssumeDenied, awsprobe.ReasonSimulateDenied,
		awsprobe.ReasonTimeout, awsprobe.ReasonError, awsprobe.ReasonNotChecked, awsprobe.ReasonProbeDisabled,
	}
	obj := regexp.MustCompile(`(?s)const BASE_REASON = \{(.*?)\};`)
	pair := regexp.MustCompile(`(\w+)\s*:\s*(?:'([^']*)'|"([^"]*)")`)
	for _, file := range []string{"../broker/agentbox-env.js", "static/sessionrow.js"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m := obj.FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: no BASE_REASON literal", file)
		}
		got := map[string]string{}
		for _, p := range pair.FindAllSubmatch(m[1], -1) {
			v := string(p[2])
			if v == "" {
				v = string(p[3])
			}
			got[string(p[1])] = v
		}
		if len(got) != len(reasons) {
			t.Errorf("%s: BASE_REASON has %d entries, want %d", file, len(got), len(reasons))
		}
		for _, r := range reasons {
			if got[string(r)] != awsprobe.ReasonText(r) {
				t.Errorf("%s: BASE_REASON[%q] = %q, want %q", file, r, got[string(r)], awsprobe.ReasonText(r))
			}
		}
	}
}
