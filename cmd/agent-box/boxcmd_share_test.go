package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/vmshares"
)

func shareArgs(action string, path string) cliArgs {
	return cliArgs{Cmd: "box", Sub: "share", ShareAction: action, SharePath: path}
}

// otherDir is a folder outside the test layer's home and data root.
func otherDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/var/tmp", "shr")
	if err != nil {
		d = t.TempDir()
	} else {
		t.Cleanup(func() { os.RemoveAll(d) })
	}
	r, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBoxShareAddRefusedPrintsReason(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	rc, out := runVM(t, l, shareArgs("add", "~/.ssh"))
	if rc != 1 || !strings.Contains(out, "refusing: ") || !strings.Contains(out, "holds keys and sockets") {
		t.Fatalf("add ~/.ssh = %d:\n%s", rc, out)
	}
	if fc.called("stop") || fc.called("start") {
		t.Fatal("a share add must never touch the VM")
	}
}

func TestBoxShareListAddRmAreSavedNotApplied(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	dir := otherDir(t)
	rc, out := runVM(t, l, shareArgs("list", ""))
	if rc != 0 || !strings.Contains(out, "CuliBox data (") || !strings.Contains(out, "{boxstate}; ro: bundle, broker, ca)") {
		t.Fatalf("list = %d:\n%s", rc, out)
	}
	rc, out = runVM(t, l, cliArgs{Cmd: "box", Sub: "share", ShareAction: "add", SharePath: dir, ReadOnly: true})
	if rc != 0 || !strings.Contains(out, "ro  "+dir) || !strings.Contains(out, "not applied yet: run `agent-box box share apply` (restarts the box VM)") {
		t.Fatalf("add = %d:\n%s", rc, out)
	}
	list, _, err := vmshares.Load(l.dataDir)
	if err != nil || len(list) != 1 || list[0].Path != dir || list[0].Mode != vmshares.RO {
		t.Fatalf("saved list = %+v, %v", list, err)
	}
	rc, out = runVM(t, l, cliArgs{Cmd: "box", Sub: "share", ShareAction: "add", SharePath: filepath.Join(dir, "sub"), ReadOnly: true})
	if rc != 0 || !strings.Contains(out, "already covered by "+dir+"; nothing to add") {
		t.Fatalf("covered add = %d:\n%s", rc, out)
	}
	rc, out = runVM(t, l, shareArgs("list", ""))
	if rc != 0 || !strings.Contains(out, "changes not applied yet: run agent-box box share apply") {
		t.Fatalf("list after add = %d:\n%s", rc, out)
	}
	rc, out = runVM(t, l, shareArgs("rm", dir))
	if rc != 0 || strings.Contains(out, "ro  "+dir) {
		t.Fatalf("rm = %d:\n%s", rc, out)
	}
	rc, out = runVM(t, l, shareArgs("rm", dir))
	if rc != 1 || !strings.Contains(out, "refusing: ") {
		t.Fatalf("rm of an unlisted folder = %d:\n%s", rc, out)
	}
	if fc.called("stop") || fc.called("start") {
		t.Fatal("list, add and rm must never touch the VM")
	}
}

// pendingLayer is a running, set-up VM whose saved list differs from what it
// has applied, so an apply has something to do. Its fake mounts are what the
// applied list will mount.
func pendingLayer(t *testing.T) (*boxLayer, *fakeColima, *fakeDocker) {
	t.Helper()
	l, fc, d := applyLayer(t)
	dir := otherDir(t)
	if err := vmshares.Save(l.dataDir, []vmshares.Share{{Path: dir, Mode: vmshares.RW}}); err != nil {
		t.Fatal(err)
	}
	next := l.vm.Config()
	next.Shares = []vmshares.Share{{Path: dir, Mode: vmshares.RW}}
	var mb strings.Builder
	for i, m := range next.Mounts() {
		mode := "ro"
		if m.Writable {
			mode = "rw"
		}
		mb.WriteString("mount" + string(rune('a'+i)) + " " + m.Location + " virtiofs " + mode + ",relatime 0 0\n")
	}
	fc.mounts = mb.String()
	return l, fc, d
}

