package account

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/statusline"
)

const sampleStatusInput = `{"model":{"display_name":"Opus 5.5"},"workspace":{"current_dir":"/work/proj"},"cwd":"/elsewhere","effort":{"level":"high"},"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":1000},"seven_day":{"used_percentage":7,"resets_at":2000}}}`

// runStatusScript execs the embedded script with cfg as CLAUDE_CONFIG_DIR.
func runStatusScript(t *testing.T, cfg, input string) string {
	t.Helper()
	for _, bin := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	script := filepath.Join(t.TempDir(), "statusline-ratelimits.sh")
	if err := os.WriteFile(script, statusline.Script, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+cfg, "AGENTBOX_STATUSLINE_DELEGATING=")
	cmd.Stdin = strings.NewReader(input)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\nstderr: %s", err, errb.String())
	}
	return out.String()
}

func assertHarvested(t *testing.T, cfg string) {
	t.Helper()
	m := readJSON(t, filepath.Join(cfg, "agent-box-rate-limits.json"))
	fh, _ := m["five_hour"].(map[string]any)
	if fh == nil || fh["used_percentage"] != float64(42) {
		t.Fatalf("rate limits not harvested: %v", m)
	}
}

func TestStatusScriptFallbackFormat(t *testing.T) {
	cfg := t.TempDir()
	if got := runStatusScript(t, cfg, sampleStatusInput); got != "Opus 5.5 | ~/proj | effort high\n" {
		t.Fatalf("output = %q", got)
	}
	assertHarvested(t, cfg)
}

func TestStatusScriptFallbackOmitsAbsentEffort(t *testing.T) {
	in := `{"model":{"display_name":"Opus 5.5"},"cwd":"/a/b/"}`
	if got := runStatusScript(t, t.TempDir(), in); got != "Opus 5.5 | ~/b\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestStatusScriptGarbageInputNeverErrors(t *testing.T) {
	if got := runStatusScript(t, t.TempDir(), "not json"); got != "" {
		t.Fatalf("output = %q", got)
	}
}

func TestStatusScriptDelegates(t *testing.T) {
	cfg := t.TempDir()
	stub := filepath.Join(t.TempDir(), "stub.sh")
	// Echoes a marker plus the model from the same stdin, proving it was piped through.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\necho STUB-FOOTER\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, DelegateFileName), []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runStatusScript(t, cfg, sampleStatusInput); got != "STUB-FOOTER\n" {
		t.Fatalf("output = %q", got)
	}
	assertHarvested(t, cfg)
}

func TestStatusScriptDelegateGetsStdin(t *testing.T) {
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, DelegateFileName), []byte(`grep -o Opus`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runStatusScript(t, cfg, sampleStatusInput); got != "Opus\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestStatusScriptFailingDelegateFallsBack(t *testing.T) {
	cfg := t.TempDir()
	for name, cmd := range map[string]string{"nonzero": "exit 3", "empty": "true", "missing": "/no/such/script"} {
		if err := os.WriteFile(filepath.Join(cfg, DelegateFileName), []byte(cmd), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := runStatusScript(t, cfg, sampleStatusInput); got != "Opus 5.5 | ~/proj | effort high\n" {
			t.Fatalf("%s: output = %q", name, got)
		}
	}
}

func TestStatusScriptSelfReferencingDelegateFallsBack(t *testing.T) {
	cfg := t.TempDir()
	// Points back at an agent-box statusline script (the name check) ...
	self := filepath.Join(t.TempDir(), "statusline-ratelimits.sh")
	if err := os.WriteFile(self, statusline.Script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, DelegateFileName), []byte(self), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runStatusScript(t, cfg, sampleStatusInput); got != "Opus 5.5 | ~/proj | effort high\n" {
		t.Fatalf("output = %q", got)
	}
	assertHarvested(t, cfg)
}

func TestStatusScriptToleratesWrongJSONTypes(t *testing.T) {
	cases := map[string]string{
		`{"model":"Opus 5.5","effort":"high","workspace":"x","cwd":"/work/proj"}`: "Opus 5.5 | ~/proj\n",
		`{"model":7,"effort":[1],"workspace":[],"cwd":"/work/proj"}`:              "unknown | ~/proj\n",
		`{"model":{"display_name":"Opus 5.5"},"effort":{"level":3},"cwd":5}`:      "Opus 5.5 | ~/ | effort 3\n",
	}
	for in, want := range cases {
		if got := runStatusScript(t, t.TempDir(), in); got != want {
			t.Fatalf("%s\n got %q want %q", in, got, want)
		}
	}
}

func TestStatusScriptKeepsDelegateOutputOnNonZeroExit(t *testing.T) {
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, DelegateFileName), []byte("cat >/dev/null; echo PARTIAL-FOOTER; exit 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runStatusScript(t, cfg, sampleStatusInput); got != "PARTIAL-FOOTER\n" {
		t.Fatalf("output = %q", got)
	}
}
