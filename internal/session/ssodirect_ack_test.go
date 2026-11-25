package session

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

const ackTestCaller = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_abc"

func wideGate(patterns ...string) awsprobe.GateResult {
	r := awsprobe.GateResult{State: awsprobe.GateNotReadOnly, RoleARN: ssoTestRole, Caller: ackTestCaller, CheckedAt: time.Now()}
	for _, p := range patterns {
		r.Refused = append(r.Refused, awsprobe.Verdict{Pattern: p, Why: "not a read verb"})
	}
	return r
}

func ackedConn(name string, acked awsprobe.GateResult) config.AWSConnection {
	c := ssoConn(name)
	a := awsprobe.NewAck(acked, "alice", time.Unix(1700000000, 0).UTC())
	c.FullAccessAck = &a
	return c
}

func spawnConn(t *testing.T, c config.AWSConnection, res awsprobe.GateResult) (*Manager, *fakeBoxes, *Session, error) {
	t.Helper()
	m, boxes, _ := ssoConnManager(t, connLauncherFor(c), map[string]ssoVerdict{c.Name: {res: res, exp: time.Now().Add(time.Hour)}})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		t.Cleanup(func() { _ = m.Kill(s.ID) })
	}
	return m, boxes, s, err
}

func TestSpawnSSODirectAckedNotReadOnlyBox(t *testing.T) {
	res := wideGate("s3:Put*")
	_, boxes, s, err := spawnConn(t, ackedConn("ro", res), res)
	if err != nil {
		t.Fatal(err)
	}
	if boxes.wraps == 0 {
		t.Fatal("the box was never started")
	}
	cs := s.Info().AWSConnections
	if len(cs) != 1 || !cs[0].FullAccess || cs[0].BaseAccess != string(awsprobe.Writes) || cs[0].AccountID != "123456789012" {
		t.Fatalf("wire = %+v", cs)
	}
}

func TestSpawnSSODirectAckedNotReadOnlyHost(t *testing.T) {
	res := wideGate("s3:Put*")
	c := ackedConn("ro", res)
	hooks := &ssoHooks{fakeBrokerHooks: &fakeBrokerHooks{}, verdicts: map[string]ssoVerdict{"ro": {res: res, exp: time.Now().Add(time.Hour)}}}
	base, info, err := gateSSODirect(hooks, config.Bindings{AWSConns: []config.AWSConnection{c}}, "p", true)
	if err != nil {
		t.Fatal(err)
	}
	if base["ro"].State != awsprobe.Writes || base["ro"].CheckedAt.IsZero() || info["ro"].AccountID != "123456789012" {
		t.Fatalf("base %+v info %+v", base, info)
	}
	if awsprobe.NeedsRecheck(base, []string{"ro"}) != nil {
		t.Fatal("an acknowledged result is queued for a re-probe")
	}
}

func TestSpawnSSODirectAccountFallsBackToCaller(t *testing.T) {
	res := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms, Caller: ackTestCaller,
		MissingPerms: []string{"iam:GetRolePolicy"}, CheckedAt: time.Now()}
	_, _, s, err := spawnConn(t, ackedConn("ro", res), res)
	if err != nil {
		t.Fatal(err)
	}
	if cs := s.Info().AWSConnections; len(cs) != 1 || cs[0].AccountID != "123456789012" || !cs[0].FullAccess {
		t.Fatalf("wire = %+v", cs)
	}
}

func TestSpawnSSODirectStaleAckRefuses(t *testing.T) {
	acked := wideGate("s3:Put*")
	now := wideGate("s3:Put*", "iam:Create*")
	_, boxes, _, err := spawnConn(t, ackedConn("ro", acked), now)
	if err == nil || !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "acknowledge again") ||
		!strings.Contains(err.Error(), "iam:Create*") || boxes.wraps != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestSpawnSSODirectEligibleWithoutAckSaysAcknowledge(t *testing.T) {
	_, _, _, err := spawnConn(t, ssoConn("ro"), wideGate("s3:Put*"))
	if err == nil || !strings.Contains(err.Error(), "acknowledge full access on the Credentials screen") {
		t.Fatalf("err = %v", err)
	}
}

func simDeniedGate(digest string) awsprobe.GateResult {
	return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonSimulateDenied,
		RoleARN: ssoTestRole, Caller: ackTestCaller, PolicyDigest: digest, CheckedAt: time.Now()}
}

func TestSpawnSSODirectSimulateDenied(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	acked := simDeniedGate(digest)
	if _, _, _, err := spawnConn(t, ackedConn("ro", acked), acked); err != nil {
		t.Fatalf("matching ack: %v", err)
	}
	_, _, _, err := spawnConn(t, ackedConn("ro", acked), simDeniedGate(strings.Repeat("b", 64)))
	if err == nil || !strings.Contains(err.Error(), "acknowledge again") {
		t.Fatalf("stale: err = %v", err)
	}
	_, _, _, err = spawnConn(t, ssoConn("ro"), acked)
	if err == nil || !strings.Contains(err.Error(), "acknowledge full access on the Credentials screen") {
		t.Fatalf("eligible: err = %v", err)
	}
}

