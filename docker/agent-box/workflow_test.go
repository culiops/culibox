package boximage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const workflowPath = "../../.github/workflows/box-image.yml"

// TestWorkflowSupplyChain pins the shape of the CI that pushes to the public
// ghcr package: the workflow is read as text, so no YAML dependency is needed.
// A fork PR or a moved action tag must never be able to push an image.
func TestWorkflowSupplyChain(t *testing.T) {
	b, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)

	pinned := regexp.MustCompile(`uses: [\w./-]+@[0-9a-f]{40} # v\d`)
	uses := 0
	for _, line := range strings.Split(wf, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "uses:") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		uses++
		if !pinned.MatchString(trimmed) {
			t.Errorf("action not pinned by full commit sha with a version comment: %q", trimmed)
		}
	}
	if uses == 0 {
		t.Error("no uses: lines found")
	}

	for _, banned := range []string{"pull_request_target", "pull_request"} {
		if strings.Contains(wf, banned) {
			t.Errorf("workflow must not mention %s (merges here are local; a PR must never build or push)", banned)
		}
	}

	if !regexp.MustCompile(`(?m)^permissions:\n  contents: read\n`).MatchString(wf) {
		t.Error("top level must declare `permissions:` with `contents: read`")
	}
	// build and merge are the two jobs that push, and the only two that may hold
	// packages: write.
	if n := strings.Count(wf, "packages: write"); n != 2 {
		t.Errorf("packages: write appears %d times, want 2 (build, merge)", n)
	}
	build, merge := jobBlock(t, wf, "build"), jobBlock(t, wf, "merge")
	for name, job := range map[string]string{"build": build, "merge": merge} {
		if !strings.Contains(job, "\n    permissions:\n") || !strings.Contains(job, "      packages: write\n") {
			t.Errorf("job %s must declare packages: write in its own permissions block", name)
		}
		// A workflow_dispatch from another branch must not move :base or the dated tag.
		if !strings.Contains(job, "\n    if: github.ref == 'refs/heads/main'\n") {
			t.Errorf("job %s lacks the job-level `if: github.ref == 'refs/heads/main'`", name)
		}
	}
	if !regexp.MustCompile(`(?m)^    branches: \[main\]$`).MatchString(wf) {
		t.Error("the push trigger must be limited to `branches: [main]`")
	}
	for _, banned := range []string{"write-all", "id-token: write", "contents: write"} {
		if strings.Contains(wf, banned) {
			t.Errorf("workflow must not contain %q", banned)
		}
	}

	// The BuildKit engine runs every RUN and holds the push credentials, so it is
	// pinned by index digest, never left at the mutable buildx-stable-1 tag.
	if !regexp.MustCompile(`(?m)^          driver-opts: image=moby/buildkit:v\d+\.\d+\.\d+@sha256:[0-9a-f]{64}$`).MatchString(build) {
		t.Error("the build job's setup-buildx must set driver-opts: image=moby/buildkit:vX.Y.Z@sha256:<64 hex>")
	}
	// The merge job only runs `docker buildx imagetools`, which needs no builder.
	if strings.Contains(merge, "setup-buildx-action") {
		t.Error("the merge job must not start a BuildKit builder (imagetools needs none)")
	}

	// The token goes to docker/login-action and nowhere else.
	tokens := 0
	for _, step := range strings.Split(wf, "\n      - ") {
		if !strings.Contains(step, "secrets.GITHUB_TOKEN") {
			continue
		}
		tokens++
		if !strings.HasPrefix(strings.TrimSpace(step), "uses: docker/login-action@") {
			t.Errorf("secrets.GITHUB_TOKEN used outside a docker/login-action step: %.160q", step)
		}
	}
	if tokens != 2 {
		t.Errorf("secrets.GITHUB_TOKEN appears in %d steps, want 2 (one login per job)", tokens)
	}

	for _, want := range []string{
		"sha256sum docker/agent-box/Dockerfile | cut -c1-12",
		"provenance: false",
		"sbom: false",
		"ubuntu-24.04-arm",
		"push-by-digest=true",
		"persist-credentials: false",
		"docker/agent-box/**",
		".github/workflows/box-image.yml",
		// Layer cache in the registry, per arch. GitHub's Actions cache drops an
		// entry unused for 7 days: the 2026-11-02 run came 8 days after the last one
		// and hit 0 steps, so every layer was new and every host re-pulled ~4 GB.
		// Its own package, so cache manifests never list as versions of the image.
		"  CACHE: ghcr.io/culiops/agent-box-cache\n",
		"cache-from: type=registry,ref=${{ env.CACHE }}:${{ matrix.arch }}",
		"cache-to: type=registry,ref=${{ env.CACHE }}:${{ matrix.arch }},mode=max,image-manifest=true,oci-mediatypes=true,ignore-error=true",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q", want)
		}
	}
	if strings.Contains(wf, "type=gha") {
		t.Error("the layer cache must not use type=gha (entries expire after 7 days unused)")
	}
}

