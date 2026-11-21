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
	// The same sha256 as vendor/monaco/MANIFEST.sha256 (scripts/vendor-monaco.sh, pinned monaco-editor@0.57.0).
	"internal/web/static/vendor/monaco/vs/editor-BdtEMBbM.js": "e1e745003eb416b7ca6a1956a333e6f8434ff48381824858cb63c99e9c8f8acf",
}

var (
	glHeader   = regexp.MustCompile(`^\[\[?[A-Za-z0-9_."-]+\]\]?\s*(?:#.*)?$`)
	glPathsKey = regexp.MustCompile(`(?:^|[\s{,])["']?paths["']?\s*=`)
	glItem     = regexp.MustCompile(`'''(.*?)'''`)
	glLiteral  = regexp.MustCompile(`^\^([A-Za-z0-9_./\\-]+)\$$`)
	glPlain    = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	glComment  = regexp.MustCompile(`(?m)\s*#.*$`)
)

// gitleaksExemptPaths returns the literal file paths of every path exemption in the
// config, and a problem for anything it cannot read. A path exemption hides a whole
// file from the scan, so the reader fails closed: only a `paths = [...]` key at
// column 0 of a global [[allowlists]] table, whose items are triple-single-quoted
// anchored literals (^a/b\.js$), is accepted. Any other `paths` (indented, inline, in
// the singular [allowlist], in a rule's own allowlist, a scalar, a quoted or regex
// item) is a problem.
func gitleaksExemptPaths(cfg string) (paths, problems []string) {
	lines := strings.Split(cfg, "\n")
	header := ""
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if glHeader.MatchString(t) {
			header = strings.TrimSpace(glComment.ReplaceAllString(t, ""))
			continue
		}
		if !glPathsKey.MatchString(lines[i]) {
			continue
		}
		at := fmt.Sprintf(".gitleaks.toml:%d (%s)", i+1, header)
		if header != "[[allowlists]]" || !strings.HasPrefix(lines[i], "paths") {
			problems = append(problems, at+": a path exemption is only read at column 0 of a global [[allowlists]] table; this one is not")
			continue
		}
		// The value: from after "=" to the closing bracket, possibly over several lines.
		value := lines[i][strings.Index(lines[i], "=")+1:]
		for depth := strings.Count(value, "[") - strings.Count(value, "]"); depth > 0 && i+1 < len(lines); {
			i++
			value += "\n" + lines[i]
			depth += strings.Count(lines[i], "[") - strings.Count(lines[i], "]")
		}
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(value, "[") {
			problems = append(problems, at+": paths must be an array")
			continue
		}
		for _, m := range glItem.FindAllStringSubmatch(value, -1) {
			lit := glLiteral.FindStringSubmatch(m[1])
			if lit == nil || !glPlain.MatchString(strings.ReplaceAll(lit[1], `\.`, ".")) {
				problems = append(problems, fmt.Sprintf("%s: item %q is not an anchored literal path (^a/b\\.js$): no other regex syntax", at, m[1]))
				continue
			}
			paths = append(paths, strings.ReplaceAll(lit[1], `\.`, "."))
		}
		// Whatever is left once the items, brackets, commas and comments are gone is a
		// form that was not read (a double-quoted or single-quoted item, say).
		rest := glComment.ReplaceAllString(glItem.ReplaceAllString(value, ""), "")
		if strings.Trim(rest, "[], \t\r\n") != "" {
			problems = append(problems, at+": has an item that is not a triple-single-quoted string: "+strings.TrimSpace(rest))
		}
	}
	sort.Strings(paths)
	return paths, problems
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
	exempt, problems := gitleaksExemptPaths(string(cfg))
	for _, p := range problems {
		t.Error(p)
	}
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
	got, problems := gitleaksExemptPaths(cfg)
	if strings.Join(got, " ") != "b.js internal/x/a.js" || len(problems) != 0 {
		t.Errorf("exempt paths = %q, problems %v", got, problems)
	}
	// Fail closed: a path exemption the reader cannot take apart is a problem, so the
	// pin check cannot be dodged by writing the exemption in another form.
	const head = "[extend]\nuseDefault = true\n"
	for name, c := range map[string]string{
		"indented block":       head + "[[allowlists]]\n  paths = [\n    '''^a\\.js$''',\n  ]\n",
		"double-quoted item":   head + "[[allowlists]]\npaths = [\n  \"^a\\\\.js$\",\n]\n",
		"single-quoted item":   head + "[[allowlists]]\npaths = ['^a\\.js$']\n",
		"regex item":           head + "[[allowlists]]\npaths = [\n  '''^internal/web/.*$''',\n]\n",
		"unanchored item":      head + "[[allowlists]]\npaths = [\n  '''a\\.js''',\n]\n",
		"mixed good and bad":   head + "[[allowlists]]\npaths = [\n  '''^a\\.js$''',\n  '''^b/.+$''',\n]\n",
		"singular allowlist":   head + "[allowlist]\npaths = [\n  '''^a\\.js$''',\n]\n",
		"rule-level allowlist": head + "[[rules]]\nid = \"x\"\nregex = '''x'''\n[[rules.allowlists]]\npaths = [\n  '''^a\\.js$''',\n]\n",
		"rule-level scalar":    head + "[[rules]]\nid = \"x\"\nregex = '''x'''\n[[rules.allowlists]]\npaths='''^a\\.js$'''\n",
		"inline table":         head + "allowlists = [{paths = ['''^a\\.js$''']}]\n",
	} {
		if _, problems := gitleaksExemptPaths(c); len(problems) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
	// A one-line list in a global allowlist is readable, so it is not a problem.
	if got, problems := gitleaksExemptPaths(head + "[[allowlists]]\npaths = ['''^a\\.js$''', '''^b/c\\.js$''']\n"); len(problems) != 0 || strings.Join(got, " ") != "a.js b/c.js" {
		t.Errorf("one-line list: %v, %v", got, problems)
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

// The force-push range scan runs with scripts/ci/gitleaks-range.toml, the repo config
// minus its private-identifier rules (old commits legitimately hold fixtures those
// rules were added later to catch). The list of disabled rules must be exactly the
// custom rules of .gitleaks.toml, so a new custom rule is a deliberate choice: add it
// to the range config's list or move it out of the repo config.
func TestForcePushRangeConfigDisablesExactlyTheCustomRules(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	custom := customRuleIDs(read("../../.gitleaks.toml"))
	if len(custom) == 0 {
		t.Fatal("found no custom [[rules]] in .gitleaks.toml: has its layout changed?")
	}
	rng := read("../../scripts/ci/gitleaks-range.toml")
	if !regexp.MustCompile(`(?m)^path = "\.gitleaks\.toml"$`).MatchString(rng) {
		t.Error(`gitleaks-range.toml must extend the repo config: path = ".gitleaks.toml"`)
	}
	disabled := disabledRuleIDs(rng)
	if strings.Join(disabled, ",") != strings.Join(custom, ",") {
		t.Errorf("gitleaks-range.toml disables %v, the custom rules of .gitleaks.toml are %v: they must match", disabled, custom)
	}
	if got := customRuleIDs("[[rules]]\nid = \"b\"\n[[allowlists]]\nregexes = ['''x''']\n[[rules]]\nid = \"a\"\n"); strings.Join(got, ",") != "a,b" {
		t.Errorf("customRuleIDs = %v", got)
	}
	if got := disabledRuleIDs("[extend]\ndisabledRules = [\n  \"b\",\n  \"a\", # c\n]\n"); strings.Join(got, ",") != "a,b" {
		t.Errorf("disabledRuleIDs = %v", got)
	}
}

// customRuleIDs returns the sorted ids of the [[rules]] tables.
func customRuleIDs(cfg string) []string {
	var ids []string
	inRule := false
	for _, l := range strings.Split(cfg, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "[["):
			inRule = t == "[[rules]]"
		case strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[["):
			inRule = false
		case inRule:
			if m := regexp.MustCompile(`^id\s*=\s*"([^"]+)"`).FindStringSubmatch(t); m != nil {
				ids = append(ids, m[1])
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// disabledRuleIDs returns the sorted entries of extend.disabledRules.
func disabledRuleIDs(cfg string) []string {
	m := regexp.MustCompile(`(?s)disabledRules\s*=\s*\[(.*?)\]`).FindStringSubmatch(cfg)
	if m == nil {
		return nil
	}
	var ids []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(regexp.MustCompile(`(?m)#.*$`).ReplaceAllString(m[1], ""), -1) {
		ids = append(ids, q[1])
	}
	sort.Strings(ids)
	return ids
}
