package bundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	// ImageMCPPath is where a box image declares its own MCP servers.
	ImageMCPPath = "/etc/agent-box/mcp.json"
	// maxImageMCPBytes caps the image's file. The probe reads one byte more, so
	// an oversized file is reported rather than silently truncated.
	maxImageMCPBytes = 64 << 10
	probeTimeout     = 60 * time.Second
)

// ImageFacts is what one probe container learned about the box image.
type ImageFacts struct {
	Resolves map[string]bool
	MCP      []byte // the image's mcp.json, at most maxImageMCPBytes+1 bytes
	HasMCP   bool
}

// ImageProbe is an optional extension of Prober. A Prober that also implements
// it keys the bundle on the image ID and contributes the image's own MCP
// servers; any other Prober keeps the plain command check.
type ImageProbe interface {
	ImageID() (string, error)
	Probe(imageID string, cmds []string) (ImageFacts, error)
}

// ImageID resolves the configured image reference to its content ID, so a
// rebuilt or repinned image rebuilds the bundle.
func (p ImageProber) ImageID() (string, error) {
	if p.Docker == "" || p.Image == "" {
		return "", errors.New("no docker binary or box image configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	args := append(append([]string(nil), p.Args...), "image", "inspect", "--format", "{{.Id}}", p.Image)
	out, err := exec.CommandContext(ctx, p.Docker, args...).Output()
	if err != nil {
		return "", fmt.Errorf("docker image inspect %s: %w%s", p.Image, err, stderrHint(err))
	}
	id := strings.TrimSpace(string(out))
	if !strings.HasPrefix(id, "sha256:") {
		return "", fmt.Errorf("docker image inspect %s: unexpected image ID %q", p.Image, id)
	}
	return id, nil
}

// imageMCPLine prints the image's file, base64-encoded, as the FIRST output
// line. Only line 1 is read for it, and every later line derives from user MCP
// command names, so a user config can never forge the image's servers.
const imageMCPLine = "if [ -e " + ImageMCPPath + " ]; then printf 'MCP '; head -c 65537 " + ImageMCPPath +
	" | base64 | tr -d '\\n'; echo; else echo NOMCP; fi\n"

// Probe runs one container from the exact image ID: it prints the image's MCP
// file, then which of cmds resolve. It never pulls and needs no network.
func (p ImageProber) Probe(imageID string, cmds []string) (ImageFacts, error) {
	f := ImageFacts{Resolves: make(map[string]bool, len(cmds))}
	for _, c := range cmds {
		f.Resolves[c] = false
	}
	if p.Docker == "" {
		return f, errors.New("no docker binary configured")
	}
	var sb strings.Builder
	sb.WriteString(imageMCPLine)
	for _, c := range cmds {
		sb.WriteString("if command -v " + shellQuote(c) + " >/dev/null 2>&1; then echo OK " + shellQuote(c) + "; fi\n")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	args := append(append([]string(nil), p.Args...), "run", "--rm", "--pull=never", "--network", "none",
		"--entrypoint", "sh", imageID, "-c", sb.String())
	res, err := exec.CommandContext(ctx, p.Docker, args...).Output()
	if err != nil {
		return f, fmt.Errorf("docker run %s: %w%s", imageID, err, stderrHint(err))
	}
	lines := strings.Split(string(res), "\n")
	switch first := strings.TrimSpace(lines[0]); {
	case first == "NOMCP":
	case first == "MCP" || strings.HasPrefix(first, "MCP "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(first, "MCP")))
		if err != nil {
			return f, fmt.Errorf("decoding %s from the box image: %w", ImageMCPPath, err)
		}
		f.MCP, f.HasMCP = raw, true
	default:
		return f, fmt.Errorf("unexpected first line from the box image probe")
	}
	for _, line := range lines[1:] {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "OK "); ok {
			if _, asked := f.Resolves[name]; asked {
				f.Resolves[name] = true
			}
		}
	}
	return f, nil
}

// stderrHint appends docker's first stderr line ("No such image", ...) to an
// error, which is what makes a failed probe diagnosable from the daemon log.
func stderrHint(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return ""
	}
	line, _, _ := strings.Cut(string(bytes.TrimSpace(ee.Stderr)), "\n")
	if line == "" {
		return ""
	}
	if len(line) > 200 {
		line = line[:200]
	}
	return ": " + line
}

// resolvedProber answers the user filter from facts already probed, so the
// image is still probed once per build.
type resolvedProber map[string]bool

func (r resolvedProber) Resolves(cmds []string) map[string]bool {
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		out[c] = r[c]
	}
	return out
}

// mcpCommands lists the stdio commands of the user's MCP file. A missing or
// invalid file yields none; filterMCP reports it.
func mcpCommands(src mcpSource) []string {
	raw, err := src.raw, src.err
	if err != nil {
		return nil
	}
	var doc struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var cmds []string
	for _, srv := range doc.MCPServers {
		if c, ok := srv["command"].(string); ok && c != "" {
			cmds = append(cmds, c)
		}
	}
	sort.Strings(cmds)
	return cmds
}

// mergeImageMCP adds the servers the box image declares to the filtered user
// servers. An image server replaces a same-named user server, kept or dropped,
// and the replacement is recorded as a drop. The third result is the image's
// servers alone (never nil), written as image-mcp.json.
//
// Image servers skip the user filter on purpose: the image is as trusted as the
// agent-box binary. The filter exists to keep the user's secrets off disk and to
// drop commands missing from the image, and neither applies to the image's own
// declarations.
func mergeImageMCP(user map[string]any, drops []Drop, f ImageFacts) (map[string]any, []Drop, map[string]any) {
	image := map[string]any{}
	if !f.HasMCP {
		return user, drops, image
	}
	if len(f.MCP) > maxImageMCPBytes {
		return user, append(drops, Drop{Kind: "mcp", Name: "image",
			Reason: "the box image's " + ImageMCPPath + " is larger than 64 KiB"}), image
	}
	invalid := Drop{Kind: "mcp", Name: "image", Reason: "the box image's " + ImageMCPPath + " is not valid JSON"}
	var top any
	if json.Unmarshal(f.MCP, &top) != nil {
		return user, append(drops, invalid), image
	}
	doc, ok := top.(map[string]any)
	if !ok {
		return user, append(drops, invalid), image
	}
	var servers map[string]any
	switch v := doc["mcpServers"].(type) {
	case nil: // absent or null: no servers
	case map[string]any:
		servers = v
	default:
		// The same shape error fails the whole user file in filterMCP.
		return user, append(drops, invalid), image
	}

	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make(map[string]any, len(user)+len(servers))
	for k, v := range user {
		out[k] = v
	}
	for _, n := range names {
		srv, ok := servers[n].(map[string]any)
		if !ok {
			drops = append(drops, Drop{Kind: "mcp", Name: n, Reason: "the box image declares it, but not as a JSON object"})
			continue
		}
		_, replaced := out[n]
		kept := drops[:0:0]
		for _, d := range drops {
			// "*" names the user's whole file, not a server; it is never replaced.
			if d.Kind == "mcp" && d.Name == n && n != "*" {
				replaced = true
				continue
			}
			kept = append(kept, d)
		}
		drops = kept
		out[n] = srv
		image[n] = srv
		if replaced {
			drops = append(drops, Drop{Kind: "mcp", Name: n, Reason: "replaced by the image's " + n})
		}
	}
	return out, drops, image
}
