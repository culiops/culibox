package web

import (
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
)

const ackCaller = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Example_0000000000000000"

func wideGate(patterns ...string) awsprobe.GateResult {
	g := readOnlyGate()
	g.State = awsprobe.GateNotReadOnly
	g.Caller = ackCaller
	for _, p := range patterns {
		g.Refused = append(g.Refused, awsprobe.Verdict{Pattern: p, Why: "allows writes"})
	}
	return g
}

func ackBody(conn, refused string) string {
	return strings.TrimSuffix(conn, "}") + `,"ack_full_access":{"refused":` + refused + `}}`
}

func credEventsOf(a *awsRoutes, action string) []broker.CredEvent {
	a.fx.rec.mu.Lock()
	defer a.fx.rec.mu.Unlock()
	var out []broker.CredEvent
	for _, e := range a.fx.rec.events {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func TestSaveSSODirectAckSavesNotReadOnly(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`))
	if code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	ack := a.fx.file(t).AWSConnections[0].FullAccessAck
	if ack == nil || ack.Actor != "web" || ack.Caller != ackCaller || ack.State != awsprobe.GateNotReadOnly || ack.At.IsZero() {
		t.Fatalf("stored ack %+v", ack)
	}
	evs := credEventsOf(a, broker.CredFullAccessAck)
	if len(evs) != 1 || !strings.Contains(evs[0].Detail, "iam:*") || evs[0].Name != "ro" {
		t.Fatalf("audit %+v", evs)
	}
}

func TestSaveSSODirectAckMissingPermsAndContext(t *testing.T) {
	t.Run("missing_permissions", func(t *testing.T) {
		a := newAWSRoutes(t)
		g := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms,
			Caller: ackCaller, MissingPerms: []string{"iam:GetRolePolicy"}}
		a.svc.gate = g
		body := strings.TrimSuffix(ssoConnJSON("ro"), "}") + `,"ack_full_access":{"missing_perms":["iam:GetRolePolicy"]}}`
		if code, m, _ := a.call("POST", pathAWS, body); code != 201 {
			t.Fatalf("%d %v", code, m)
		}
		if ack := a.fx.file(t).AWSConnections[0].FullAccessAck; ack == nil || ack.Reason != awsprobe.ReasonMissingPerms {
			t.Fatalf("%+v", ack)
		}
	})
	t.Run("missing_context", func(t *testing.T) {
		a := newAWSRoutes(t)
		g := wideGate("s3:putobject")
		g.State, g.Reason = awsprobe.GateUnverified, awsprobe.ReasonMissingContext
		a.svc.gate = g
		if code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["s3:putobject"]`)); code != 201 {
			t.Fatalf("%d %v", code, m)
		}
		if ack := a.fx.file(t).AWSConnections[0].FullAccessAck; ack == nil || ack.Reason != awsprobe.ReasonMissingContext {
			t.Fatalf("%+v", ack)
		}
	})
}

func TestSaveSSODirectAckSimulateDenied(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	a := newAWSRoutes(t)
	g := readOnlyGate()
	g.State, g.Reason, g.Caller, g.PolicyDigest = awsprobe.GateUnverified, awsprobe.ReasonSimulateDenied, ackCaller, digest
	a.svc.gate = g
	body := strings.TrimSuffix(ssoConnJSON("ro"), "}") + `,"ack_full_access":{}}`
	code, m, _ := a.call("POST", pathAWS, body)
	if code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	ack := a.fx.file(t).AWSConnections[0].FullAccessAck
	if ack == nil || ack.Reason != awsprobe.ReasonSimulateDenied || ack.PolicyDigest != digest {
		t.Fatalf("%+v", ack)
	}
	// without an ack it is refused as eligible
	b := newAWSRoutes(t)
	b.svc.gate = g
	code, m, _ = b.call("POST", pathAWS, ssoConnJSON("ro"))
	if code != 409 || m["ack_eligible"] != true {
		t.Fatalf("%d %v", code, m)
	}
}

