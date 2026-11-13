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
		Archives: map[string]string{
			"culibox_v0.1.0_darwin_arm64.tar.gz": sum("arm"),
			"culibox_v0.1.0_darwin_amd64.tar.gz": sum("amd"),
		},
		Arches: []ArchResult{
			{Arch: "arm64", Mode: "native", TestsPassed: true, BinarySHA256: sum("armbin"), TestEnv: "CGO_ENABLED=0 GOOS=darwin GOARCH=arm64"},
			{Arch: "amd64", Mode: "rosetta2", TestsPassed: true, BinarySHA256: sum("amdbin"), TestEnv: "CGO_ENABLED=0 GOOS=darwin GOARCH=amd64"},
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
		{"nil archives", mut(func(r *Run) { r.Archives = nil }), "partial"},
		{"one tarball", mut(func(r *Run) { delete(r.Archives, "culibox_v0.1.0_darwin_amd64.tar.gz") }), "partial"},
		{"bad sha", mut(func(r *Run) { r.Archives["culibox_v0.1.0_darwin_arm64.tar.gz"] = "aa" }), "partial"},
		{"empty sha", mut(func(r *Run) { r.Archives["culibox_v0.1.0_darwin_amd64.tar.gz"] = "" }), "partial"},
		{"two arm64 tarballs", mut(func(r *Run) { r.Archives["culibox_v0.0.9_darwin_arm64.tar.gz"] = sum("old") }), "partial"},
		{"misnamed tarballs", mut(func(r *Run) {
			r.Archives = map[string]string{"a_darwin_arm64.tar.gz": sum("x"), "a_darwin_amd64.tar.gz": sum("y")}
		}), "partial"},
		{"linux tarballs only", mut(func(r *Run) {
			r.Archives = map[string]string{"culibox_v0.1.0_linux_arm64.tar.gz": sum("x"), "culibox_v0.1.0_linux_amd64.tar.gz": sum("y")}
		}), "partial"},
		{"amd64 native not rosetta", mut(func(r *Run) { r.Arches[1].Mode = "native" }), "partial"},
		{"arm64 under rosetta", mut(func(r *Run) { r.Arches[0].Mode = "rosetta2" }), "partial"},
		{"empty mode", mut(func(r *Run) { r.Arches[0].Mode = "" }), "partial"},
		// The shipped tarballs are built with CGO_ENABLED=0 and the manual rows run the
		// extracted binary: "verified" needs the record to say both.
		{"no binary sha256", mut(func(r *Run) { r.Arches[0].BinarySHA256 = "" }), "partial"},
		{"bad binary sha256", mut(func(r *Run) { r.Arches[1].BinarySHA256 = "zz" }), "partial"},
		{"no test env", mut(func(r *Run) { r.Arches[0].TestEnv = "" }), "partial"},
		{"tests with cgo on", mut(func(r *Run) { r.Arches[1].TestEnv = "CGO_ENABLED=1 GOOS=darwin GOARCH=amd64" }), "partial"},
		{"cgo off only by substring", mut(func(r *Run) { r.Arches[1].TestEnv = "CGO_ENABLED=01 GOOS=darwin GOARCH=amd64" }), "partial"},
		{"failure beats bad archives", mut(func(r *Run) { r.Archives = nil; r.Arches[0].TestsPassed = false }), "failed"},
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
	// v0.9.2 has the asset but its notes say failed: never named as verified.
	for _, r := range rs {
		if r.TagName == "v0.9.2" && (!hasVerificationAsset(r) || bodyShowsVerified(r.Body)) {
			t.Fatal("fixture: v0.9.2 must have the asset and a failed body")
		}
	}
	if got, ok := LastVerified(rs, "v0.9.3"); !ok || got.TagName != "v0.9.0" {
		t.Fatalf("LastVerified(v0.9.3) = %q, %v; want v0.9.0 (v0.9.2's record failed)", got.TagName, ok)
	}
	// An asset with no status block, or a block without a verified line, is skipped too.
	noBody := append([]Release{}, rs...)
	for i := range noBody {
		if noBody[i].TagName == "v0.9.0" {
			noBody[i].Body = ""
		}
	}
	if got, ok := LastVerified(noBody, "v0.10.0"); ok {
		t.Fatalf("LastVerified with no verified body = %q, want none", got.TagName)
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
	more = append(more, Release{TagName: "v0.10.0", HTMLURL: "u10", Body: RenderBlock("v0.10.0", "ci", Release{}, false, DarwinLine(goodRun())), Assets: []struct {
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
	// What CI really ran: both arches run the source tests (cgo, -race) on a native
	// runner; only the linux/amd64 tarball is run (its version is checked), the
	// linux/arm64 tarball never is. "tested in CI (native)" claimed more than that.
	if strings.Contains(b, "tested in CI (native)") {
		t.Errorf("the block must not say 'tested in CI (native)':\n%s", b)
	}
	amd := lineWith(b, "- linux/amd64:")
	arm := lineWith(b, "- linux/arm64:")
	for name, l := range map[string]string{"amd64": amd, "arm64": arm} {
		if !strings.Contains(l, "source tests passed in CI on a native runner (cgo, -race)") {
			t.Errorf("linux/%s line must say what the CI tests were: %q", name, l)
		}
	}
	if !strings.Contains(amd, "this tarball ran and reported its version") {
		t.Errorf("linux/amd64 line must say its tarball was run: %q", amd)
	}
	if !strings.Contains(arm, "this tarball was not run") || strings.Contains(arm, "reported its version") {
		t.Errorf("linux/arm64 line must say its tarball was not run: %q", arm)
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

// lineWith returns the first line of s that starts with prefix.
func lineWith(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
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
	for _, want := range []string{"✅ verified on macOS 15.1 (Apple M2)", "arm64 native", "amd64 under Rosetta 2 (not Intel hardware)", "2026-11-12",
		// what the check covered, so the tick is not read as more
		"tarball sha256", "shipped binary", "go test with CGO_ENABLED=0"} {
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
	// The Rosetta sentence comes from the recorded Modes, never a constant.
	for name, tc := range map[string]struct {
		f    func(*Run)
		want string
	}{
		"amd64 native":   {func(r *Run) { r.Arches[1].Mode = "native" }, "amd64 ran in mode \"native\""},
		"arm64 rosetta2": {func(r *Run) { r.Arches[0].Mode = "rosetta2" }, "arm64 ran in mode \"rosetta2\""},
		"nil archives":   {func(r *Run) { r.Archives = nil }, "no darwin arm64 tarball sha256"},
		"bad sha":        {func(r *Run) { r.Archives["culibox_v0.1.0_darwin_amd64.tar.gz"] = "zz" }, "darwin amd64 tarball sha256 is not 64 hex"},
		"no binary sha":  {func(r *Run) { r.Arches[0].BinarySHA256 = "" }, "arm64 has no sha256 of the binary that was run"},
		"cgo on":         {func(r *Run) { r.Arches[1].TestEnv = "CGO_ENABLED=1" }, "amd64 tests were not run with CGO_ENABLED=0"},
	} {
		r := goodRun()
		tc.f(&r)
		got := DarwinLine(r)
		if strings.HasPrefix(got, "✅") || strings.Contains(got, "under Rosetta 2") || !strings.HasPrefix(got, "⚠️ automated checks incomplete") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: line %q (want incomplete, naming %q)", name, got, tc.want)
		}
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

func TestCheckSumsCLI_DuplicateBaseName(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, sub := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, sub, "x.tar.gz")
		if err := os.WriteFile(p, []byte("same"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte(sum("same")+"  x.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := run(append([]string{"check-sums", "--sums", sums}, paths...), &out)
	if err == nil || !strings.Contains(err.Error(), "x.tar.gz") || out.Len() != 0 {
		t.Fatalf("duplicate base names must error and print nothing, got err=%v out=%q", err, out.String())
	}
	if err := run([]string{"check-sums", "--sums", sums, paths[0]}, &out); err != nil {
		t.Fatalf("one file: %v", err)
	}
}

// rewrite --not-verified puts both darwin lines back to "NOT yet verified" without a
// record: macos-verify.sh uses it to clear an older tick before it uploads a new
// record, so a failed notes edit afterwards can never leave that tick standing.
func TestRewriteNotVerifiedCLI(t *testing.T) {
	const ci = "https://github.com/culiops/culibox/actions/runs/42"
	block := RenderBlock("v0.1.0", ci, Release{}, false, DarwinLine(goodRun()))
	notes := "## v0.1.0\n\n- a thing\n\n" + block + "\n\ntail\n"
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte(notes), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := run([]string{"rewrite", "--notes", path, "--not-verified"}, &out); err != nil {
		t.Fatal(err)
	}
	want, err := RewriteDarwin(notes, notYetVerified)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != want || strings.Contains(out.String(), "✅") || strings.Count(out.String(), notYetVerified) != 2 {
		t.Errorf("rewrite --not-verified printed:\n%s", out.String())
	}
	if err := run([]string{"rewrite", "--notes", path, "--not-verified", "--record", "x.json"}, &out); err == nil {
		t.Error("--not-verified together with --record must error")
	}
	if err := run([]string{"rewrite", "--notes", path}, &out); err == nil {
		t.Error("rewrite with neither --record nor --not-verified must error")
	}
}
