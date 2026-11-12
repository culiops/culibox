package boximage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The one whole-file exemption in .gitleaks.toml is the vendored, minified xterm
// code. A path exemption hides everything in the file, so the exempt files are
// pinned by sha256: changing a byte of either (a secret slipped into a "vendored"
// file, or an honest upstream bump) fails here and has to be re-pinned in review.
// To re-pin after a deliberate upgrade: sha256sum the two files and edit the map.
var vendoredPins = map[string]string{
	"internal/web/static/xterm/xterm.js":       "14903579ff54664cd72f8e8699e6961a6272c21863ec1c3b118cdc8af5d4a972",
	"internal/web/static/xterm/addon-webgl.js": "b85f8d4b3e9756bebb757e3fe47134d70f03ea3d6b187624426d2e2b65dec06c",
}

// gitleaksExemptPaths returns the literal file paths of every `paths = [...]`
// entry in the config (each is an anchored regex of the form ^a/b\.js$).
func gitleaksExemptPaths(cfg string) []string {
	var out []string
	for _, block := range regexp.MustCompile(`(?s)\npaths\s*=\s*\[(.*?)\]`).FindAllStringSubmatch(cfg, -1) {
		for _, m := range regexp.MustCompile(`'''\^(.*?)\$'''`).FindAllStringSubmatch(block[1], -1) {
			out = append(out, strings.ReplaceAll(m[1], `\.`, "."))
		}
	}
	sort.Strings(out)
	return out
}

// vendoredPinViolations: the exempt paths and the pinned paths are the same set,
// and each file under root hashes to its pin.
func vendoredPinViolations(root string, exempt []string, pins map[string]string) []string {
	var bad []string
	for _, p := range exempt {
		if _, ok := pins[p]; !ok {
			bad = append(bad, fmt.Sprintf(".gitleaks.toml exempts %s from scanning but no sha256 pins it", p))
		}
	}
	for p, want := range pins {
		found := false
		for _, e := range exempt {
			found = found || e == p
		}
		if !found {
			bad = append(bad, fmt.Sprintf("%s is pinned but not exempted in .gitleaks.toml: drop the pin", p))
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			bad = append(bad, fmt.Sprintf("%s has sha256 %s, pinned %s: a scan-exempt file changed; review it and re-pin", p, got, want))
		}
	}
	sort.Strings(bad)
	return bad
}

func TestGitleaksExemptFilesArePinned(t *testing.T) {
	cfg, err := os.ReadFile("../../.gitleaks.toml")
	if err != nil {
		t.Fatal(err)
	}
	exempt := gitleaksExemptPaths(string(cfg))
	if len(exempt) == 0 {
		t.Fatal("found no path exemption in .gitleaks.toml: has its layout changed?")
	}
	for _, msg := range vendoredPinViolations("../..", exempt, vendoredPins) {
		t.Error(msg)
	}
}

func TestVendoredPinCheckCatchesViolations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if bad := vendoredPinViolations(dir, []string{"a.js"}, map[string]string{"a.js": abc}); len(bad) != 0 {
		t.Errorf("a matching pin reported: %v", bad)
	}
	for name, tc := range map[string]struct {
		exempt []string
		pins   map[string]string
	}{
		"changed file":  {[]string{"a.js"}, map[string]string{"a.js": strings.Repeat("0", 64)}},
		"unpinned path": {[]string{"a.js", "b.js"}, map[string]string{"a.js": abc}},
		"stale pin":     {[]string{"a.js"}, map[string]string{"a.js": abc, "b.js": abc}},
		"missing file":  {[]string{"c.js"}, map[string]string{"c.js": abc}},
	} {
		if bad := vendoredPinViolations(dir, tc.exempt, tc.pins); len(bad) == 0 {
			t.Errorf("%s: no violation reported", name)
		}
	}
	cfg := "x\n[[allowlists]]\nregexes = ['''^a$''']\n[[allowlists]]\npaths = [\n  '''^internal/x/a\\.js$''',\n  '''^b\\.js$''',\n]\n"
	if got := strings.Join(gitleaksExemptPaths(cfg), " "); got != "b.js internal/x/a.js" {
		t.Errorf("exempt paths = %q", got)
	}
}

// The CI scan lives in scripts/ci/secret-scan.sh (tested offline by its own
// _test.sh): it must keep using the repo's config and never print a matched value.
func TestSecretScanScriptKeepsConfigAndRedaction(t *testing.T) {
	b, err := os.ReadFile("../../scripts/ci/secret-scan.sh")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(string(b), "")
	for _, want := range []string{"gitleaks git", "gitleaks dir", "--config .gitleaks.toml", "--redact", "^[0-9a-f]{40}$"} {
		if !strings.Contains(code, want) {
			t.Errorf("secret-scan.sh lacks %q", want)
		}
	}
}
