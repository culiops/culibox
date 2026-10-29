package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/egress"
)

func openRetentionAudit(t *testing.T, oldRows int) *egress.Audit {
	t.Helper()
	a, err := egress.OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	old := time.Now().Add(-200 * 24 * time.Hour)
	for i := 0; i < oldRows; i++ {
		if err := a.Record(egress.Row{Time: old, SessionID: "s", Host: "h.example", Port: 443, Outcome: egress.OutcomeAllowed}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Record(egress.Row{Time: time.Now(), SessionID: "s", Host: "n.example", Port: 443, Outcome: egress.OutcomeAllowed}); err != nil {
		t.Fatal(err)
	}
	return a
}

func countRows(t *testing.T, a *egress.Audit) int {
	t.Helper()
	rows, err := a.ForSession("s", 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// days == 0 means keep forever: startRetention returns at once and deletes nothing.
func TestRetentionZeroKeepsAll(t *testing.T) {
	a := openRetentionAudit(t, 5)
	done := make(chan struct{})
	go func() {
		startRetention(context.Background(), a, 0, time.Millisecond, func(string, ...any) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startRetention(days=0) did not return at once")
	}
	if got := countRows(t, a); got != 6 {
		t.Fatalf("rows = %d, want 6 (nothing deleted)", got)
	}
	startRetention(context.Background(), nil, 90, time.Millisecond, func(string, ...any) {}) // nil audit: must return
}

func TestRetentionPrunesOnStartAndOnTick(t *testing.T) {
	a := openRetentionAudit(t, 5)
	var mu sync.Mutex
	var logs []string
	logf := func(f string, args ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, args...)); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { startRetention(ctx, a, 90, 20*time.Millisecond, logf); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for countRows(t, a) != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countRows(t, a); got != 1 {
		t.Fatalf("rows = %d, want 1 (the run at start prunes)", got)
	}
	// A row that ages past the cutoff after start is removed on a later tick.
	if err := a.Record(egress.Row{Time: time.Now().Add(-120 * 24 * time.Hour), SessionID: "s", Host: "late.example", Port: 443, Outcome: egress.OutcomeAllowed}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for countRows(t, a) != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countRows(t, a); got != 1 {
		t.Fatalf("rows = %d, want 1 after a tick", got)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	want := "audit: pruned 5 egress rows older than 90 days"
	found := false
	for _, l := range logs {
		if l == want {
			found = true
		}
		if strings.Contains(l, "pruned 0 ") {
			t.Errorf("a run that deleted nothing must not log: %q", l)
		}
	}
	if !found {
		t.Errorf("logs = %q, want one containing %q", logs, want)
	}
}

func TestPruneJobStopsOnCancel(t *testing.T) {
	a := openRetentionAudit(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { startRetention(ctx, a, 90, time.Hour, func(string, ...any) {}); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startRetention did not stop within 1s of cancel")
	}
}

// runRetention's stop waits for the goroutine, so the audit store can be closed after it.
func TestRunRetentionStopWaits(t *testing.T) {
	a := openRetentionAudit(t, 3)
	var mu sync.Mutex
	var logs []string
	stop := runRetention(a, 90, func(f string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(f, args...))
	})
	stop()
	// stop waits for the run, which either finished or never began; in both
	// cases it is over, so closing the store now fails nothing.
	if got := countRows(t, a); got != 1 && got != 4 {
		t.Fatalf("rows = %d, want 1 (pruned) or 4 (stopped before the run)", got)
	}
	a.Close()
	mu.Lock()
	for _, l := range logs {
		if strings.Contains(l, "failed") {
			t.Errorf("a prune failed: %q", l)
		}
	}
	mu.Unlock()
	runRetention(nil, 90, func(string, ...any) {})()
	runRetention(a, 0, func(string, ...any) {})()
}

// The prune log names the window in words: "1 day", not "1 days".
func TestRetentionDaysWording(t *testing.T) {
	for _, c := range []struct {
		days int
		want string
	}{{1, "1 day"}, {2, "2 days"}, {90, "90 days"}} {
		if got := retentionDays(c.days); got != c.want {
			t.Errorf("retentionDays(%d) = %q, want %q", c.days, got, c.want)
		}
	}
	a := openRetentionAudit(t, 2)
	var logs []string
	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled once the start run has logged, so only that run happens (a
	// cancelled ctx now stops a prune before it starts).
	startRetention(ctx, a, 1, time.Hour, func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)); cancel() })
	want := "audit: pruned 2 egress rows older than 1 day"
	if len(logs) != 1 || logs[0] != want {
		t.Errorf("logs = %q, want [%q]", logs, want)
	}
}

// #24: the Audit footer states the policy the job really runs. No egress audit
// store (egress control off or failed) means nothing prunes.
func TestRetentionInEffect(t *testing.T) {
	a := openRetentionAudit(t, 0)
	if got := retentionInEffect(a, 90); got != 90 {
		t.Errorf("egress on, 90 days = %d, want 90", got)
	}
	if got := retentionInEffect(nil, 90); got != 0 {
		t.Errorf("egress off, 90 days = %d, want 0 (nothing prunes)", got)
	}
	if got := retentionInEffect(a, 0); got != 0 {
		t.Errorf("egress on, keep forever = %d, want 0", got)
	}
}
