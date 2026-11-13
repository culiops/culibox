package analytics

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/boxread"
)

var logLinePrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// A file name a box picks inside its own home, newline included, must reach the
// daemon log only quoted: every log line stays one the logger wrote.
func TestIngestBoxLogQuotesBoxChosenFileNames(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	base := t.TempDir()
	projects := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p")
	svc := boxSvc(t, base)

	// 1. a refused file (symlink), logged by the ingest error line
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	writeFile(t, outside, line1)
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projects, "a\nFORGED one.jsonl")); err != nil {
		t.Fatal(err)
	}

	// 2. a file that shrinks between passes
	shrink := filepath.Join(projects, "b\nFORGED two.jsonl")
	writeFile(t, shrink, line1+line2)
	// 3. an over-long unterminated line
	long := filepath.Join(projects, "c\nFORGED three.jsonl")
	writeFile(t, long, strings.Repeat("x", boxread.MaxLineBytes+10))

	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, shrink, line1)
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	for _, want := range []string{"shrank", "unterminated line", "ingest"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected a %q log line, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\nFORGED") {
		t.Fatalf("a box-chosen file name reached the log raw:\n%s", got)
	}
	for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !logLinePrefix.MatchString(l) {
			t.Fatalf("a log line without the logger's own prefix:\n%s", got)
		}
	}
}
