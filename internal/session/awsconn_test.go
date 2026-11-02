package session

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// connLauncher is a tieredLauncher whose profile "conn" binds the given AWS
// connections (already sorted, as the real launcher returns them) and whose
// profile "bad" has an AWSConnectionsFor error.
type connLauncher struct {
	tieredLauncher
	conns []config.AWSConnection
	err   error
}

func (l connLauncher) AWSFor(string) *config.AWSBinding { return nil }

func (l connLauncher) AWSConnectionsFor(agent string) ([]config.AWSConnection, error) {
	switch agent {
	case "conn":
		var out []config.AWSConnection
		for _, c := range l.conns {
			out = append(out, c.Clone())
		}
		return out, nil
	case "bad":
		return nil, l.err
	}
	return nil, nil
}

func connA() config.AWSConnection {
	return config.AWSConnection{Name: "sbx-a", Description: "read", AWSBinding: config.AWSBinding{SourceProfile: "sandbox-admin",
		RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent", Region: "ap-southeast-1", Label: "Sandbox A",
		Elevate: &config.AWSElevate{RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent-write"}}}
}

func connB() config.AWSConnection {
	return config.AWSConnection{Name: "sbx-b", AWSBinding: config.AWSBinding{SourceProfile: "sandbox-admin",
		RoleARN: "arn:aws:iam::111122223333:role/b", Region: "us-east-1", Label: "Sandbox B", Prod: true}}
}

func connLauncherFor(conns ...config.AWSConnection) connLauncher {
	return connLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, conns: conns,
		err: errors.New(`profile "bad" names AWS connection "gone", which is not defined in aws_connections`)}
}

func connManager(t *testing.T, l connLauncher) (*Manager, *fakeBoxes, *fakeBrokerHooks) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	return m, boxes, hooks
}

func TestSpawnWithConnectionsIsBoundAndKeyed(t *testing.T) {
	c2 := connB()
	c2.Name = "sbx-c"
	conns := []config.AWSConnection{connB(), c2}
	m, boxes, _ := connManager(t, connLauncherFor(conns...))
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want := config.Bindings{AWSConns: conns}.Hash()
	if want == "" || boxes.lastKey.Binding != want {
		t.Fatalf("box key binding = %q, want %q", boxes.lastKey.Binding, want)
	}
	if len(s.AWSConns) != 2 || s.AWS != nil || !s.Bindings().Any() || s.BrokerNonce == "" {
		t.Fatalf("session not bound by its connections: AWS=%v conns=%d nonce=%q", s.AWS, len(s.AWSConns), s.BrokerNonce)
	}
	if !reflect.DeepEqual(s.Bindings().AWSConns, s.AWSConns) {
		t.Fatalf("Bindings().AWSConns = %+v", s.Bindings().AWSConns)
	}
	if k := bindingKinds(s.Bindings()); !reflect.DeepEqual(k, []string{"AWS"}) {
		t.Fatalf("bindingKinds = %v, want AWS", k)
	}
	if boxes.lastKey.Session != "" || s.Exclusive {
		t.Fatalf("connections without elevate share boxes: key %+v exclusive %v", boxes.lastKey, s.Exclusive)
	}
	if in := s.Info(); in.Egress != "allowlist" {
		t.Fatalf("Info.Egress = %q, want allowlist", in.Egress)
	}
}

func TestSpawnElevateConnIsExclusive(t *testing.T) {
	m, boxes, _ := connManager(t, connLauncherFor(connA(), connB()))
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.lastKey.Session != s.ID || !s.Exclusive {
		t.Fatalf("one elevatable connection must give the session its own box: key %+v exclusive %v", boxes.lastKey, s.Exclusive)
	}
}

func TestSpawnConnectionsRefusedOnHostTier(t *testing.T) {
	l := connLauncherFor(connA())
	l.runtime = config.RuntimeHost
	m, boxes, _ := connManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a connection-bound profile spawned on the host tier")
	}
	if !strings.Contains(err.Error(), "an AWS binding") || boxes.wraps != 0 {
		t.Fatalf("err = %v wraps=%d", err, boxes.wraps)
	}
}

