package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// ssoVerdict is what ssoHooks answers for one binding key.
type ssoVerdict struct {
	res awsprobe.GateResult
	exp time.Time
}

// ssoHooks is fakeBrokerHooks plus the sso_direct gate check.
type ssoHooks struct {
	*fakeBrokerHooks
	mu2      sync.Mutex
	verdicts map[string]ssoVerdict
	checked  []string
	hadDL    bool
}

func (h *ssoHooks) CheckSSODirect(ctx context.Context, bs config.Bindings, key string) (awsprobe.GateResult, time.Time) {
	h.mu2.Lock()
	defer h.mu2.Unlock()
	h.checked = append(h.checked, key)
	_, h.hadDL = ctx.Deadline()
	v, ok := h.verdicts[key]
	if !ok {
		return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonError}, time.Time{}
	}
	return v.res, v.exp
}

func (h *ssoHooks) checkedKeys() []string {
	h.mu2.Lock()
	defer h.mu2.Unlock()
	return append([]string(nil), h.checked...)
}

const ssoTestRole = "arn:aws:iam::226198813800:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_ReadOnly_abc"

func readOnlyVerdict(exp time.Time) ssoVerdict {
	return ssoVerdict{res: awsprobe.GateResult{State: awsprobe.GateReadOnly, RoleARN: ssoTestRole, CheckedAt: time.Now()}, exp: exp}
}

func ssoConn(name string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{SourceProfile: "sandbox-admin", Region: "ap-southeast-1",
		Label: "Sandbox RO", Mode: config.AWSModeSSODirect}}
}

func ssoConnManager(t *testing.T, l connLauncher, v map[string]ssoVerdict) (*Manager, *fakeBoxes, *ssoHooks) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &ssoHooks{fakeBrokerHooks: &fakeBrokerHooks{}, verdicts: v}
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	return m, boxes, hooks
}

func TestSpawnRefusesSSODirectThatIsNotReadOnly(t *testing.T) {
	v := map[string]ssoVerdict{"ro": {res: awsprobe.GateResult{State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{
		{Pattern: "s3:*", Why: "allows every S3 action"}, {Pattern: "ssm:SendCommand", Why: "runs commands on instances"}}}}}
	m, boxes, hooks := ssoConnManager(t, connLauncherFor(ssoConn("ro")), v)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a non-read-only sso_direct connection spawned")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want it to wrap box.ErrBoxUnavailable so the UI shows it", err)
	}
	for _, want := range []string{`"ro"`, "not read-only", "s3:*: allows every S3 action", "ssm:SendCommand: runs commands on instances"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
	if boxes.wraps != 0 || hooks.boxForCalls != 0 {
		t.Fatal("a refused spawn touched the box layer")
	}
	if !hooks.hadDL {
		t.Error("the gate check ran without a deadline")
	}
}

func TestSpawnRefusesSSODirectUnverified(t *testing.T) {
	v := map[string]ssoVerdict{"ro": {res: awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms,
		MissingPerms: []string{"iam:GetRolePolicy", "iam:ListAttachedRolePolicies"}}}}
	m, _, _ := ssoConnManager(t, connLauncherFor(ssoConn("ro")), v)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("an unverified sso_direct connection spawned")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"could not verify", awsprobe.ReasonText(awsprobe.ReasonMissingPerms), "iam:GetRolePolicy", "iam:ListAttachedRolePolicies"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
}

// A read-only verdict that names no role is not a proof.
func TestSpawnRefusesReadOnlyVerdictWithoutRole(t *testing.T) {
	v := map[string]ssoVerdict{"ro": {res: awsprobe.GateResult{State: awsprobe.GateReadOnly}, exp: time.Now().Add(time.Hour)}}
	m, _, _ := ssoConnManager(t, connLauncherFor(ssoConn("ro")), v)
	if s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()}); err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a read-only verdict without a role spawned")
	}
}

func TestSpawnRefusalIsBounded(t *testing.T) {
	var refused []awsprobe.Verdict
	for i := range 40 {
		refused = append(refused, awsprobe.Verdict{Pattern: fmt.Sprintf("svc%02d:Write", i), Why: strings.Repeat("x", 500)})
	}
	v := map[string]ssoVerdict{"ro": {res: awsprobe.GateResult{State: awsprobe.GateNotReadOnly, Refused: refused}}}
	m, _, _ := ssoConnManager(t, connLauncherFor(ssoConn("ro")), v)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("spawned")
	}
	if len(err.Error()) > 3000 || !strings.Contains(err.Error(), "more") || !strings.Contains(err.Error(), "svc00:Write") {
		t.Fatalf("refusal is %d bytes: %.300s", len(err.Error()), err.Error())
	}
}

// One refusing connection refuses the whole spawn, whatever the others say.
func TestSpawnRefusesWhenAnyConnectionRefuses(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour)
	v := map[string]ssoVerdict{"ok": readOnlyVerdict(exp),
		"bad": {res: awsprobe.GateResult{State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{{Pattern: "*", Why: "everything"}}}}}
	m, _, hooks := ssoConnManager(t, connLauncherFor(ssoConn("ok"), ssoConn("bad")), v)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawned with a refusing connection")
	}
	if !strings.Contains(err.Error(), `"bad"`) || strings.Contains(err.Error(), `"ok"`) {
		t.Fatalf("err = %v", err)
	}
	if len(hooks.checkedKeys()) != 2 {
		t.Fatalf("checked %v, want both", hooks.checkedKeys())
	}
}

