package broker

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
)

const ackCaller = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_0123456789abcdef"

func wideResult(patterns ...string) awsprobe.GateResult {
	r := awsprobe.GateResult{State: awsprobe.GateNotReadOnly, RoleARN: ssoRoleARN, Caller: ackCaller}
	for _, p := range patterns {
		r.Refused = append(r.Refused, awsprobe.Verdict{Pattern: p, Why: "not a read verb"})
	}
	return r
}

// ackedSetup is a broker serving session s-1 with a named sso_direct
// connection "ro" holding an acknowledgement of acked (nil: none).
func ackedSetup(t *testing.T, g *fakeGate, acked *awsprobe.GateResult) (*Broker, *fakeMinter, *memRec) {
	t.Helper()
	b, fm, rec, _ := ssoDirectSetup(t, g)
	conn := config.AWSConnection{Name: "ro", AWSBinding: *ssoDirectBinding()}
	if acked != nil {
		a := awsprobe.NewAck(*acked, "alice", time.Unix(1700000000, 0).UTC())
		conn.FullAccessAck = &a
	}
	sv := live()
	sv.AWS = nil
	sv.AWSConns = []config.AWSConnection{conn}
	b.lookup = withTestBox(func(string) (SessionView, bool) { return sv, true })
	return b, fm, rec
}

func callRO(b *Broker) *httptest.ResponseRecorder { return callBody(b, tokA("s-1"), `{"name":"ro"}`) }

func TestServeSSODirectAckedNotReadOnlyMints(t *testing.T) {
	res := wideResult("s3:Put*")
	b, _, rec := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{res}}, &res)
	w := callRO(b)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	r := lastRow(rec)
	if r.Outcome != OutcomeMinted || r.Mode != config.AWSModeSSODirect || r.RoleARN != ssoRoleARN ||
		!strings.Contains(r.FullAccess, "s3:Put*") || r.Entry != "ro" {
		t.Fatalf("row %+v", r)
	}
}

func TestServeSSODirectAckStaleRefuses(t *testing.T) {
	acked := wideResult("s3:Put*")
	now := wideResult("s3:Put*", "iam:Create*")
	b, _, rec := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{now}}, &acked)
	w := callRO(b)
	var body struct{ Error string }
	decode(t, w, &body)
	if w.Code != http.StatusForbidden || body.Error != boxNeedsAck || strings.Contains(w.Body.String(), "ASIAFAKE") ||
		strings.Contains(w.Body.String(), "iam:") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	r := lastRow(rec)
	if r.Outcome != OutcomeDeniedNotReadOnly || !strings.HasPrefix(r.Error, "not covered by the full-access acknowledgement") ||
		r.FullAccess != "" || !strings.Contains(r.Error, "iam:Create*") {
		t.Fatalf("row %+v", r)
	}
	b.mu.Lock()
	n := len(b.cache)
	b.mu.Unlock()
	if n != 0 {
		t.Fatal("a refused credential was cached")
	}
}

func TestServeSSODirectEligibleWithoutAckSaysAcknowledge(t *testing.T) {
	b, _, rec := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{wideResult("s3:Put*")}}, nil)
	w := callRO(b)
	var body struct{ Error string }
	decode(t, w, &body)
	if w.Code != http.StatusForbidden || body.Error != boxNeedsAck ||
		!strings.HasPrefix(lastRow(rec).Error, "eligible for a full-access acknowledgement") {
		t.Fatalf("status %d body %s row %+v", w.Code, w.Body, lastRow(rec))
	}
}

func TestServeSSODirectAckNeverCoversTransient(t *testing.T) {
	acked := wideResult("s3:Put*")
	for _, reason := range []awsprobe.Reason{awsprobe.ReasonSSOExpired, awsprobe.ReasonTimeout, awsprobe.ReasonError,
		awsprobe.ReasonSimulateDenied, awsprobe.ReasonAssumeDenied, awsprobe.ReasonProbeDisabled} {
		res := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: reason, Caller: ackCaller}
		b, _, rec := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{res}}, &acked)
		w := callRO(b)
		var body struct{ Error string }
		decode(t, w, &body)
		if w.Code != http.StatusServiceUnavailable || body.Error != boxUnverified || lastRow(rec).Outcome != OutcomeDeniedNotReadOnly {
			t.Fatalf("%s: status %d body %s", reason, w.Code, w.Body)
		}
	}
}

func TestServeSSODirectAckIdentityRefusal(t *testing.T) {
	acked := wideResult("s3:Put*")
	res := wideResult("account")
	b, _, _ := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{res}}, &acked)
	w := callRO(b)
	var body struct{ Error string }
	decode(t, w, &body)
	if w.Code != http.StatusForbidden || body.Error != boxNotReadOnly {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}

func TestServeSSODirectReadOnlyUnderAckHasNoFullAccessMark(t *testing.T) {
	acked := wideResult("s3:Put*")
	ro := readOnly()
	ro.Caller = ackCaller
	b, _, rec := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{ro}}, &acked)
	if w := callRO(b); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if r := lastRow(rec); r.Outcome != OutcomeMinted || r.FullAccess != "" {
		t.Fatalf("row %+v", r)
	}
}

func TestServeSSODirectLegacyBindingIgnoresAck(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{wideResult("s3:Put*")}}
	b, _, rec, _ := ssoDirectSetup(t, g)
	w := call(b, tokA("s-1"))
	var body struct{ Error string }
	decode(t, w, &body)
	if w.Code != http.StatusForbidden || body.Error != boxNotReadOnly || strings.Contains(lastRow(rec).Error, "acknowledg") {
		t.Fatalf("status %d body %s row %+v", w.Code, w.Body, lastRow(rec))
	}
}

func TestServeSSODirectAckAuditWriteFailureWithholds(t *testing.T) {
	res := wideResult("s3:Put*")
	b, _, rec := ackedSetup(t, &fakeGate{results: []awsprobe.GateResult{res}}, &res)
	rec.mu.Lock()
	rec.recErr = http.ErrAbortHandler
	rec.mu.Unlock()
	w := callRO(b)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestAuditFullAccessColumnMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(m3aSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO credential_audit (ts, session_id, outcome) VALUES (1, 'old', 'minted')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for i := 0; i < 2; i++ {
		a, err := OpenAudit(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := a.Record(Row{Time: time.Unix(2, 0), SessionID: "new", Outcome: OutcomeMinted, FullAccess: "not read-only: s3:Put*: x"}); err != nil {
				t.Fatal(err)
			}
		}
		old, _ := a.ForSession("old", 10)
		nw, err := a.ForSession("new", 10)
		if len(old) != 1 || old[0].FullAccess != "" || err != nil || len(nw) != 1 || nw[0].FullAccess != "not read-only: s3:Put*: x" {
			t.Fatalf("old %+v new %+v %v", old, nw, err)
		}
		a.Close()
	}
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
}