func TestSpawnConnectionsForErrorRefuses(t *testing.T) {
	m, boxes, _ := connManager(t, connLauncherFor(connA()))
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bad", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a profile naming an undefined connection spawned")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), `names AWS connection "gone"`) {
		t.Fatalf("err = %v", err)
	}
	if boxes.prepares != 0 || boxes.wraps != 0 {
		t.Fatal("refused spawn touched the box layer")
	}
}

func spawnConnSession(t *testing.T, l connLauncher, path string, fb *fakeBackend) (*Manager, *Session) {
	t.Helper()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return m, s
}

func reconcileWith(t *testing.T, l connLauncher, path string, fb *fakeBackend, id string) *Session {
	t.Helper()
	fb.seedLive(id)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m2.SetBoxes(&fakeBoxes{})
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(id)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	return r
}

func TestSidecarRoundTripAWSConns(t *testing.T) {
	l := connLauncherFor(connA(), connB())
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	_, s := spawnConnSession(t, l, path, fb)
	md := sidecarEntry(t, path, s.ID)
	if !reflect.DeepEqual(md.AWSConns, []config.AWSConnection{connA(), connB()}) || md.AWS != nil {
		t.Fatalf("sidecar entry AWSConns=%+v AWS=%+v", md.AWSConns, md.AWS)
	}
	raw, _ := json.Marshal(md)
	if !strings.Contains(string(raw), `"aws_connections":[`) || strings.Contains(string(raw), `"aws":`) {
		t.Fatalf("sidecar JSON = %s", raw)
	}

	r := reconcileWith(t, l, path, fb, s.ID)
	if !reflect.DeepEqual(r.AWSConns, s.AWSConns) || r.AWS != nil {
		t.Fatalf("restored AWSConns=%+v", r.AWSConns)
	}
	if r.Info().Egress != "allowlist" || len(r.Info().AWSConnections) != 2 || r.Bindings().Hash() != s.Bindings().Hash() {
		t.Fatalf("restored Info = %+v", r.Info())
	}
}

// A record in the shape this daemon wrote before connections existed has no
// aws_connections key; it restores exactly as it always did.
func TestOldSidecarWithoutAWSConnsRestores(t *testing.T) {
	old := `{"agent":"aws","created":"2026-10-01T00:00:00Z","runtime":"box","box":"ab-box-test","replyable":false,` +
		`"aws":{"source_profile":"sandbox-admin","role_arn":"arn:aws:iam::226198813800:role/agentbox-agent","region":"ap-southeast-1","s3_read":["culishop-tfstate"],"label":"sandbox"},` +
		`"aws_nonce":"` + fakeNonce + `","egress_nonce":"` + fakeNonce + `"}`
	var md sessionMeta
	if err := json.Unmarshal([]byte(old), &md); err != nil {
		t.Fatal(err)
	}
	if md.AWSConns != nil || md.AWS == nil {
		t.Fatalf("old record decoded to %+v", md)
	}
	raw, _ := json.Marshal(md)
	if strings.Contains(string(raw), "aws_connections") {
		t.Fatalf("an old record re-encoded with a connections key: %s", raw)
	}

	l := awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: testBinding()}
	path := filepath.Join(t.TempDir(), "sessions.json")
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	sc.put("oldsess1", md)
	fb := newFakeBackend()
	fb.seedLive("oldsess1")
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m.Get("oldsess1")
	if !ok {
		t.Fatal("old session not reattached")
	}
	t.Cleanup(func() { _ = m.Kill(r.ID) })
	if r.AWS == nil || r.AWSConns != nil || r.Info().AWSConnections != nil || r.Info().AWS == nil {
		t.Fatalf("restored old session: AWS=%+v conns=%+v", r.AWS, r.AWSConns)
	}
	if r.Bindings().Hash() != testBinding().Hash() {
		t.Fatalf("restored hash %q, want the legacy %q", r.Bindings().Hash(), testBinding().Hash())
	}
}

