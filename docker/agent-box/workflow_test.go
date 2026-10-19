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
	// build and merge are the two jobs that push.
	if n := strings.Count(wf, "packages: write"); n != 2 {
		t.Errorf("packages: write appears %d times, want 2 (build, merge)", n)
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
