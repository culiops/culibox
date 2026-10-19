package boximage

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHashIsStableAndShort(t *testing.T) {
	if Hash() != Hash() || len(Hash()) != 12 {
		t.Fatalf("Hash() = %q", Hash())
	}
}

func TestContextTarHoldsOnlyTheDockerfile(t *testing.T) {
	b, err := contextTar()
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(b))
	h, err := tr.Next()
	if err != nil || h.Name != "Dockerfile" {
		t.Fatalf("first entry: %v %v", h, err)
	}
	body, _ := io.ReadAll(tr)
	if !bytes.Equal(body, dockerfile) {
		t.Fatal("tar body differs from the embedded Dockerfile")
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatal("context must contain only the Dockerfile")
	}
}

func fakeDocker(t *testing.T, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "docker")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" + script
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// A user's local node:* tag can be any arch (spike run 2): a plain build
// silently produced an emulated box. --pull and --platform are mandatory.
func TestBuildPinsPlatformPullsAndLabels(t *testing.T) {
	bin, log := fakeDocker(t, `case "$*" in *"version"*) echo arm64;; *) cat >/dev/null;; esac`+"\n")
	err := Docker{Bin: bin, Args: []string{"--host", "unix:///s"}}.Build(context.Background(), []string{"culiops/agent-box:base"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, want := range []string{"--host unix:///s build", "--pull", "--platform linux/arm64",
		"--label " + LabelHash + "=" + Hash(), "-t culiops/agent-box:base -"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build argv missing %q: %s", want, got)
		}
	}
}

func TestImageCheck(t *testing.T) {
	cases := []struct {
		s     ImageState
		fatal bool
		msg   string
	}{
		{ImageState{}, true, "box setup"},
		{ImageState{Present: true, Arch: "amd64", Hash: Hash()}, true, "amd64"},
		{ImageState{Present: true, Arch: "arm64", Hash: "old"}, false, "-rebuild"},
		{ImageState{Present: true, Arch: "arm64", Hash: Hash()}, false, ""},
	}
	for _, c := range cases {
		fatal, msg := c.s.Check("arm64")
		if fatal != c.fatal || !strings.Contains(msg, c.msg) || (c.msg == "" && msg != "") {
			t.Fatalf("%+v: got (%v, %q)", c.s, fatal, msg)
		}
	}
}

func TestInspectMissingImage(t *testing.T) {
	bin, _ := fakeDocker(t, "echo 'Error: No such image: x' >&2; exit 1\n")
	st, err := Docker{Bin: bin}.Inspect(context.Background(), "x")
	if err != nil || st.Present {
		t.Fatalf("missing image: %+v %v", st, err)
	}
}

// A CLI or context warning on stderr must not become part of the answer: read
// into the arch it made every darwin spawn fail as "wrong arch".
func TestInspectIgnoresAWarningOnStderr(t *testing.T) {
	bin, _ := fakeDocker(t, "echo 'WARNING: some context warning' >&2; echo 'arm64|"+Hash()+"'\n")
	st, err := Docker{Bin: bin}.Inspect(context.Background(), "x")
	if err != nil || !st.Present || st.Arch != "arm64" || st.Hash != Hash() {
		t.Fatalf("a stderr warning corrupted the answer: %+v %v", st, err)
	}
	if fatal, msg := st.Check("arm64"); fatal {
		t.Fatalf("a current image read as fatal: %s", msg)
	}
}

// Other failures stay errors and carry docker's stderr.
func TestInspectFailureIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, "echo 'Cannot connect to the Docker daemon' >&2; exit 1\n")
	if _, err := (Docker{Bin: bin}).Inspect(context.Background(), "x"); err == nil ||
		!strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("want an error carrying stderr, got %v", err)
	}
}

