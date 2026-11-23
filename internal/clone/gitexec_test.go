package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// errAfter is a writer that fails once n bytes went through.
type errAfter struct {
	n   int
	got int
}

var errEnough = errors.New("enough")

func (e *errAfter) Write(p []byte) (int, error) {
	e.got += len(p)
	if e.got >= e.n {
		return len(p), errEnough
	}
	return len(p), nil
}

// gitProcsWith counts live processes whose command line holds marker (Linux /proc).
func gitProcsWith(t *testing.T, marker string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc")
	}
	n := 0
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && bytes.Contains(b, []byte(marker)) {
			n++
		}
	}
	return n
}

func TestExecGitStreamStopsOnWriterError(t *testing.T) {
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "big"), bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(mustRun(t, g, dir, "hash-object", "-w", "big")))
	// 50 MiB of output into a writer that gives up after 10 bytes: git must
	// not be left writing into a pipe nobody reads.
	in := strings.NewReader(strings.Repeat(id+"\n", 50))
	marker := fmt.Sprintf("user.name=stream-marker-%d", time.Now().UnixNano())
	start := time.Now()
	err = g.Stream(context.Background(), dir, CleanEnv(), in, &errAfter{n: 10}, hardened("-c", marker, "cat-file", "--batch")...)
	if !errors.Is(err, errEnough) {
		t.Fatalf("err = %v, want the writer's error", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Stream took %s to stop", d)
	}
	if n := gitProcsWith(t, marker); n != 0 {
		t.Errorf("%d git process(es) left running", n)
	}
}

// panicAfter is a writer that panics once n bytes went through, as a parser
// would on hostile bytes. os/exec calls it on its own copy goroutine.
type panicAfter struct{ n, got int }

func (p *panicAfter) Write(b []byte) (int, error) {
	p.got += len(b)
	if p.got >= p.n {
		panic("hostile bytes")
	}
	return len(b), nil
}

// The writer runs on os/exec's goroutine, not the caller's: a panic in it must
// come back as an error that stops git, never crash the process.
func TestExecGitStreamTurnsAWriterPanicIntoAnError(t *testing.T) {
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "big"), bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(mustRun(t, g, dir, "hash-object", "-w", "big")))
	in := strings.NewReader(strings.Repeat(id+"\n", 50))
	marker := fmt.Sprintf("user.name=panic-marker-%d", time.Now().UnixNano())
	start := time.Now()
	err = g.Stream(context.Background(), dir, CleanEnv(), in, &panicAfter{n: 10}, hardened("-c", marker, "cat-file", "--batch")...)
	if !errors.Is(err, errWriterPanicked) {
		t.Fatalf("err = %v, want errWriterPanicked", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Stream took %s to stop", d)
	}
	if n := gitProcsWith(t, marker); n != 0 {
		t.Errorf("%d git process(es) left running", n)
	}
}

func TestExecGitStreamDeliversOutputAndStderr(t *testing.T) {
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q")
	var out bytes.Buffer
	if err := g.Stream(context.Background(), dir, CleanEnv(), strings.NewReader("nonexistent\n"), &out, "cat-file", "--batch-check"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "missing") {
		t.Errorf("out = %q", out.String())
	}
	err = g.Stream(context.Background(), dir, CleanEnv(), nil, &out, "cat-file", "-p", "deadbeef")
	if err == nil || !strings.Contains(err.Error(), "git cat-file") {
		t.Errorf("err = %v, want a git failure naming the subcommand", err)
	}
	if _, ok := exitCode(err); !ok {
		t.Errorf("exit status not reachable with errors.As: %v", err)
	}
}

func TestExecGitStreamHonoursContext(t *testing.T) {
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Stream(ctx, dir, CleanEnv(), nil, &bytes.Buffer{}, "status"); err == nil {
		t.Error("a cancelled context ran git")
	}
}

func mustRun(t *testing.T, g ExecGit, dir string, args ...string) []byte {
	t.Helper()
	out, err := g.Run(context.Background(), dir, CleanEnv(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
