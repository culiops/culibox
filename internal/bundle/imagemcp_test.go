package bundle

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeImageProbe is an ImageProbe with a settable image ID that counts probes.
type fakeImageProbe struct {
	id       string
	idErr    error
	facts    ImageFacts
	probeErr error
	probes   int
}

func (f *fakeImageProbe) ImageID() (string, error) { return f.id, f.idErr }

func (f *fakeImageProbe) Probe(imageID string, cmds []string) (ImageFacts, error) {
	f.probes++
	if f.probeErr != nil {
		return ImageFacts{}, f.probeErr
	}
	out := f.facts
	out.Resolves = map[string]bool{}
	for _, c := range cmds {
		out.Resolves[c] = f.facts.Resolves[c]
	}
	return out, nil
}

// Resolves is never the path an ImageProbe takes; reaching it means the
// Manager skipped the image probe.
func (f *fakeImageProbe) Resolves([]string) map[string]bool {
	panic("an ImageProbe must be probed through Probe")
}

func imageFacts(mcp string) ImageFacts {
	return ImageFacts{HasMCP: true, MCP: []byte(mcp), Resolves: map[string]bool{}}
}

// imageManager builds a Manager over an empty source dir. userMCP == "" means
// the user has no MCP file at all.
func imageManager(t *testing.T, userMCP string, p *fakeImageProbe) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(dir, ".claude.json")
	if userMCP != "" {
		if err := os.WriteFile(mcpFile, []byte(userMCP), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "bundles")
	m, err := New(Options{Source: src, Root: root, MCPFile: mcpFile, Prober: p})
	if err != nil {
		t.Fatal(err)
	}
	return m, root
}

func bundleServers(t *testing.T, b *Built) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.Dir, mcpName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.MCPServers
}

func dropsNamed(b *Built, name string) []Drop {
	var out []Drop
	for _, d := range b.Lock.Dropped {
		if d.Kind == "mcp" && d.Name == name {
			out = append(out, d)
		}
	}
	return out
}

func mustEnsure(t *testing.T, m *Manager) *Built {
	t.Helper()
	b, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The image's own servers skip the user filter: a literal env value would be
// dropped from a user server, and the command is not even in Resolves.
func TestImageServersAreMergedUnfiltered(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a",
		facts: imageFacts(`{"mcpServers":{"cbm":{"command":"codebase-memory-mcp","env":{"K":"v"}}}}`)}
	m, _ := imageManager(t, `{"mcpServers":{}}`, p)
	b := mustEnsure(t, m)

	srv, ok := bundleServers(t, b)["cbm"]
	if !ok {
		t.Fatalf("the image's cbm must be in the bundle; drops=%+v", b.Lock.Dropped)
	}
	if env, _ := srv["env"].(map[string]any); env["K"] != "v" {
		t.Fatalf("the image's env must be kept verbatim, got %v", srv)
	}
	if d := dropsNamed(b, "cbm"); len(d) != 0 {
		t.Fatalf("no drop expected for cbm, got %+v", d)
	}
}

func TestImageServerReplacesUserServer(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a",
		facts: imageFacts(`{"mcpServers":{"cbm":{"command":"codebase-memory-mcp"}}}`)}
	m, _ := imageManager(t, `{"mcpServers":{"cbm":{"url":"https://user.invalid/mcp"}}}`, p)
	b := mustEnsure(t, m)

	if got := bundleServers(t, b)["cbm"]["command"]; got != "codebase-memory-mcp" {
		t.Fatalf("the image's cbm must win, got %v", bundleServers(t, b)["cbm"])
	}
	want := Drop{Kind: "mcp", Name: "cbm", Reason: "replaced by the image's cbm"}
	if d := dropsNamed(b, "cbm"); len(d) != 1 || d[0] != want {
		t.Fatalf("want exactly %+v, got %+v", want, d)
	}
}

func TestImageServerReplacesAFilteredUserServer(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a",
		facts: imageFacts(`{"mcpServers":{"cbm":{"command":"codebase-memory-mcp"}}}`)}
	m, _ := imageManager(t, `{"mcpServers":{"cbm":{"command":"not-in-the-image"}}}`, p)
	b := mustEnsure(t, m)

	want := Drop{Kind: "mcp", Name: "cbm", Reason: "replaced by the image's cbm"}
	if d := dropsNamed(b, "cbm"); len(d) != 1 || d[0] != want {
		t.Fatalf("the filter's drop must give way to the replaced row, got %+v", d)
	}
	if _, ok := bundleServers(t, b)["cbm"]; !ok {
		t.Fatal("the image's cbm must be in the bundle")
	}
}

func TestInvalidImageMCPIsADropRow(t *testing.T) {
	for _, doc := range []string{`{"mcpServers":`, `[]`} {
		t.Run(doc, func(t *testing.T) {
			p := &fakeImageProbe{id: "sha256:a", facts: imageFacts(doc)}
			m, _ := imageManager(t, `{"mcpServers":{"remote":{"url":"https://user.invalid/mcp"}}}`, p)
			b := mustEnsure(t, m)

			got := bundleServers(t, b)
			if len(got) != 1 || got["remote"] == nil {
				t.Fatalf("user servers must be unchanged, got %v", got)
			}
			d := dropsNamed(b, "image")
			if len(d) != 1 || !strings.Contains(d[0].Reason, "not valid JSON") {
				t.Fatalf("want one image drop saying not valid JSON, got %+v", d)
			}
		})
	}
}

