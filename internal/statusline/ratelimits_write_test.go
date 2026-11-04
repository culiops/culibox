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

// runIn execs the embedded script with cfg as CLAUDE_CONFIG_DIR and input on stdin.
func runIn(t *testing.T, cfg, input string) { t.Helper(); runInEnv(t, cfg, input) }

// runInEnv is runIn with extra environment entries (they override inherited ones).
func runInEnv(t *testing.T, cfg, input string, env ...string) {
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
	cmd.Stdin = strings.NewReader(input)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\nstderr: %s", err, errb.String())
	}
}

func readSnap(t *testing.T, cfg string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg, "agent-box-rate-limits.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, b)
	}
	return m
}

const noLimitsInput = `{"model":{"id":"claude-opus-4-5","display_name":"Opus"},"cwd":"/work/proj"}`

func TestScriptWritesNothingWithoutRateLimits(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, noLimitsInput)
	if _, err := os.Stat(filepath.Join(cfg, "agent-box-rate-limits.json")); err == nil {
		t.Fatal("wrote a snapshot that has neither five_hour nor seven_day")
	}
}

func TestScriptKeepsGoodSnapshotWhenLimitsAbsent(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, sampleInput)
	runIn(t, cfg, noLimitsInput) // a session's first render, before any API reply
	assertHarvested(t, cfg)
}

func TestScriptIgnoresEmptyWindowObjects(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, `{"rate_limits":{"five_hour":{},"seven_day":null}}`)
	if _, err := os.Stat(filepath.Join(cfg, "agent-box-rate-limits.json")); err == nil {
		t.Fatal("wrote a snapshot from empty window objects")
	}
}

func TestScriptReplacesSnapshotAtomically(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, sampleInput)
	path := filepath.Join(cfg, "agent-box-rate-limits.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	runIn(t, cfg, sampleInput)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// An in-place open(path, "w") keeps the inode and empties the file first;
	// a temp file + rename gives the new snapshot a new inode.
	if os.SameFile(before, after) {
		t.Fatal("snapshot was rewritten in place, not replaced by rename")
	}
	ents, _ := os.ReadDir(cfg)
	for _, e := range ents {
		if e.Name() != "agent-box-rate-limits.json" {
			t.Errorf("leftover file in config dir: %s", e.Name())
		}
	}
}

// The snapshot holds account usage, not something other users need: it is
// created owner-only, whatever the umask.
func TestScriptSnapshotIsOwnerOnly(t *testing.T) {
	cfg := t.TempDir()
	runIn(t, cfg, sampleInput)
	fi, err := os.Stat(filepath.Join(cfg, "agent-box-rate-limits.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("snapshot mode = %o, want 600", perm)
	}
}

// Boxes can share a config dir across PID namespaces, so two writers can have
// the same pid. A temp file named only by pid would then collide, and the script
// would delete the other writer's file. Here a python3 shim plants a file named
// for the interpreter's own pid (exec keeps the pid), the worst case for a
// pid-only name: the snapshot must still be written and the planted file left.
func TestScriptTempNameDoesNotCollideWithAnotherWritersPid(t *testing.T) {
	real, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	cfg := t.TempDir()
	bin := t.TempDir()
	shim := "#!/bin/sh\n: > \"$CLAUDE_CONFIG_DIR/agent-box-rate-limits.json.$$.tmp\"\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	runInEnv(t, cfg, sampleInput, "PATH="+bin+":"+os.Getenv("PATH"))
	assertHarvested(t, cfg)
	ents, _ := os.ReadDir(cfg)
	planted := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			planted++
		}
	}
	if planted != 1 {
		t.Fatalf("the other writer's temp file was removed or ours was left behind: %d .tmp files", planted)
	}
}
