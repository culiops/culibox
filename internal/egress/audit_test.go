package egress

import (
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
