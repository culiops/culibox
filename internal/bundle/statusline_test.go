package bundle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/culiops/agent-box/internal/statusline"
)

const bundleStatusLineCommand = "AGENTBOX_STATUSLINE_DELEGATE_FILE=/opt/agent-bundle/statusline-delegate /opt/agent-bundle/statusline-ratelimits.sh"

func writeSettings(t *testing.T, src, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func bundledStatusLine(t *testing.T, b *Built) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.Dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	sl, _ := m["statusLine"].(map[string]any)
	return sl
}

func TestBundleShipsStatuslineScript(t *testing.T) {
	b := buildForTest(t, t.TempDir())
	p := filepath.Join(b.Dir, statusline.Name)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, statusline.Script) {
		t.Fatal("bundled script differs from statusline.Script")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("script mode = %v, want 0755", fi.Mode().Perm())
	}
	if b.Lock.Files[statusline.Name] == "" {
		t.Fatalf("the script is not in Lock.Files: %v", b.Lock.Files)
	}
}

func TestBundleStatusLineAlwaysSet(t *testing.T) {
	src := t.TempDir() // no settings.json at all
	b := buildForTest(t, src)
	sl := bundledStatusLine(t, b)
	if sl["type"] != "command" || sl["command"] != bundleStatusLineCommand {
		t.Fatalf("statusLine = %v", sl)
	}
	if b.Lock.hasDrop("settings", "statusLine") {
		t.Fatalf("nothing to replace, so no drop: %+v", b.Lock.Dropped)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, delegateName)); !os.IsNotExist(err) {
		t.Fatalf("no delegate expected without a host statusLine: %v", err)
	}
}

func TestBundleWritesDelegate(t *testing.T) {
	src := t.TempDir()
	const host = "bash /home/u/.claude/statusline-command.sh --flag"
	writeSettings(t, src, `{"statusLine":{"type":"command","command":"`+host+`"}}`)
	b := buildForTest(t, src)
	p := filepath.Join(b.Dir, delegateName)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != host {
		t.Fatalf("delegate = %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Fatalf("delegate mode = %v, want 0644", fi.Mode().Perm())
	}
	if b.Lock.Files[delegateName] == "" {
		t.Fatalf("the delegate is not in Lock.Files: %v", b.Lock.Files)
	}
	sl := bundledStatusLine(t, b)
	if sl["command"] != bundleStatusLineCommand {
		t.Fatalf("host command leaked into the box settings: %v", sl)
	}
	var drop *Drop
	for i, d := range b.Lock.Dropped {
		if d.Kind == "settings" && d.Name == "statusLine" {
			drop = &b.Lock.Dropped[i]
		}
	}
	if drop == nil || drop.Reason != "replaced: the box runs agent-box's statusLine, which calls yours if it works in the box" {
		t.Fatalf("drop = %+v", drop)
	}
}

func TestBundleNoDelegateForOwnScript(t *testing.T) {
	src := t.TempDir()
	writeSettings(t, src, `{"statusLine":{"type":"command","command":"/home/u/agent-box-data/bin/statusline-ratelimits.sh"}}`)
	b := buildForTest(t, src)
	if _, err := os.Stat(filepath.Join(b.Dir, delegateName)); !os.IsNotExist(err) {
		t.Fatalf("a delegate pointing at our own script would recurse: %v", err)
	}
	if _, ok := b.Lock.Files[delegateName]; ok {
		t.Fatal("delegate must not be in Lock.Files")
	}
	if sl := bundledStatusLine(t, b); sl["command"] != bundleStatusLineCommand {
		t.Fatalf("statusLine = %v", sl)
	}
}

func TestBundleNoDelegateForOtherTypesOrBlank(t *testing.T) {
	for name, body := range map[string]string{
		"other type": `{"statusLine":{"type":"text","command":"x"}}`,
		"blank":      `{"statusLine":{"type":"command","command":"   "}}`,
		"not object": `{"statusLine":"/home/u/s.sh"}`,
	} {
		src := t.TempDir()
		writeSettings(t, src, body)
		b := buildForTest(t, src)
		if _, err := os.Stat(filepath.Join(b.Dir, delegateName)); !os.IsNotExist(err) {
			t.Errorf("%s: unexpected delegate: %v", name, err)
		}
		if sl := bundledStatusLine(t, b); sl["command"] != bundleStatusLineCommand {
			t.Errorf("%s: statusLine = %v", name, sl)
		}
		if !b.Lock.hasDrop("settings", "statusLine") {
			t.Errorf("%s: the replaced host statusLine must be a named drop", name)
		}
	}
}

func TestBundleLayoutBumped(t *testing.T) {
	if layoutMarker != "layout\x00v4\n" {
		t.Fatalf("layout marker = %q, want v4 so cached v3 bundles rebuild", layoutMarker)
	}
}
