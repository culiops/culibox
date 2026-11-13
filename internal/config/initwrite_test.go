package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func planParsed(t *testing.T, existing []byte, e InitEdit) (map[string]json.RawMessage, []string, []byte) {
	t.Helper()
	out, skipped, err := PlanInitWrite(existing, e)
	if err != nil {
		t.Fatalf("PlanInitWrite: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("output is not a JSON object: %v\n%s", err, out)
	}
	return top, skipped, out
}

func rawString(t *testing.T, top map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(top[key], &s); err != nil {
		t.Fatalf("key %q: %v (raw %s)", key, err, top[key])
	}
	return s
}

func agentsOf(t *testing.T, top map[string]json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var as []map[string]json.RawMessage
	if err := json.Unmarshal(top["agents"], &as); err != nil {
		t.Fatalf("agents: %v", err)
	}
	return as
}

func compactStr(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestPlanInitWriteNewFileSeedsDefaults(t *testing.T) {
	top, skipped, out := planParsed(t, nil, InitEdit{DefaultCwd: "/w"})
	if got := rawString(t, top, "bind_addr"); got != "127.0.0.1:8899" {
		t.Errorf("bind_addr = %q", got)
	}
	as := agentsOf(t, top)
	if len(as) != 1 || rawString(t, as[0], "name") != "claude" || rawString(t, as[0], "bin") != "claude" {
		t.Errorf("agents = %v", as)
	}
	if _, ok := as[0]["account"]; ok {
		t.Errorf("no account was asked for, yet the entry has one")
	}
	if got := rawString(t, top, "default_runtime"); got != "box" {
		t.Errorf("default_runtime = %q", got)
	}
	if got := rawString(t, top, "default_cwd"); got != "/w" {
		t.Errorf("default_cwd = %q", got)
	}
	if _, ok := top["box_vm_cpus"]; ok {
		t.Errorf("box_vm_cpus present with VMCPUs 0")
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
	if !bytes.HasSuffix(out, []byte("}\n")) {
		t.Errorf("output must end with a newline: %q", out)
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestPlanInitWriteKeepsUnknownKeysAndAgents(t *testing.T) {
	existing := []byte(`{
 "bind_addr": "127.0.0.1:8899",
 "x_custom": {"a":1,  "b":"<&>"},
 "allowed_hosts": ["box.example"],
 "agents": [
  {"name":"claude","bin":"/opt/claude","args":["--x"]},
  {"name":"shell","bin":"bash"}
 ]
}`)
	top, skipped, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	if compactStr(t, top["x_custom"]) != `{"a":1,"b":"<&>"}` {
		t.Errorf("x_custom changed: %s", top["x_custom"])
	}
	if !strings.Contains(string(top["allowed_hosts"]), "box.example") {
		t.Errorf("allowed_hosts lost: %s", top["allowed_hosts"])
	}
	as := agentsOf(t, top)
	if len(as) != 2 {
		t.Fatalf("agents = %v", as)
	}
	if rawString(t, as[0], "account") != "claude-box" {
		t.Errorf("claude account = %s", as[0]["account"])
	}
	if rawString(t, as[0], "bin") != "/opt/claude" || compactStr(t, as[0]["args"]) != `["--x"]` {
		t.Errorf("claude entry's other keys changed: %v", as[0])
	}
	if _, ok := as[1]["account"]; ok {
		t.Errorf("shell agent got an account")
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestPlanInitWriteRespectsOtherAccount(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"claude","account":"work"}]}`)
	top, skipped, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	as := agentsOf(t, top)
	if rawString(t, as[0], "account") != "work" {
		t.Errorf("account = %s, want work", as[0]["account"])
	}
	if len(skipped) != 1 || skipped[0] != "claude" {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestPlanInitWriteWrapperNamedClaudeIsTheClaudeAgent(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"/opt/bin/claude-wrap"}]}`)
	top, skipped, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	as := agentsOf(t, top)
	if len(as) != 1 {
		t.Fatalf("a second claude agent was appended: %v", as)
	}
	if rawString(t, as[0], "account") != "claude-box" || rawString(t, as[0], "bin") != "/opt/bin/claude-wrap" {
		t.Errorf("entry = %v", as[0])
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestPlanInitWriteWrapperNamedClaudeOnOtherAccountIsSkipped(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"/opt/bin/claude-wrap","account":"work"}]}`)
	top, skipped, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	as := agentsOf(t, top)
	if len(as) != 1 || rawString(t, as[0], "account") != "work" {
		t.Fatalf("agents = %v", as)
	}
	if len(skipped) != 1 || skipped[0] != "claude" {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestPlanInitWriteSameAccountNotReported(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"claude","account":"claude-box"}]}`)
	_, skipped, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	if len(skipped) != 0 {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestPlanInitWriteNoAccountLeavesAgentsAlone(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"claude","account":"work"}]}`)
	top, skipped, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w"})
	if rawString(t, agentsOf(t, top)[0], "account") != "work" || len(skipped) != 0 {
		t.Errorf("agents %s skipped %v", top["agents"], skipped)
	}
}

func TestPlanInitWriteAppendsClaudeAgent(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"shell","bin":"bash"}]}`)
	top, _, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	as := agentsOf(t, top)
	if len(as) != 2 {
		t.Fatalf("agents = %v", as)
	}
	c := as[1]
	if rawString(t, c, "name") != "claude" || rawString(t, c, "bin") != "claude" || rawString(t, c, "account") != "claude-box" {
		t.Errorf("appended entry = %v", c)
	}
}