// Hooks without the gate check (an older or partial broker) cannot vouch for
// an sso_direct binding, so the spawn is refused.
func TestSpawnRefusesSSODirectWithoutAChecker(t *testing.T) {
	m, _, _ := connManager(t, connLauncherFor(ssoConn("ro")))
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawned without a read-only gate")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestSpawnSSODirectReadOnlyReportsModeAndHours(t *testing.T) {
	exp := time.Now().Add(7*time.Hour + 20*time.Minute)
	v := map[string]ssoVerdict{"ro": readOnlyVerdict(exp)}
	m, boxes, hooks := ssoConnManager(t, connLauncherFor(ssoConn("ro"), connB()), v)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.wraps == 0 {
		t.Fatal("the box was never started")
	}
	if got := hooks.checkedKeys(); len(got) != 1 || got[0] != "ro" {
		t.Fatalf("gate checked %v, want only the sso_direct connection", got)
	}
	info := s.Info()
	var ro, ar *AWSConnInfo
	for i := range info.AWSConnections {
		switch info.AWSConnections[i].Name {
		case "ro":
			ro = &info.AWSConnections[i]
		case "sbx-b":
			ar = &info.AWSConnections[i]
		}
	}
	if ro == nil || ar == nil {
		t.Fatalf("connections = %+v", info.AWSConnections)
	}
	if ro.Mode != config.AWSModeSSODirect || ro.Hours != 8 || ro.RoleARN != "" || ro.AccountID != "226198813800" {
		t.Fatalf("sso_direct info = %+v, want mode sso_direct, 8 h, no role, the gate's account", ro)
	}
	if ro.BaseAccess != string(awsprobe.NoneFound) {
		t.Fatalf("base_access = %q: a passed gate is a definite result, not a watched one", ro.BaseAccess)
	}
	if ar.Mode != "" || ar.Hours != 0 {
		t.Fatalf("assume_role info gained sso fields: %+v", ar)
	}
	for _, keys := range hooks.watched {
		if slices.Contains(keys, "ro") {
			t.Fatalf("an sso_direct key was queued for a base-role re-probe: %v", hooks.watched)
		}
	}
	if got := hooks.lastBase["ro"]; got.State != awsprobe.NoneFound {
		t.Fatalf("BoxFor got base %+v for the sso_direct key", got)
	}
}

func TestSpawnSSODirectInlineBinding(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	b := &config.AWSBinding{SourceProfile: "sandbox-admin", Region: "ap-southeast-1", Mode: config.AWSModeSSODirect}
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: b},
		nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	hooks := &ssoHooks{fakeBrokerHooks: &fakeBrokerHooks{}, verdicts: map[string]ssoVerdict{"": readOnlyVerdict(exp)}}
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	in := s.Info().AWS
	if in == nil || in.Mode != config.AWSModeSSODirect || in.Hours != 1 || in.RoleARN != "" || in.AccountID != "226198813800" {
		t.Fatalf("AWSInfo = %+v", in)
	}
	hooks.verdicts[""] = ssoVerdict{res: awsprobe.GateResult{State: awsprobe.GateNotReadOnly}}
	if s2, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()}); err == nil {
		_ = m.Kill(s2.ID)
		t.Fatal("a non-read-only inline binding spawned")
	}
}

// AssumeRole bindings never reach the gate, even with a checker installed.
func TestSpawnAssumeRoleNeverCallsTheGate(t *testing.T) {
	m, _, hooks := ssoConnManager(t, connLauncherFor(connA(), connB()), nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if got := hooks.checkedKeys(); len(got) != 0 {
		t.Fatalf("the gate checked an assume_role connection: %v", got)
	}
	for _, c := range s.Info().AWSConnections {
		if c.Mode != "" || c.Hours != 0 {
			t.Fatalf("assume_role info gained sso fields: %+v", c)
		}
	}
}

func TestSSODirectInfoSurvivesARestart(t *testing.T) {
	exp := time.Now().Add(3 * time.Hour)
	l := connLauncherFor(ssoConn("ro"))
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&ssoHooks{fakeBrokerHooks: &fakeBrokerHooks{}, verdicts: map[string]ssoVerdict{"ro": readOnlyVerdict(exp)}})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	r := reconcileWith(t, l, path, fb, s.ID)
	got := r.Info().AWSConnections
	if len(got) != 1 || got[0].Mode != config.AWSModeSSODirect || got[0].Hours != 3 || got[0].AccountID != "226198813800" {
		t.Fatalf("restored info = %+v", got)
	}
}

func TestHoursUntil(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		exp  time.Time
		want int
	}{
		{time.Time{}, 0},
		{now.Add(-time.Minute), 0},
		{now.Add(time.Minute), 1},
		{now.Add(time.Hour), 1},
		{now.Add(time.Hour + time.Second), 2},
		{now.Add(8 * time.Hour), 8},
	} {
		if got := hoursUntil(c.exp, now); got != c.want {
			t.Errorf("hoursUntil(%v) = %d, want %d", c.exp.Sub(now), got, c.want)
		}
	}
}