// jobBlock returns the text of one top-level job (from `  <name>:` to the next
// job or the end of the file).
func jobBlock(t *testing.T, wf, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(wf, "\njobs:\n")
	if !ok {
		t.Fatal("no jobs: section")
	}
	start := strings.Index("\n"+rest, "\n  "+name+":\n")
	if start < 0 {
		t.Fatalf("no job %q", name)
	}
	block := ("\n" + rest)[start+1:]
	if next := regexp.MustCompile(`\n  [a-z][\w-]*:\n`).FindStringIndex(block[1:]); next != nil {
		block = block[:next[0]+1]
	}
	return block
}

// The Dockerfile COPYs extra-ca.pem, which agent-box's own build puts in its
// stdin context. The published image builds from the directory instead, so CI
// creates the file, empty: the image on ghcr never trusts an extra CA. The file
// is untracked (and gitignored), so the step must come before the build.
func TestWorkflowCreatesEmptyExtraCABeforeBuild(t *testing.T) {
	b, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	build := jobBlock(t, string(b), "build")
	mk := strings.Index(build, ": > docker/agent-box/extra-ca.pem\n")
	bp := strings.Index(build, "docker/build-push-action@")
	if mk < 0 {
		t.Fatal("the build job lacks a step that writes an EMPTY docker/agent-box/extra-ca.pem (`: > docker/agent-box/extra-ca.pem`)")
	}
	if bp < 0 || mk > bp {
		t.Error("the extra-ca.pem step must precede the build-push step")
	}
	// The file is generated, never tracked.
	gi, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^docker/agent-box/extra-ca\.pem$`).Match(gi) {
		t.Error(".gitignore must list docker/agent-box/extra-ca.pem")
	}
}

const stableWorkflowPath = "../../.github/workflows/claude-code-stable.yml"

// The weekly Claude Code check only REPORTS: it compares the npm `stable`
// dist-tag with the Dockerfile pin and keeps one issue up to date. It must never
// write code, push, or touch packages: a bump of the agent that runs with
// --dangerously-skip-permissions in every box is a reviewed human change.
func TestClaudeCodeStableWorkflowOnlyReports(t *testing.T) {
	b, err := os.ReadFile(stableWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)

	pinned := regexp.MustCompile(`uses: [\w./-]+@[0-9a-f]{40} # v\d`)
	for _, line := range strings.Split(wf, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "uses:") && !strings.HasPrefix(trimmed, "#") && !pinned.MatchString(trimmed) {
			t.Errorf("action not pinned by full commit sha with a version comment: %q", trimmed)
		}
	}
	if !regexp.MustCompile(`(?m)^permissions:\n  contents: read\n`).MatchString(wf) {
		t.Error("top level must declare `permissions:` with `contents: read`")
	}
	if n := strings.Count(wf, "issues: write"); n != 1 {
		t.Errorf("issues: write appears %d times, want 1 (the check job)", n)
	}
	check := jobBlock(t, wf, "check")
	if !strings.Contains(check, "\n    permissions:\n") || !strings.Contains(check, "      issues: write\n") {
		t.Error("job check must declare issues: write in its own permissions block")
	}
	for _, banned := range []string{
		"pull_request", "write-all", "id-token: write", "contents: write", "packages: write",
		"git push", "git commit", "github.event.", "secrets.",
	} {
		if strings.Contains(wf, banned) {
			t.Errorf("workflow must not contain %q", banned)
		}
	}
	for _, want := range []string{
		"schedule:",
		"workflow_dispatch:",
		"persist-credentials: false",
		"ARG CLAUDE_CODE_VERSION=",
		"registry.npmjs.org/-/package/@anthropic-ai/claude-code/dist-tags",
		// Both versions are checked against x.y.z before they reach a command or the issue.
		`^[0-9]+\.[0-9]+\.[0-9]+$`,
		// The repo has 100+ open issues: the lookup must search by title, not read
		// the newest page, or each week opens a duplicate. Only the bot's own issue
		// is ever edited or closed.
		`--search 'in:title`,
		"--author app/github-actions",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q", want)
		}
	}
}