// Every name goes on one build, so the dated tag and :base are the same image.
func TestBuildTagsEveryName(t *testing.T) {
	bin, log := fakeDocker(t, `case "$*" in *"version"*) echo arm64;; *) cat >/dev/null;; esac`+"\n")
	d := Docker{Bin: bin}
	if err := d.Build(context.Background(), []string{"r:2026-10-18", "r:base"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "-t r:2026-10-18 -t r:base -") {
		t.Fatalf("build argv: %s", b)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := d.Build(context.Background(), nil, io.Discard); err == nil {
		t.Fatal("a build with no tags must be an error")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("docker must not run for a build with no tags")
	}
}

func TestIsDated(t *testing.T) {
	digest := "x@sha256:" + strings.Repeat("a", 64)
	for ref, want := range map[string]bool{
		"culiops/agent-box:2026-10-18":    true,
		"culiops/agent-box:2026-10-18.2":  true,
		"reg:5000/a/b:2026-10-18":         true,
		"culiops/agent-box:base":          false,
		"culiops/agent-box:2026-10-18.10": false,
		"culiops/agent-box":               false,
		digest:                            false,
		"reg:5000/a":                      false,
	} {
		if got := IsDated(ref); got != want {
			t.Errorf("IsDated(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestRepo(t *testing.T) {
	for ref, want := range map[string]string{
		"reg:5000/a/b:base":            "reg:5000/a/b",
		"a/b":                          "a/b",
		"culiops/agent-box:2026-10-18": "culiops/agent-box",
	} {
		got, err := Repo(ref)
		if err != nil || got != want {
			t.Errorf("Repo(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := Repo("x@sha256:" + strings.Repeat("a", 64)); err == nil {
		t.Error("a digest ref has no repo+tag split; want an error")
	}
}

// presentScript answers `image inspect <ref>` as present for the listed refs
// and as missing for any other.
func presentScript(refs ...string) string {
	s := "last=\"\"; for a; do last=$a; done\ncase \"$last\" in\n"
	for _, r := range refs {
		s += "  " + r + ") echo 'arm64|x'; exit 0;;\n"
	}
	return s + "  *) echo 'Error: No such image: '$last >&2; exit 1;;\nesac\n"
}

func TestNextDatedTag(t *testing.T) {
	ny := time.FixedZone("EST", -5*3600)
	day := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	all := []string{"r:2026-10-18"}
	for _, n := range "23456789" {
		all = append(all, "r:2026-10-18."+string(n))
	}
	cases := []struct {
		name    string
		present []string
		now     time.Time
		want    string
		err     bool
	}{
		{"none exist", nil, day, "r:2026-10-18", false},
		{"base taken", []string{"r:2026-10-18"}, day, "r:2026-10-18.2", false},
		{"two taken", []string{"r:2026-10-18", "r:2026-10-18.2"}, day, "r:2026-10-18.3", false},
		{"all taken", all, day, "", true},
		{"UTC date, not local", nil, time.Date(2026, 10, 18, 23, 30, 0, 0, ny), "r:2026-10-19", false},
	}
	for _, c := range cases {
		bin, _ := fakeDocker(t, presentScript(c.present...))
		got, err := Docker{Bin: bin}.NextDatedTag(context.Background(), "r", c.now)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: got %q, %v; want %q (err %v)", c.name, got, err, c.want, c.err)
		}
	}
}

func TestCheckForPinned(t *testing.T) {
	const pin, base = "culiops/agent-box:2026-10-18", "culiops/agent-box:base"
	fatal, msg := ImageState{}.CheckFor(pin, "arm64")
	if !fatal || !strings.Contains(msg, "never rebuilds or pulls") || !strings.Contains(msg, pin) {
		t.Fatalf("missing pin: (%v, %q)", fatal, msg)
	}
	stale := ImageState{Present: true, Arch: "arm64", Hash: "old"}
	fatal, msg = stale.CheckFor(pin, "arm64")
	if fatal || !strings.Contains(msg, "pinned to "+pin) {
		t.Fatalf("stale pin: (%v, %q)", fatal, msg)
	}
	if fatal, msg = stale.CheckFor(base, "arm64"); fatal || !strings.Contains(msg, "-rebuild") || strings.Contains(msg, "pinned") {
		t.Fatalf("stale unpinned: (%v, %q)", fatal, msg)
	}
	if fatal, _ = (ImageState{Present: true, Arch: "amd64", Hash: Hash()}).CheckFor(pin, "arm64"); !fatal {
		t.Fatal("a wrong-arch pin is still fatal")
	}
	if fatal, msg = (ImageState{Present: true, Arch: "arm64", Hash: Hash()}).CheckFor(pin, "arm64"); fatal || msg != "" {
		t.Fatalf("current pin: (%v, %q)", fatal, msg)
	}
}

// A tag the caller names as taken is skipped even when the daemon lacks it.
func TestNextDatedTagSkipsNamedTakenTags(t *testing.T) {
	bin, _ := fakeDocker(t, presentScript())
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	got, err := Docker{Bin: bin}.NextDatedTag(context.Background(), "r", now, "r:2026-10-18", "r:2026-10-18.2")
	if err != nil || got != "r:2026-10-18.3" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// dockerfileInstructions joins backslash-continued lines into one instruction
// each and drops comment lines.
func dockerfileInstructions() []string {
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

func TestDockerfileVerifiesEveryDownload(t *testing.T) {
	// A download is a curl invocation (curl followed by an option), not the
	// word "curl" in an apt package list.
	curlCall := regexp.MustCompile(`\bcurl\s+-`)
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	downloads := 0
	for _, in := range dockerfileInstructions() {
		switch {
		case strings.HasPrefix(in, "RUN ") && curlCall.MatchString(in):
			downloads++
			if !strings.Contains(in, "sha256sum -c") {
				t.Errorf("download without sha256sum -c: %.120s", in)
			}
		case strings.HasPrefix(in, "ARG "):
			name, val, _ := strings.Cut(strings.TrimPrefix(in, "ARG "), "=")
			if strings.Contains(name, "SHA256") && !sha.MatchString(val) {
				t.Errorf("ARG %s is not a 64-hex sha256: %q", name, val)
			}
		}
	}
	if downloads < 17 {
		t.Errorf("found %d downloads, want at least 17 (aws, terraform, gh, go, abduco, codebase-memory, kubectl, helm, kubeconform, stern, yq, shellcheck, tflint, tflint-ruleset-aws, jsonnet, tfenv, hashicorp key)", downloads)
	}
}

func TestDockerfileGoMatchesGoMod(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("no go directive in go.mod")
	}
	want := "ARG GO_VERSION=" + string(m[1])
	found := false
	for _, in := range dockerfileInstructions() {
		if in == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Dockerfile has no %q (go.mod and the image toolchain must agree)", want)
	}
}

func TestDockerfileDeclaresCodebaseMemory(t *testing.T) {
	var line string
	for _, in := range dockerfileInstructions() {
		if strings.Contains(in, "printf") && strings.Contains(in, "/etc/agent-box/mcp.json") {
			line = in
		}
	}
	if line == "" {
		t.Fatal("no printf writing /etc/agent-box/mcp.json")
	}
	i, j := strings.Index(line, "'{"), strings.LastIndex(line, "}'")
	if i < 0 || j < i {
		t.Fatalf("no quoted JSON in %q", line)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(line[i+1:j+1]), &doc); err != nil {
		t.Fatalf("mcp.json payload is not JSON: %v", err)
	}
	if got := doc.MCPServers["codebase-memory"]["command"]; got != "/usr/local/bin/codebase-memory-mcp" {
		t.Errorf("codebase-memory command = %v", got)
	}
}

// Each value must be an ENV instruction of its own, not text anywhere in the
// file: a comment naming GOTOOLCHAIN=local must not keep this green after the
// ENV line itself is deleted. GOFLAGS=-modcacherw keeps Go's module cache
// writable, so the box home stays deletable and box-state reclaim works.
func TestDockerfileSetsGoEnvUnderHome(t *testing.T) {
	env := map[string]bool{}
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "ENV ") {
			env[strings.TrimPrefix(in, "ENV ")] = true
		}
	}
	for _, want := range []string{
		"GOPATH=/home/agent/go",
		"GOMODCACHE=/home/agent/go/pkg/mod",
		"GOCACHE=/home/agent/.cache/go-build",
		"GOTMPDIR=/home/agent/.cache/go-tmp",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-modcacherw",
		"PATH=/usr/local/go/bin:/home/agent/go/bin:$PATH",
	} {
		if !env[want] {
			t.Errorf("Dockerfile has no instruction ENV %s", want)
		}
	}
}

func TestDockerfilePinsCodebaseMemoryVersion(t *testing.T) {
	if !strings.Contains(string(dockerfile), "\nARG CBM_VERSION=0.10.8\n") {
		t.Error("Dockerfile does not pin ARG CBM_VERSION=0.10.8")
	}
}

// scripts/lint.sh maps eslint's report back to source files with python3, so a
// box without it cannot pass scripts/test.sh (measured in the A2b integration
// run: "python3: not found", rc 127). It is installed in its own block.
func TestDockerfileInstallsPython3InItsOwnBlock(t *testing.T) {
	df := string(dockerfile)
	i := strings.Index(df, "# --- python3 ---")
	if i < 0 {
		t.Fatal("Dockerfile has no '# --- python3 ---' block")
	}
	block := df[i:]
	if j := strings.Index(block[1:], "\n# --- "); j >= 0 {
		block = block[:j+1]
	}
	if !strings.Contains(block, "apt-get install -y --no-install-recommends python3") {
		t.Errorf("python3 block does not install python3:\n%s", block)
	}
	if !strings.Contains(block, "rm -rf /var/lib/apt/lists/*") {
		t.Errorf("python3 block leaves the apt lists in the image:\n%s", block)
	}
}

// mcpServersFromDockerfile parses the one JSON document the image's
// /etc/agent-box/mcp.json printf writes.
func mcpServersFromDockerfile(t *testing.T) (string, map[string]map[string]any) {
	t.Helper()
	var line string
	for _, in := range dockerfileInstructions() {
		if strings.Contains(in, "printf") && strings.Contains(in, "/etc/agent-box/mcp.json") {
			line = in
		}
	}
	if line == "" {
		t.Fatal("no printf writing /etc/agent-box/mcp.json")
	}
	i, j := strings.Index(line, "'{"), strings.LastIndex(line, "}'")
	if i < 0 || j < i {
		t.Fatalf("no quoted JSON in %q", line)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(line[i+1:j+1]), &doc); err != nil {
		t.Fatalf("mcp.json payload is not JSON: %v", err)
	}
	return line, doc.MCPServers
}

func TestDockerfilePinsPlaywrightMCPVersion(t *testing.T) {
	df := string(dockerfile)
	if !strings.Contains(df, "\nARG PLAYWRIGHT_MCP_VERSION=0.0.83\n") {
		t.Error("Dockerfile does not pin ARG PLAYWRIGHT_MCP_VERSION=0.0.83")
	}
	found := false
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "RUN ") && strings.Contains(in, "npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION}") {
			found = true
		}
	}
	if !found {
		t.Error("no RUN installs @playwright/mcp@${PLAYWRIGHT_MCP_VERSION}")
	}
}

func TestDockerfileDeclaresPlaywright(t *testing.T) {
	line, servers := mcpServersFromDockerfile(t)
	if got := servers["codebase-memory"]["command"]; got != "/usr/local/bin/codebase-memory-mcp" {
		t.Errorf("codebase-memory command = %v", got)
	}
	pw, ok := servers["playwright"]
	if !ok {
		t.Fatal("mcp.json has no playwright server")
	}
	if pw["command"] != "/usr/local/bin/playwright-mcp" {
		t.Errorf("playwright command = %v", pw["command"])
	}
	var args []string
	for _, a := range pw["args"].([]any) {
		s, _ := a.(string)
		if strings.Contains(s, "/home/claude") {
			t.Errorf("playwright arg %q names the host home", s)
		}
		args = append(args, s)
	}
	has := func(flag, val string) bool {
		for i, a := range args {
			if a == flag && (val == "" || (i+1 < len(args) && args[i+1] == val)) {
				return true
			}
		}
		return false
	}
	for _, w := range [][2]string{
		{"--headless", ""}, {"--isolated", ""},
		{"--output-dir", "/tmp/playwright-mcp"},
		{"--browser", "chromium"},
		{"--executable-path", "/usr/bin/chromium"},
	} {
		if !has(w[0], w[1]) {
			t.Errorf("playwright args %v lack %s %s", args, w[0], w[1])
		}
	}
	// Both servers are checked at build time, not just written.
	for _, name := range []string{"codebase-memory", "playwright"} {
		if !strings.Contains(line, `jq -e '.mcpServers["`+name+`"].command'`) {
			t.Errorf("mcp.json RUN has no jq -e check for %s", name)
		}
	}
}

func TestDockerfileInstallsBrowserInItsOwnBlock(t *testing.T) {
	df := string(dockerfile)
	i := strings.Index(df, "# --- Playwright MCP + Chromium ---")
	if i < 0 {
		t.Fatal("Dockerfile has no '# --- Playwright MCP + Chromium ---' block")
	}
	block := df[i:]
	if j := strings.Index(block[1:], "\n# --- "); j >= 0 {
		block = block[:j+1]
	}
	for _, want := range []string{
		"apt-get install -y --no-install-recommends chromium",
		"rm -rf /var/lib/apt/lists/*",
		"npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION}",
		"chromium --version",
		`playwright-mcp --version | grep -q "${PLAYWRIGHT_MCP_VERSION}"`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("browser block lacks %q:\n%s", want, block)
		}
	}
	// Debian's browser was chosen: no bundled-browser download or path.
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "ENV ") && strings.Contains(in, "PLAYWRIGHT_BROWSERS_PATH") {
			t.Error("Dockerfile sets PLAYWRIGHT_BROWSERS_PATH, but the Debian chromium is the chosen browser")
		}
	}
}