// Review focus 3: a connection removed from config.json while a session runs
// stays with that session across a daemon restart.
func TestRestoredSessionKeepsRemovedConnection(t *testing.T) {
	l := connLauncherFor(connA(), connB())
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	_, s := spawnConnSession(t, l, path, fb)

	removed := connLauncherFor() // the host no longer defines either connection
	removed.err = errors.New("not defined")
	r := reconcileWith(t, removed, path, fb, s.ID)
	if !reflect.DeepEqual(r.AWSConns, []config.AWSConnection{connA(), connB()}) {
		t.Fatalf("restored session lost its connections: %+v", r.AWSConns)
	}
	if !r.Bindings().Any() || r.Bindings().Hash() != s.Bindings().Hash() {
		t.Fatalf("restored bindings differ: %q vs %q", r.Bindings().Hash(), s.Bindings().Hash())
	}
}

func TestInfoAWSConnections(t *testing.T) {
	m, _, _ := connManager(t, connLauncherFor(connB(), connA())) // deliberately unsorted
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	in := s.Info()
	want := []AWSConnInfo{
		{Name: "sbx-a", Label: "Sandbox A", AccountID: "226198813800", RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent", Region: "ap-southeast-1",
			WriteCapable: true, Elevate: &ElevateInfo{RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent-write", MaxMinutes: 60},
			Fields: awsprobe.Disabled().Fields()},
		{Name: "sbx-b", Label: "Sandbox B", AccountID: "111122223333", RoleARN: "arn:aws:iam::111122223333:role/b", Region: "us-east-1", Prod: true,
			Fields: awsprobe.Disabled().Fields()},
	}
	if !reflect.DeepEqual(in.AWSConnections, want) {
		t.Fatalf("Info.AWSConnections = %+v\nwant %+v", in.AWSConnections, want)
	}
	if in.AWS != nil {
		t.Fatalf("Info.AWS = %+v: a connection session has no inline binding", in.AWS)
	}
	b, _ := json.Marshal(in)
	for _, bad := range []string{"source_profile", "sandbox-admin", fakeNonce, `"description"`} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("Info leaks %q: %s", bad, b)
		}
	}
	for _, key := range []string{`"aws_connections":[{"name":"sbx-a"`, `"write_capable":true`, `"account_id":"226198813800"`, `"role_arn"`, `"prod":true`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("Info JSON lacks %s: %s", key, b)
		}
	}
	if strings.Contains(string(b), "elevated_until") || strings.Contains(string(b), "sso_expired") {
		t.Fatalf("Task 3 does not fill the live fields: %s", b)
	}
}

// Without connections, Info's wire shape does not change.
func TestInfoWithoutAWSConnectionsHasNoKey(t *testing.T) {
	m, _, _, _ := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	b, _ := json.Marshal(s.Info())
	if strings.Contains(string(b), "aws_connections") {
		t.Fatalf("Info grew an aws_connections key: %s", b)
	}
}

// List folds the broker's per-connection state into each connection: the
// window on the connection it is for, an expired sign-in on its own connection.
func TestListCarriesPerConnectionBrokerState(t *testing.T) {
	m, _, hooks := connManager(t, connLauncherFor(connA(), connB()))
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	until := time.Unix(1_800_003_600, 0)
	hooks.setConnState(s.ID, awsState{until: until, conn: "sbx-a", sso: "corp-sso", conns: map[string]string{"sbx-b": "corp-sso"}})
	var got Info
	for _, i := range m.List() {
		if i.ID == s.ID {
			got = i
		}
	}
	if len(got.AWSConnections) != 2 || got.Need != "sso" {
		t.Fatalf("List: %+v", got)
	}
	a, b := got.AWSConnections[0], got.AWSConnections[1]
	if a.Name != "sbx-a" || a.ElevatedUntil == nil || !a.ElevatedUntil.Equal(until) || a.SSOExpired != "" ||
		b.ElevatedUntil != nil || b.SSOExpired != "corp-sso" {
		t.Fatalf("connections %+v / %+v", a, b)
	}
}
