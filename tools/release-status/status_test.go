package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goodRun() Run {
	return Run{
		Date: "2026-11-12", MacOS: "15.1", Chip: "Apple M2", GoVersion: "go1.26.5", Verifier: "founder",
		Archives: map[string]string{"a_darwin_arm64.tar.gz": "aa", "a_darwin_amd64.tar.gz": "bb"},
		Arches: []ArchResult{
			{Arch: "arm64", Mode: "native", TestsPassed: true},
			{Arch: "amd64", Mode: "rosetta2", TestsPassed: true},
		},
		Manual: []ManualRow{{ID: "M1", Result: "pass"}},
	}
}

func TestVerdict(t *testing.T) {
	mut := func(f func(*Run)) Run { r := goodRun(); f(&r); return r }
	cases := []struct {
		name string
		run  Run
		want string
	}{
		{"all pass", goodRun(), "verified"},
		{"pass plus na", mut(func(r *Run) { r.Manual = append(r.Manual, ManualRow{ID: "M2", Result: "na"}) }), "verified"},
		{"zero manual rows", mut(func(r *Run) { r.Manual = nil }), "partial"},
		{"all manual na", mut(func(r *Run) { r.Manual = []ManualRow{{ID: "M1", Result: "na"}, {ID: "M2", Result: "na"}} }), "partial"},
		{"unknown manual result", mut(func(r *Run) { r.Manual = []ManualRow{{ID: "M1", Result: ""}} }), "partial"},
		{"zero arches", mut(func(r *Run) { r.Arches = nil }), "partial"},
		{"amd64 missing", mut(func(r *Run) { r.Arches = r.Arches[:1] }), "partial"},
		{"arch tests fail", mut(func(r *Run) { r.Arches[1].TestsPassed = false }), "failed"},
		{"manual fail", mut(func(r *Run) { r.Manual = append(r.Manual, ManualRow{ID: "M2", Result: "fail"}) }), "failed"},
		{"fail beats missing arch", mut(func(r *Run) { r.Arches = []ArchResult{{Arch: "arm64", TestsPassed: false}} }), "failed"},
		{"stored verdict ignored", mut(func(r *Run) { r.Verdict = "verified"; r.Manual = nil }), "partial"},
	}
	for _, c := range cases {
		if got := Verdict(c.run); got != c.want {
			t.Errorf("%s: Verdict = %q, want %q", c.name, got, c.want)
		}
	}
}