func TestOversizedImageMCPIsADropRow(t *testing.T) {
	big := `{"mcpServers":{"a":{"url":"https://x"}},"pad":"` + strings.Repeat("x", 65537) + `"}`
	p := &fakeImageProbe{id: "sha256:a", facts: imageFacts(big[:65537])}
	m, _ := imageManager(t, `{"mcpServers":{}}`, p)
	b := mustEnsure(t, m)

	want := Drop{Kind: "mcp", Name: "image", Reason: "the box image's /etc/agent-box/mcp.json is larger than 64 KiB"}
	if d := dropsNamed(b, "image"); len(d) != 1 || d[0] != want {
		t.Fatalf("want %+v, got %+v", want, d)
	}
	if len(bundleServers(t, b)) != 0 {
		t.Fatal("an oversized image file contributes no servers")
	}
}

func TestNonObjectImageServerIsDropped(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a",
		facts: imageFacts(`{"mcpServers":{"a":"x","b":{"url":"https://x"}}}`)}
	m, _ := imageManager(t, `{"mcpServers":{}}`, p)
	b := mustEnsure(t, m)

	got := bundleServers(t, b)
	if _, ok := got["b"]; !ok {
		t.Fatalf("b must be kept, got %v", got)
	}
	if _, ok := got["a"]; ok {
		t.Fatal("a must be dropped")
	}
	want := Drop{Kind: "mcp", Name: "a", Reason: "the box image declares it, but not as a JSON object"}
	if d := dropsNamed(b, "a"); len(d) != 1 || d[0] != want {
		t.Fatalf("want %+v, got %+v", want, d)
	}
}

func TestImageServersSurviveAMissingUserMCPFile(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a",
		facts: imageFacts(`{"mcpServers":{"cbm":{"command":"codebase-memory-mcp"}}}`)}
	m, _ := imageManager(t, "", p)
	b := mustEnsure(t, m)

	got := bundleServers(t, b)
	if len(got) != 1 || got["cbm"] == nil {
		t.Fatalf("kept must be exactly the image servers, got %v", got)
	}
	if len(dropsNamed(b, "*")) != 1 {
		t.Fatalf("the missing-file drop must be kept, got %+v", b.Lock.Dropped)
	}
}

func TestAbsentOrNullImageServersAreNoDrop(t *testing.T) {
	for _, doc := range []string{`{}`, `{"mcpServers":null}`} {
		t.Run(doc, func(t *testing.T) {
			p := &fakeImageProbe{id: "sha256:a", facts: imageFacts(doc)}
			m, _ := imageManager(t, `{"mcpServers":{}}`, p)
			b := mustEnsure(t, m)
			if len(b.Lock.Dropped) != 0 || len(bundleServers(t, b)) != 0 {
				t.Fatalf("zero servers and no drop expected, got %+v", b.Lock.Dropped)
			}
		})
	}
}

func TestImageChangeRebuildsTheBundle(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a", facts: ImageFacts{}}
	m, _ := imageManager(t, `{"mcpServers":{}}`, p)
	first := mustEnsure(t, m)
	p.id = "sha256:b"
	second := mustEnsure(t, m)
	if first.Hash == second.Hash || second.Reused {
		t.Fatalf("a new image must rebuild: first=%s second=%s reused=%v", first.Hash, second.Hash, second.Reused)
	}
	if p.probes != 2 {
		t.Fatalf("want 2 probes, got %d", p.probes)
	}

	p2 := &fakeImageProbe{id: "sha256:a", facts: ImageFacts{}}
	m2, _ := imageManager(t, `{"mcpServers":{}}`, p2)
	mustEnsure(t, m2)
	again := mustEnsure(t, m2)
	if !again.Reused {
		t.Fatal("the same image must reuse the bundle")
	}
	if p2.probes != 1 {
		t.Fatalf("a reused bundle must not probe again, got %d probes", p2.probes)
	}
}

// A failed probe is not cached: caching it would pin "no image servers" and
// "every stdio server dropped" until the source changed.
func TestProbeErrorPublishesNothing(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a", probeErr: errors.New("daemon down")}
	m, root := imageManager(t, `{"mcpServers":{}}`, p)
	if _, err := m.Ensure(); err == nil || !strings.Contains(err.Error(), "probing the box image") {
		t.Fatalf("want a probe error, got %v", err)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("nothing may be published after a failed probe, root holds %v", ents)
	}
	p.probeErr = nil
	b := mustEnsure(t, m)
	if b.Reused {
		t.Fatal("the next Ensure must build")
	}
}

