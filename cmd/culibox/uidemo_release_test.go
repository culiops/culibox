//go:build !uidemo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// #235 item 6: no normal build carries the demo. The hook is nil here, and
// scripts/release/assert-no-uidemo.sh (run by build.sh on every release binary)
// fails on a binary built with the uidemo tag.
func TestNormalBuildHasNoUIDemo(t *testing.T) {
	if uidemoVMFolders != nil {
		t.Fatal("uidemoVMFolders is set in a build without the uidemo tag")
	}
}

// #343: the scan's base override is a demo hook too. A normal build never reads
// CULIBOX_WSROOTS_SCAN_BASES: the hook is nil even when the variable is set, and
// no file outside a uidemo-tagged one names it.
func TestNormalBuildIgnoresTheScanBasesVariable(t *testing.T) {
	t.Setenv("CULIBOX_WSROOTS_SCAN_BASES", "/tmp/should-never-be-read")
	if uidemoScanBases != nil {
		t.Fatal("uidemoScanBases is set in a build without the uidemo tag")
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "uidemo_wsroots.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "CULIBOX_WSROOTS_SCAN_BASES") {
			t.Errorf("%s reads CULIBOX_WSROOTS_SCAN_BASES outside the uidemo file", f)
		}
	}
	dirs, err := filepath.Glob("../../internal/*/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range dirs {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "CULIBOX_WSROOTS_SCAN_BASES") {
			t.Errorf("%s reads CULIBOX_WSROOTS_SCAN_BASES", f)
		}
	}
}

func TestReleaseAssertRejectsAUIDemoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two small programs")
	}
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(gobin); err != nil {
		t.Skip("no go tool next to the test")
	}
	dir := t.TempDir()
	build := func(name string, tags ...string) string {
		out := filepath.Join(dir, name)
		args := []string{"build", "-o", out}
		if len(tags) > 0 {
			args = append(args, "-tags", strings.Join(tags, ","))
		}
		args = append(args, "../../tools/release-status")
		cmd := exec.Command(gobin, args...)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", name, err, b)
		}
		return out
	}
	plain, demo, other := build("plain"), build("demo", "uidemo"), build("other", "netgo")
	script, err := filepath.Abs("../../scripts/release/assert-no-uidemo.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(bins ...string) error {
		cmd := exec.Command("sh", append([]string{script}, bins...)...)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(gobin)+string(os.PathListSeparator)+os.Getenv("PATH"))
		b, err := cmd.CombinedOutput()
		t.Logf("%v: %s", filepath.Base(bins[0]), b)
		return err
	}
	if err := run(plain, other); err != nil {
		t.Errorf("a normal build was refused: %v", err)
	}
	if err := run(demo); err == nil {
		t.Error("a uidemo build was accepted")
	}
	if err := run(plain, demo); err == nil {
		t.Error("a uidemo build among normal ones was accepted")
	}
	if err := run(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file was accepted")
	}
	if err := exec.Command("sh", script).Run(); err == nil {
		t.Error("no argument was accepted")
	}
}
