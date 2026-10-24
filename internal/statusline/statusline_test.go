package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sampleInput = `{"model":{"display_name":"Opus 5.5"},"workspace":{"current_dir":"/work/proj"},"effort":{"level":"high"},"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":1000}}}`

// run execs the embedded script. env entries override the inherited ones.
func run(t *testing.T, cfg string, env ...string) string {
	t.Helper()
	for _, bin := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	script := filepath.Join(t.TempDir(), Name)
	if err := os.WriteFile(script, Script, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+cfg, "AGENTBOX_STATUSLINE_DELEGATING=")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(sampleInput)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\nstderr: %s", err, errb.String())
	}
	return out.String()
}

func assertHarvested(t *testing.T, cfg string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg, "agent-box-rate-limits.json"))
	if err != nil {
		t.Fatalf("rate limits not written: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	fh, _ := m["five_hour"].(map[string]any)
	if fh == nil || fh["used_percentage"] != float64(42) {
		t.Fatalf("rate limits not harvested: %v", m)
	}
}

func TestScriptHonoursDelegateEnv(t *testing.T) {
	cfg := t.TempDir()
	del := filepath.Join(t.TempDir(), "delegate")
	if err := os.WriteFile(del, []byte("cat >/dev/null; echo custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(t, cfg, "AGENTBOX_STATUSLINE_DELEGATE_FILE="+del); got != "custom\n" {
		t.Fatalf("output = %q", got)
	}
	assertHarvested(t, cfg)
}

func TestScriptDelegateEnvBeatsConfigDirFile(t *testing.T) {
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, ".agentbox-statusline-delegate"), []byte("cat >/dev/null; echo from-cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	del := filepath.Join(t.TempDir(), "delegate")
	if err := os.WriteFile(del, []byte("cat >/dev/null; echo from-env"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(t, cfg, "AGENTBOX_STATUSLINE_DELEGATE_FILE="+del); got != "from-env\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestScriptFallsBackWhenDelegateMissing(t *testing.T) {
	cfg := t.TempDir()
	missing := filepath.Join(t.TempDir(), "no-such-delegate")
	if got := run(t, cfg, "AGENTBOX_STATUSLINE_DELEGATE_FILE="+missing); got != "Opus 5.5 | ~/proj | effort high\n" {
		t.Fatalf("output = %q", got)
	}
	assertHarvested(t, cfg)
}

func TestScriptFallsBackWhenDelegateCommandFails(t *testing.T) {
	cfg := t.TempDir()
	del := filepath.Join(t.TempDir(), "delegate")
	// A host-only path, as a box sees the host's statusLine.
	if err := os.WriteFile(del, []byte("/home/hostuser/.claude/statusline-command.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(t, cfg, "AGENTBOX_STATUSLINE_DELEGATE_FILE="+del); got != "Opus 5.5 | ~/proj | effort high\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestIsOwnCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/data/bin/statusline-ratelimits.sh":                      true,
		"AGENTBOX_X=1 /opt/agent-bundle/statusline-ratelimits.sh": true,
		"/home/u/.claude/statusline-command.sh":                   false,
		"":                                                        false,
	} {
		if got := IsOwnCommand(cmd); got != want {
			t.Errorf("IsOwnCommand(%q) = %v", cmd, got)
		}
	}
}