// wfLine is one non-comment line of a workflow, with the original line number
// and where it sits: the job it belongs to and whether it is inside that job's
// own `permissions:` block.
type wfLine struct {
	n       int
	text    string
	job     string
	inPerms bool
}

var (
	wfFullComment = regexp.MustCompile(`^\s*#`)
	wfTrailing    = regexp.MustCompile(`\s+#.*$`)
	wfJobHeader   = regexp.MustCompile(`^  ([\w-]+):\s*(?:#.*)?$`)
	wfJobPerms    = regexp.MustCompile(`^    permissions:\s*(?:#.*)?$`)
	wfWriteScope  = regexp.MustCompile(`^      ["']?([\w-]+)["']?: ["']?write["']?\s*(?:#.*)?$`)
	wfWriteAny    = regexp.MustCompile(`:\s*["']?write\b|write-all`)
	wfUses        = regexp.MustCompile(`(?:^|[\s{,])(uses:.*)$`)
	wfPinned      = regexp.MustCompile(`^uses: [\w./-]+@[0-9a-f]{40} # v\d+\.\d+\.\d+\s*$`)
	wfLocalReuse  = regexp.MustCompile(`^    uses: \./\.github/workflows/[\w.-]+\.ya?ml\s*(?:#.*)?$`)
	wfPersist     = regexp.MustCompile(`^\s*persist-credentials: false\s*(?:#.*)?$`)
	wfTopPerms    = regexp.MustCompile(`(?m)^permissions:[ \t]*(?:#.*)?\n  contents: read[ \t]*(?:#.*)?\n`)
)