// The browser must run as the box's own user, not only as root at build time.
func TestDockerfileChecksBrowserAsAgent(t *testing.T) {
	user := ""
	ok := false
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "USER ") {
			user = strings.TrimPrefix(in, "USER ")
		}
		if user == "agent" && strings.HasPrefix(in, "RUN ") && strings.Contains(in, "chromium --version") {
			ok = true
		}
	}
	if !ok {
		t.Error("no RUN after USER agent asserts chromium --version")
	}
}

func TestIsRemote(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, c := range []struct {
		ref  string
		want bool
	}{
		{"ghcr.io/culiops/agent-box" + digest, true},
		{"culiops/agent-box" + digest, true}, // a digest is never buildable
		{"ghcr.io/culiops/agent-box:base", true},
		{"ghcr.io/culiops/agent-box:2026-10-20-abc1234", true},
		{"localhost/x:1", true},
		{"registry:5000/x:1", true},
		{"culiops/agent-box:base", false},
		{"culiops/agent-box:2026-10-18", false},
		{"agent-box", false},
		{"", false},
	} {
		if got := IsRemote(c.ref); got != c.want {
			t.Errorf("IsRemote(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

// A pulled box must be the daemon's own architecture: an index digest on an
// arm64 daemon otherwise runs whatever docker picks, silently emulated.
func TestPullPinsPlatform(t *testing.T) {
	ref := "ghcr.io/culiops/agent-box@sha256:" + strings.Repeat("a", 64)
	bin, log := fakeDocker(t, "echo pulling\n")
	var out bytes.Buffer
	d := Docker{Bin: bin, Args: []string{"--host", "unix:///s"}}
	if err := d.Pull(context.Background(), ref, "arm64", &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if want := "--host unix:///s pull --platform linux/arm64 ghcr.io/culiops/agent-box@sha256:"; !strings.Contains(string(b), want) {
		t.Fatalf("pull argv missing %q: %s", want, b)
	}
	if !strings.Contains(out.String(), "pulling") {
		t.Fatalf("docker output did not reach the writer: %q", out.String())
	}

	bad, _ := fakeDocker(t, "echo 'manifest unknown' >&2; exit 1\n")
	err := Docker{Bin: bad}.Pull(context.Background(), ref, "arm64", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("want an error carrying docker's stderr, got %v", err)
	}
}

// os/exec copies two distinct writers from two goroutines; a bytes.Buffer is
// not safe for that, so Pull must hand it one stream. Meaningful under -race.
func TestPullWritesTheWriterFromOneGoroutine(t *testing.T) {
	ref := "ghcr.io/culiops/agent-box@sha256:" + strings.Repeat("a", 64)
	bin, _ := fakeDocker(t, "i=0; while [ $i -lt 200 ]; do echo out$i; echo err$i >&2; i=$((i+1)); done; exit 1\n")
	var out bytes.Buffer
	err := Docker{Bin: bin}.Pull(context.Background(), ref, "arm64", &out)
	if err == nil || !strings.Contains(err.Error(), "err199") {
		t.Fatalf("want an error carrying docker's stderr, got %v", err)
	}
	if !strings.Contains(out.String(), "out199") || !strings.Contains(out.String(), "err199") {
		t.Fatalf("both streams should reach the writer: %q", out.String())
	}
}

func TestCheckForRemote(t *testing.T) {
	ref := "ghcr.io/culiops/agent-box@sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name  string
		s     ImageState
		fatal bool
		has   []string
		exact string // checked only when set
	}{
		{"missing", ImageState{}, true, []string{"agent-box box setup", "pull"}, ""},
		{"wrong arch", ImageState{Present: true, Arch: "amd64", Hash: Hash()}, true, []string{"amd64", "arm64"}, ""},
		{"stale", ImageState{Present: true, Arch: "arm64", Hash: "0123456789ab"}, false, nil,
			"this image was built from an older Dockerfile; pull a newer digest from ghcr"},
		{"current", ImageState{Present: true, Arch: "arm64", Hash: Hash()}, false, nil, ""},
	}
	for _, c := range cases {
		fatal, msg := c.s.CheckFor(ref, "arm64")
		if fatal != c.fatal {
			t.Errorf("%s: fatal = %v, want %v (%s)", c.name, fatal, c.fatal, msg)
		}
		for _, w := range c.has {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: msg %q lacks %q", c.name, msg, w)
			}
		}
		if c.name == "stale" || c.name == "current" {
			want := c.exact
			if msg != want {
				t.Errorf("%s: msg = %q, want %q", c.name, msg, want)
			}
		}
		if strings.Contains(msg, "-rebuild") {
			t.Errorf("%s: a remote image is never rebuilt, msg %q", c.name, msg)
		}
	}
}

// The base is pinned by digest, so a rebuild cannot silently follow a moved
// tag. Every stage carries the same digest; bumping it is a manual edit.
func TestDockerfilePinsBaseByDigest(t *testing.T) {
	from := regexp.MustCompile(`^FROM node:24-bookworm-slim@sha256:([0-9a-f]{64})( AS \S+)?$`)
	digest, n := "", 0
	for _, in := range dockerfileInstructions() {
		if !strings.HasPrefix(in, "FROM ") {
			continue
		}
		n++
		m := from.FindStringSubmatch(in)
		if m == nil {
			t.Errorf("FROM is not node:24-bookworm-slim pinned by sha256 digest: %q", in)
			continue
		}
		if digest == "" {
			digest = m[1]
		} else if m[1] != digest {
			t.Errorf("FROM digests differ: %s vs %s", digest, m[1])
		}
	}
	if n == 0 {
		t.Fatal("no FROM instruction found")
	}
}

// toolBatch is the SRE/k8s batch: every tool has a version ARG, both per-arch
// sums, and a RUN whose version check greps the pinned version.
var toolBatch = []struct{ arg, check string }{
	{"KUBECTL", "kubectl version --client"},
	{"HELM", "helm version"},
	{"KUBECONFORM", "kubeconform -v"},
	{"STERN", "stern --version"},
	{"YQ", "yq --version"},
	{"SHELLCHECK", "shellcheck --version"},
	{"TFLINT", "tflint --version"},
	{"TFLINT_RULESET_AWS", "tflint --config /etc/agent-box/tflint.hcl --version"},
	{"JSONNET", "jsonnet --version"},
	{"JSONNET", "jsonnetfmt --version"},
}

func TestDockerfileInstallsToolBatch(t *testing.T) {
	df := string(dockerfile)
	seen := map[string]bool{}
	for _, tb := range toolBatch {
		if !seen[tb.arg] {
			seen[tb.arg] = true
			for _, suffix := range []string{"_VERSION=", "_SHA256_AMD64=", "_SHA256_ARM64="} {
				if !strings.Contains(df, "\nARG "+tb.arg+suffix) {
					t.Errorf("Dockerfile has no ARG %s%s", tb.arg, suffix)
				}
			}
		}
		grep := regexp.MustCompile(regexp.QuoteMeta(tb.check) + `\s*\|\s*grep -q "[^"]*\$\{` + tb.arg + `_VERSION\}`)
		found := false
		for _, in := range dockerfileInstructions() {
			if strings.HasPrefix(in, "RUN ") && grep.MatchString(in) {
				found = true
			}
		}
		if !found {
			t.Errorf("no RUN checks %q and greps ${%s_VERSION}", tb.check, tb.arg)
		}
	}
}

func TestDockerfileAptExtras(t *testing.T) {
	for _, in := range dockerfileInstructions() {
		if !strings.HasPrefix(in, "RUN ") || !strings.Contains(in, "apt-get install") {
			continue
		}
		ok := true
		for _, pkg := range []string{"make", "openssl", "dnsutils", "netcat-openbsd"} {
			if !regexp.MustCompile(`\s` + pkg + `(\s|$)`).MatchString(in) {
				ok = false
			}
		}
		if ok {
			if !strings.Contains(in, "rm -rf /var/lib/apt/lists/*") {
				t.Errorf("apt extras block leaves the apt lists in the image: %s", in)
			}
			return
		}
	}
	t.Error("no single RUN apt-get install names make, openssl, dnsutils and netcat-openbsd")
}

// Pins that move often sit after the marker so a bump rebuilds only the tail;
// the heavy, rarely bumped toolchains sit before it.
func TestDockerfileFrequentPinsLast(t *testing.T) {
	df := string(dockerfile)
	const marker = "# === frequently bumped pins (keep last) ==="
	if strings.Count(df, marker) != 1 {
		t.Fatalf("want exactly one %q", marker)
	}
	m := strings.Index(df, marker)
	chromium := strings.Index(df, "apt-get install -y --no-install-recommends chromium")
	if chromium < 0 || m < chromium {
		t.Error("the marker must come after the chromium/playwright RUN")
	}
	var after []string
	for _, tb := range toolBatch {
		for _, suffix := range []string{"_VERSION=", "_SHA256_AMD64=", "_SHA256_ARM64="} {
			after = append(after, tb.arg+suffix)
		}
	}
	after = append(after, "TFENV_VERSION=", "TFENV_SHA256=")
	for _, line := range strings.Split(df, "\n") {
		if strings.HasPrefix(line, "ARG GH_") {
			after = append(after, strings.SplitAfter(strings.TrimPrefix(line, "ARG "), "=")[0])
		}
	}
	for _, a := range after {
		i := strings.Index(df, "\nARG "+a)
		if i < 0 {
			t.Errorf("no ARG %s", a)
		} else if i < m {
			t.Errorf("ARG %s is before the marker", a)
		}
	}
	for _, a := range []string{"GO_VERSION=", "AWSCLI_VERSION=", "PLAYWRIGHT_MCP_VERSION="} {
		i := strings.Index(df, "\nARG "+a)
		if i < 0 || i > m {
			t.Errorf("ARG %s must be before the marker", a)
		}
	}
}

// tflint's AWS ruleset is baked in; the box home is a bind mount, so the
// per-home config and plugin link are made by the entrypoint. No env override
// is set: both outrank a repo's own config or init path.
func TestDockerfileTflintAWSDefaultIsAHomeFallback(t *testing.T) {
	df := string(dockerfile)
	for _, banned := range []string{"TFLINT_CONFIG_FILE", "TFLINT_PLUGIN_DIR"} {
		if strings.Contains(df, banned) {
			t.Errorf("Dockerfile mentions %s, which outranks a repo's own tflint setup", banned)
		}
	}
	var hcl, entry string
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "RUN ") && strings.Contains(in, "> /etc/agent-box/tflint.hcl") {
			hcl = in
		}
		if strings.HasPrefix(in, "RUN ") && strings.Contains(in, "> /usr/local/bin/agent-box-entrypoint") {
			entry = in
		}
	}
	if hcl == "" {
		t.Fatal("no RUN writes /etc/agent-box/tflint.hcl")
	}
	for _, want := range []string{`plugin "aws"`, `enabled = true`, `version = "%s"`,
		`"${TFLINT_RULESET_AWS_VERSION}"`, `source = "github.com/terraform-linters/tflint-ruleset-aws"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("tflint.hcl RUN lacks %s: %s", want, hcl)
		}
	}
	if entry == "" {
		t.Fatal("no RUN writes the entrypoint")
	}
	for _, want := range []string{
		`[ -e "$HOME/.tflint.hcl" ] || cp /etc/agent-box/tflint.hcl "$HOME/.tflint.hcl"`,
		`$HOME/.tflint.d/plugins/`,
		`[ -e "$d" ] ||`,
		`ln -s "$p" "$d"`,
		`/opt/tflint/plugins/github.com/terraform-linters/tflint-ruleset-aws/`,
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("entrypoint lacks %s: %s", want, entry)
		}
	}
	if !strings.Contains(df, "/opt/tflint/plugins/github.com/terraform-linters/tflint-ruleset-aws/${TFLINT_RULESET_AWS_VERSION}") {
		t.Error("the plugin is not baked at /opt/tflint/plugins/<source>/<version>")
	}
}

// The label links the public ghcr package to this repository.
func TestDockerfileSourceLabel(t *testing.T) {
	df := string(dockerfile)
	const label = `LABEL org.opencontainers.image.source="https://github.com/culiops/agent-box"`
	i := strings.Index(df, label)
	if i < 0 {
		t.Fatalf("Dockerfile lacks %s", label)
	}
	if last := strings.LastIndex(df, "\nFROM "); i < last {
		t.Error("the source label must be in the final stage")
	}
}

// stageInstructions returns the instructions of the stage whose FROM line
// matches header, up to (not including) the next FROM.
func stageInstructions(t *testing.T, header *regexp.Regexp) []string {
	t.Helper()
	var out []string
	in := false
	for _, ins := range dockerfileInstructions() {
		if strings.HasPrefix(ins, "FROM ") {
			if in {
				break
			}
			in = header.MatchString(ins)
			continue
		}
		if in {
			out = append(out, ins)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no stage matching %v", header)
	}
	return out
}

const hashicorpKeyFpr = "C874011F0AB405110D02105534365D9472D7468F"

// tfenv only checks HashiCorp's signature when a use-gpgv file says so, and then
// against the keyring file in its own tree. The key is fetched in a builder
// stage, pinned by sha256 AND by fingerprint, and replaces tfenv's bundled
// (expired) keyring.
func TestDockerfileTfenvVerifiesSignatures(t *testing.T) {
	stage := strings.Join(stageInstructions(t, regexp.MustCompile(`^FROM node:24-bookworm-slim@sha256:[0-9a-f]{64} AS hashicorp-key$`)), "\n")
	for _, want := range []string{
		"ARG HASHICORP_KEY_FPR=" + hashicorpKeyFpr,
		"ARG HASHICORP_KEY_SHA256=",
		"https://www.hashicorp.com/.well-known/pgp-key.txt",
		"sha256sum -c",
		"gpg --show-keys --with-colons",
		`"${HASHICORP_KEY_FPR}"`,
		"gpg --dearmor",
		"/out/hashicorp-keys.pgp",
	} {
		if !strings.Contains(stage, want) {
			t.Errorf("hashicorp-key stage lacks %q", want)
		}
	}
	// gnupg is for the builder only.
	final := dockerfileInstructions()
	copyAt, extractAt, gpgvAt := -1, -1, -1
	for i, in := range final {
		switch {
		case strings.HasPrefix(in, "COPY --from=hashicorp-key") && strings.Contains(in, "/out/hashicorp-keys.pgp /opt/tfenv/share/hashicorp-keys.pgp"):
			copyAt = i
		case strings.HasPrefix(in, "RUN ") && strings.Contains(in, "/opt/tfenv") && strings.Contains(in, "--strip-components=1"):
			extractAt = i
		case strings.HasPrefix(in, "RUN ") && strings.Contains(in, "> /opt/tfenv/use-gpgv"):
			gpgvAt = i
			if !strings.Contains(in, "trust-tfenv: yes") {
				t.Errorf("use-gpgv must say \"trust-tfenv: yes\": %.200s", in)
			}
			if !strings.Contains(in, "chown root:root /opt/tfenv/use-gpgv") || !strings.Contains(in, "chmod 0644 /opt/tfenv/use-gpgv") {
				t.Errorf("/opt/tfenv/use-gpgv must be root-owned 0644")
			}
		}
	}
	if copyAt < 0 {
		t.Fatal("final stage does not COPY --from=hashicorp-key the keyring over /opt/tfenv/share/hashicorp-keys.pgp")
	}
	if extractAt < 0 || copyAt < extractAt {
		t.Error("the keyring COPY must come after the tfenv tarball is extracted, or the extract overwrites it")
	}
	if gpgvAt < 0 {
		t.Error("no RUN writes /opt/tfenv/use-gpgv")
	}
	for _, in := range final {
		// use-gnupg takes precedence over use-gpgv in tfenv: only the entrypoint's
		// rm -f may mention it.
		if strings.Contains(in, "use-gnupg") && !strings.Contains(in, `rm -f "$TFENV_CONFIG_DIR/use-gnupg"`) {
			t.Errorf("use-gnupg must only ever be removed: %.120s", in)
		}
	}
}

// Boxes run --read-only: the only writable places are /tmp, the workspace and
// the box home. tfenv state therefore lives in the home, seeded by the
// entrypoint from root-owned defaults under /opt/tfenv. The default version is a
// file, never TFENV_TERRAFORM_VERSION, which outranks a repo's .terraform-version.
func TestDockerfileTfenvDefaultIsAFileNotEnv(t *testing.T) {
	df := string(dockerfile)
	for _, in := range dockerfileInstructions() {
		if strings.Contains(in, "TFENV_TERRAFORM_VERSION") {
			t.Errorf("Dockerfile sets TFENV_TERRAFORM_VERSION, which overrides a repo's .terraform-version: %.120s", in)
		}
		if strings.Contains(in, "/var/lib/tfenv") {
			t.Errorf("no tfenv state in the (read-only) image layer: %.120s", in)
		}
	}
	if !strings.Contains(df, "\nENV TFENV_CONFIG_DIR=/home/agent/.tfenv\n") {
		t.Error("ENV TFENV_CONFIG_DIR must be under the writable box home (/home/agent/.tfenv)")
	}
	if !strings.Contains(df, "\nENV TFENV_ROOT=/opt/tfenv\n") {
		t.Error("ENV TFENV_ROOT=/opt/tfenv is not set")
	}
	// The effective PATH is the LAST ENV PATH= line; it must put the tfenv shim
	// ahead of /usr/local/bin/terraform.
	last := ""
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "ENV PATH=") {
			last = in
		}
	}
	if !strings.HasPrefix(last, "ENV PATH=/opt/tfenv/bin:") {
		t.Errorf("the final ENV PATH must start with /opt/tfenv/bin:, got %q", last)
	}
	var block string
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "RUN ") && strings.Contains(in, "/opt/tfenv/default-version") && !strings.Contains(in, "agent-box-entrypoint &&") {
			block = in
		}
	}
	for _, want := range []string{
		`echo "${TERRAFORM_VERSION}" > /opt/tfenv/default-version`,
		"chmod 0644 /opt/tfenv/use-gpgv /opt/tfenv/default-version",
		// the build-time proof: the REAL entrypoint seeds a throwaway dir, then the shim runs
		"TFENV_CONFIG_DIR=/tmp/tfenv-check /usr/local/bin/agent-box-entrypoint true",
		`TFENV_CONFIG_DIR=/tmp/tfenv-check terraform version | grep -q "v${TERRAFORM_VERSION}"`,
		"rm -rf /tmp/tfenv-check",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("tfenv RUN lacks %q: %.300s", want, block)
		}
	}
}

// The entrypoint runs on every start: it re-arms signature checking (use-gpgv is
// copied unconditionally, a use-gnupg is removed) and seeds the default version
// and its symlink only when absent. Each step's failure is non-fatal. It must not
// reference build ARGs.
func TestDockerfileTfenvEntrypointSeedsHome(t *testing.T) {
	var entry string
	for _, in := range dockerfileInstructions() {
		if strings.HasPrefix(in, "RUN ") && strings.Contains(in, "> /usr/local/bin/agent-box-entrypoint") {
			entry = in
		}
	}
	if entry == "" {
		t.Fatal("no RUN writes the entrypoint")
	}
	for _, want := range []string{
		`if [ -n "${TFENV_CONFIG_DIR:-}" ]; then`,
		`mkdir -p "$TFENV_CONFIG_DIR/versions" 2>/dev/null`,
		`rm -f "$TFENV_CONFIG_DIR/use-gnupg" "$TFENV_CONFIG_DIR/use-gpgv" 2>/dev/null; cp /opt/tfenv/use-gpgv "$TFENV_CONFIG_DIR/use-gpgv" 2>/dev/null`,
		`[ -e "$TFENV_CONFIG_DIR/version" ] || cp /opt/tfenv/default-version "$TFENV_CONFIG_DIR/version" 2>/dev/null`,
		`v=$(cat /opt/tfenv/default-version 2>/dev/null)`,
		`[ -e "$TFENV_CONFIG_DIR/versions/$v/terraform" ] ||`,
		`ln -s /usr/local/bin/terraform "$TFENV_CONFIG_DIR/versions/$v/terraform"`,
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("entrypoint lacks %s", want)
		}
	}
	// use-gpgv is copied unconditionally: never behind a [ -e ] guard.
	if strings.Contains(entry, `[ -e "$TFENV_CONFIG_DIR/use-gpgv" ] ||`) {
		t.Error("use-gpgv must be re-armed on every start, not only when absent")
	}
	if strings.Contains(entry, "${TERRAFORM_VERSION}") || strings.Contains(entry, "$TERRAFORM_VERSION") {
		t.Error("the entrypoint must not reference build ARGs; it reads /opt/tfenv/default-version")
	}
}

func TestDockerfileTfenvPinned(t *testing.T) {
	df := string(dockerfile)
	const marker = "# === frequently bumped pins (keep last) ==="
	m, gh := strings.Index(df, marker), strings.Index(df, "# --- gh (GitHub CLI) ---")
	if strings.Count(df, "\nARG TFENV_VERSION=") != 1 || strings.Count(df, "\nARG TFENV_SHA256=") != 1 {
		t.Fatal("want exactly one ARG TFENV_VERSION and one ARG TFENV_SHA256 (the tarball is arch-independent)")
	}
	for _, a := range []string{"\nARG TFENV_VERSION=", "\nARG TFENV_SHA256=", "\nENV TFENV_CONFIG_DIR="} {
		if i := strings.Index(df, a); i < m || i > gh {
			t.Errorf("%q must sit after the marker and before the gh block", strings.TrimSpace(a))
		}
	}
	// HASHICORP_KEY_* belong to the builder stage, ahead of the final stage.
	if i := strings.Index(df, "\nARG HASHICORP_KEY_SHA256="); i < 0 || i > strings.LastIndex(df, "\nFROM ") {
		t.Error("ARG HASHICORP_KEY_SHA256 must be in the hashicorp-key builder stage")
	}
}
