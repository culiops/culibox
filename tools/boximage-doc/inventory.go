package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// toolInfo describes one ARG <KEY>_VERSION pin. The version comes from the
// Dockerfile; this is what the Dockerfile says only in comments.
type toolInfo struct {
	Name   string // what a user types or installs
	Kind   string // "tool" or "MCP server"
	Source string // where the build downloads it from
	Verify string // how the build checks the download
}

const (
	sha256Arch = "sha256 per arch, pinned"
	npmPin     = "version pin only: npm checks the registry's integrity, no hash is pinned here, and its dependencies resolve at build time"
)

// tools is keyed by the ARG name without _VERSION. TestEveryVersionArgIsDescribed
// keeps it equal to the Dockerfile's ARGs, both ways.
var tools = map[string]toolInfo{
	"ABDUCO":             {"abduco", "tool", "github.com/martanne/abduco release tarball", "sha256, pinned; built from source in a separate stage, only the binary is copied"},
	"AWSCLI":             {"aws (AWS CLI v2)", "tool", "awscli.amazonaws.com", sha256Arch},
	"TERRAFORM":          {"terraform", "tool", "releases.hashicorp.com", sha256Arch + ", taken from HashiCorp's signed SHA256SUMS"},
	"GO":                 {"go", "tool", "go.dev/dl", sha256Arch},
	"PLAYWRIGHT_MCP":     {"@playwright/mcp (with Debian chromium)", "MCP server", "npm registry", npmPin},
	"KUBECTL":            {"kubectl", "tool", "dl.k8s.io", sha256Arch},
	"HELM":               {"helm", "tool", "get.helm.sh", sha256Arch},
	"KUBECONFORM":        {"kubeconform", "tool", "github.com/yannh/kubeconform releases", sha256Arch},
	"STERN":              {"stern", "tool", "github.com/stern/stern releases", sha256Arch},
	"YQ":                 {"yq", "tool", "github.com/mikefarah/yq releases", sha256Arch},
	"SHELLCHECK":         {"shellcheck", "tool", "github.com/koalaman/shellcheck releases", sha256Arch + " (GitHub asset digest)"},
	"JSONNET":            {"jsonnet, jsonnetfmt", "tool", "github.com/google/go-jsonnet releases", sha256Arch},
	"DUCKDB":             {"duckdb", "tool", "github.com/duckdb/duckdb releases", sha256Arch + " (GitHub asset digest)"},
	"S5CMD":              {"s5cmd", "tool", "github.com/peak/s5cmd releases", sha256Arch},
	"TFLINT":             {"tflint", "tool", "github.com/terraform-linters/tflint releases", sha256Arch},
	"TFLINT_RULESET_AWS": {"tflint-ruleset-aws", "tool", "github.com/terraform-linters/tflint-ruleset-aws releases", sha256Arch},
	"TFENV":              {"tfenv", "tool", "github.com/tfutils/tfenv source tarball", "sha256, pinned; `tfenv install` checks HashiCorp's signature with a fingerprint-pinned key"},
	"GH":                 {"gh", "tool", "github.com/cli/cli releases", sha256Arch},
	"GRAFANA_MCP":        {"mcp-grafana", "MCP server", "github.com/grafana/mcp-grafana releases", sha256Arch},
	"SENTRY_MCP":         {"@sentry/mcp-server", "MCP server", "npm registry", npmPin},
	"MCP_ATLASSIAN":      {"mcp-atlassian", "MCP server", "PyPI", "pip hash lock per arch (--require-hashes, wheels only)"},
	"CLAUDE_CODE":        {"Claude Code (@anthropic-ai/claude-code)", "tool", "npm registry", npmPin},
}

