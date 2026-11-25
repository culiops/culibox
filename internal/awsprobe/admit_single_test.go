package awsprobe

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAdmitSSOIsTheOnlyDecision: save, spawn, doctor and mint turn a gate
// result and an acknowledgement into pass or refuse through AdmitSSO only, so
// a rule added there reaches every surface. No caller outside this package
// reaches past it to Covers or AckEligible.
func TestAdmitSSOIsTheOnlyDecision(t *testing.T) {
	bypass := regexp.MustCompile(`\.Covers\(|awsprobe\.AckEligible\(`)
	for _, dir := range []string{"../web", "../session", "../broker", "../doctor"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				if bypass.MatchString(line) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
					t.Errorf("%s:%d decides outside AdmitSSO: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
