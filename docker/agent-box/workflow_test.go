package boximage

import (
	"os"
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
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q", want)
		}
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
