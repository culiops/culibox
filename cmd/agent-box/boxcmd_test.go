package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
)

var setupNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

// setupDocker is a fake docker for `box setup`: a daemon of arch arm64 that
// holds the given images (ref -> "arch|hash"). It logs argv and never builds.
func setupDocker(t *testing.T, images map[string]string) (boximage.Docker, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" +
		"case \"$*\" in\n*version*) echo arm64; exit 0;;\n\"build \"*) cat >/dev/null; exit 0;;\nesac\n" +
		"last=\"\"; for a; do last=$a; done\ncase \"$last\" in\n"
	for ref, out := range images {
		script += "  " + ref + ") echo '" + out + "'; exit 0;;\n"
	}
	script += "  *) echo \"Error: No such image: $last\" >&2; exit 1;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return boximage.Docker{Bin: bin}, log
}

func argvLog(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return string(b)
}

func buildLines(log string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, "build ") {
			out = append(out, l)
		}
	}
	return out
}

const (
	repo     = "culiops/agent-box"
	baseRef  = repo + ":base"
	datedRef = repo + ":2026-10-18"
)

func current() string { return "arm64|" + boximage.Hash() }

func TestBoxSetupUnpinnedBuildsBaseAndDated(t *testing.T) {
	d, log := setupDocker(t, nil)
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, baseRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	b := buildLines(argvLog(t, log))
	if len(b) != 1 || !strings.Contains(b[0], "-t "+datedRef+" -t "+baseRef+" -") {
		t.Fatalf("build argv: %v", b)
	}
	for _, want := range []string{"PASS image: built " + datedRef, baseRef + " now points at " + datedRef,
		`pin this instance: set "box_image": "` + datedRef + `" in its config`} {
		if !strings.Contains(w.String(), want) {
			t.Errorf("output missing %q:\n%s", want, w.String())
		}
	}
}

func TestBoxSetupUnpinnedCurrentDoesNotBuild(t *testing.T) {
	d, log := setupDocker(t, map[string]string{baseRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, baseRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 || !strings.Contains(w.String(), "PASS image: "+baseRef+" is current") {
		t.Fatalf("log %q out %q", argvLog(t, log), w.String())
	}
}

func TestBoxSetupUnpinnedRebuildBuildsBaseAndDated(t *testing.T) {
	d, log := setupDocker(t, map[string]string{baseRef: current(), datedRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, baseRef, true, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	b := buildLines(argvLog(t, log))
	if len(b) != 1 || !strings.Contains(b[0], "-t "+datedRef+".2 -t "+baseRef+" -") {
		t.Fatalf("build argv: %v", b)
	}
}

func TestBoxSetupPinnedRebuildNeverMovesPinOrBase(t *testing.T) {
	d, log := setupDocker(t, map[string]string{datedRef: current(), baseRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, datedRef, true, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	b := buildLines(argvLog(t, log))
	if len(b) != 1 || strings.Count(b[0], "-t ") != 1 || !strings.Contains(b[0], "-t "+datedRef+".2 -") {
		t.Fatalf("build argv: %v", b)
	}
	if strings.Contains(b[0], ":base") || strings.Contains(b[0], "-t "+datedRef+" ") {
		t.Fatalf("a pinned rebuild moved the pin or :base: %s", b[0])
	}
	if !strings.Contains(w.String(), "stays on "+datedRef) || !strings.Contains(w.String(), `"box_image": "`+datedRef+`.2"`) {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxSetupMissingPinNeverBuilds(t *testing.T) {
	d, log := setupDocker(t, nil)
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, datedRef, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a missing pin was built: %s", argvLog(t, log))
	}
	if !strings.Contains(w.String(), "FAIL image:") || !strings.Contains(w.String(), "never rebuilds or pulls") {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxSetupWrongArchPinNeverBuilds(t *testing.T) {
	d, log := setupDocker(t, map[string]string{datedRef: "amd64|" + boximage.Hash()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, datedRef, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a wrong-arch pin was built: %s", argvLog(t, log))
	}
}

func TestBoxSetupPinnedCurrentIsPass(t *testing.T) {
	d, log := setupDocker(t, map[string]string{datedRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, datedRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 || !strings.Contains(w.String(), "PASS image: "+datedRef+" is current") {
		t.Fatalf("log %q out %q", argvLog(t, log), w.String())
	}
}

func TestBoxSetupPinnedStaleWarnsAndDoesNotBuild(t *testing.T) {
	d, log := setupDocker(t, map[string]string{datedRef: "arm64|old"})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, datedRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 || !strings.Contains(w.String(), "WARN image:") || !strings.Contains(w.String(), "pinned to "+datedRef) {
		t.Fatalf("log %q out %q", argvLog(t, log), w.String())
	}
}

func TestBoxSetupDigestRefCannotBeBuilt(t *testing.T) {
	d, log := setupDocker(t, nil)
	var w bytes.Buffer
	ref := repo + "@sha256:" + strings.Repeat("a", 64)
	if rc := boxSetupImage(context.Background(), d, ref, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 || !strings.Contains(w.String(), "FAIL image: box_image is a digest; agent-box cannot build it") {
		t.Fatalf("log %q out %q", argvLog(t, log), w.String())
	}
}