func wfIndent(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// parseWorkflow drops full-line comments (keeping each line's original number)
// and tags every line with its job and permissions-block position. Layout is
// the repo's: jobs at 2 spaces, a job's `permissions:` at 4, its scopes at 6.
func parseWorkflow(content string) []wfLine {
	var out []wfLine
	inJobs, job, inPerms := false, "", false
	for i, raw := range strings.Split(content, "\n") {
		if wfFullComment.MatchString(raw) {
			continue
		}
		if strings.TrimSpace(raw) != "" {
			ind := wfIndent(raw)
			if ind == 0 {
				inJobs = strings.HasPrefix(raw, "jobs:")
				job, inPerms = "", false
			} else if inJobs {
				if m := wfJobHeader.FindStringSubmatch(raw); m != nil {
					job, inPerms = m[1], false
				} else if wfJobPerms.MatchString(raw) {
					inPerms = true
				} else if ind <= 4 {
					inPerms = false
				}
			}
		}
		out = append(out, wfLine{n: i + 1, text: raw, job: job, inPerms: inPerms})
	}
	return out
}

// checkWorkflowSupplyChain returns one message per violation, each naming the
// file and (where there is one) the offending line.
func checkWorkflowSupplyChain(name, content string) []string {
	var bad []string
	add := func(n int, format string, args ...any) {
		where := name
		if n > 0 {
			where = fmt.Sprintf("%s:%d", name, n)
		}
		bad = append(bad, where+": "+fmt.Sprintf(format, args...))
	}
	lines := parseWorkflow(content)

	var plain []string
	for _, l := range lines {
		plain = append(plain, l.text)
	}
	if !wfTopPerms.MatchString(strings.Join(plain, "\n") + "\n") {
		add(0, "top level must declare `permissions:` with `contents: read` directly below")
	}

	for i, l := range lines {
		text := strings.TrimSpace(l.text)
		if text == "" {
			continue
		}
		if m := wfUses.FindStringSubmatch(l.text); m != nil {
			switch {
			case wfLocalReuse.MatchString(l.text):
				// A local reusable workflow is allowed, as a job's own `uses:` only.
			case wfPinned.MatchString(strings.TrimSpace(m[1])):
			default:
				add(l.n, "action not pinned by full commit sha with a `# vX.Y.Z` comment (a local reusable workflow is allowed only directly under a job): %q", text)
			}
		}
		if strings.Contains(wfTrailing.ReplaceAllString(l.text, ""), "pull_request") {
			add(l.n, "must not mention pull_request (a PR must never run a job here): %q", text)
		}
		if wfWriteAny.MatchString(wfTrailing.ReplaceAllString(l.text, "")) {
			if !wfWriteScope.MatchString(l.text) || !l.inPerms || l.job == "" {
				add(l.n, "a write grant is allowed only as `      <scope>: write` inside a job's own `permissions:` block: %q", text)
			}
		}
		if strings.Contains(l.text, "actions/checkout@") {
			start, dash := i, wfIndent(l.text)
			if !strings.HasPrefix(text, "- ") {
				start = -1
				for j := i - 1; j >= 0; j-- {
					t := strings.TrimSpace(lines[j].text)
					if strings.HasPrefix(t, "- ") && wfIndent(lines[j].text) < wfIndent(l.text) {
						start, dash = j, wfIndent(lines[j].text)
						break
					}
				}
			}
			if start < 0 {
				add(l.n, "cannot locate the step of this checkout: %q", text)
				continue
			}
			ok := false
			for k := start + 1; k < len(lines); k++ {
				if strings.TrimSpace(lines[k].text) == "" {
					continue
				}
				if wfIndent(lines[k].text) <= dash {
					break
				}
				if wfPersist.MatchString(lines[k].text) {
					ok = true
				}
			}
			if !ok {
				add(l.n, "actions/checkout step lacks `persist-credentials: false`: %q", text)
			}
		}
	}
	return bad
}

func workflowFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, pat := range []string{"*.yml", "*.yaml"} {
		m, err := filepath.Glob("../../.github/workflows/" + pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	return files
}

// TestAllWorkflowsSupplyChain holds every workflow file, present and future
// (ci.yml, release.yml), to the same supply-chain rules as box-image.yml.
func TestAllWorkflowsSupplyChain(t *testing.T) {
	files := workflowFiles(t)
	if len(files) < 2 {
		t.Fatalf("found %d workflow files, want at least 2", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range checkWorkflowSupplyChain(filepath.Base(f), string(b)) {
			t.Error(msg)
		}
	}
}

const releaseWorkflowPath = "../../.github/workflows/release.yml"

// release.yml is the only workflow that publishes and attests: its three write
// scopes must each be granted once, to the release job and nowhere else.
func TestReleaseWorkflowWriteScopes(t *testing.T) {
	b, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range releaseWriteViolations("release.yml", string(b)) {
		t.Error(msg)
	}
}

// releaseWriteViolations checks that each release write scope is granted
// exactly once, inside job release (quoted grants count too).
func releaseWriteViolations(name, content string) []string {
	var bad []string
	for _, scope := range []string{"contents", "id-token", "attestations"} {
		var hits []wfLine
		for _, l := range parseWorkflow(content) {
			if m := wfWriteScope.FindStringSubmatch(l.text); m != nil && m[1] == scope {
				hits = append(hits, l)
			}
		}
		if len(hits) != 1 {
			bad = append(bad, fmt.Sprintf("%s: `%s: write` appears %d times, want exactly 1 (in job release)", name, scope, len(hits)))
			continue
		}
		if hits[0].job != "release" {
			bad = append(bad, fmt.Sprintf("%s:%d: `%s: write` is in job %q, want job release", name, hits[0].n, scope, hits[0].job))
		}
	}
	return bad
}

// A guard that cannot fail is worth nothing: each bad workflow below must be
// reported, and the good one must not.
func TestWorkflowGuardCatchesViolations(t *testing.T) {
	const sha = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	const top = "name: x\non:\n  push:\n    branches: [main]\npermissions:\n  contents: read\njobs:\n"
	const checkout = "      - uses: actions/checkout@" + sha + " # v7.0.1\n        with:\n          persist-credentials: false\n"
	const head = "  a:\n    runs-on: ubuntu-24.04\n    steps:\n"

	good := "# pull_request is only mentioned in a comment\n" + top +
		"  r:\n    uses: ./.github/workflows/other.yml\n" +
		head + checkout + "      # - uses: x@v1\n" +
		"  w:\n    runs-on: x\n    permissions: # why\n      contents: write # why\n    steps:\n" + checkout
	if bad := checkWorkflowSupplyChain("good.yml", good); len(bad) != 0 {
		t.Fatalf("good workflow reported: %v", bad)
	}

	for name, tc := range map[string]struct{ wf, want string }{
		"unpinned tag":         {top + head + "      - uses: actions/checkout@v4 # v4.0.0\n        with:\n          persist-credentials: false\n", "not pinned"},
		"no version":           {top + head + "      - uses: actions/checkout@" + sha + "\n        with:\n          persist-credentials: false\n", "not pinned"},
		"local in steps":       {top + head + "      - uses: ./.github/workflows/other.yml\n", "not pinned"},
		"pull_request":         {strings.Replace(top, "  push:", "  pull_request:", 1) + head + checkout, "pull_request"},
		"no top perms":         {strings.Replace(top, "permissions:\n  contents: read\n", "", 1) + head + checkout, "top level"},
		"top write":            {strings.Replace(top, "contents: read", "contents: write", 1) + head + checkout, "a write grant"},
		"flow write":           {top + head + checkout + "  w:\n    permissions: {contents: write}\n", "a write grant"},
		"write-all":            {top + "  w:\n    permissions: write-all\n" + head + checkout, "a write grant"},
		"write in steps":       {top + head + checkout + "        env:\n          contents: write\n", "a write grant"},
		"quoted top write":     {strings.Replace(top, "contents: read\n", "contents: read\n  packages: \"write\"\n", 1) + head + checkout, "a write grant"},
		"quoted step write":    {top + head + checkout + "        env:\n          packages: 'write'\n", "a write grant"},
		"quoted flow write":    {top + head + checkout + "  w:\n    permissions: {packages: \"write\"}\n", "a write grant"},
		"quoted other job":     {top + head + checkout + "  w:\n    runs-on: x\n    env: {a: 1}\n    packages: 'write'\n", "a write grant"},
		"no persist":           {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n", "persist-credentials"},
		"persist true":         {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n        with:\n          persist-credentials: true\n", "persist-credentials"},
		"persist in next step": {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n      - name: y\n        with:\n          persist-credentials: false\n", "persist-credentials"},
	} {
		bad := checkWorkflowSupplyChain("bad.yml", tc.wf)
		if len(bad) == 0 {
			t.Errorf("%s: no violation reported", name)
			continue
		}
		if !strings.Contains(strings.Join(bad, "\n"), tc.want) || !strings.Contains(bad[0], "bad.yml") {
			t.Errorf("%s: violations %v do not name bad.yml and %q", name, bad, tc.want)
		}
	}
}

func TestReleaseGuardCatchesQuotedAndMisplacedWrites(t *testing.T) {
	job := func(n string, scopes ...string) string {
		s := "  " + n + ":\n    runs-on: x\n    permissions:\n"
		for _, sc := range scopes {
			s += "      " + sc + "\n"
		}
		return s
	}
	const top = "permissions:\n  contents: read\njobs:\n"
	all := []string{"contents: write", "id-token: write", "attestations: write"}
	if bad := releaseWriteViolations("r.yml", top+job("release", all...)); len(bad) != 0 {
		t.Fatalf("good release reported: %v", bad)
	}
	for name, wf := range map[string]string{
		"quoted extra in other job": top + job("release", all...) + job("other", `contents: "write"`),
		"single-quoted extra":       top + job("release", all...) + job("other", "id-token: 'write'"),
		"wrong job":                 top + job("release", "contents: write", "id-token: write") + job("other", "attestations: write"),
		"quoted in release twice":   top + job("release", append(all, `contents: "write"`)...),
	} {
		if bad := releaseWriteViolations("r.yml", wf); len(bad) == 0 {
			t.Errorf("%s: no violation reported", name)
		}
	}
}

const ciWorkflowPath = "../../.github/workflows/ci.yml"

// ciCode returns ci.yml without its full-line comments, so a test asserts what
// the workflow does and a comment that explains a rule cannot satisfy or break it.
func ciCode(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !wfFullComment.MatchString(l) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// TestCIWorkflow pins what ci.yml must keep doing. The supply-chain rules (pins,
// permissions, persist-credentials) are TestAllWorkflowsSupplyChain's job; this
// holds the CI-specific shape: which events, which runners, which checks.
func TestCIWorkflow(t *testing.T) {
	wf := ciCode(t)

	// Triggers: push to main only, a manual run, and workflow_call so release.yml
	// can run the same checks on a tag.
	if !regexp.MustCompile(`(?m)^\s+push:\s*\n\s+branches:\s*\[\s*main\s*\]\s*$`).MatchString(wf) {
		t.Error("the push trigger must be limited to `branches: [main]`")
	}
	for _, trig := range []string{"workflow_dispatch:", "workflow_call:"} {
		if !regexp.MustCompile(`(?m)^  ` + trig + `\s*$`).MatchString(wf) {
			t.Errorf("ci.yml lacks the %s trigger", trig)
		}
	}

	// Runners: the test matrix is exactly the two Linux runners, and no macOS
	// runner exists anywhere (macOS is verified on a host, never in CI). The one
	// allowed mention is the offline test of the verify script, which runs on Linux.
	if strings.Contains(strings.ToLower(strings.ReplaceAll(wf, "scripts/macos-verify_test.sh", "")), "macos") {
		t.Error("ci.yml must not mention macos (no macOS runners)")
	}
	matrix := regexp.MustCompile(`(?m)^\s+os:\s*\[(.*)\]\s*$`).FindStringSubmatch(jobBlock(t, wf, "test"))
	if matrix == nil {
		t.Fatal("job test has no `os: [..]` matrix")
	}
	var runners []string
	for _, r := range strings.Split(matrix[1], ",") {
		runners = append(runners, strings.Trim(strings.TrimSpace(r), `"'`))
	}
	if strings.Join(runners, " ") != "ubuntu-24.04 ubuntu-24.04-arm" {
		t.Errorf("test matrix runners = %v, want exactly [ubuntu-24.04 ubuntu-24.04-arm]", runners)
	}

	for _, want := range []string{
		"go test -race ./...",
		"scripts/ci/install-abduco.sh",
		"GOOS=darwin GOARCH=arm64 go vet ./...",
		"GOOS=darwin GOARCH=amd64 go vet ./...",
		"-tags uidemo",
		"node --test",
		"scripts/lint.sh",
		"bash scripts/macos-verify_test.sh",
		"gitleaks git --config .gitleaks.toml",
		"gitleaks dir --config .gitleaks.toml",
		"sha256sum -c",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("ci.yml lacks %q", want)
		}
	}
	// The env switch that stops an abduco test from passing by skipping.
	if !regexp.MustCompile(`(?m)^\s+AGENT_BOX_REQUIRE_ABDUCO:\s*["']1["']\s*$`).MatchString(jobBlock(t, wf, "test")) {
		t.Error(`job test must set AGENT_BOX_REQUIRE_ABDUCO: "1"`)
	}
	// All four GOOS/GOARCH targets compile the binary.
	for _, tgt := range []string{"darwin arm64", "darwin amd64", "linux arm64", "linux amd64"} {
		if !strings.Contains(jobBlock(t, wf, "darwin"), tgt) {
			t.Errorf("job darwin does not build for %q", tgt)
		}
	}

	// A push run must never be cancelled (a cancelled run leaves its BEFORE..sha
	// range unscanned, and GitHub also cancels a pending run in a shared group), so
	// a push gets a group of its own, keyed on the run id. Other events share a
	// ci-<ref> group and replace their older run. Never github.workflow: release.yml
	// calls this workflow and the callee inherits the caller's github.workflow, so
	// it would collide with its own caller.
	if !regexp.MustCompile(`(?m)^concurrency:\s*\n(\s+.*\n)*?\s+cancel-in-progress:\s*true\s*$`).MatchString(wf) {
		t.Error("ci.yml needs a top-level concurrency with cancel-in-progress: true")
	}
	group := regexp.MustCompile(`(?m)^\s+group:\s*(.+?)\s*$`).FindStringSubmatch(wf)
	if group == nil {
		t.Fatal("ci.yml concurrency has no group")
	}
	for _, want := range []string{"github.event_name == 'push'", "ci-push-{0}", "github.run_id", "ci-{0}", "github.ref"} {
		if !strings.Contains(group[1], want) {
			t.Errorf("concurrency group %q lacks %q (a push run needs its own run-id group)", group[1], want)
		}
	}
	if strings.Contains(wf, "github.workflow") {
		t.Error("ci.yml must not use github.workflow (it is the caller's name under workflow_call)")
	}

	// No write grant of any kind (the supply-chain guard allows job-level ones).
	if wfWriteAny.MatchString(wf) {
		t.Error("ci.yml must not hold any write permission")
	}

	// The event payload reaches the shell only through env:, never interpolated.
	for _, l := range strings.Split(wf, "\n") {
		if strings.Contains(l, "github.event.") && !strings.Contains(l, "BEFORE:") {
			t.Errorf("github.event is used outside the BEFORE env entry: %q", strings.TrimSpace(l))
		}
	}
}

// The CI install script and the box image build the same abduco: the version and
// sum in scripts/ci/install-abduco.sh must equal the Dockerfile ARGs.
func TestAbducoPinMatchesDockerfile(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	script, docker := read("../../scripts/ci/install-abduco.sh"), read("Dockerfile")
	for _, name := range []string{"ABDUCO_VERSION", "ABDUCO_SHA256"} {
		s := regexp.MustCompile(`(?m)^` + name + `=["']?([^\s"']+)["']?\s*$`).FindStringSubmatch(script)
		d := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)\s*$`).FindStringSubmatch(docker)
		if s == nil || d == nil {
			t.Errorf("%s: not found in both install-abduco.sh (%v) and the Dockerfile (%v)", name, s != nil, d != nil)
			continue
		}
		if s[1] != d[1] {
			t.Errorf("%s: install-abduco.sh has %q, Dockerfile has %q", name, s[1], d[1])
		}
	}
}

// stepBlock returns the text of the step of job that contains needle: from its
// `      - ` line to the next step (or the end of the job).
func stepBlock(t *testing.T, job, needle string) string {
	t.Helper()
	lines := strings.Split(job, "\n")
	for i, l := range lines {
		if !strings.Contains(l, needle) {
			continue
		}
		start := i
		for start > 0 && !strings.HasPrefix(lines[start], "      - ") {
			start--
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "      - ") {
			end++
		}
		return strings.Join(lines[start:end], "\n")
	}
	t.Fatalf("no step containing %q", needle)
	return ""
}

// inlineExpr returns the lines of wf where a ${{ }} expression sits inside a
// run: script (a block or a one-liner), which is how an input or a ref name
// becomes shell injection. Such values must go through env: instead.
func inlineExpr(wf string) []string {
	var bad []string
	runIndent := -1
	for _, l := range strings.Split(wf, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ind := wfIndent(l)
		if runIndent >= 0 && ind > runIndent {
			if strings.Contains(l, "${{") {
				bad = append(bad, strings.TrimSpace(l))
			}
			continue
		}
		runIndent = -1
		trimmed := strings.TrimPrefix(strings.TrimSpace(l), "- ")
		if strings.HasPrefix(trimmed, "run:") {
			if strings.Contains(l, "${{") {
				bad = append(bad, strings.TrimSpace(l))
			}
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "run:")); rest == "|" || rest == ">" || rest == "|-" || rest == ">-" {
				runIndent = ind
				if strings.HasPrefix(strings.TrimSpace(l), "- ") {
					runIndent = ind + 2
				}
			}
		}
	}
	return bad
}

// TestReleaseWorkflow pins what release.yml must do: a vX.Y.Z tag push that is on
// main runs the same CI, builds the four tarballs and opens a DRAFT release; a
// manual run builds and packages but never releases. The supply-chain rules and the
// write scopes are TestAllWorkflowsSupplyChain's and TestReleaseWorkflowWriteScopes's.
func TestReleaseWorkflow(t *testing.T) {
	b, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !wfFullComment.MatchString(l) {
			keep = append(keep, l)
		}
	}
	wf := strings.Join(keep, "\n")

	// Triggers: a version tag push and a manual dry run, nothing else.
	if !regexp.MustCompile(`(?m)^on:\n  push:\n    tags: \[\s*"v\*\.\*\.\*"\s*\]\n  workflow_dispatch:[ \t]*\n`).MatchString(wf) {
		t.Error("`on:` must be exactly `push: tags: [\"v*.*.*\"]` plus `workflow_dispatch:`")
	}
	if strings.Contains(strings.ToLower(wf), "macos") {
		t.Error("release.yml must not mention macos (no macOS runners)")
	}

	// R4: a per-ref group that never cancels. Never github.workflow, which the
	// called ci.yml would inherit.
	conc := regexp.MustCompile(`(?m)^concurrency:\n  group: (.+)\n  cancel-in-progress: (\w+)\n`).FindStringSubmatch(wf)
	if conc == nil {
		t.Fatal("release.yml needs a top-level concurrency with a group and cancel-in-progress")
	}
	if conc[1] != "release-${{ github.ref }}" || conc[2] != "false" {
		t.Errorf("concurrency = group %q, cancel-in-progress %s; want group release-${{ github.ref }}, false", conc[1], conc[2])
	}
	if strings.Contains(wf, "github.workflow") {
		t.Error("release.yml must not use github.workflow (the called ci.yml would inherit it)")
	}

	// verify always runs (a skipped job skips every job that needs it); only its
	// ancestry step is guarded.
	verify := jobBlock(t, wf, "verify")
	if regexp.MustCompile(`(?m)^    if:`).MatchString(verify) {
		t.Error("job verify must not be skipped as a whole: the dry run needs it")
	}
	anc := stepBlock(t, verify, "git merge-base --is-ancestor")
	if !strings.Contains(anc, "if: github.event_name == 'push'") {
		t.Error("the ancestry step must be guarded by `if: github.event_name == 'push'`")
	}
	if !strings.Contains(anc, `"$GITHUB_SHA" origin/main`) {
		t.Error("the ancestry check must be `git merge-base --is-ancestor \"$GITHUB_SHA\" origin/main`")
	}
	if !regexp.MustCompile(`fetch-depth: 0`).MatchString(verify) {
		t.Error("job verify must check out with fetch-depth: 0")
	}

	// tests reuses ci.yml, and the later jobs wait on it.
	if !regexp.MustCompile(`(?m)^    uses: \./\.github/workflows/ci\.yml\s*$`).MatchString(jobBlock(t, wf, "tests")) {
		t.Error("job tests must be `uses: ./.github/workflows/ci.yml`")
	}
	build, release := jobBlock(t, wf, "build"), jobBlock(t, wf, "release")
	for name, job := range map[string]string{"tests": jobBlock(t, wf, "tests"), "build": build, "release": release} {
		if !strings.Contains(job, "verify") {
			t.Errorf("job %s must need verify", name)
		}
	}
	for _, need := range []string{"verify", "tests", "build"} {
		if !regexp.MustCompile(`(?m)^    needs:.*\b` + need + `\b`).MatchString(release) {
			t.Errorf("job release must need %s", need)
		}
	}

	// build: the script, the smoke test, the artifact (also on a dry run).
	for _, want := range []string{"scripts/release/build.sh", "sha256sum -c SHA256SUMS", `/agent-box" version`, "actions/upload-artifact@"} {
		if !strings.Contains(build, want) {
			t.Errorf("job build lacks %q", want)
		}
	}
	if regexp.MustCompile(`(?m)^    if:`).MatchString(build) {
		t.Error("job build must run on a dry run too")
	}
	if !strings.Contains(build, "v0.0.0-dryrun") {
		t.Error("job build must fall back to v0.0.0-dryrun when there is no tag")
	}

	// release: push only, draft only, notes with the status block.
	if !regexp.MustCompile(`(?m)^    if: github\.event_name == 'push'\s*$`).MatchString(release) {
		t.Error("job release must be guarded by `if: github.event_name == 'push'`")
	}
	create := stepBlock(t, release, "gh release create")
	if !strings.Contains(create, "--draft") {
		t.Error("`gh release create` must pass --draft")
	}
	if !strings.Contains(create, "--verify-tag") {
		t.Error("`gh release create` must pass --verify-tag (the tag already exists; never create one)")
	}
	if strings.Count(wf, "gh release create") != 1 || strings.Contains(wf, "gh release edit") || strings.Contains(wf, "--draft=false") {
		t.Error("release.yml must create exactly one release, as a draft, and never publish one")
	}
	for _, want := range []string{
		"go run ./tools/release-status notes",
		"git log --format='- %s'",
		"git describe --tags --abbrev=0",
		"releases.json",
		"jq -s .",
		"GH_TOKEN: ${{ github.token }}",
		"$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("job release lacks %q", want)
		}
	}
	if strings.Contains(release, "--paginate") && strings.Contains(release, "--slurp") {
		t.Error("--slurp nests the pages ([[..],[..]]); flatten with --jq '.[]' | jq -s . instead")
	}

	// Provenance: only for a public repository, with a notice otherwise.
	att := stepBlock(t, release, "actions/attest-build-provenance@")
	if !strings.Contains(att, "if: github.event.repository.visibility == 'public'") {
		t.Error("the attest step must be guarded by `if: github.event.repository.visibility == 'public'`")
	}
	if !strings.Contains(att, "subject-path:") || !strings.Contains(att, "culibox_*") {
		t.Error("the attest step must take the culibox_* tarballs as subject-path")
	}
	skip := stepBlock(t, release, "attestations skipped: repository is private")
	if !strings.Contains(skip, "if: github.event.repository.visibility != 'public'") || !strings.Contains(skip, "GITHUB_STEP_SUMMARY") {
		t.Error("the private-repository notice must be guarded by `visibility != 'public'` and write to GITHUB_STEP_SUMMARY")
	}

	// Nothing here publishes: no job but release may call gh, and every checkout
	// is read-only (the supply-chain guard checks persist-credentials).
	for _, job := range []string{"verify", "tests", "build"} {
		if strings.Contains(jobBlock(t, wf, job), "gh release") {
			t.Errorf("job %s must not touch releases", job)
		}
	}

	// A tag name or input reaches a script only through env:.
	for _, l := range inlineExpr(wf) {
		t.Errorf("a ${{ }} expression is interpolated inside a run script (use env:): %q", l)
	}
}

// The expression check is itself checked: a bad workflow must be reported.
func TestInlineExprCatchesInterpolation(t *testing.T) {
	bad := "    steps:\n      - run: echo ${{ github.ref_name }}\n      - name: x\n        run: |\n          echo hi\n          echo ${{ github.ref_name }}\n"
	if got := inlineExpr(bad); len(got) != 2 {
		t.Errorf("inlineExpr found %v, want both interpolations", got)
	}
	ok := "    steps:\n      - name: x\n        env:\n          TAG: ${{ github.ref_name }}\n        run: |\n          echo \"$TAG\"\n"
	if got := inlineExpr(ok); len(got) != 0 {
		t.Errorf("inlineExpr flagged an env: use: %v", got)
	}
}
