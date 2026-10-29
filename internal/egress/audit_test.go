package egress

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
)

func TestAuditRoundTripAndCoexistsWithBroker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	b, err := broker.OpenAudit(path) // test-only import: proves the shared file works
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1792166400, 0)
	if err := b.Record(broker.Row{Time: now, SessionID: "s1", Outcome: broker.OutcomeMinted}); err != nil {
		t.Fatal(err)
	}
	r := Row{Time: now, SessionID: "s1", Box: "ab-box-1", Host: "api.anthropic.com", Port: 443,
		Outcome: OutcomeAllowed, BytesUp: 10, BytesDown: 20, Duration: 1500 * time.Millisecond}
	if err := a.Record(r); err != nil {
		t.Fatal(err)
	}
	got, err := a.ForSession("s1", 10)
	if err != nil || len(got) != 1 || got[0].Host != r.Host || got[0].Duration != r.Duration || got[0].BytesDown != 20 {
		t.Fatalf("ForSession = %+v, %v", got, err)
	}
	// The broker's table is untouched by the egress writes, and vice versa.
	if br, err := b.ForSession("s1", 10); err != nil || len(br) != 1 {
		t.Fatalf("broker rows = %+v, %v", br, err)
	}
}

func TestAuditOrderLimitIsolationAndFields(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	base := time.Unix(1792166400, 0)
	rec := func(sid, host string, off int, o Outcome, sup int) {
		t.Helper()
		if err := a.Record(Row{Time: base.Add(time.Duration(off) * time.Second), SessionID: sid, Host: host, Port: 443, Outcome: o, Suppressed: sup}); err != nil {
			t.Fatal(err)
		}
	}
	rec("s1", "a.example", 0, OutcomeAllowed, 0)
	rec("s1", "b.example", 5, OutcomeDenied, 7)
	rec("s1", "c.example", 5, OutcomeDialError, 0) // same second: later insert first
	rec("s2", "other.example", 1, OutcomeBadMethod, 0)
	rec("", "", 2, OutcomeBadAuth, 0) // unauthenticated

	got, err := a.ForSession("s1", 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("ForSession = %+v, %v", got, err)
	}
	if got[0].Host != "c.example" || got[1].Host != "b.example" || got[2].Host != "a.example" {
		t.Fatalf("order = %s,%s,%s", got[0].Host, got[1].Host, got[2].Host)
	}
	if got[1].Outcome != OutcomeDenied || got[1].Suppressed != 7 || got[1].Port != 443 || !got[1].Time.Equal(base.Add(5*time.Second)) {
		t.Fatalf("fields lost: %+v", got[1])
	}
	if lim, _ := a.ForSession("s1", 2); len(lim) != 2 {
		t.Fatalf("limit: got %d rows", len(lim))
	}
	if un, _ := a.ForSession("", 10); len(un) != 1 || un[0].Outcome != OutcomeBadAuth {
		t.Fatalf("unauthenticated rows = %+v", un)
	}
	if none, err := a.ForSession("nobody", 10); err != nil || len(none) != 0 {
		t.Fatalf("unknown session = %+v, %v", none, err)
	}
}

// The host is attacker-controlled text (the CONNECT target). Whatever the
// proxy hands in, the store keeps at most 253 printable bytes.
func TestAuditSanitisesHost(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	long := strings.Repeat("a", 400)
	for _, h := range []string{"evil\r\nX-Injected: 1.example", "nul\x00byte\x7f.example", "caf\xc3\xa9.example", long} {
		if err := a.Record(Row{Time: time.Unix(1792166400, 0), SessionID: "s", Host: h, Outcome: OutcomeDenied}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.ForSession("s", 10)
	if err != nil || len(got) != 4 {
		t.Fatalf("rows = %+v, %v", got, err)
	}
	for _, r := range got {
		if len(r.Host) > 253 {
			t.Errorf("host not truncated: %d bytes", len(r.Host))
		}
		for i := 0; i < len(r.Host); i++ {
			if c := r.Host[i]; c < 0x20 || c > 0x7e {
				t.Errorf("non-printable byte %#x in %q", c, r.Host)
			}
		}
	}
}

func TestSessionIDsDistinct(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, sid := range []string{"a-1", "a-1", "b-2"} {
		if err := a.Record(Row{Time: time.Unix(100, 0), SessionID: sid, Host: "h", Port: 443, Outcome: OutcomeAllowed}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.SessionIDs()
	if err != nil || len(got) != 2 || got[0] != "a-1" || got[1] != "b-2" {
		t.Fatalf("SessionIDs = %v, %v", got, err)
	}
}

func TestPruneBatchesAndKeepsOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	b, err := broker.OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1792166400, 0)
	old, recent := now.Add(-100*24*time.Hour), now.Add(-time.Hour)
	for i := 0; i < 2500; i++ {
		if err := a.Record(Row{Time: old.Add(time.Duration(i) * time.Second), SessionID: "s1", Host: "old.example", Port: 443, Outcome: OutcomeAllowed}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		if err := a.Record(Row{Time: recent, SessionID: "s1", Host: "new.example", Port: 443, Outcome: OutcomeAllowed}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := b.Record(broker.Row{Time: old, SessionID: "s1", Outcome: broker.OutcomeMinted}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := a.Prune(context.Background(), now.Add(-90*24*time.Hour), 1000)
	if err != nil || n != 2500 {
		t.Fatalf("Prune = %d, %v; want 2500", n, err)
	}
	got, err := a.ForSession("s1", 100)
	if err != nil || len(got) != 10 {
		t.Fatalf("egress rows left = %d, %v; want 10", len(got), err)
	}
	for _, r := range got {
		if r.Host != "new.example" {
			t.Fatalf("an old row survived: %+v", r)
		}
	}
	if br, err := b.ForSession("s1", 100); err != nil || len(br) != 3 {
		t.Fatalf("broker rows = %d, %v; want 3 (Prune must touch egress only)", len(br), err)
	}
	if n, err := a.Prune(context.Background(), now.Add(-90*24*time.Hour), 1000); err != nil || n != 0 {
		t.Fatalf("second Prune = %d, %v; want 0", n, err)
	}
}

// #24: shutdown cancels a prune mid-backlog; it must stop within one batch and
// say why, not run on until the store is closed under it.
func TestPruneStopsOnCancel(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	old := time.Now().Add(-200 * 24 * time.Hour)
	for i := 0; i < 2000; i++ {
		if err := a.Record(Row{Time: old, SessionID: "s", Host: "h.example", Port: 443, Outcome: OutcomeAllowed}); err != nil {
			t.Fatal(err)
		}
	}
	cut := time.Now().Add(-90 * 24 * time.Hour)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := a.Prune(done, cut, 100); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("Prune(cancelled) = %d, %v; want 0, context.Canceled", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(3*pruneGap/2, cancel) // inside the second gap
	start := time.Now()
	n, err := a.Prune(ctx, cut, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prune err = %v; want context.Canceled", err)
	}
	if n == 0 || n >= 2000 {
		t.Fatalf("Prune deleted %d; want some but not all of 2000", n)
	}
	if el := time.Since(start); el > 10*pruneGap {
		t.Fatalf("Prune took %s after cancel; want it to stop within a batch", el)
	}
}

// Prune on a closed store reports an error rather than panicking.
func TestPruneAfterClose(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if _, err := a.Prune(context.Background(), time.Now(), 10); err == nil {
		t.Fatal("Prune after Close = nil error")
	}
}