// instructions joins backslash-continued lines into one instruction each and
// drops comment lines (the same rules as docker/culibox's tests).
func instructions(dockerfile []byte) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(string(dockerfile), "\n") {
		trimmed := strings.TrimSpace(line)
		if cur.Len() == 0 && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			cur.WriteString(strings.TrimSuffix(trimmed, "\\"))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var (
	fromRe    = regexp.MustCompile(`^FROM (\S+)@sha256:([0-9a-f]{64})`)
	versionRe = regexp.MustCompile(`^ARG ([A-Z0-9_]+)_VERSION=(\S+)$`)
)

type pin struct{ key, version string }

type inventory struct {
	baseImage, baseDigest string
	pins                  []pin
	apt                   []string
	mcp                   []string
}

func parse(dockerfile []byte) (inventory, error) {
	var inv inventory
	ins := instructions(dockerfile)
	final := 0
	for i, in := range ins {
		if m := fromRe.FindStringSubmatch(in); m != nil {
			inv.baseImage, inv.baseDigest, final = m[1], m[2], i
		}
		if m := versionRe.FindStringSubmatch(in); m != nil {
			inv.pins = append(inv.pins, pin{m[1], m[2]})
		}
	}
	if inv.baseDigest == "" {
		return inv, errors.New("no FROM pinned by sha256 digest")
	}
	for _, in := range ins[final:] {
		if strings.HasPrefix(in, "RUN ") && strings.Contains(in, "apt-get install") {
			pkgs := aptPackages(in)
			if len(pkgs) == 0 {
				return inv, fmt.Errorf("cannot read the packages of %.80q: list them after --no-install-recommends", in)
			}
			inv.apt = append(inv.apt, pkgs...)
		}
		if strings.Contains(in, "printf") && strings.Contains(in, "/etc/agent-box/mcp.json") {
			names, err := mcpServers(in)
			if err != nil {
				return inv, err
			}
			inv.mcp = names
		}
	}
	sort.Strings(inv.apt)
	inv.apt = slices.Compact(inv.apt)
	return inv, nil
}

// aptPackages returns the words after --no-install-recommends, up to the next &&.
func aptPackages(run string) []string {
	var out []string
	words := strings.Fields(run)
	for i, w := range words {
		if w != "--no-install-recommends" {
			continue
		}
		for _, p := range words[i+1:] {
			if p == "&&" {
				break
			}
			out = append(out, p)
		}
	}
	return out
}

func mcpServers(printf string) ([]string, error) {
	i, j := strings.Index(printf, "'{"), strings.LastIndex(printf, "}'")
	if i < 0 || j < i {
		return nil, errors.New("no quoted JSON in the mcp.json printf")
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(printf[i+1:j+1]), &doc); err != nil {
		return nil, fmt.Errorf("mcp.json payload: %w", err)
	}
	var names []string
	for k := range doc.MCPServers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// Render returns docs/BOX-IMAGE.md for the given Dockerfile.
func Render(dockerfile []byte) (string, error) {
	inv, err := parse(dockerfile)
	if err != nil {
		return "", err
	}
	var missing []string
	for _, p := range inv.pins {
		if _, ok := tools[p.key]; !ok {
			missing = append(missing, p.key)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("no tools entry for ARG %s_VERSION: describe it in tools/boximage-doc/inventory.go", strings.Join(missing, "_VERSION, ARG "))
	}

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# What is in the box image\n\n")
	w("<!-- Generated by `go run ./tools/boximage-doc -w` from docker/culibox/Dockerfile. Do not edit by hand. -->\n\n")
	w("Every boxed session runs in this image (`ghcr.io/culiops/culibox`). This page lists what it ships, at which\n")
	w("version, where each piece comes from and how the build checks it. It is generated from the Dockerfile, and a\n")
	w("test fails when the two drift apart.\n\n")

	w("## Base image\n\n")
	w("`%s`, pinned by digest `sha256:%s`.\n", inv.baseImage, inv.baseDigest)
	w("It carries Node.js, npm and the Debian packages of the base itself, which change only when this digest is\n")
	w("bumped.\n\n")

	w("## Pinned tools and MCP servers\n\n")
	w("| Tool | Version | Kind | Source | Checked by |\n|---|---|---|---|---|\n")
	for _, p := range inv.pins {
		t := tools[p.key]
		w("| %s | %s | %s | %s | %s |\n", t.Name, p.version, t.Kind, t.Source, t.Verify)
	}
	w("\n")

	w("## Debian packages\n\n")
	w("The packages the Dockerfile installs by name. Their dependencies (Chromium pulls in many libraries) and the\n")
	w("base image's own packages are not listed here; the scans below cover them.\n\n")
	w("Installed with apt from Debian's signed bookworm and bookworm-security archives. The archive is live, not a\n")
	w("snapshot, so each package is at the version Debian served when its image layer was last built. Layers are\n")
	w("cached between builds, so a Debian fix (Chromium's, for one) reaches the image when that layer is rebuilt: a\n")
	w("base digest bump rebuilds every layer.\n\n")
	for i, p := range inv.apt {
		if i > 0 {
			w(", ")
		}
		w("`%s`", p)
	}
	w("\n\n")

	w("## MCP servers the image declares\n\n")
	w("Declared for every session in `/etc/agent-box/mcp.json`:\n\n| Server |\n|---|\n")
	for _, m := range inv.mcp {
		w("| `%s` |\n", m)
	}
	w("\nThe other MCP servers in the table above need a URL or a token, so a session declares one only when it binds\n")
	w("a credential for it.\n\n")

	w("## Services\n\nNone. The image runs no daemon: a box runs `sleep infinity` and the processes a session starts.\n\n")

	w("## Go and the race detector\n\n")
	w("`gcc` and `libc6-dev` are in the image so the race detector works, and `CGO_ENABLED=0` stays the default so\n")
	w("Go builds stay static, as they were with no compiler. Ask for cgo when you need it:\n\n")
	w("```\nCGO_ENABLED=1 go test -race ./...\n```\n\n")

	w("## Vulnerability scans\n\n")
	w("- Every image build scans both architectures with a pinned Grype and puts the findings in the box-image\n")
	w("  workflow's job summary.\n")
	w("- A weekly workflow (box-image-scan) scans the `base` image and the one the latest release pins, and keeps\n")
	w("  one issue open, titled \"Box image: vulnerabilities with a fix\", while a HIGH or CRITICAL finding has a fix.\n")
	w("- Accepted findings are listed in `scripts/ci/image-scan-accepted.txt`, each with a date and a reason, and\n")
	w("  stop counting as accepted 180 days after that date.\n\n")
	w("Fail policy: report only for now. Once a first baseline has been reviewed, an image build will fail on a\n")
	w("CRITICAL finding that has a fix and is not accepted, and publishing will wait for the scan.\n")
	return b.String(), nil
}
