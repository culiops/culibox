package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	beginMarker = "<!-- platform-status:begin -->"
	endMarker   = "<!-- platform-status:end -->"

	// VerificationAsset is the release asset macos-verify.sh uploads.
	VerificationAsset = "macos-verification.json"

	notYetVerified = "⚠️ NOT yet verified on macOS"

	darwinARM = "- darwin/arm64:"
	darwinAMD = "- darwin/amd64:"
)

// Release is the part of a GitHub Releases API object this tool reads.
type Release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// ArchResult is one darwin arch's automated result. Mode is "native" or "rosetta2".
type ArchResult struct {
	Arch        string `json:"arch"`
	Mode        string `json:"mode"`
	TestsPassed bool   `json:"tests_passed"`
	TestSummary string `json:"test_summary"`
	VersionLine string `json:"version_line"`
}

// ManualRow is one manual acceptance row. Result is "pass", "fail" or "na".
type ManualRow struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	Note   string `json:"note"`
}

// Run is one verification run. Archives maps a tarball name to its sha256.
type Run struct {
	Date      string            `json:"date"`
	MacOS     string            `json:"macos"`
	Chip      string            `json:"chip"`
	GoVersion string            `json:"go_version"`
	Verifier  string            `json:"verifier"`
	Archives  map[string]string `json:"archives"`
	Arches    []ArchResult      `json:"arches"`
	Manual    []ManualRow       `json:"manual"`
	Verdict   string            `json:"verdict"`
}

// Record is the content of macos-verification.json: every run for one tag/commit.
type Record struct {
	Tag    string `json:"tag"`
	Commit string `json:"commit"`
	Runs   []Run  `json:"runs"`
}

func archName(a string) string { return strings.TrimPrefix(a, "darwin/") }

// missingArches lists the darwin arches the run has no result for.
func missingArches(r Run) []string {
	have := map[string]bool{}
	for _, a := range r.Arches {
		have[archName(a.Arch)] = true
	}
	var out []string
	for _, want := range []string{"arm64", "amd64"} {
		if !have[want] {
			out = append(out, want)
		}
	}
	return out
}

func failures(r Run) (arches, rows []string) {
	for _, a := range r.Arches {
		if !a.TestsPassed {
			arches = append(arches, archName(a.Arch))
		}
	}
	for _, m := range r.Manual {
		if m.Result == "fail" {
			rows = append(rows, m.ID)
		}
	}
	return
}

// Verdict is "verified", "partial" or "failed". Anything that failed is "failed".
// "verified" needs both arches present and passing and at least one manual row
// marked pass, so a run never defaults to a green tick: zero rows, all-n/a rows or a
// missing arch are "partial".
func Verdict(r Run) string {
	fa, fr := failures(r)
	if len(fa) > 0 || len(fr) > 0 {
		return "failed"
	}
	if len(missingArches(r)) > 0 {
		return "partial"
	}
	for _, m := range r.Manual {
		if m.Result == "pass" {
			return "verified"
		}
	}
	return "partial"
}

// DarwinLine is the status text for the darwin lines. It recomputes the verdict and
// never trusts r.Verdict.
func DarwinLine(r Run) string {
	switch Verdict(r) {
	case "verified":
		s := fmt.Sprintf("✅ verified on macOS %s (%s), arm64 native, amd64 under Rosetta 2 (not Intel hardware)", r.MacOS, r.Chip)
		if r.Date != "" {
			s += ", " + r.Date
		}
		return s
	case "failed":
		fa, fr := failures(r)
		var parts []string
		if len(fa) > 0 {
			parts = append(parts, strings.Join(fa, ", ")+" tests failed")
		}
		if len(fr) > 0 {
			parts = append(parts, "manual rows "+strings.Join(fr, ", ")+" failed")
		}
		s := "❌ failed: " + strings.Join(parts, "; ")
		if r.Date != "" {
			s += " (" + r.Date + ")"
		}
		return s
	default:
		if miss := missingArches(r); len(miss) > 0 {
			return "⚠️ automated checks incomplete (no " + strings.Join(miss, ", ") + " result)"
		}
		s := "⚠️ automated checks passed, manual checks not run"
		if r.Date != "" {
			s += " (" + r.Date + ")"
		}
		return s
	}
}

// semver is the numeric X.Y.Z core of a tag.
type semver [3]int

