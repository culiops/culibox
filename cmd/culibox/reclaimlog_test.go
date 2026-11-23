package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogOnChangeLogsARepeatedErrorOnce(t *testing.T) {
	var l logOnChange
	if !l.changed("a") {
		t.Fatal("the first error must be logged")
	}
	if l.changed("a") {
		t.Fatal("the same error again must not be logged")
	}
	if !l.changed("b") {
		t.Fatal("a different error must be logged")
	}
	if l.changed("") {
		t.Fatal("a clean pass logs nothing")
	}
	if !l.changed("b") {
		t.Fatal("an error that comes back after a clean pass must be logged again")
	}
}

// The VM-start pass and the hourly ticker can overlap.
func TestLogOnChangeIsSafeForConcurrentUse(t *testing.T) {
	var l logOnChange
	var wg sync.WaitGroup
	var mu sync.Mutex
	logged := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.changed("same") {
				mu.Lock()
				logged++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if logged != 1 {
		t.Fatalf("the same error was logged %d times by 50 concurrent passes, want 1", logged)
	}
}

// capturedLog returns what log.Printf writes until the test ends.
func capturedLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// seedLayerHome makes a box home under the layer's state root whose age (no
// marker, so the home's own mtime) is age.
func seedLayerHome(t *testing.T, l *boxLayer, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(l.stateRoot, "boxstate", name)
	if err := os.MkdirAll(filepath.Join(dir, ".npm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".npm", "blob"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A box home whose caches were pruned is named as such, not as a reclaimed home:
// the home stays, and the age is in minutes, not days.
func TestReclaimBoxStateLogsAPruneAsAPrune(t *testing.T) {
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n") // every box is gone
	dir := seedLayerHome(t, l, "ab-box-aaaaaaaaaaaa", 5*time.Hour)
	out := capturedLog(t)
	l.reclaimBoxState(context.Background())
	got := out.String()
	if !strings.Contains(got, "pruned caches of box state ab-box-aaaaaaaaaaaa: unused for 5h0m0s, 0 MiB freed") {
		t.Fatalf("no prune line in the log:\n%s", got)
	}
	if strings.Contains(got, "reclaimed box state") {
		t.Fatalf("a prune was logged as a reclaimed home:\n%s", got)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the home must stay: %v", err)
	}
}

// A failure that is still there an hour later must not be logged every hour, and
// must be logged again when it comes back after a clean pass.
func TestReclaimBoxStateLogsARepeatedErrorOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failure cannot be simulated")
	}
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n") // every box is gone
	dir := seedLayerHome(t, l, "ab-box-bbbbbbbbbbbb", 40*24*time.Hour)
	stuck := filepath.Join(dir, ".npm")
	// A directory this user cannot write to keeps its files, so the home stays.
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	out := capturedLog(t)
	ctx := context.Background()
	const line = "box state reclaim:"

	l.reclaimBoxState(ctx)
	l.reclaimBoxState(ctx)
	if n := strings.Count(out.String(), line); n != 1 {
		t.Fatalf("the same failure on two passes was logged %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out.String(), "ab-box-bbbbbbbbbbbb") {
		t.Fatalf("the error must name the box:\n%s", out)
	}

	// Fixed by hand: the next pass reclaims the home and the failure is over.
	if err := os.Chmod(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	l.reclaimBoxState(ctx)
	if strings.Contains(out.String(), line) || !strings.Contains(out.String(), "reclaimed box state ab-box-bbbbbbbbbbbb") {
		t.Fatalf("the clean pass must log the reclaim and no error:\n%s", out)
	}

	// The same failure on another home after that is news again.
	dir = seedLayerHome(t, l, "ab-box-cccccccccccc", 40*24*time.Hour)
	stuck = filepath.Join(dir, ".npm")
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	out.Reset()
	l.reclaimBoxState(ctx)
	if n := strings.Count(out.String(), line); n != 1 {
		t.Fatalf("a failure after a clean pass was logged %d times, want 1:\n%s", n, out)
	}
}

// A file a box names inside its own home, newline included, must not reach the
// daemon log through a failed reclaim: the log names the box and the errno.
var logPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

func TestReclaimBoxStateLogOmitsAgentChosenFileNames(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failure cannot be simulated")
	}
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n") // every box is gone
	const name = "ab-box-dddddddddddd"
	dir := seedLayerHome(t, l, name, 40*24*time.Hour)
	stuck := filepath.Join(dir, "sub")
	if err := os.MkdirAll(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "x\nFORGED daemon line 2099-01-01"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	// Adding sub moved the home's mtime, which is its age here: put it back.
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	out := capturedLog(t)

	l.reclaimBoxState(context.Background())

	got := out.String()
	if !strings.Contains(got, "box state reclaim:") || !strings.Contains(got, name) || !strings.Contains(got, "permission denied") {
		t.Fatalf("the log must name the failure, the box and the errno:\n%s", got)
	}
	if strings.Contains(got, "FORGED") || strings.Contains(got, "2099-01-01") {
		t.Fatalf("the log carries a file name chosen inside the home:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !logPrefix.MatchString(line) {
			t.Fatalf("a log line without the logger's own prefix (a raw newline got in):\n%s", got)
		}
	}
}

// #330: a home past the TTL that holds a conversation stays, and the log says so
// once, naming it, not on every hourly pass. A change in the kept set is news.
func TestReclaimBoxStateLogsKeptHomesOncePerChange(t *testing.T) {
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n") // every box is gone
	seedConv := func(name string) string {
		dir := seedLayerHome(t, l, name, 40*24*time.Hour)
		conv := filepath.Join(dir, ".claude", "projects", "-w")
		if err := os.MkdirAll(conv, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(conv, "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-40 * 24 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir := seedConv("ab-box-aaaaaaaaaaaa")
	out := capturedLog(t)
	ctx := context.Background()
	const line = "kept box state ab-box-aaaaaaaaaaaa (unused 40 days)"

	l.reclaimBoxState(ctx)
	l.reclaimBoxState(ctx)
	if n := strings.Count(out.String(), line); n != 1 {
		t.Fatalf("the kept home was logged %d times over two passes, want 1:\n%s", n, out)
	}
	if !strings.Contains(out.String(), "culibox doctor") {
		t.Fatalf("the line must say where to see and remove kept homes:\n%s", out)
	}
	if strings.Contains(out.String(), "reclaimed box state") {
		t.Fatalf("a kept home was logged as reclaimed:\n%s", out)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the home must stay: %v", err)
	}

	// A pass that cannot list the boxes reports nothing; that is not "nothing
	// kept", so the next clean pass does not log the same set again.
	d.set(t, "psfail", "")
	l.reclaimBoxState(ctx)
	if err := os.Remove(filepath.Join(d.dir, "psfail")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	l.reclaimBoxState(ctx)
	if strings.Contains(out.String(), "kept box state") {
		t.Fatalf("the same kept set was logged again after a failed pass:\n%s", out)
	}

	out.Reset()
	seedConv("ab-box-bbbbbbbbbbbb")
	l.reclaimBoxState(ctx)
	if got := out.String(); !strings.Contains(got, "ab-box-aaaaaaaaaaaa") || !strings.Contains(got, "ab-box-bbbbbbbbbbbb") {
		t.Fatalf("a new kept home must log the whole kept set again:\n%s", got)
	}
}

// A home that will not go errs on every pass; that must not hide the kept set.
func TestReclaimBoxStateLogsKeptHomesBesideAStuckHome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failure cannot be simulated")
	}
	l, _, d := darwinLayer(t, false)
	d.set(t, "inspect", "false\n")
	stuckHome := seedLayerHome(t, l, "ab-box-eeeeeeeeeeee", 40*24*time.Hour)
	stuck := filepath.Join(stuckHome, ".npm")
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	kept := seedLayerHome(t, l, "ab-box-ffffffffffff", 40*24*time.Hour)
	conv := filepath.Join(kept, ".claude", "projects", "-w")
	if err := os.MkdirAll(conv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conv, "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(kept, old, old); err != nil {
		t.Fatal(err)
	}
	out := capturedLog(t)
	l.reclaimBoxState(context.Background())
	got := out.String()
	if !strings.Contains(got, "box state reclaim:") || !strings.Contains(got, "kept box state ab-box-ffffffffffff") {
		t.Fatalf("want both the stuck home's error and the kept set:\n%s", got)
	}
}