func TestSaveSSODirectAckRefusesTransient(t *testing.T) {
	for _, r := range []awsprobe.Reason{awsprobe.ReasonSSOExpired, awsprobe.ReasonTimeout, awsprobe.ReasonError,
		awsprobe.ReasonAssumeDenied, awsprobe.ReasonProbeDisabled, awsprobe.ReasonNotChecked} {
		a := newAWSRoutes(t)
		a.svc.gate = awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: r, Caller: ackCaller, RoleARN: readOnlyGate().RoleARN,
			MissingPerms: []string{"iam:GetRolePolicy"}}
		body := strings.TrimSuffix(ssoConnJSON("ro"), "}") + `,"ack_full_access":{"missing_perms":["iam:GetRolePolicy"]}}`
		code, m, _ := a.call("POST", pathAWS, body)
		if code != 409 || m["code"] != "unverified" || m["ack_eligible"] != false {
			t.Errorf("%s: %d %v", r, code, m)
		}
		if a.fx.fileBytes(t) != "" || len(credEventsOf(a, broker.CredFullAccessAck)) != 0 {
			t.Errorf("%s: something was written", r)
		}
	}
}

func TestSaveSSODirectAckStaleWhenGateWiderThanSeen(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*", "ec2:*")
	code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`))
	if code != 409 || m["ack_stale"] != true || m["ack_eligible"] != true || m["code"] != "not_read_only" {
		t.Fatalf("%d %v", code, m)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("something was written")
	}
}

func TestSaveSSODirectIgnoresClientAckRecord(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	body := `{"connection":{"name":"ro","mode":"sso_direct","source_profile":"p","region":"us-east-1",` +
		`"full_access_ack":{"state":"not_read_only","caller":"` + ackCaller + `","refused":["iam:*"],"at":"2026-01-01T00:00:00Z","actor":"forged"}}}`
	code, m, _ := a.call("POST", pathAWS, body)
	if code != 409 || m["ack_eligible"] != true {
		t.Fatalf("%d %v", code, m)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("a forged ack was saved")
	}
}

func TestSaveSSODirectForgedAckNeverReplacesServerOne(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	body := `{"connection":{"name":"ro","mode":"sso_direct","source_profile":"p","region":"us-east-1",` +
		`"full_access_ack":{"state":"not_read_only","caller":"forged-caller","refused":["iam:*"],"at":"2026-01-01T00:00:00Z","actor":"forged"}},` +
		`"ack_full_access":{"refused":["iam:*"]}}`
	if code, m, _ := a.call("POST", pathAWS, body); code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	ack := a.fx.file(t).AWSConnections[0].FullAccessAck
	if ack == nil || ack.Actor != "web" || ack.Caller != ackCaller {
		t.Fatalf("%+v", ack)
	}
}

func TestSaveSSODirectIdentityRefusalNotAckable(t *testing.T) {
	a := newAWSRoutes(t)
	g := wideGate("account")
	a.svc.gate = g
	code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["account"]`))
	if code != 409 || m["ack_eligible"] != false {
		t.Fatalf("%d %v", code, m)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("written")
	}
}

func TestSaveSSODirectEditKeepsCoveringAck(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	if code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`)); code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	first := *a.fx.file(t).AWSConnections[0].FullAccessAck
	put := `{"connection":{"name":"ro","mode":"sso_direct","source_profile":"p","region":"us-east-1","label":"Wide"}}`
	if code, m, _ := a.call("PUT", pathAWS+"/ro", put); code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	got := a.fx.file(t).AWSConnections[0]
	if got.FullAccessAck == nil || !got.FullAccessAck.At.Equal(first.At) || got.FullAccessAck.Actor != first.Actor || got.Label != "Wide" {
		t.Fatalf("%+v", got)
	}
	if n := len(credEventsOf(a, broker.CredFullAccessAck)); n != 1 {
		t.Fatalf("an edit re-audited the ack: %d", n)
	}
}

func TestSaveSSODirectEditProfileChangeNeedsNewAck(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	if code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`)); code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	put := `{"connection":{"name":"ro","mode":"sso_direct","source_profile":"other","region":"us-east-1"}}`
	code, m, _ := a.call("PUT", pathAWS+"/ro", put)
	if code != 409 || m["ack_eligible"] != true {
		t.Fatalf("%d %v", code, m)
	}
	if a.fx.file(t).AWSConnections[0].SourceProfile != "p" {
		t.Fatal("the edit was saved")
	}
}

func TestSaveSSODirectEditWiderGateWithoutAckRefused(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	if code, _, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`)); code != 201 {
		t.Fatal(code)
	}
	a.svc.gate = wideGate("iam:*", "ec2:*")
	code, m, _ := a.call("PUT", pathAWS+"/ro", ssoConnJSON("ro"))
	if code != 409 || m["ack_stale"] != true {
		t.Fatalf("%d %v", code, m)
	}
}

func TestSaveSSODirectReadOnlyClearsAck(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	if code, _, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`)); code != 201 {
		t.Fatal(code)
	}
	a.svc.gate = readOnlyGate()
	if code, m, _ := a.call("PUT", pathAWS+"/ro", ssoConnJSON("ro")); code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	if a.fx.file(t).AWSConnections[0].FullAccessAck != nil {
		t.Fatal("the ack survived a read-only verdict")
	}
	evs := credEventsOf(a, broker.CredFullAccessAck)
	if len(evs) != 2 || !strings.Contains(evs[1].Detail, "cleared: read_only") {
		t.Fatalf("%+v", evs)
	}
}