func TestPlanInitWriteAppendedClaudeHasNoAccountKeyWhenNoneAsked(t *testing.T) {
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"shell","bin":"bash"}]}`)
	top, _, _ := planParsed(t, existing, InitEdit{DefaultCwd: "/w"})
	if _, ok := agentsOf(t, top)[1]["account"]; ok {
		t.Errorf("empty account written")
	}
}

func TestPlanInitWriteIdempotent(t *testing.T) {
	e := InitEdit{DefaultCwd: "/w", VMCPUs: 4, VMMemoryGiB: 6, Account: "claude-box"}
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","x_custom":[1,2],"agents":[{"name":"claude","bin":"claude"}]}`)
	first, _, err := PlanInitWrite(existing, e)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := PlanInitWrite(first, e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("not idempotent:\n%s\n---\n%s", first, second)
	}
	norm, err := NormalizeJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(norm, first) {
		t.Errorf("NormalizeJSON of the output differs:\n%s\n---\n%s", first, norm)
	}
}

func TestPlanInitWriteInvalidRefused(t *testing.T) {
	existing := []byte(`{"bind_addr":"0.0.0.0:1","agents":[{"name":"claude","bin":"claude"}]}`)
	out, _, err := PlanInitWrite(existing, InitEdit{DefaultCwd: "/w"})
	if err == nil {
		t.Fatalf("want a validation error, got output %s", out)
	}
	if out != nil {
		t.Errorf("output returned with an error: %s", out)
	}
}

func TestPlanInitWriteMalformedExistingRefused(t *testing.T) {
	for _, in := range []string{"", "{", "[]", "null", `{"agents":{}}`} {
		if _, _, err := PlanInitWrite([]byte(in), InitEdit{DefaultCwd: "/w"}); err == nil {
			t.Errorf("existing %q: want an error", in)
		}
	}
}

func TestPlanInitWriteVMKeysOnlyWhenSet(t *testing.T) {
	top, _, _ := planParsed(t, nil, InitEdit{DefaultCwd: "/w", VMCPUs: 4, VMMemoryGiB: 4})
	if string(top["box_vm_cpus"]) != "4" || string(top["box_vm_memory_gib"]) != "4" {
		t.Errorf("vm keys = %s / %s", top["box_vm_cpus"], top["box_vm_memory_gib"])
	}
	existing := []byte(`{"bind_addr":"127.0.0.1:8899","box_vm_cpus":8,"box_vm_memory_gib":16,"agents":[{"name":"claude","bin":"claude"}]}`)
	top, _, _ = planParsed(t, existing, InitEdit{DefaultCwd: "/w"})
	if string(top["box_vm_cpus"]) != "8" || string(top["box_vm_memory_gib"]) != "16" {
		t.Errorf("zero must leave the keys: %s / %s", top["box_vm_cpus"], top["box_vm_memory_gib"])
	}
}

func TestNormalizeJSONMatchesPlanForUnchangedFile(t *testing.T) {
	existing := []byte(`{"z":1,"bind_addr":"127.0.0.1:8899","agents":[{"bin":"claude","name":"claude","account":"claude-box"}],"default_runtime":"box","default_cwd":"/w"}`)
	out, _, err := PlanInitWrite(existing, InitEdit{DefaultCwd: "/w", Account: "claude-box"})
	if err != nil {
		t.Fatal(err)
	}
	norm, err := NormalizeJSON(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, norm) {
		t.Errorf("a no-op edit must equal the normalized file:\n%s\n---\n%s", out, norm)
	}
}

func TestWriteFileAtomicRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("new")); err == nil {
		t.Fatal("want a refusal for a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "orig" {
		t.Errorf("target was modified: %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Errorf("stray files left: %v", ents)
	}
}

func TestWriteFileAtomicMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := filepath.Join(t.TempDir(), "sub", "cfg")
	p := filepath.Join(dir, "config.json")
	if err := WriteFileAtomic(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v", fi, err)
	}
	// overwrite an existing 0644 file: the result is 0600 with the new content
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(b) != "two" || fi.Mode().Perm() != 0o600 {
		t.Errorf("content %q mode %v", b, fi.Mode())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("stray files left: %v", ents)
	}
}

func TestWriteFileAtomicRemovesTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.Mkdir(p, 0o700); err != nil { // rename of a file over a directory fails
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("x")); err == nil {
		t.Fatal("want an error")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("temp file left behind: %v", ents)
	}
}