func TestSpawnSSODirectAckNeverCoversTransient(t *testing.T) {
	acked := wideGate("s3:Put*")
	for _, reason := range []awsprobe.Reason{awsprobe.ReasonSSOExpired, awsprobe.ReasonTimeout, awsprobe.ReasonError,
		awsprobe.ReasonAssumeDenied, awsprobe.ReasonProbeDisabled} {
		res := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: reason, Caller: ackTestCaller}
		_, _, _, err := spawnConn(t, ackedConn("ro", acked), res)
		if err == nil || strings.Contains(err.Error(), "acknowledge") {
			t.Fatalf("%s: err = %v", reason, err)
		}
	}
}

func TestSpawnSSODirectAckIdentityRefusal(t *testing.T) {
	acked := wideGate("s3:Put*")
	for _, p := range []string{"account", "role", "caller"} {
		_, _, _, err := spawnConn(t, ackedConn("ro", acked), wideGate(p))
		if err == nil || strings.Contains(err.Error(), "acknowledge") {
			t.Fatalf("%s: err = %v", p, err)
		}
	}
}

func TestSpawnSSODirectInlineBindingNeverAckHint(t *testing.T) {
	hooks := &ssoHooks{fakeBrokerHooks: &fakeBrokerHooks{}, verdicts: map[string]ssoVerdict{"": {res: wideGate("s3:Put*")}}}
	b := &config.AWSBinding{SourceProfile: "p", Region: "ap-southeast-1", Mode: config.AWSModeSSODirect}
	_, _, err := gateSSODirect(hooks, config.Bindings{AWS: b}, "p", false)
	if err == nil || strings.Contains(err.Error(), "acknowledge") {
		t.Fatalf("err = %v", err)
	}
}

func TestSSODirectAckSurvivesARestart(t *testing.T) {
	res := wideGate("s3:Put*")
	c := ackedConn("ro", res)
	l := connLauncherFor(c)
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&ssoHooks{fakeBrokerHooks: &fakeBrokerHooks{}, verdicts: map[string]ssoVerdict{"ro": {res: res, exp: time.Now().Add(time.Hour)}}})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	r := reconcileWith(t, l, path, fb, s.ID)
	got := r.Info().AWSConnections
	if len(got) != 1 || !got[0].FullAccess || got[0].Mode != config.AWSModeSSODirect {
		t.Fatalf("restored info = %+v", got)
	}
	if len(r.AWSConns) != 1 || r.AWSConns[0].FullAccessAck == nil || r.AWSConns[0].FullAccessAck.Caller != ackTestCaller {
		t.Fatalf("restored session lost the ack: %+v", r.AWSConns)
	}
}

// ssoRefusal words each kind of refusal with its own remedy: an ack hint
// that also says a config.json connection must be re-created on the
// Credentials screen, a retry for a run cut short, the reason for a set that
// cannot simulate.
func TestSSORefusalHints(t *testing.T) {
	partial := func(r awsprobe.Reason) awsprobe.GateResult {
		g := wideGate("s3:Put*")
		g.Incomplete, g.PolicyDigest = r, strings.Repeat("a", 64)
		return g
	}
	perm := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms, Caller: ackTestCaller,
		MissingPerms: []string{"iam:GetRole"}}
	for _, tc := range []struct {
		name    string
		res     awsprobe.GateResult
		adm     awsprobe.Admission
		want    []string
		mustNot []string
	}{
		{"eligible", wideGate("s3:Put*"), awsprobe.Admission{Eligible: true},
			[]string{"is not read-only", "acknowledge full access on the Credentials screen", awsprobe.AckSeedHint}, nil},
		{"stale", wideGate("s3:Put*"), awsprobe.Admission{Eligible: true, Stale: true},
			[]string{"acknowledge again on the Credentials screen", awsprobe.AckSeedHint}, nil},
		{"missing perms", perm, awsprobe.Admission{Eligible: true},
			[]string{"could not verify", "grant it iam:GetRole", "acknowledge full access", awsprobe.AckSeedHint}, nil},
		{"partial timeout", partial(awsprobe.ReasonTimeout), awsprobe.Admission{},
			[]string{"is not read-only", "the check stopped early (timed out)", "check again"}, []string{"acknowledg"}},
		{"partial simulate denied", partial(awsprobe.ReasonSimulateDenied), awsprobe.Admission{Eligible: true},
			[]string{"is not read-only", "the check stopped early (role cannot simulate itself)", "acknowledge full access", awsprobe.AckSeedHint}, []string{"check again"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ssoRefusal("ro", tc.res, tc.adm)
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("lacks %q: %s", w, s)
				}
			}
			for _, w := range tc.mustNot {
				if strings.Contains(s, w) {
					t.Errorf("has %q: %s", w, s)
				}
			}
		})
	}
}