type failOnAction struct {
	inner  CredRecorder
	action string
}

func (f failOnAction) RecordCred(e broker.CredEvent) error {
	if e.Action == f.action {
		return errAuditDown
	}
	return f.inner.RecordCred(e)
}

var errAuditDown = &auditDownErr{}

type auditDownErr struct{}

func (*auditDownErr) Error() string { return "audit down" }

func TestSaveSSODirectAckAuditFailureWritesNothing(t *testing.T) {
	a := newAWSRoutes(t)
	a.fx.cm.rec = failOnAction{inner: a.fx.rec, action: broker.CredFullAccessAck}
	a.svc.gate = wideGate("iam:*")
	code, _, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`))
	if code != 500 {
		t.Fatalf("%d", code)
	}
	if a.fx.fileBytes(t) != "" || a.fx.applyCount() != 0 {
		t.Fatal("something was saved")
	}
}

func TestCheckSSODirectReportsAckEligible(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*")
	code, m, _ := a.call("POST", pathAWS+"/check", ssoCheckBody)
	g, _ := m["gate"].(map[string]any)
	if code != 200 || m["ack_eligible"] != true || g["caller"] != ackCaller || g["role_arn"] == "" {
		t.Fatalf("%d %v", code, m)
	}
	a.svc.gate = awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonSSOExpired}
	_, m, _ = a.call("POST", pathAWS+"/check", ssoCheckBody)
	if m["ack_eligible"] != false {
		t.Fatalf("%v", m)
	}
	a.svc.gate = readOnlyGate()
	_, m, _ = a.call("POST", pathAWS+"/check", ssoCheckBody)
	if m["ack_eligible"] != false {
		t.Fatalf("%v", m)
	}
}

// A not_read_only result whose simulation the permission set could not run
// saves under an ack pinned to the policy digest. One cut short by a
// transient failure is refused as not eligible, even with every pattern
// listed.
func TestSaveSSODirectAckPartialRun(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Run("simulate_denied", func(t *testing.T) {
		a := newAWSRoutes(t)
		g := wideGate("iam:*")
		g.Incomplete, g.PolicyDigest = awsprobe.ReasonSimulateDenied, digest
		a.svc.gate = g
		code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`))
		if code != 201 {
			t.Fatalf("%d %v", code, m)
		}
		ack := a.fx.file(t).AWSConnections[0].FullAccessAck
		if ack == nil || ack.Incomplete != awsprobe.ReasonSimulateDenied || ack.PolicyDigest != digest {
			t.Fatalf("%+v", ack)
		}
	})
	for _, r := range []awsprobe.Reason{awsprobe.ReasonTimeout, awsprobe.ReasonError, awsprobe.ReasonSSOExpired} {
		t.Run(string(r), func(t *testing.T) {
			a := newAWSRoutes(t)
			g := wideGate("iam:*")
			g.Incomplete, g.PolicyDigest = r, digest
			a.svc.gate = g
			code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("ro"), `["iam:*"]`))
			gm, _ := m["gate"].(map[string]any)
			if code != 409 || m["ack_eligible"] != false || gm["incomplete"] != string(r) {
				t.Fatalf("%d %v", code, m)
			}
			if a.fx.fileBytes(t) != "" || len(credEventsOf(a, broker.CredFullAccessAck)) != 0 {
				t.Fatal("something was written")
			}
		})
	}
}

// The 409 of a run cut short says to check again, not that the set is
// simply not read-only.
func TestErrSSOGatePartialRunText(t *testing.T) {
	g := wideGate("iam:*")
	g.Incomplete = awsprobe.ReasonTimeout
	if s := (ErrSSOGate{Gate: g}).Error(); !strings.Contains(s, "the check stopped early (timed out)") || !strings.Contains(s, "check again") {
		t.Errorf("transient: %q", s)
	}
	g.Incomplete = awsprobe.ReasonSimulateDenied
	if s := (ErrSSOGate{Gate: g, Eligible: true}).Error(); strings.Contains(s, "check again") || !strings.Contains(s, "role cannot simulate itself") {
		t.Errorf("simulate_denied: %q", s)
	}
}
