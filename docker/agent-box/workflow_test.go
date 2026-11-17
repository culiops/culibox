package boximage

import (
	"encoding/json"
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
	if next := regexp.MustCompile(`\n  [A-Za-z_][\w-]*:[ \t]*(#[^\n]*)?\n`).FindStringIndex(block[1:]); next != nil {
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
	n        int
	text     string
	job      string
	inPerms  bool
	inScript bool // inside a `run: |` style block scalar: shell text, not YAML structure
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
	wfWith        = regexp.MustCompile(`^\s*with:\s*(?:#.*)?$`)
	wfBlockScalar = regexp.MustCompile(`:\s*[|>][-+0-9]*\s*$`)
	// A block scalar opener on any line, a list item (`- |`) included.
	wfBlockOpen = regexp.MustCompile(`(?::|^\s*-)\s*[|>][-+0-9]*\s*$`)
	// The only line shapes outside a script: `key:`, `key: value`, `- item`.
	wfKeyLine  = regexp.MustCompile(`^\s*(?:-\s+)*[\w.-]+:(?:\s|$)`)
	wfItemLine = regexp.MustCompile(`^\s*-(?:\s|$)`)
	// A character that ends a YAML line, besides LF: it can start a new key in the
	// middle of what the guard reads as one line (a run script included).
	wfOddBreak = regexp.MustCompile("[\r\u0085\u2028\u2029]")

	// Forms the line-based reader cannot follow. Each one could hide a `uses:`, a
	// trigger or a write grant from the rules above, so the guard refuses the form
	// instead of passing it (fail closed).
	wfQuotedKey  = regexp.MustCompile(`^\s*(?:-\s+)?["'][^"']*["']\s*:(?:\s|$)`)
	wfSpaceColon = regexp.MustCompile(`^\s*(?:-\s+)?[\w.-]+\s+:(?:\s|$)`)
	wfComplexKey = regexp.MustCompile(`^\s*\?(?:\s|$)`)
	wfAnchor     = regexp.MustCompile(`(?:^|[\s\[{,:])&[A-Za-z_][\w-]*(?:\s|$|[,\]}])`)
	wfAlias      = regexp.MustCompile(`(?:^|[\s\[{,:])\*[A-Za-z_][\w-]*(?:\s|$|[,\]}])`)
	wfTag        = regexp.MustCompile(`(?:^|[\s\[{,:])!(?:[!<\w-]|\s|$)`)
	wfExprSpan   = regexp.MustCompile(`\$\{\{.*?\}\}`)
	// The one flow form the guard reads: `key: [a, "b"]` on one line, items plain or
	// quoted scalars (so no nested collection, pair, alias, anchor or tag in it).
	wfFlowList  = regexp.MustCompile(`^\s*[\w.-]+:\s*\[\s*(?:` + wfFlowItem + `(?:\s*,\s*` + wfFlowItem + `)*)?\s*\]\s*$`)
	wfDocMarker = regexp.MustCompile(`^(?:---|\.\.\.)(?:\s|$)`)
	wfTopKey    = regexp.MustCompile(`^([\w-]+):`)
	// A quote that opens a scalar sits where a value starts: after `key:`, a dash, `[` or `,`.
	wfOpenQuote    = regexp.MustCompile(`(?:^\s*(?:-\s+)*(?:[\w.-]+:\s*)?|[\[,]\s*)["']`)
	wfDashSpaces   = regexp.MustCompile(`^\s*-\s{2,}\S`)
	wfMergeKey     = regexp.MustCompile(`^\s*<<\s*:`)
	wfSingleRunKey = regexp.MustCompile(`^\s*(?:-\s+)?run:`)
)

// wfReplaceQuoted swaps every closed quoted span in s for repl. A span runs from a
// quote to its close: in single quotes a doubled quote is an escaped quote, in
// double quotes a backslash escapes the next character. A quote that never closes
// on the line is left in place (the open-quote rule reads it). Pairing by a regexp
// cannot do this: it takes the shortest match, and so reads `'a”` as closed.
func wfReplaceQuoted(s, repl string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		q := s[i]
		if q != '"' && q != '\'' {
			b.WriteByte(q)
			i++
			continue
		}
		end := -1
		for j := i + 1; j < len(s); j++ {
			if q == '"' && s[j] == '\\' {
				j++
			} else if s[j] == q {
				if q == '\'' && j+1 < len(s) && s[j+1] == q {
					j++
					continue
				}
				end = j
				break
			}
		}
		if end < 0 {
			b.WriteByte(q)
			i++
			continue
		}
		b.WriteString(repl)
		i = end + 1
	}
	return b.String()
}