func loadFixture(t *testing.T) []Release {
	t.Helper()
	b, err := os.ReadFile("testdata/releases.json")
	if err != nil {
		t.Fatal(err)
	}
	var rs []Release
	if err := json.Unmarshal(b, &rs); err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestLastVerified(t *testing.T) {
	rs := loadFixture(t)
	got, ok := LastVerified(rs, "v0.10.0")
	if !ok || got.TagName != "v0.9.0" {
		t.Fatalf("LastVerified(v0.10.0) = %q, %v; want v0.9.0 (v0.9.1 has no asset, v0.10.0 is a draft, rc is a pre-release)", got.TagName, ok)
	}
	// A pre-release current is compared on its X.Y.Z core.
	if got, ok := LastVerified(rs, "v0.0.0-test"); ok {
		t.Fatalf("LastVerified(v0.0.0-test) = %q, want none", got.TagName)
	}
	if got, ok := LastVerified(rs, "v0.10.0-rc1"); !ok || got.TagName != "v0.9.0" {
		t.Fatalf("LastVerified(v0.10.0-rc1) = %q, %v; want v0.9.0", got.TagName, ok)
	}
	// Strictly older: the release itself is never its own "last verified".
	if got, ok := LastVerified(rs, "v0.9.0"); ok {
		t.Fatalf("LastVerified(v0.9.0) = %q, want none", got.TagName)
	}
	// Numeric, not lexical: 0.10.0 beats 0.9.0.
	more := append([]Release{}, rs...)
	more = append(more, Release{TagName: "v0.10.0", HTMLURL: "u10", Assets: []struct {
		Name string `json:"name"`
	}{{Name: "macos-verification.json"}}})
	if got, ok := LastVerified(more, "v0.11.0"); !ok || got.HTMLURL != "u10" {
		t.Fatalf("LastVerified(v0.11.0) = %q, %v; want the published v0.10.0", got.TagName, ok)
	}
	if _, ok := LastVerified(rs, "not-a-version"); ok {
		t.Fatal("unparseable current must find nothing")
	}
}

func TestRenderBlock_NoPriorVerification(t *testing.T) {
	const ci = "https://github.com/culiops/culibox/actions/runs/42"
	b := RenderBlock("v0.1.0", ci, Release{}, false, "")
	for _, want := range []string{beginMarker, endMarker, "Last macOS-verified release: none yet"} {
		if !strings.Contains(b, want) {
			t.Errorf("block lacks %q:\n%s", want, b)
		}
	}
	if strings.Count(b, "tested in CI (native)") != 2 {
		t.Errorf("want two Linux lines saying tested in CI (native):\n%s", b)
	}
	if strings.Count(b, ci) != 2 {
		t.Errorf("both Linux lines must link the run URL:\n%s", b)
	}
	if strings.Count(b, "NOT yet verified on macOS") != 2 {
		t.Errorf("want two darwin lines saying NOT yet verified on macOS:\n%s", b)
	}
	for _, p := range []string{"- linux/amd64:", "- linux/arm64:", "- darwin/arm64:", "- darwin/amd64:"} {
		if !strings.Contains(b, p) {
			t.Errorf("block lacks line %q", p)
		}
	}
}

func TestRenderBlock_WithPrior(t *testing.T) {
	last := Release{TagName: "v0.9.0", HTMLURL: "https://github.com/culiops/culibox/releases/tag/v0.9.0"}
	b := RenderBlock("v0.10.0", "https://ci/1", last, true, "")
	if !strings.Contains(b, "[v0.9.0](https://github.com/culiops/culibox/releases/tag/v0.9.0)") {
		t.Errorf("block lacks the prior release link:\n%s", b)
	}
	if strings.Contains(b, "none yet") {
		t.Errorf("block must not say none yet:\n%s", b)
	}
}

func TestRenderBlock_WithDarwinLine(t *testing.T) {
	line := DarwinLine(goodRun())
	b := RenderBlock("v0.1.0", "https://ci/1", Release{}, false, line)
	if strings.Contains(b, "NOT yet verified") {
		t.Errorf("a verified block must not say NOT yet verified:\n%s", b)
	}
	if strings.Count(b, line) != 2 {
		t.Errorf("both darwin lines carry the status line:\n%s", b)
	}
}

func TestDarwinLine(t *testing.T) {
	v := DarwinLine(goodRun())
	for _, want := range []string{"✅ verified on macOS 15.1 (Apple M2)", "arm64 native", "amd64 under Rosetta 2 (not Intel hardware)", "2026-11-12"} {
		if !strings.Contains(v, want) {
			t.Errorf("verified line lacks %q: %s", want, v)
		}
	}
	if !strings.HasPrefix(v, "✅ verified on macOS 15.1 (Apple M2)") {
		t.Errorf("verified line prefix: %s", v)
	}
	p := goodRun()
	p.Manual = nil
	if got := DarwinLine(p); !strings.HasPrefix(got, "⚠️ automated checks passed, manual checks not run") {
		t.Errorf("partial line: %s", got)
	}
	f := goodRun()
	f.Arches[0].TestsPassed = false
	f.Manual = []ManualRow{{ID: "M3", Result: "fail"}, {ID: "M4", Result: "pass"}, {ID: "M7", Result: "fail"}}
	got := DarwinLine(f)
	if !strings.HasPrefix(got, "❌ failed: ") {
		t.Fatalf("failed line: %s", got)
	}
	for _, want := range []string{"arm64", "M3", "M7"} {
		if !strings.Contains(got, want) {
			t.Errorf("failed line must name %q: %s", want, got)
		}
	}
	if strings.Contains(got, "M4") || strings.Contains(got, "amd64") {
		t.Errorf("failed line must name only what failed: %s", got)
	}
	// The stored verdict is never trusted.
	lie := goodRun()
	lie.Verdict = "verified"
	lie.Manual = nil
	if got := DarwinLine(lie); strings.HasPrefix(got, "✅") {
		t.Errorf("DarwinLine trusted a stored verdict: %s", got)
	}
	// An incomplete run (no amd64 result) never reads as verified or as "all checks passed".
	miss := goodRun()
	miss.Arches = miss.Arches[:1]
	if got := DarwinLine(miss); !strings.HasPrefix(got, "⚠️ automated checks incomplete") || !strings.Contains(got, "amd64") {
		t.Errorf("incomplete line: %s", got)
	}
}

func TestReplaceBlock_MissingOrDuplicateMarkers(t *testing.T) {
	block := beginMarker + "\nNEW\n" + endMarker
	if _, err := ReplaceBlock("no markers here", block); err == nil {
		t.Error("missing markers must error, never append")
	}
	if _, err := ReplaceBlock("x\n"+beginMarker+"\na\n"+endMarker+"\n"+beginMarker+"\nb\n"+endMarker, block); err == nil {
		t.Error("two marker pairs must error")
	}
	if _, err := ReplaceBlock(beginMarker+"\na\n", block); err == nil {
		t.Error("a lone begin marker must error")
	}
	if _, err := ReplaceBlock(endMarker+"\na\n"+beginMarker, block); err == nil {
		t.Error("reversed markers must error")
	}
	got, err := ReplaceBlock("head\n"+beginMarker+"\nOLD\n"+endMarker+"\ntail\n", block)
	if err != nil {
		t.Fatal(err)
	}
	if want := "head\n" + beginMarker + "\nNEW\n" + endMarker + "\ntail\n"; got != want {
		t.Errorf("ReplaceBlock = %q, want %q", got, want)
	}
}

func TestRewriteDarwin(t *testing.T) {
	const ci = "https://github.com/culiops/culibox/actions/runs/42"
	last := Release{TagName: "v0.9.0", HTMLURL: "https://github.com/culiops/culibox/releases/tag/v0.9.0"}
	block := RenderBlock("v0.10.0", ci, last, true, "")
	notes := "## What changed\n\n- a thing\n\n" + block + "\n\nFooter text\n"
	line := DarwinLine(goodRun())
	got, err := RewriteDarwin(notes, line)
	if err != nil {
		t.Fatal(err)
	}
	for _, same := range []string{"## What changed", "- a thing", "Footer text", ci, "[v0.9.0](" + last.HTMLURL + ")", "- linux/amd64:", "- linux/arm64:"} {
		if !strings.Contains(got, same) {
			t.Errorf("rewrite lost %q:\n%s", same, got)
		}
	}
	if strings.Contains(got, "NOT yet verified") {
		t.Errorf("darwin lines not replaced:\n%s", got)
	}
	// Everything but the two darwin lines is byte-identical to the original.
	want := strings.NewReplacer(
		"- darwin/arm64: "+notYetVerified, "- darwin/arm64: "+line,
		"- darwin/amd64: "+notYetVerified, "- darwin/amd64: "+line,
	).Replace(notes)
	if got != want {
		t.Errorf("rewrite changed more than the darwin lines:\n got: %q\nwant: %q", got, want)
	}
	// A second rewrite replaces, it does not stack.
	again, err := RewriteDarwin(got, "❌ failed: arm64 tests failed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again, "✅") || strings.Count(again, "❌ failed") != 2 {
		t.Errorf("second rewrite did not replace:\n%s", again)
	}
	// CRLF notes (a release body edited in the web UI) keep their line endings.
	crlf := strings.ReplaceAll(notes, "\n", "\r\n")
	gotCRLF, err := RewriteDarwin(crlf, line)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotCRLF, "NOT yet verified") || strings.Contains(strings.ReplaceAll(gotCRLF, "\r\n", ""), "\n") {
		t.Errorf("CRLF notes were mangled: %q", gotCRLF)
	}
	// Errors: no block, duplicated block, block without the darwin lines.
	if _, err := RewriteDarwin("nothing", line); err == nil {
		t.Error("no block must error")
	}
	if _, err := RewriteDarwin(block+"\n"+block, line); err == nil {
		t.Error("duplicated block must error")
	}
	if _, err := RewriteDarwin(beginMarker+"\n- linux/amd64: x\n"+endMarker, line); err == nil {
		t.Error("a block without darwin lines must error")
	}
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestCheckSums(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := write("a.tar.gz", "alpha"), write("b.tar.gz", "beta")
	sums := []byte(sum("alpha") + "  a.tar.gz\n" + strings.ToUpper(sum("beta")) + " *b.tar.gz\n\n")
	files := map[string]string{"a.tar.gz": a, "b.tar.gz": b}
	if err := CheckSums(sums, files); err != nil {
		t.Fatalf("matching sums: %v", err)
	}
	bad := []byte(sum("WRONG") + "  a.tar.gz\n" + sum("beta") + "  b.tar.gz\n")
	if err := CheckSums(bad, files); err == nil || !strings.Contains(err.Error(), "a.tar.gz") {
		t.Fatalf("wrong sum must name a.tar.gz, got %v", err)
	}
	if err := CheckSums([]byte(sum("alpha")+"  a.tar.gz\n"), files); err == nil || !strings.Contains(err.Error(), "b.tar.gz") {
		t.Fatalf("file absent from SHA256SUMS must name b.tar.gz, got %v", err)
	}
	gone := map[string]string{"a.tar.gz": a, "c.tar.gz": filepath.Join(dir, "nope")}
	withC := []byte(string(sums) + sum("x") + "  c.tar.gz\n")
	if err := CheckSums(withC, gone); err == nil || !strings.Contains(err.Error(), "c.tar.gz") {
		t.Fatalf("unreadable file must name c.tar.gz, got %v", err)
	}
	if err := CheckSums([]byte("not a sums file\n"), files); err == nil {
		t.Fatal("malformed SHA256SUMS must error")
	}
	if err := CheckSums(sums, nil); err == nil {
		t.Fatal("no files must error: nothing was verified")
	}
}

func TestRecordAppendsRun(t *testing.T) {
	first := goodRun()
	first.Verdict = "verified"
	existing := &Record{Tag: "v0.1.0", Commit: "abc", Runs: []Run{first}}
	nr := goodRun()
	nr.Verdict = "verified" // ignored
	nr.Manual = nil
	got, err := AddRun(existing, "v0.1.0", "abc", nr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 2 || got.Tag != "v0.1.0" || got.Commit != "abc" {
		t.Fatalf("got %+v", got)
	}
	if got.Runs[0].Verdict != "verified" || got.Runs[1].Verdict != "partial" {
		t.Errorf("verdicts must be recomputed: %q, %q", got.Runs[0].Verdict, got.Runs[1].Verdict)
	}
	if len(existing.Runs) != 1 {
		t.Error("AddRun mutated its input")
	}
	if _, err := AddRun(existing, "v0.2.0", "abc", nr); err == nil {
		t.Error("tag mismatch must error")
	}
	if _, err := AddRun(existing, "v0.1.0", "def", nr); err == nil {
		t.Error("commit mismatch (a moved tag) must error")
	}
	fresh, err := AddRun(nil, "v0.1.0", "abc", nr)
	if err != nil || len(fresh.Runs) != 1 || fresh.Tag != "v0.1.0" || fresh.Commit != "abc" {
		t.Fatalf("fresh record: %+v, %v", fresh, err)
	}
	if _, err := AddRun(nil, "", "abc", nr); err == nil {
		t.Error("empty tag must error")
	}
	if _, err := AddRun(nil, "v0.1.0", "", nr); err == nil {
		t.Error("empty commit must error")
	}
}
