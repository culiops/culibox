package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
)

// The kept stores' audit rows reach the audit store under the store's profile
// with the actor the box layer was given: "culibox" for the keep pass, the
// person for a forget. With no store installed the hook refuses instead of
// dropping the row silently.
func TestBoxLayerOptionsAuditKeepRows(t *testing.T) {
	l := &boxLayer{goos: "linux"}
	o := l.options(config.Config{DockerBin: "/bin/sh"})
	if o.Audit == nil {
		t.Fatal("no Audit hook")
	}
	if err := o.Audit("culibox", "box_kept", "p", "paths=.culiops box=ab-box-0123456789ab"); err == nil {
		t.Fatal("no error with no audit store installed")
	}
	var got []broker.CredEvent
	l.setAudit(func(e broker.CredEvent) error { got = append(got, e); return nil })
	if err := o.Audit("culibox", "box_kept", "p", "paths=.culiops box=ab-box-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if err := o.Audit("web", "box_kept_forget", "p", "path=.culiops"); err != nil {
		t.Fatal(err)
	}
	want := []broker.CredEvent{
		{Action: "box_kept", Kind: "box", Name: "p", Actor: "culibox", Detail: "paths=.culiops box=ab-box-0123456789ab"},
		{Action: "box_kept_forget", Kind: "box", Name: "p", Actor: "web", Detail: "path=.culiops"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

const keptWireKey = "0123456789ab"

// keptWireHome makes box's home under the layer's state root, age old (its own
// mtime: no marker), with its kept-store record and a .culiops folder.
func keptWireHome(t *testing.T, l *boxLayer, box string, age time.Duration) string {
	t.Helper()
	dir := seedLayerHome(t, l, box, age)
	if err := os.MkdirAll(filepath.Join(dir, ".culiops"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".culiops", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	recs := filepath.Join(l.stateRoot, "boxstate", ".kept", ".boxes")
	if err := os.MkdirAll(recs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recs, box), []byte(`{"key":"`+keptWireKey+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// At boot (and after a VM start) the homes of boxes stopped while the daemon
// was down are passed, in the background: a young home, which reclaim leaves
// alone, still has its app folders kept.
func TestReconcileAndReclaimKeepsStoppedHomes(t *testing.T) {
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n") // every box is stopped
	home := keptWireHome(t, l, "ab-box-aaaaaaaaaaaa", 5*time.Minute)
	l.reconcileAndReclaim(context.Background())
	l.boxes.WaitKeptPasses()
	kept := filepath.Join(l.stateRoot, "boxstate", ".kept", keptWireKey, "home", ".culiops", "x")
	if b, err := os.ReadFile(kept); err != nil || string(b) != "x" {
		t.Fatalf("kept copy: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".culiops")); !os.IsNotExist(err) {
		t.Fatalf(".culiops still in the home: %v", err)
	}
}

// A home past the TTL that reclaim keeps because a folder to keep could not
// move is logged once, naming the folder quoted, never as reclaimed, and the
// conversations line does not claim it.
func TestReclaimBoxStateLogsHeldHomeOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failure cannot be simulated")
	}
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n")
	home := keptWireHome(t, l, "ab-box-aaaaaaaaaaaa", 40*24*time.Hour)
	// The store's home/ will not take the folder: the move fails.
	shome := filepath.Join(l.stateRoot, "boxstate", ".kept", keptWireKey, "home")
	if err := os.MkdirAll(shome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shome, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shome, 0o700) })
	out := capturedLog(t)
	ctx := context.Background()
	l.reclaimBoxState(ctx)
	l.reclaimBoxState(ctx)
	got := out.String()
	if n := strings.Count(got, "kept box state past"); n != 1 {
		t.Fatalf("the held home was logged %d times over two passes, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "ab-box-aaaaaaaaaaaa") || !strings.Contains(got, `".culiops"`) {
		t.Fatalf("the line must name the home and the folder, quoted:\n%s", got)
	}
	if strings.Contains(got, "reclaimed box state") || strings.Contains(got, "Claude Code conversations") {
		t.Fatalf("a held home was logged as reclaimed or as holding conversations:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".culiops", "x")); err != nil {
		t.Fatalf("the home went with its folder: %v", err)
	}
}