func TestBoxShareApplyRefusesLiveBoxesUnlessForced(t *testing.T) {
	l, fc, d := pendingLayer(t)
	d.set(t, "ps", "ab-box-1\trunning\t/Users/a/p\t2026-10-15 10:00:00 +0000 UTC\nab-box-2\texited\t/Users/a/q\t\n")
	rc, out := runVM(t, l, shareArgs("apply", ""))
	if rc != 1 || !strings.Contains(out, "refusing: 1 box(es) running") || !strings.Contains(out, "ab-box-1") {
		t.Fatalf("apply with a running box = %d:\n%s", rc, out)
	}
	if fc.called("stop") || fc.called("start") {
		t.Fatal("a refused apply must not touch the VM")
	}
	a := shareArgs("apply", "")
	a.Force = true
	rc, out = runVM(t, l, a)
	if rc != 0 {
		t.Fatalf("forced apply = %d:\n%s", rc, out)
	}
	for _, want := range []string{"stop… done (", "start… done (", "check… done ("} {
		if !strings.Contains(out, want) {
			t.Errorf("apply output lacks %q:\n%s", want, out)
		}
	}
	stop, start := -1, -1
	for i, c := range fc.calls {
		switch {
		case strings.HasPrefix(c, "stop "):
			stop = i
		case strings.HasPrefix(c, "start "):
			start = i
		case strings.HasPrefix(c, "delete"):
			t.Fatal("an apply must never delete the VM")
		}
	}
	if stop < 0 || start < stop {
		t.Fatalf("want stop then start, calls = %v", fc.calls)
	}
}

func TestBoxShareApplyFailureIsPrinted(t *testing.T) {
	l, fc, _ := pendingLayer(t)
	fc.mounts = "" // the check after the start finds the folders missing
	a := shareArgs("apply", "")
	a.Force = true
	rc, out := runVM(t, l, a)
	if rc != 1 || !strings.Contains(out, "check… failed: ") || !strings.Contains(out, "detail: ") {
		t.Fatalf("failed apply = %d:\n%s", rc, out)
	}
}

func TestBoxSetupMountsOnlyDriftApplies(t *testing.T) {
	l, fc, d := pendingLayer(t)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	d.set(t, "probe", blockedProbe)
	d.set(t, "context", "default\n")
	fc.running = true
	d.set(t, "ps", "ab-box-1\trunning\t/Users/a/p\t2026-10-15 10:00:00 +0000 UTC\n")
	// A running box: the same guard as share apply.
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 1 || !strings.Contains(out, "refusing: 1 box(es) running") {
		t.Fatalf("setup with a running box = %d:\n%s", rc, out)
	}
	if fc.called("stop") || fc.called("start") || fc.called("delete") {
		t.Fatal("a refused setup must not touch the VM")
	}
	rc, out = runVM(t, l, cliArgs{Sub: "setup", Force: true})
	if rc != 0 {
		t.Fatalf("setup = %d:\n%s", rc, out)
	}
	if !fc.called("stop") || !fc.called("start") || fc.called("delete") {
		t.Fatalf("want stop and start, never delete: %v", fc.calls)
	}
	if k, err := l.vm.DriftKind(); err != nil || k != 0 {
		t.Fatalf("drift after setup = %v %v", k, err)
	}
}

func TestBoxShareOnLinux(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("the Linux refusal")
	}
	var buf bytes.Buffer
	rc := runBox(shareArgs("list", ""), config.Config{}, t.TempDir(), &buf)
	if rc != 1 || !strings.Contains(buf.String(), "agent-box box share: only on macOS, where boxes run in agent-box's own VM") {
		t.Fatalf("share on Linux = %d:\n%s", rc, buf.String())
	}
}