func TestImageIDErrorFailsEnsure(t *testing.T) {
	p := &fakeImageProbe{idErr: errors.New("no such image")}
	m, _ := imageManager(t, `{"mcpServers":{}}`, p)
	_, err := m.Ensure()
	if err == nil || !strings.Contains(err.Error(), "bundle: box image") {
		t.Fatalf("want a box image error, got %v", err)
	}
	if p.probes != 0 {
		t.Fatal("no probe without an image ID")
	}
}

func TestLockRecordsImageID(t *testing.T) {
	p := &fakeImageProbe{id: "sha256:a"}
	m, _ := imageManager(t, `{"mcpServers":{}}`, p)
	if b := mustEnsure(t, m); b.Lock.ImageID != "sha256:a" {
		t.Fatalf("lock must record the image ID, got %q", b.Lock.ImageID)
	}
	if b := mustEnsure(t, m); !b.Reused || b.Lock.ImageID != "sha256:a" {
		t.Fatalf("the reused lock must carry the image ID, got %+v", b.Lock)
	}
	// A prober without the image extension keeps today's lock.
	if b := buildForTest(t, realisticSource(t)); b.Lock.ImageID != "" {
		t.Fatalf("no image ID without an ImageProbe, got %q", b.Lock.ImageID)
	}
}

// fakeDocker writes a docker stand-in that logs its argv and prints out on
// stdout (and a warning on stderr), exiting with code.
func fakeDocker(t *testing.T, out string, code int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	outFile := filepath.Join(dir, "out")
	if err := os.WriteFile(outFile, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\ncat " + outFile +
		"\necho 'WARNING: something' >&2\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestImageProberProbeFraming(t *testing.T) {
	empty := `{"mcpServers":{}}`
	b64 := base64.StdEncoding.EncodeToString([]byte(empty))

	bin, log := fakeDocker(t, "MCP "+b64+"\nOK node\n", 0)
	f, err := ImageProber{Docker: bin, Image: "img"}.Probe("sha256:abc", []string{"node", "uvx"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.HasMCP || string(f.MCP) != empty || !f.Resolves["node"] || f.Resolves["uvx"] {
		t.Fatalf("framing not parsed: %+v", f)
	}
	argv, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(argv), "run --rm --pull=never --network none --entrypoint sh sha256:abc -c ") {
		t.Fatalf("probe argv: %q", argv)
	}
	firstLine := `if [ -e /etc/agent-box/mcp.json ]; then printf 'MCP '; head -c 65537 /etc/agent-box/mcp.json | base64 | tr -d '\n'; echo; else echo NOMCP; fi` + "\n"
	if !strings.Contains(string(argv), " -c "+firstLine) {
		t.Fatalf("the image file must be printed first: %q", argv)
	}

	// The image file is only ever read from line 1, so output derived from
	// user command names cannot forge image servers.
	bin, _ = fakeDocker(t, "OK node\nMCP e30=\n", 0)
	if _, err := (ImageProber{Docker: bin, Image: "img"}).Probe("sha256:abc", []string{"node"}); err == nil {
		t.Fatal("MCP off line 1 must be an error")
	}
	bin, _ = fakeDocker(t, "NOMCP\nMCP e30=\n", 0)
	f, err = ImageProber{Docker: bin, Image: "img"}.Probe("sha256:abc", nil)
	if err != nil || f.HasMCP {
		t.Fatalf("MCP after NOMCP must be ignored: %+v %v", f, err)
	}
	bin, _ = fakeDocker(t, "MCP !!!\n", 0)
	if _, err := (ImageProber{Docker: bin, Image: "img"}).Probe("sha256:abc", nil); err == nil {
		t.Fatal("bad base64 must be an error")
	}
	bin, _ = fakeDocker(t, "NOMCP\n", 1)
	if _, err := (ImageProber{Docker: bin, Image: "img"}).Probe("sha256:abc", nil); err == nil {
		t.Fatal("a docker error must be an error")
	}
	// Resolves keeps today's contract: all false on error.
	if got := (ImageProber{Docker: bin, Image: "img"}).Resolves([]string{"node"}); got["node"] || len(got) != 1 {
		t.Fatalf("Resolves on error: %v", got)
	}
}

func TestImageProberImageID(t *testing.T) {
	bin, log := fakeDocker(t, "sha256:abc\n", 0)
	id, err := ImageProber{Docker: bin, Args: []string{"--host", "unix:///v/s"}, Image: "img:tag"}.ImageID()
	if err != nil || id != "sha256:abc" {
		t.Fatalf("got %q %v", id, err)
	}
	argv, _ := os.ReadFile(log)
	if strings.TrimSpace(string(argv)) != "--host unix:///v/s image inspect --format {{.Id}} img:tag" {
		t.Fatalf("image inspect argv: %q", argv)
	}
	bin, _ = fakeDocker(t, "abc\n", 0)
	if _, err := (ImageProber{Docker: bin, Image: "img"}).ImageID(); err == nil {
		t.Fatal("an ID without sha256: must be an error")
	}
	bin, _ = fakeDocker(t, "", 1)
	if _, err := (ImageProber{Docker: bin, Image: "img"}).ImageID(); err == nil {
		t.Fatal("a docker error must be an error")
	}
}
