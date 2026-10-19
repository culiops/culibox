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
	if downloads < 5 {
		t.Errorf("found %d downloads, want at least 5 (aws, terraform, go, abduco, codebase-memory)", downloads)
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