func parseSemver(tag string) (semver, bool) {
	s := strings.TrimPrefix(tag, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var v semver
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || p[0] == '+' {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func (a semver) less(b semver) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func hasVerificationAsset(r Release) bool {
	for _, a := range r.Assets {
		if a.Name == VerificationAsset {
			return true
		}
	}
	return false
}

// LastVerified returns the newest published release older than current (compared on
// the X.Y.Z core, so a pre-release current works) that carries macos-verification.json.
// Drafts and pre-release tags (anything with "-") are never candidates.
func LastVerified(rs []Release, current string) (Release, bool) {
	cur, ok := parseSemver(current)
	if !ok {
		return Release{}, false
	}
	var best Release
	var bestV semver
	found := false
	for _, r := range rs {
		if r.Draft || strings.Contains(r.TagName, "-") || !hasVerificationAsset(r) {
			continue
		}
		v, ok := parseSemver(r.TagName)
		if !ok || !v.less(cur) {
			continue
		}
		if !found || bestV.less(v) {
			best, bestV, found = r, v, true
		}
	}
	return best, found
}

// RenderBlock renders the platform status block, markers included. darwin is "" for
// "not verified" or a DarwinLine; it fills both darwin lines. Each darwin line starts
// with "- darwin/<arch>:" so RewriteDarwin can find it.
func RenderBlock(tag, ciRunURL string, last Release, hasLast bool, darwin string) string {
	if darwin == "" {
		darwin = notYetVerified
	}
	prior := "none yet"
	if hasLast {
		prior = fmt.Sprintf("[%s](%s)", last.TagName, last.HTMLURL)
	}
	var b strings.Builder
	b.WriteString(beginMarker + "\n")
	b.WriteString("### Platform status\n\n")
	fmt.Fprintf(&b, "- linux/amd64: tested in CI (native), [run](%s)\n", ciRunURL)
	fmt.Fprintf(&b, "- linux/arm64: tested in CI (native), [run](%s)\n", ciRunURL)
	b.WriteString(darwinARM + " " + darwin + "\n")
	b.WriteString(darwinAMD + " " + darwin + "\n\n")
	b.WriteString("Last macOS-verified release: " + prior + "\n")
	b.WriteString(endMarker)
	return b.String()
}

// locateBlock finds the one marker pair in notes: the byte range from the start of the
// begin marker to the end of the end marker.
func locateBlock(notes string) (start, end int, err error) {
	if n := strings.Count(notes, beginMarker); n != 1 {
		return 0, 0, fmt.Errorf("want exactly one %q marker in the notes, found %d", beginMarker, n)
	}
	if n := strings.Count(notes, endMarker); n != 1 {
		return 0, 0, fmt.Errorf("want exactly one %q marker in the notes, found %d", endMarker, n)
	}
	start = strings.Index(notes, beginMarker)
	end = strings.Index(notes, endMarker)
	if end < start {
		return 0, 0, errors.New("platform-status end marker comes before the begin marker")
	}
	return start, end + len(endMarker), nil
}

// ReplaceBlock swaps the marked block in notes for block (markers included; a block
// without markers is wrapped). It errors on missing or duplicated markers and never
// appends.
func ReplaceBlock(notes, block string) (string, error) {
	start, end, err := locateBlock(notes)
	if err != nil {
		return "", err
	}
	if !strings.Contains(block, beginMarker) {
		block = beginMarker + "\n" + strings.Trim(block, "\r\n") + "\n" + endMarker
	}
	return notes[:start] + block + notes[end:], nil
}

// RewriteDarwin replaces the two darwin lines inside the notes' status block with
// darwin and leaves every other byte alone, so the Linux lines, the CI run URL and the
// "last verified" line survive. It errors unless there is one marker pair holding
// exactly one line for each darwin arch.
func RewriteDarwin(notes, darwin string) (string, error) {
	start, end, err := locateBlock(notes)
	if err != nil {
		return "", err
	}
	lines := strings.Split(notes[start:end], "\n")
	seen := map[string]int{}
	for i, l := range lines {
		cr := ""
		if strings.HasSuffix(l, "\r") {
			cr = "\r"
		}
		for _, prefix := range []string{darwinARM, darwinAMD} {
			if strings.HasPrefix(l, prefix) {
				seen[prefix]++
				lines[i] = prefix + " " + darwin + cr
			}
		}
	}
	for _, prefix := range []string{darwinARM, darwinAMD} {
		if seen[prefix] != 1 {
			return "", fmt.Errorf("want exactly one %q line in the status block, found %d", prefix, seen[prefix])
		}
	}
	return ReplaceBlock(notes, strings.Join(lines, "\n"))
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// parseSums reads sha256sum output: "<hex>  <name>" or "<hex> *<name>".
func parseSums(sums []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		h, name, ok := strings.Cut(line, " ")
		if !ok || !isHex64(h) {
			return nil, fmt.Errorf("SHA256SUMS line %d is not '<sha256>  <name>'", i+1)
		}
		name = strings.TrimPrefix(strings.TrimPrefix(name, " "), "*")
		if name == "" {
			return nil, fmt.Errorf("SHA256SUMS line %d has no file name", i+1)
		}
		h = strings.ToLower(h)
		if prev, dup := out[name]; dup && prev != h {
			return nil, fmt.Errorf("SHA256SUMS lists %s twice with different sums", name)
		}
		out[name] = h
	}
	return out, nil
}

// CheckSums verifies each file (name to local path) against a SHA256SUMS body. A file
// missing from the sums, unreadable, or with a different sum is an error naming it.
func CheckSums(sums []byte, files map[string]string) error {
	if len(files) == 0 {
		return errors.New("no files to check")
	}
	want, err := parseSums(sums)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		w, ok := want[name]
		if !ok {
			return fmt.Errorf("%s is not listed in SHA256SUMS", name)
		}
		got, err := fileSum(files[name])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if got != w {
			return fmt.Errorf("%s: sha256 mismatch: SHA256SUMS says %s, the file is %s", name, w, got)
		}
	}
	return nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AddRun returns existing (or a new record when nil) with run appended and its Verdict
// recomputed. The existing record's tag and commit must equal the arguments: a tag that
// moved must never merge runs for different bytes. existing is not modified.
func AddRun(existing *Record, tag, commit string, run Run) (Record, error) {
	if tag == "" || commit == "" {
		return Record{}, errors.New("tag and commit are required")
	}
	rec := Record{Tag: tag, Commit: commit}
	if existing != nil {
		if existing.Tag != tag {
			return Record{}, fmt.Errorf("existing record is for tag %q, not %q", existing.Tag, tag)
		}
		if existing.Commit != commit {
			return Record{}, fmt.Errorf("existing record is for commit %s but %s is at %s now (a moved tag?); not merging runs for different bytes", existing.Commit, tag, commit)
		}
		rec.Runs = append(rec.Runs, existing.Runs...)
	}
	run.Verdict = Verdict(run)
	rec.Runs = append(rec.Runs, run)
	return rec, nil
}