func wfIndent(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// parseWorkflow drops full-line comments (keeping each line's original number)
// and tags every line with its job and permissions-block position. Layout is
// the repo's: jobs at 2 spaces, a job's `permissions:` at 4, its scopes at 6.
func parseWorkflow(content string) []wfLine {
	var out []wfLine
	inJobs, job, inPerms := false, "", false
	scriptIndent := -1 // >= 0 while inside a block scalar opened at this indent
	for i, raw := range strings.Split(content, "\n") {
		if wfFullComment.MatchString(raw) {
			continue
		}
		inScript := false
		if strings.TrimSpace(raw) != "" {
			ind := wfIndent(raw)
			if scriptIndent >= 0 && ind > scriptIndent {
				inScript = true
			} else {
				scriptIndent = -1
				if wfBlockScalar.MatchString(wfTrailing.ReplaceAllString(raw, "")) {
					scriptIndent = ind
					if strings.HasPrefix(strings.TrimSpace(raw), "- ") {
						scriptIndent = ind + 2
					}
				}
			}
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
		out = append(out, wfLine{n: i + 1, text: raw, job: job, inPerms: inPerms, inScript: inScript})
	}
	return out
}

const wfFlowItem = `(?:[A-Za-z0-9_./@+-][A-Za-z0-9_./@*+-]*|"[^"\\]*"|'[^']*')`

var (
	wfOn         = regexp.MustCompile(`^["']?on["']?:[ \t]*(.*)$`)
	wfOnKey      = regexp.MustCompile(`^["']?([\w-]+)["']?\s*:`)
	wfOnItem     = regexp.MustCompile(`^-[ \t]+["']?([\w-]+)["']?\s*$`)
	wfOnAllowed  = map[string]bool{"push": true, "workflow_dispatch": true, "workflow_call": true, "schedule": true}
	wfOnAllowMsg = "push, workflow_dispatch, workflow_call, schedule"
)

// workflowTriggers returns the event names under a workflow's top-level `on:`
// in any of its three forms: a block of keys (or a `- item` list), a flow
// list/map (`on: [push, x]`) or a scalar (`on: push`). ok is false when there is
// no `on:` at all. misaligned is true when a line under a block `on:` sits left of
// the first child (not valid YAML, and the reader would skip it).
func workflowTriggers(lines []wfLine) (names []string, ok, misaligned bool) {
	for i, l := range lines {
		m := wfOn.FindStringSubmatch(l.text)
		if m == nil {
			continue
		}
		rest := strings.TrimSpace(wfTrailing.ReplaceAllString(" "+m[1], ""))
		switch {
		case rest == "":
			base := -1
			for _, c := range lines[i+1:] {
				t := strings.TrimSpace(c.text)
				if t == "" {
					continue
				}
				ind := wfIndent(c.text)
				if ind == 0 {
					break
				}
				if base < 0 {
					base = ind
				}
				if ind < base {
					misaligned = true
				}
				if ind != base {
					continue
				}
				if k := wfOnKey.FindStringSubmatch(t); k != nil {
					names = append(names, k[1])
				} else if k := wfOnItem.FindStringSubmatch(t); k != nil {
					names = append(names, k[1])
				}
			}
		case rest[0] == '{':
			names = flowMapKeys(rest)
		case rest[0] == '[':
			for _, it := range strings.Split(strings.Trim(rest, "[] \t"), ",") {
				if t := strings.Trim(strings.TrimSpace(it), `"'`); t != "" {
					names = append(names, t)
				}
			}
		default:
			names = append(names, strings.Trim(rest, `"'`))
		}
		return names, true, misaligned
	}
	return nil, false, false
}

// flowMapKeys returns the top-level keys of a one-line flow map `{a: 1, b: {c: 2}}`.
func flowMapKeys(s string) []string {
	var keys []string
	depth, cur, inVal := 0, "", false
	for _, r := range s {
		switch {
		case r == '{' || r == '[':
			depth++
		case r == '}' || r == ']':
			depth--
		case r == ',' && depth == 1:
			cur, inVal = "", false
		case r == ':' && depth == 1 && !inVal:
			if k := strings.Trim(strings.TrimSpace(cur), `"'`); k != "" {
				keys = append(keys, k)
			}
			inVal = true
		case depth == 1 && !inVal:
			cur += string(r)
		}
	}
	return keys
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

	if trig, ok, misaligned := workflowTriggers(lines); !ok {
		add(0, "no top-level `on:` found")
	} else {
		if misaligned {
			add(0, "inconsistent indentation under `on:`: a trigger could be hidden from the guard")
		}
		if len(trig) == 0 {
			add(0, "no trigger found under `on:` (an empty or unreadable `on:` must not pass)")
		}
		for _, tr := range trig {
			if !wfOnAllowed[tr] {
				add(0, "trigger %q is not allowed (only %s): a fork or an outsider must not be able to start a job here", tr, wfOnAllowMsg)
			}
		}
	}

	// Whole file, run scripts included: a lone CR, NEL, LS or PS is a line break to a
	// YAML parser, so the text after one in a script can be a `permissions:` line.
	for n, raw := range strings.Split(content, "\n") {
		if wfOddBreak.MatchString(raw) {
			add(n+1, "line break other than LF (carriage return, U+0085, U+2028 or U+2029) anywhere in the file, a run script included: the guard reads LF line ends only")
		}
	}

	topKeys := map[string]int{}
	for i, l := range lines {
		text := strings.TrimSpace(l.text)
		if text == "" {
			continue
		}
		if !l.inScript {
			if lead := l.text[:len(l.text)-len(strings.TrimLeft(l.text, " \t"))]; strings.Contains(lead, "\t") {
				add(l.n, "tab in indentation is not valid YAML and is not read by the guard: %q", text)
			}
			if wfDocMarker.MatchString(l.text) {
				add(l.n, "document marker (`---` or `...`): a workflow is one document, the guard reads only that: %q", text)
			}
			if wfIndent(l.text) == 0 {
				if m := wfTopKey.FindStringSubmatch(l.text); m != nil {
					if first, dup := topKeys[m[1]]; dup {
						add(l.n, "duplicate top-level key %q (first at line %d): the guard reads one of them: %q", m[1], first, text)
					}
					topKeys[m[1]] = l.n
				}
			}
			code := wfTrailing.ReplaceAllString(l.text, "")
			if !wfKeyLine.MatchString(code) && !wfItemLine.MatchString(code) {
				add(l.n, "line is not a `key:`, `key: value` or `- item` (a continuation line or a multi-line scalar could carry a value the guard cannot read): %q", text)
			}
			// bare: expressions and quoted text taken out, then the comment, so a brace
			// or an exclamation mark inside text is not structure.
			bare := wfTrailing.ReplaceAllString(wfReplaceQuoted(wfExprSpan.ReplaceAllString(l.text, "x"), `""`), "")
			// A quote still open once expressions, closed spans and the comment are gone
			// runs to a later line, where a `#` line is string text, not a comment.
			unquoted := wfTrailing.ReplaceAllString(wfReplaceQuoted(wfExprSpan.ReplaceAllString(l.text, "x"), "x"), "")
			if wfOpenQuote.MatchString(unquoted) {
				add(l.n, "multi-line quoted scalar (a quote is left open on this line): the guard cannot tell what is a comment inside it: %q", text)
			}
			if wfDashSpaces.MatchString(code) {
				add(l.n, "more than one space after a list dash: the guard reads the key column as dash+2: %q", text)
			}
			if wfBlockOpen.MatchString(bare) && !wfSingleRunKey.MatchString(code) {
				add(l.n, "block scalar (`|` or `>`) is allowed only on a `run:` key: %q", text)
			}
			if !wfSingleRunKey.MatchString(code) {
				if strings.Contains(bare, "{") {
					add(l.n, "flow map (`{...}`) is not supported by the guard: write a block mapping: %q", text)
				}
				if (strings.Contains(bare, "[") || strings.Contains(bare, "]")) && !wfFlowList.MatchString(code) {
					add(l.n, "flow list that is not a one-line `key: [a, b]` of plain or quoted scalars: write a block list: %q", text)
				}
				if wfTag.MatchString(bare) {
					add(l.n, "YAML tag (`!`) is not supported by the guard: %q", text)
				}
				if wfAnchor.MatchString(bare) {
					add(l.n, "YAML anchor (`&name`) is not supported by the guard: repeat the text instead: %q", text)
				}
				if wfAlias.MatchString(bare) {
					add(l.n, "YAML alias (`*name`) is not supported by the guard: repeat the text instead: %q", text)
				}
			}
			switch {
			case wfQuotedKey.MatchString(code):
				add(l.n, "quoted key: write workflow keys unquoted, the guard reads plain text: %q", text)
			case wfSpaceColon.MatchString(code):
				add(l.n, "space before the colon of a key: the guard reads `key:` only: %q", text)
			case wfComplexKey.MatchString(code):
				add(l.n, "complex key (`? key`) is not supported by the guard: %q", text)
			case wfMergeKey.MatchString(code):
				add(l.n, "merge key (`<<:`) is not supported by the guard: %q", text)
			}
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
				if wfPersist.MatchString(lines[k].text) && persistUnderWith(lines, k, dash) {
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

// persistUnderWith reports whether the `persist-credentials` line k is a direct
// child of the step's own `with:` (a key of the step, two columns right of its
// dash), not of an `env:`, a script or some other mapping.
func persistUnderWith(lines []wfLine, k, dash int) bool {
	for j := k - 1; j >= 0; j-- {
		if strings.TrimSpace(lines[j].text) == "" || wfIndent(lines[j].text) >= wfIndent(lines[k].text) {
			continue
		}
		return wfWith.MatchString(lines[j].text) && wfIndent(lines[j].text) == dash+2
	}
	return false
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
	// Lists of scalars on one line, and brackets or braces in quoted text, are fine.
	flowOK := top + head + checkout + "    needs: [verify, tests]\n    strategy:\n      matrix:\n        os: [ubuntu-24.04, \"ubuntu-24.04-arm\"]\n" +
		"      - name: \"a {b} [c] !d\"\n        if: ${{ !cancelled() && github.event_name == 'push' }}\n        run: if [ -n \"$x\" ]; then echo \"{}\"; fi\n"
	if bad := checkWorkflowSupplyChain("good.yml", flowOK); len(bad) != 0 {
		t.Fatalf("one-line scalar lists and quoted brackets reported: %v", bad)
	}
	// A run: block scalar may carry any chomping or indent indicator.
	for _, ind := range []string{"|", "|-", "|+", ">", ">-", "|2", "|2-", "|-2", ">+1"} {
		wf := top + head + checkout + "      - run: " + ind + "\n          echo a\n          x: y\n"
		if bad := checkWorkflowSupplyChain("good.yml", wf); len(bad) != 0 {
			t.Errorf("run: %s script reported: %v", ind, bad)
		}
	}
	// Apostrophes and quotes that do not open a scalar are plain text.
	quotesOK := top + head + checkout + "      - name: don't \"stop\" # it's fine\n        if: github.event_name == 'push'\n        run: echo \"it's\"\n    env:\n      A: \"x # y\"\n      B: 'it''s'\n"
	if bad := checkWorkflowSupplyChain("good.yml", quotesOK); len(bad) != 0 {
		t.Fatalf("quotes that open no scalar reported: %v", bad)
	}
	// Escapes inside quotes, a `#` that is not a comment, and quotes inside an
	// expression are all plain text.
	escapesOK := top + head + checkout + "      - name: Don't stop\n        run: echo a\n    env:\n      U: https://example.com/a#frag\n" +
		"      V: ${{ format('{0}', 'x') }}\n      W: 'it''s # x'\n      X: \"a\\\"b # c\"\n      Y: \"${{ github.ref }}\"\n"
	if bad := checkWorkflowSupplyChain("good.yml", escapesOK); len(bad) != 0 {
		t.Fatalf("quote escapes reported: %v", bad)
	}
	// Shell text and quoted values in a script are not YAML structure.
	scripted := top + head + checkout + "      - name: s\n        run: |\n          echo \"a: b\" && ls *.go\n          case x in *) : ;; esac\n          x=\"uses :\"\n"
	if bad := checkWorkflowSupplyChain("good.yml", scripted); len(bad) != 0 {
		t.Fatalf("shell text in a run script reported: %v", bad)
	}
	for name, on := range map[string]string{
		"flow list":   "on: [push, workflow_dispatch]  # why\n",
		"quoted flow": "on: [\"push\", 'workflow_call', schedule]\n",
		"scalar":      "on: push\n",
		"item list":   "on:\n  - push\n  - workflow_call\n",
		"block":       "on:\n  schedule:\n    - cron: \"17 2 * * 1\"\n  workflow_dispatch:\n",
	} {
		wf := "name: x\n" + on + "permissions:\n  contents: read\njobs:\n" + head + checkout
		if bad := checkWorkflowSupplyChain("good.yml", wf); len(bad) != 0 {
			t.Errorf("good triggers (%s) reported: %v", name, bad)
		}
	}

	for name, tc := range map[string]struct{ wf, want string }{
		"unpinned tag":         {top + head + "      - uses: actions/checkout@v4 # v4.0.0\n        with:\n          persist-credentials: false\n", "not pinned"},
		"no version":           {top + head + "      - uses: actions/checkout@" + sha + "\n        with:\n          persist-credentials: false\n", "not pinned"},
		"local in steps":       {top + head + "      - uses: ./.github/workflows/other.yml\n", "not pinned"},
		"pull_request":         {strings.Replace(top, "  push:", "  pull_request:", 1) + head + checkout, "pull_request"},
		"workflow_run":         {strings.Replace(top, "  push:\n    branches: [main]\n", "  push:\n  workflow_run:\n    workflows: [ci]\n", 1) + head + checkout, `trigger "workflow_run"`},
		"issue_comment":        {strings.Replace(top, "  push:\n    branches: [main]\n", "  issue_comment:\n", 1) + head + checkout, `trigger "issue_comment"`},
		"flow issue_comment":   {"name: x\non: [push, issue_comment]\npermissions:\n  contents: read\njobs:\n" + head + checkout, `trigger "issue_comment"`},
		"scalar release":       {"name: x\non: release\npermissions:\n  contents: read\njobs:\n" + head + checkout, `trigger "release"`},
		"flow map target":      {"name: x\non: {push: {branches: [main]}, pull_request_target: {}}\npermissions:\n  contents: read\njobs:\n" + head + checkout, `trigger "pull_request_target"`},
		"no on":                {strings.Replace(top, "on:\n  push:\n    branches: [main]\n", "", 1) + head + checkout, "no top-level `on:`"},
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
		// Fail closed: a form the line-based guard cannot read is a violation, not a pass.
		"quoted uses key":     {top + head + "      - \"uses\": actions/checkout@v4\n", "quoted key"},
		"single-quoted key":   {top + head + "      - 'uses': actions/checkout@v4\n", "quoted key"},
		"quoted trigger key":  {strings.Replace(top, "  push:\n    branches: [main]\n", "  push:\n  \"issue_comment\":\n", 1) + head + checkout, "quoted key"},
		"space before colon":  {top + head + "      - uses : actions/checkout@v4\n", "space before the colon"},
		"complex key":         {top + head + "      ? uses\n      : actions/checkout@v4\n", "complex key"},
		"anchor":              {top + head + checkout + "    env: &shared\n      A: b\n", "anchor"},
		"alias":               {top + head + checkout + "    env: *shared\n", "alias"},
		"list alias":          {top + head + checkout + "      - *step\n", "alias"},
		"merge key":           {top + head + checkout + "    env:\n      <<: *shared\n", "merge key"},
		"persist under env":   {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n        env:\n          persist-credentials: false\n", "persist-credentials"},
		"persist in a script": {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n        with:\n          ref: x\n      - run: |\n          persist-credentials: false\n", "persist-credentials"},
		// Flow collections hide structure from a line reader: only a one-line list of
		// plain or quoted scalars (`branches: [main]`) is accepted.
		"flow map step":          {top + head + "      - {\"uses\": \"actions/checkout@v4\"}\n", "flow map"},
		"flow map plain step":    {top + head + "      - {uses: actions/checkout@v4}\n", "flow map"},
		"flow map on":            {"name: x\non: {push: {}, issue_comment: {}}\npermissions:\n  contents: read\njobs:\n" + head + checkout, "flow map"},
		"on flow map next line":  {"name: x\non:\n  {push: {}, issue_comment: {}}\npermissions:\n  contents: read\njobs:\n" + head + checkout, "flow map"},
		"on flow list next line": {"name: x\non:\n  [push, issue_comment]\npermissions:\n  contents: read\njobs:\n" + head + checkout, "flow list"},
		"multi-line flow list":   {top + head + checkout + "    needs: [\n      a,\n    ]\n", "flow list"},
		"nested flow list":       {top + head + checkout + "    needs: [a, [b]]\n", "flow list"},
		"flow list of pairs":     {top + head + checkout + "    needs: [uses: x]\n", "flow list"},
		"no triggers found":      {"name: x\non:\n  # nothing\npermissions:\n  contents: read\njobs:\n" + head + checkout, "no trigger"},
		"alias in flow list":     {top + head + checkout + "    needs: [*step]\n", "alias"},
		"anchor in flow list":    {top + head + checkout + "    needs: [&step x]\n", "anchor"},
		"anchor after comma":     {top + head + checkout + "    needs: [a,&step x]\n", "anchor"},
		"tag on a quoted key":    {top + head + "      - !!str \"uses\": actions/checkout@v4\n", "YAML tag"},
		"tag on a value":         {top + head + checkout + "    env:\n      A: !!binary x\n", "YAML tag"},
		"tag in a flow list":     {top + head + checkout + "    needs: [!x y]\n", "YAML tag"},
		"persist sibling key":    {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n        with:\n          ref: x\n        env:\n          persist-credentials: false\n", "persist-credentials"},
		// Documents, duplicate keys, tabs and CRs are not forms the line reader models:
		// a second `on:` or a second document could carry a trigger the first one lacks.
		"duplicate top key":      {top + "on:\n  issue_comment:\n" + head + checkout, "duplicate top-level key"},
		"second document":        {top + head + checkout + "---\non: issue_comment\n", "document marker"},
		"document end":           {top + head + checkout + "...\n", "document marker"},
		"on children misaligned": {"name: x\non:\n    push:\n  issue_comment:\npermissions:\n  contents: read\njobs:\n" + head + checkout, "inconsistent indentation under `on:`"},
		"tab in indentation":     {"name: x\non:\n  push:\n\tissue_comment:\npermissions:\n  contents: read\njobs:\n" + head + checkout, "tab"},
		"carriage return":        {strings.Replace(top+head+checkout, "\n", "\r\n", 1), "line break"},
		// Inside an open quote a `#` line is text, not a comment, so lines that look like
		// structure can sit inside a string that a later `#"` line closes.
		"open quote hides persist": {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n        with:\n          ref: \"main\n          persist-credentials: false\n#\"\n", "multi-line quoted scalar"},
		"open quote hides perms":   {"name: \"x\non:\n  push:\npermissions:\n  contents: read\n#\"\njobs:\n" + head + checkout, "multi-line quoted scalar"},
		"open single quote":        {top + head + checkout + "    env:\n      A: 'x\n      B: y\n#'\n", "multi-line quoted scalar"},
		"open quote in a list":     {top + head + checkout + "    needs: [\"a\n", "multi-line quoted scalar"},
		// In single quotes a doubled quote is an escaped quote, not a close, so a value
		// ending in one is still open and the lines below it are string text.
		"escaped quote leaves it open":     {top + head + "      - uses: actions/checkout@" + sha + " # v7.0.1\n        with:\n          ref: 'main''\n          persist-credentials: false\n#'\n", "multi-line quoted scalar"},
		"escaped quote hides top perms":    {"name: 'x''\non:\n  push:\npermissions:\n  contents: read\n#'\njobs:\n" + head + checkout, "multi-line quoted scalar"},
		"escaped quote then double escape": {top + head + checkout + "    env:\n      A: 'a''''b''\n      B: y\n#'\n", "multi-line quoted scalar"},
		"backslash escape leaves it open":  {top + head + checkout + "    env:\n      A: \"a\\\"\n      B: y\n#\"\n", "multi-line quoted scalar"},
		"dash with two spaces":             {top + head + "      -  run: |\n          echo a\n        uses: actions/checkout@v4\n", "after a list dash"},
		// A lone CR, NEL, LS or PS ends a YAML line anywhere, a run script included: the
		// text after it could be a `permissions:` line the guard sees as shell text.
		"CR inside a run script":  {top + head + checkout + "      - run: |\n          echo a\r    permissions:\r      contents: write\n", "line break"},
		"NEL inside a run script": {top + head + checkout + "      - run: |\n          echo a\u0085      contents: write\n", "line break"},
		"LS inside a run script":  {top + head + checkout + "      - run: |\n          echo a\u2028      contents: write\n", "line break"},
		"PS inside a run script":  {top + head + checkout + "      - run: |\n          echo a\u2029      contents: write\n", "line break"},
		// A write value is read from one line only if every line is `key:`, `key: value`
		// or `- item`, and a block scalar belongs to a run: key alone.
		"write on the next line":      {top + head + checkout + "  w:\n    permissions:\n      packages:\n        write\n", "not a `key:`"},
		"top write on the next line":  {strings.Replace(top, "contents: read\n", "contents: read\n  packages:\n    write\n", 1) + head + checkout, "not a `key:`"},
		"write in a folded scalar":    {top + head + checkout + "  w:\n    permissions:\n      packages: >-\n        write\n", "block scalar"},
		"top write in a literal":      {strings.Replace(top, "contents: read\n", "contents: read\n  packages: |\n    write\n", 1) + head + checkout, "block scalar"},
		"block scalar with indicator": {top + head + checkout + "    env:\n      A: |2+\n        x\n", "block scalar"},
		"plain scalar continuation":   {top + head + checkout + "    env:\n      A: x\n        write\n", "not a `key:`"},
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

// workflowNameViolations: GitHub runs both .yml and .yaml, but release.yml and
// ci.yml are looked up by exact name in these tests, so a sibling named .yaml
// would dodge the per-file rules. Only .yml is allowed under .github/workflows.
func workflowNameViolations(names []string) []string {
	var bad []string
	for _, n := range names {
		if filepath.Ext(n) != ".yml" {
			bad = append(bad, fmt.Sprintf("%s: workflow files must be named *.yml (a .yaml sibling of release.yml would dodge its guard)", n))
		}
	}
	return bad
}

// otherWorkflowScopeViolations: the release scopes (contents, id-token,
// attestations) belong to release.yml's release job alone. In any other workflow
// they are a violation whatever the job, so a new file cannot take over the
// release's power. (releaseWriteViolations holds release.yml itself to one grant.)
func otherWorkflowScopeViolations(name, content string) []string {
	var bad []string
	for _, l := range parseWorkflow(content) {
		if m := wfWriteScope.FindStringSubmatch(l.text); m != nil && (m[1] == "contents" || m[1] == "id-token" || m[1] == "attestations") {
			bad = append(bad, fmt.Sprintf("%s:%d: `%s: write` is only for release.yml's release job", name, l.n, m[1]))
		}
	}
	return bad
}

// macosRunnerViolations is the macOS runner rule for ci.yml. It reads the WHOLE
// text, never a job block cut out by jobBlock (a job key with a trailing comment
// or an odd name could otherwise hide inside another job's block): across all
// non-comment lines the word macos (bar the offline verify-script test) may appear
// exactly once, and that line must be `    runs-on: macos-15` in job darwin-test,
// which has exactly one runs-on. Larger runners (macos-15-xlarge) and macos-latest
// are billed or drift, so each is refused.
func macosRunnerViolations(t *testing.T, wf string) []string {
	t.Helper()
	var out []string
	var hits []string
	for _, l := range strings.Split(strings.ReplaceAll(wf, "scripts/macos-verify_test.sh", ""), "\n") {
		code, _, _ := strings.Cut(l, "#")
		if strings.Contains(strings.ToLower(code), "macos") {
			hits = append(hits, strings.TrimRight(code, " \t"))
		}
	}
	if len(hits) != 1 || hits[0] != "    runs-on: macos-15" {
		out = append(out, fmt.Sprintf("ci.yml: the only macOS mention outside comments must be exactly %q (job darwin-test), got %q", "    runs-on: macos-15", hits))
	}
	block := jobBlock(t, wf, "darwin-test")
	if n := strings.Count(block, "runs-on:"); n != 1 {
		out = append(out, fmt.Sprintf("job darwin-test has %d runs-on lines, want 1", n))
	}
	if m := regexp.MustCompile(`(?m)^    runs-on:\s*(\S+)\s*$`).FindStringSubmatch(block); m == nil || m[1] != "macos-15" {
		out = append(out, fmt.Sprintf("job darwin-test must run on macos-15, got %v", m))
	}
	return out
}

// The macOS rule must refuse every way a second macOS (or larger) runner could be
// added: a job whose key has a trailing comment, an underscore or uppercase key,
// a larger runner, macos-latest.
func TestWorkflowMacOSRunnerRule(t *testing.T) {
	b, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	if v := macosRunnerViolations(t, wf); len(v) != 0 {
		t.Fatalf("the real ci.yml violates the rule: %v", v)
	}
	extra := func(key, label string) string {
		return wf + "\n  " + key + "\n    runs-on: " + label + "\n    steps: []\n"
	}
	for name, mutated := range map[string]string{
		"xlarge job with a commented key": extra("big: # why", "macos-15-xlarge"),
		"underscore job key":              extra("_big:", "macos-15-xlarge"),
		"uppercase job key":               extra("Big:", "macos-15-xlarge"),
		"second job on macos-15":          extra("again:", "macos-15"),
		"macos-latest":                    strings.Replace(wf, "runs-on: macos-15", "runs-on: macos-latest", 1),
	} {
		if len(macosRunnerViolations(t, mutated)) == 0 {
			t.Errorf("%s: no violation, the rule is open", name)
		}
	}
}

func TestWorkflowFilesAreNamedYml(t *testing.T) {
	files := workflowFiles(t)
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	for _, msg := range workflowNameViolations(names) {
		t.Error(msg)
	}
	if bad := workflowNameViolations([]string{"ci.yml", "release.yaml"}); len(bad) != 1 {
		t.Errorf("release.yaml must be reported, got %v", bad)
	}
	if bad := workflowNameViolations([]string{"ci.yml", "release.yml", "box-image.yml"}); len(bad) != 0 {
		t.Errorf(".yml names reported: %v", bad)
	}
}

func TestOtherWorkflowsHoldNoReleaseScopes(t *testing.T) {
	for _, f := range workflowFiles(t) {
		if filepath.Base(f) == "release.yml" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range otherWorkflowScopeViolations(filepath.Base(f), string(b)) {
			t.Error(msg)
		}
	}
	job := func(scope string) string {
		return "jobs:\n  x:\n    permissions:\n      " + scope + "\n"
	}
	for _, scope := range []string{"contents: write", `"id-token": write`, "attestations: 'write'"} {
		if bad := otherWorkflowScopeViolations("x.yml", job(scope)); len(bad) != 1 {
			t.Errorf("%s in another workflow must be reported once, got %v", scope, bad)
		}
	}
	for _, scope := range []string{"packages: write", "issues: write", "contents: read"} {
		if bad := otherWorkflowScopeViolations("x.yml", job(scope)); len(bad) != 0 {
			t.Errorf("%s reported: %v", scope, bad)
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

	// Runners: the test matrix is exactly the two Linux runners. The one macOS
	// runner is job darwin-test on macos-15 (#293: the darwin unit tests, free on a
	// public repo); macos-latest would drift, and no other job may use macOS. The
	// other allowed mention is the offline test of the verify script, which runs on Linux.
	for _, v := range macosRunnerViolations(t, wf) {
		t.Error(v)
	}
	if !strings.Contains(jobBlock(t, wf, "darwin-test"), "CGO_ENABLED: \"0\"") {
		t.Error("job darwin-test must set CGO_ENABLED: \"0\" (the shipped darwin binaries are built without cgo)")
	}
	if !strings.Contains(jobBlock(t, wf, "darwin-test"), "run: go test ./...") {
		t.Error(`job darwin-test lacks "run: go test ./..."`)
	}
	// The upgrade test is Linux only: darwin's store is the login Keychain, which a
	// CI runner cannot use unattended (#293). Bringing it back means solving that first.
	if strings.Contains(jobBlock(t, wf, "darwin-test"), "upgrade-test.sh") {
		t.Error("job darwin-test must not run scripts/upgrade-test.sh (the Keychain is not usable on a CI runner)")
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
		"scripts/ci/secret-scan.sh",
		"bash scripts/ci/secret-scan_test.sh",
		// The custom private-identifier rules are tested with the pinned scanner, and
		// the run fails (not skips) when the scanner is missing.
		"GITLEAKS_RULES_TEST_REQUIRE=1 bash scripts/ci/gitleaks-rules_test.sh",
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
	// The web job runs third-party npm code (eslint). A Go cache it saved would be
	// restored by the later jobs, so it must never restore or save one.
	for _, msg := range setupGoCacheViolations("ci.yml (job web)", jobBlock(t, wf, "web")) {
		t.Error(msg)
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
	for _, want := range []string{"github.event_name == 'push'", "ci-push-{0}", "github.run_id"} {
		if !strings.Contains(group[1], want) {
			t.Errorf("concurrency group %q lacks %q (a push run needs its own run-id group)", group[1], want)
		}
	}
	// Every other event: ci-<ref>-<caller>. A release dry run (workflow_dispatch of
	// release.yml, which calls this file) reaches here as a workflow_dispatch on the
	// same ref as a manual ci.yml dispatch, so without the caller in the key the
	// two share a group and cancel each other. release.yml passes caller: release.
	if !strings.Contains(group[1], "format('ci-{0}-{1}', github.ref, inputs.caller)") {
		t.Errorf("concurrency group %q must end in format('ci-{0}-{1}', github.ref, inputs.caller) so a release dry run and a manual dispatch do not share a group", group[1])
	}
	if !regexp.MustCompile(`(?m)^  workflow_call:\s*\n    inputs:\s*\n      caller:\s*\n(      +.*\n)*?        type: string\s*\n(      +.*\n)*?        default: ["']{2}\s*$`).MatchString(wf) {
		t.Error("workflow_call must declare `inputs: caller:` as a string defaulting to the empty string")
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

// scripts/lint.sh runs eslint in CI, with the network. It must install the
// exact tree recorded in a lockfile (every package with an integrity hash) and
// run no install scripts, never `npx eslint@<range>`, which resolves the newest
// match and its dependencies fresh on every run.
func TestLintInstallsPinnedEslint(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lint := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(read("../../scripts/lint.sh"), "")
	if regexp.MustCompile(`\bnpx\b`).MatchString(lint) {
		t.Error("scripts/lint.sh must not use npx (it resolves an unpinned tree each run)")
	}
	if !strings.Contains(lint, "npm ci") || !strings.Contains(lint, "--ignore-scripts") {
		t.Error("scripts/lint.sh must install with `npm ci --ignore-scripts` from the committed lockfile")
	}
	for _, v := range lintLockViolations(read("../../scripts/lint/package.json"), read("../../scripts/lint/package-lock.json")) {
		t.Error(v)
	}
	if bad := lintLockViolations(`{"devDependencies":{"eslint":"^9.39.5"}}`, `{"packages":{}}`); len(bad) == 0 {
		t.Error("a ranged eslint version must be reported")
	}
	if bad := lintLockViolations(`{"devDependencies":{"eslint":"9.39.5"}}`,
		`{"packages":{"":{},"node_modules/eslint":{"version":"9.39.5","resolved":"https://registry.npmjs.org/eslint/-/eslint-9.39.5.tgz"}}}`); len(bad) == 0 {
		t.Error("a lock entry without integrity must be reported")
	}
}

// lintLockViolations checks scripts/lint's package.json and lock: eslint is an
// exact version, the lock resolves it to that version, and every locked package
// carries a resolved URL and a sha512 integrity.
func lintLockViolations(pkgJSON, lockJSON string) []string {
	var bad []string
	var pkg struct {
		Dev map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(pkgJSON), &pkg); err != nil {
		return []string{"package.json: " + err.Error()}
	}
	want := pkg.Dev["eslint"]
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(want) {
		bad = append(bad, fmt.Sprintf("eslint must be pinned to an exact version in package.json, got %q", want))
	}
	var lock struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
			Link      bool   `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(lockJSON), &lock); err != nil {
		return append(bad, "package-lock.json: "+err.Error())
	}
	if e, ok := lock.Packages["node_modules/eslint"]; !ok {
		bad = append(bad, "package-lock.json has no node_modules/eslint")
	} else if e.Version != want {
		bad = append(bad, fmt.Sprintf("the lock resolves eslint to %q, package.json pins %q", e.Version, want))
	}
	for path, p := range lock.Packages {
		if path == "" || p.Link {
			continue
		}
		if !strings.HasPrefix(p.Resolved, "https://registry.npmjs.org/") || !strings.HasPrefix(p.Integrity, "sha512-") {
			bad = append(bad, fmt.Sprintf("lock entry %s lacks an https registry URL or a sha512 integrity", path))
		}
	}
	return bad
}

// The CI install script, the release build and the box image build the same
// abduco: the version and sum in scripts/ci/install-abduco.sh and
// scripts/release/build-abduco.sh must equal the Dockerfile ARGs.
func TestAbducoPinMatchesDockerfile(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	docker := read("Dockerfile")
	for _, script := range []string{"../../scripts/ci/install-abduco.sh", "../../scripts/release/build-abduco.sh"} {
		body, base := read(script), filepath.Base(script)
		for _, name := range []string{"ABDUCO_VERSION", "ABDUCO_SHA256"} {
			s := regexp.MustCompile(`(?m)^` + name + `=["']?([^\s"']+)["']?\s*$`).FindStringSubmatch(body)
			d := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)\s*$`).FindStringSubmatch(docker)
			if s == nil || d == nil {
				t.Errorf("%s: not found in both %s (%v) and the Dockerfile (%v)", name, base, s != nil, d != nil)
				continue
			}
			if s[1] != d[1] {
				t.Errorf("%s: %s has %q, Dockerfile has %q", name, base, s[1], d[1])
			}
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
	// I-1: the build must never restore a Go cache that a ci.yml job (which runs
	// unpinned npm code) may have saved.
	if bad := setupGoCacheViolations("release.yml", wf); len(bad) != 0 {
		for _, m := range bad {
			t.Error(m)
		}
	}
	if strings.Count(wf, "actions/setup-go@") < 2 {
		t.Error("release.yml should have a setup-go step in jobs build and release")
	}
	if strings.Contains(strings.ToLower(wf), "macos") {
		t.Error("release.yml must not mention macos (the macOS runner is ci.yml's job darwin-test)")
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

	// tests reuses ci.yml, and the later jobs wait on it. It says who is calling, so
	// ci.yml's concurrency group differs from a manual ci.yml run on the same ref.
	if !regexp.MustCompile(`(?m)^    uses: \./\.github/workflows/ci\.yml\s*$`).MatchString(jobBlock(t, wf, "tests")) {
		t.Error("job tests must be `uses: ./.github/workflows/ci.yml`")
	}
	if !regexp.MustCompile(`(?m)^    with:\s*\n      caller: release\s*$`).MatchString(jobBlock(t, wf, "tests")) {
		t.Error("job tests must pass `with: caller: release` to ci.yml (its concurrency group keys on it)")
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
	// #317: the pinned abduco is built first and packed; the smoke test runs it and
	// checks its licence and the attached installer.
	for _, want := range []string{"scripts/release/build-abduco.sh", "scripts/release/build.sh", "sha256sum -c SHA256SUMS", `/culibox" version`, `/abduco" -v`, "LICENSE.abduco", `cmp scripts/setup.sh "$DIST/setup.sh"`, "actions/upload-artifact@"} {
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
	if !strings.Contains(att, "subject-path:") || !strings.Contains(att, "culibox_*") || !strings.Contains(att, "setup.sh") {
		t.Error("the attest step must take the culibox_* tarballs and setup.sh as subject-path")
	}
	skip := stepBlock(t, release, "attestations skipped: this repository is not public, build provenance needs a public repository")
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

	// The write-capable token is in the environment of the steps that call gh,
	// and of no other step: repo code (go run, the build scripts) never sees it.
	for _, msg := range ghTokenViolations(release) {
		t.Error(msg)
	}
	if strings.Count(release, "GH_TOKEN: ${{ github.token }}") != 2 {
		t.Error("GH_TOKEN must be set in exactly two steps of job release: the release list and the draft release")
	}
}

// ghTokenViolations checks one job's text: GH_TOKEN may not sit in the job's own
// env: (every step would inherit it), and a step that has it must call gh and
// run nothing from the repository (go run, a script, a tool).
func ghTokenViolations(job string) []string {
	var bad []string
	head, steps, _ := strings.Cut(job, "\n      - ")
	if strings.Contains(head, "GH_TOKEN") {
		bad = append(bad, "GH_TOKEN is set job-wide: scope it to the steps that call gh")
	}
	if steps == "" {
		return bad
	}
	for _, step := range strings.Split("- "+steps, "\n      - ") {
		if !strings.Contains(step, "GH_TOKEN") {
			continue
		}
		first := strings.TrimSpace(strings.SplitN(step, "\n", 2)[0])
		if !regexp.MustCompile(`(?m)(^|[\s:|])gh (api|release) `).MatchString(step) {
			bad = append(bad, fmt.Sprintf("a step holds GH_TOKEN but does not call gh: %q", first))
		}
		for _, repoCode := range []string{"go run", "scripts/", "./tools", "bash ", "make "} {
			if strings.Contains(step, repoCode) {
				bad = append(bad, fmt.Sprintf("a step holds GH_TOKEN and also runs repository code (%s): %q", repoCode, first))
			}
		}
	}
	return bad
}

func TestGHTokenCheckCatchesViolations(t *testing.T) {
	const gh = "      - name: a\n        env:\n          GH_TOKEN: ${{ github.token }}\n        run: gh api x\n"
	const run = "      - name: b\n        run: go run ./tools/x\n"
	if bad := ghTokenViolations("  release:\n    runs-on: x\n    steps:\n" + gh + run); len(bad) != 0 {
		t.Errorf("good job reported: %v", bad)
	}
	for name, job := range map[string]string{
		"job env":        "  release:\n    env:\n      GH_TOKEN: ${{ github.token }}\n    steps:\n" + run,
		"token + go run": "  release:\n    steps:\n      - name: a\n        env:\n          GH_TOKEN: ${{ github.token }}\n        run: |\n          gh api x\n          go run ./tools/x\n",
		"token, no gh":   "  release:\n    steps:\n      - name: a\n        env:\n          GH_TOKEN: ${{ github.token }}\n        run: echo hi\n",
		"token + script": "  release:\n    steps:\n      - name: a\n        env:\n          GH_TOKEN: ${{ github.token }}\n        run: |\n          gh api x\n          scripts/release/build.sh\n",
	} {
		if bad := ghTokenViolations(job); len(bad) == 0 {
			t.Errorf("%s: no violation reported", name)
		}
	}
}

// setupGoCacheViolations reports every actions/setup-go step in wf that does not
// set `cache: false` (setup-go restores and saves the shared Go cache by default).
func setupGoCacheViolations(name, wf string) []string {
	var bad []string
	lines := strings.Split(wf, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "actions/setup-go@") {
			continue
		}
		start := i
		for start > 0 && !strings.HasPrefix(strings.TrimSpace(lines[start]), "- ") {
			start--
		}
		dash := wfIndent(lines[start])
		ok := false
		for k := start + 1; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == "" {
				continue
			}
			if wfIndent(lines[k]) <= dash {
				break
			}
			if regexp.MustCompile(`^\s*cache: false\s*(?:#.*)?$`).MatchString(lines[k]) {
				ok = true
			}
		}
		if !ok {
			bad = append(bad, fmt.Sprintf("%s:%d: setup-go step lacks `cache: false` (a job that runs third-party or release code must not restore or save the shared Go cache)", name, i+1))
		}
	}
	return bad
}

func TestSetupGoCacheCheckCatchesViolations(t *testing.T) {
	const step = "      - uses: actions/setup-go@x # v1.0.0\n        with:\n          go-version-file: go.mod\n"
	if bad := setupGoCacheViolations("a.yml", step); len(bad) != 1 {
		t.Errorf("a setup-go step with no cache setting must be reported once, got %v", bad)
	}
	if bad := setupGoCacheViolations("a.yml", step+"          cache: true\n"); len(bad) != 1 {
		t.Errorf("cache: true must be reported, got %v", bad)
	}
	if bad := setupGoCacheViolations("a.yml", step+"          cache: false\n      - run: x\n"+step); len(bad) != 1 {
		t.Errorf("only the second step lacks cache: false, got %v", bad)
	}
	if bad := setupGoCacheViolations("a.yml", step+"          cache: false # why\n"); len(bad) != 0 {
		t.Errorf("cache: false must pass, got %v", bad)
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

const vulnWorkflowPath = "../../.github/workflows/vuln.yml"

// TestVulnWorkflow pins the govulncheck scan (#319): ci.yml calls vuln.yml, so every
// push to main and every release runs it, and vuln.yml's own weekly schedule catches
// an advisory published while nothing is pushed. The schedule lives in vuln.yml, not
// ci.yml, so the weekly run is one Linux job, not the whole CI with its macOS runner.
func TestVulnWorkflow(t *testing.T) {
	b, err := os.ReadFile(vulnWorkflowPath)
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

	for _, trig := range []string{"schedule:", "workflow_dispatch:", "workflow_call:"} {
		if !regexp.MustCompile(`(?m)^  ` + trig + `\s*$`).MatchString(wf) {
			t.Errorf("vuln.yml lacks the %s trigger", trig)
		}
	}
	// Weekly: a fixed weekday, any minute and hour.
	if !regexp.MustCompile(`(?m)^\s+- cron: "\d{1,2} \d{1,2} \* \* [0-6]"\s*$`).MatchString(wf) {
		t.Error(`vuln.yml needs one weekly cron ("M H * * D")`)
	}
	// ci.yml runs it on push; a push trigger here would run it twice.
	if regexp.MustCompile(`(?m)^  push:`).MatchString(wf) {
		t.Error("vuln.yml must not have its own push trigger (ci.yml calls it)")
	}
	// Installed once, at an exact version: go checks the module against the checksum
	// database, so the pin is the integrity check (@latest would run whatever is
	// newest). Never `go run`, and never with GOOS on the install: a darwin GOOS there
	// builds a tool the Linux runner cannot start.
	install := regexp.MustCompile(`^\s+run: GOBIN="\$RUNNER_TEMP/govulncheck" go install golang\.org/x/vuln/cmd/govulncheck@v\d+\.\d+\.\d+\s*$`)
	installs := 0
	for _, l := range strings.Split(wf, "\n") {
		if !strings.Contains(l, "x/vuln/cmd/govulncheck") {
			continue
		}
		installs++
		if !install.MatchString(l) {
			t.Errorf("vuln.yml must install govulncheck with an exact version into $RUNNER_TEMP/govulncheck: %q", strings.TrimSpace(l))
		}
	}
	if installs != 1 {
		t.Errorf("vuln.yml installs govulncheck %d times, want once", installs)
	}
	// Two scans with that binary, the second with GOOS: darwin: the darwin build calls
	// code the Linux one does not. GOOS must be set on that scan step only.
	scan := regexp.MustCompile(`(?m)^\s+"\$RUNNER_TEMP/govulncheck/govulncheck" \./\.\.\.\s*$`)
	if n := len(scan.FindAllString(wf, -1)); n != 2 {
		t.Errorf("vuln.yml runs %d govulncheck scans, want 2 (linux and darwin)", n)
	}
	steps := strings.Split(wf, "\n      - ")
	darwin := 0
	for _, st := range steps {
		if !regexp.MustCompile(`(?m)^\s+GOOS: darwin\s*$`).MatchString(st) {
			continue
		}
		darwin++
		if !scan.MatchString(st) || strings.Contains(st, "go install") {
			t.Errorf("GOOS: darwin must be set on a scan step only, not on: %q", strings.TrimSpace(st))
		}
	}
	if darwin != 1 {
		t.Errorf("vuln.yml has %d steps with GOOS: darwin, want exactly the darwin scan", darwin)
	}
	if regexp.MustCompile(`(?m)^\s+GOOS:`).MatchString(strings.Split(wf, "\n    steps:\n")[0]) {
		t.Error("vuln.yml must not set GOOS at the workflow or job level (the install would build for it)")
	}
	// Linux runners only (GOOS=darwin needs no Mac; the macOS budget is darwin-test).
	for _, m := range regexp.MustCompile(`(?m)^\s+runs-on:\s*(.+?)\s*$`).FindAllStringSubmatch(wf, -1) {
		if !strings.HasPrefix(m[1], "ubuntu-") {
			t.Errorf("vuln.yml runs on %q; it must use a Linux runner", m[1])
		}
	}
	// The toolchain go.mod names, or a stale go.mod hides behind a newer Go.
	if !strings.Contains(wf, "actions/setup-go@") || !regexp.MustCompile(`(?m)^\s+go-version-file: go\.mod\s*$`).MatchString(wf) {
		t.Error("vuln.yml must set up Go with actions/setup-go and go-version-file: go.mod")
	}
	// It runs a downloaded tool: no Go cache restored into it or saved from it.
	for _, msg := range setupGoCacheViolations("vuln.yml", wf) {
		t.Error(msg)
	}
	if wfWriteAny.MatchString(wf) {
		t.Error("vuln.yml must not hold any write permission")
	}
	// Never github.workflow: under workflow_call it is the caller's name.
	if strings.Contains(wf, "github.workflow") {
		t.Error("vuln.yml must not use github.workflow (it is the caller's name under workflow_call)")
	}

	if !strings.Contains(jobBlock(t, ciCode(t), "vuln"), "uses: ./.github/workflows/vuln.yml") {
		t.Error("ci.yml job vuln must call ./.github/workflows/vuln.yml")
	}
}
