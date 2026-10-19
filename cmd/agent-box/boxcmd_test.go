package main

import (
	"bytes"
	"context"
	"fmt"
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
// A pull of a ref in pullOK succeeds and makes that image present (ref ->
// "arch|hash"); a pull of any other ref exits 1 as a registry refusal.
func setupDocker(t *testing.T, images map[string]string, pullOK ...map[string]string) (boximage.Docker, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "docker")
	pulls := map[string]string{}
	for _, m := range pullOK {
		for ref, out := range m {
			pulls[ref] = out
		}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" +
		"case \"$*\" in\n*version*) echo arm64; exit 0;;\n\"build \"*) cat >/dev/null; exit 0;;\nesac\n" +
		"last=\"\"; for a; do last=$a; done\n" +
		"case \"$1\" in\npull)\n  case \"$last\" in\n"
	i := 0
	for ref := range pulls {
		script += fmt.Sprintf("    %s) touch %s/pulled-%d; echo pulled; exit 0;;\n", ref, dir, i)
		i++
	}
	script += "    *) echo 'Error response from daemon: denied' >&2; exit 1;;\n  esac;;\nesac\ncase \"$last\" in\n"
	refs := map[string]bool{}
	for ref := range images {
		refs[ref] = true
	}
	for ref := range pulls {
		refs[ref] = true
	}
	idx := map[string]int{}
	i = 0
	for ref := range pulls {
		idx[ref] = i
		i++
	}
	for ref := range refs {
		script += "  " + ref + ")\n"
		if out, ok := pulls[ref]; ok {
			script += fmt.Sprintf("    if [ -f %s/pulled-%d ]; then echo '%s'; exit 0; fi\n", dir, idx[ref], out)
		}
		if out, ok := images[ref]; ok {
			script += "    echo '" + out + "'; exit 0\n"
		}
		script += "    echo \"Error: No such image: $last\" >&2; exit 1;;\n"
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

func pullLines(log string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, "pull ") {
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

var remoteRef = "ghcr.io/culiops/agent-box@sha256:" + strings.Repeat("a", 64)

func TestBoxSetupRemoteMissingPulls(t *testing.T) {
	d, log := setupDocker(t, nil, map[string]string{remoteRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if p := pullLines(argvLog(t, log)); len(p) != 1 || p[0] != "pull --platform linux/arm64 "+remoteRef {
		t.Fatalf("pull lines: %v", p)
	}
	if len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a remote image was built: %s", argvLog(t, log))
	}
	if want := "PASS image: pulled " + remoteRef + " (linux/arm64)"; !strings.Contains(w.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, w.String())
	}
}

func TestBoxSetupRemoteTagWarnsMutable(t *testing.T) {
	tagRef := "ghcr.io/culiops/agent-box:base"
	d, _ := setupDocker(t, nil, map[string]string{tagRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, tagRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if want := "WARN image: " + tagRef + " is a mutable tag; pin box_image by digest (ghcr.io/culiops/agent-box@sha256:…)"; !strings.Contains(w.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, w.String())
	}
}

func TestBoxSetupRemoteDigestDoesNotWarnMutable(t *testing.T) {
	d, _ := setupDocker(t, nil, map[string]string{remoteRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if strings.Contains(w.String(), "mutable tag") {
		t.Fatalf("a digest ref was called a mutable tag:\n%s", w.String())
	}
}

func TestBoxSetupRemotePullFailureNeverBuilds(t *testing.T) {
	d, log := setupDocker(t, nil)
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a failed pull fell back to a build: %s", argvLog(t, log))
	}
	if !strings.Contains(w.String(), "FAIL image:") || !strings.Contains(w.String(), "denied") {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxSetupRemotePulledWrongArchFails(t *testing.T) {
	d, _ := setupDocker(t, nil, map[string]string{remoteRef: "amd64|" + boximage.Hash()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if !strings.Contains(w.String(), "FAIL image:") || !strings.Contains(w.String(), "pin the multi-arch index digest") {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxSetupRemoteCurrentDoesNotPull(t *testing.T) {
	d, log := setupDocker(t, map[string]string{remoteRef: current()}, map[string]string{remoteRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(pullLines(argvLog(t, log))) != 0 || len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a current remote image was pulled or built: %s", argvLog(t, log))
	}
	if !strings.Contains(w.String(), "PASS image: "+remoteRef+" is current") {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxSetupRemoteStaleWarnsAndDoesNotPull(t *testing.T) {
	d, log := setupDocker(t, map[string]string{remoteRef: "arm64|old"}, map[string]string{remoteRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(pullLines(argvLog(t, log))) != 0 || len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a stale remote image was pulled or built: %s", argvLog(t, log))
	}
	if !strings.Contains(w.String(), "WARN image:") || !strings.Contains(w.String(), "pull a newer digest from ghcr") {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxSetupRemoteRebuildRefused(t *testing.T) {
	d, log := setupDocker(t, map[string]string{remoteRef: current()}, map[string]string{remoteRef: current()})
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, remoteRef, true, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(pullLines(argvLog(t, log))) != 0 || len(buildLines(argvLog(t, log))) != 0 {
		t.Fatalf("a refused rebuild still ran docker: %s", argvLog(t, log))
	}
	if want := "FAIL image: " + remoteRef + " comes from a registry; agent-box pulls it and never builds it"; !strings.Contains(w.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, w.String())
	}
}

func TestBoxSetupLocalNameNeverPulls(t *testing.T) {
	d, log := setupDocker(t, nil)
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, baseRef, false, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if len(buildLines(argvLog(t, log))) != 1 || len(pullLines(argvLog(t, log))) != 0 {
		t.Fatalf("want one build and no pull: %s", argvLog(t, log))
	}
}

// A missing pin must never be built in place, even though its dated tag is
// free: the pin counts as taken, so -rebuild takes the next suffix.
func TestBoxSetupMissingPinRebuildNeverBuildsThePin(t *testing.T) {
	d, log := setupDocker(t, nil)
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), d, datedRef, true, setupNow, &w); rc != 0 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	b := buildLines(argvLog(t, log))
	if len(b) != 1 || strings.Count(b[0], "-t ") != 1 || !strings.Contains(b[0], "-t "+datedRef+".2 -") {
		t.Fatalf("build argv: %v", b)
	}
	if strings.Contains(b[0], ":base") || strings.Contains(b[0], "-t "+datedRef+" ") {
		t.Fatalf("a missing pin was built in place: %s", b[0])
	}
	if !strings.Contains(w.String(), "stays on "+datedRef) {
		t.Fatalf("output: %s", w.String())
	}
}

func TestBoxStatusPinnedMissingDoesNotRepeatTheImage(t *testing.T) {
	d, _ := setupDocker(t, nil)
	var w bytes.Buffer
	if rc := boxImageStatus(context.Background(), d, datedRef, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if strings.Contains(w.String(), datedRef+": pinned") || !strings.HasPrefix(w.String(), "FAIL image: pinned box image "+datedRef) {
		t.Fatalf("output: %s", w.String())
	}
}
