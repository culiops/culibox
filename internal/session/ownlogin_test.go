package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// fakeSpawnAudit records what the Manager asks it to audit.
type fakeSpawnAudit struct {
	mu   sync.Mutex
	recs []HostOwnLoginRecord
	err  error
}

func (f *fakeSpawnAudit) RecordHostOwnLogin(r HostOwnLoginRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.recs = append(f.recs, r)
	return nil
}

func (f *fakeSpawnAudit) records() []HostOwnLoginRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.recs)
}

// ownLoginAudits is the fake audit ownLoginHarness installed, by manager.
var ownLoginAudits sync.Map

func auditOf(h *dropsHarness) *fakeSpawnAudit {
	v, _ := ownLoginAudits.Load(h.m)
	return v.(*fakeSpawnAudit)
}

// ownLoginHarness is the harness the host_own_login tests run on: it is
// newDropsHarness with a fake spawn audit installed.
func ownLoginHarness(t *testing.T, l dropsLauncher) *dropsHarness {
	t.Helper()
	h := newDropsHarness(t, l)
	fa := &fakeSpawnAudit{}
	h.m.SetSpawnAudit(fa)
	ownLoginAudits.Store(h.m, fa)
	t.Cleanup(func() { ownLoginAudits.Delete(h.m) })
	return h
}

func ownSpec(t *testing.T, kinds ...string) launcher.SpawnSpec {
	spec := hostAckSpec(t)
	spec.HostOwnLogin = kinds
	return spec
}

func TestHostOwnLoginGitHubDropsOnlyGitHub(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.GitHub != nil || s.PATs != nil || s.AWS == nil || len(s.Tokens) == 0 || !s.HostBroker {
		t.Fatalf("session: github=%v pats=%v aws=%v tokens=%d host_broker=%v", s.GitHub, s.PATs, s.AWS, len(s.Tokens), s.HostBroker)
	}
	calls, _, _ := h.hooks.hostState()
	if len(calls) != 1 || calls[0].bs.GitHub != nil || calls[0].bs.AWS == nil || len(calls[0].bs.PATs) != 0 {
		t.Fatalf("HostFor calls = %+v, want one without GitHub and with AWS", calls)
	}
	env := envOfHostProcessKV(t, s)
	for _, gone := range []string{"GH_CONFIG_DIR", "GIT_CONFIG_COUNT", "AGENTBOX_GH_OWNERS"} {
		if v, ok := env[gone]; ok {
			t.Fatalf("%s=%q reached a session whose GitHub is on the machine login", gone, v)
		}
	}
	if dir := h.hooks.hostDirFor(s.ID); !strings.HasPrefix(env["AWS_CONFIG_FILE"], dir) {
		t.Fatalf("AWS_CONFIG_FILE = %q, want under %s", env["AWS_CONFIG_FILE"], dir)
	}
}

func TestHostOwnLoginAWSDropsOnlyAWS(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginAWS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.AWS != nil || len(s.AWSConns) != 0 || s.GitHub == nil {
		t.Fatalf("session: aws=%v conns=%d github=%v", s.AWS, len(s.AWSConns), s.GitHub)
	}
	if in := s.Info(); in.AWS != nil || len(in.AWSConnections) != 0 {
		t.Fatalf("Info shows AWS: %+v %+v", in.AWS, in.AWSConnections)
	}
	env := envOfHostProcessKV(t, s)
	if env["GH_CONFIG_DIR"] == "" {
		t.Fatal("GH_CONFIG_DIR missing: GitHub should still go through the broker")
	}
	for _, gone := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		if v, ok := env[gone]; ok {
			t.Fatalf("%s=%q set by CuliBox for a session whose AWS is on the machine login", gone, v)
		}
	}
}

func TestHostOwnLoginAllKindsIsAPlainHostRun(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	l.noTokens = true
	h := ownLoginHarness(t, l)
	h.m.SetBroker(nil)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub, launcher.OwnLoginAWS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.HostBroker || s.Bindings().Any() {
		t.Fatalf("not a plain host run: host_broker=%v bindings=%+v", s.HostBroker, s.Bindings())
	}
	if calls, _, _ := h.hooks.hostState(); len(calls) != 0 {
		t.Fatalf("HostFor called for a plain host run: %+v", calls)
	}
	if recs := auditOf(h).records(); len(recs) != 1 || !slices.Equal(recs[0].Kinds, []string{launcher.OwnLoginGitHub, launcher.OwnLoginAWS}) {
		t.Fatalf("audit rows = %+v, want one for github and aws", recs)
	}
}

func TestHostOwnLoginUnboundKindRefused(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	l.noAWS = true
	h := ownLoginHarness(t, l)
	_, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginAWS))
	want := `profile "p": host_own_login names aws, which this profile does not bind`
	if err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
}

func TestHostOwnLoginUnknownKindRefusedInSpawn(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	_, err := h.m.Spawn(ownSpec(t, "gitlab"))
	if err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), `"gitlab"`) {
		t.Fatalf("err = %v, want a refusal naming gitlab", err)
	}
	h.untouched(t)
}

func TestBoxSpawnIgnoresHostOwnLogin(t *testing.T) {
	l := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), HostDropsAck: true,
		HostOwnLogin: []string{launcher.OwnLoginGitHub, launcher.OwnLoginAWS}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.Runtime != config.RuntimeBox || s.GitHub == nil || (s.AWS == nil && len(s.AWSConns) == 0) {
		t.Fatalf("a box spawn lost a binding: runtime=%s github=%v aws=%v", s.Runtime, s.GitHub, s.AWS)
	}
}

func TestHostOwnLoginAWSKeepsTheUsersAWSEnv(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv, "AWS_PROFILE=mine", "AWS_ACCESS_KEY_ID=AKIAEXAMPLEONLY")
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginAWS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	env := envOfHostProcessKV(t, s)
	if env["AWS_PROFILE"] != "mine" || env["AWS_ACCESS_KEY_ID"] != "AKIAEXAMPLEONLY" {
		t.Fatalf("the user's AWS env was stripped: AWS_PROFILE=%q AWS_ACCESS_KEY_ID=%q", env["AWS_PROFILE"], env["AWS_ACCESS_KEY_ID"])
	}
	if v, ok := env["AWS_CONFIG_FILE"]; ok {
		t.Fatalf("AWS_CONFIG_FILE=%q set by CuliBox for a session whose AWS is on the machine login", v)
	}
}

func TestHostOwnLoginGitHubKeepsTheUsersGitHubEnvAndTokens(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv, "GH_TOKEN=user-own", "GH_CONFIG_DIR=/home/u/.config/gh")
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	env := envOfHostProcessKV(t, s)
	if env["GH_TOKEN"] != "user-own" || env["GH_CONFIG_DIR"] != "/home/u/.config/gh" {
		t.Fatalf("the user's GitHub env was stripped: GH_TOKEN=%q GH_CONFIG_DIR=%q", env["GH_TOKEN"], env["GH_CONFIG_DIR"])
	}
	if v, ok := env["GIT_CONFIG_COUNT"]; ok {
		t.Fatalf("GIT_CONFIG_COUNT=%q set for a session whose GitHub is on the machine login", v)
	}
	dir := h.hooks.hostDirFor(s.ID)
	if env["AGENTBOX_BROKER"] == "" || !strings.HasPrefix(env["PATH"], dir+"/bin") {
		t.Fatalf("the token binding was not delivered: AGENTBOX_BROKER=%q PATH=%q", env["AGENTBOX_BROKER"], env["PATH"])
	}
	// That no bin/gh shim is written is pinned by TestHostForFilesPerKind
	// (internal/broker/hostfiles_test.go): the fake HostFor here writes no files.
}

func TestHostOwnLoginOnSessionInfoAndSidecar(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	spec := ownSpec(t, launcher.OwnLoginGitHub)
	spec.Actor = "bearer"
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	want := []string{launcher.OwnLoginGitHub}
	if !slices.Equal(s.HostOwnLogin, want) || !slices.Equal(s.Info().HostOwnLogin, want) {
		t.Fatalf("HostOwnLogin = %q / %q", s.HostOwnLogin, s.Info().HostOwnLogin)
	}
	e := sidecarEntry(t, h.path, s.ID)
	if !slices.Equal(e.HostOwnLogin, want) || e.HostOwnActor != "bearer" {
		t.Fatalf("sidecar = %q by %q", e.HostOwnLogin, e.HostOwnActor)
	}

	plain, err := h.m.Spawn(hostAckSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(plain.ID) })
	if plain.Info().HostOwnLogin != nil {
		t.Fatalf("HostOwnLogin = %q on a spawn without the field", plain.Info().HostOwnLogin)
	}
	raw, err := json.Marshal(plain.Info())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "host_own_login") {
		t.Fatalf("Info JSON carries host_own_login: %s", raw)
	}
	if e := sidecarEntry(t, h.path, plain.ID); e.HostOwnLogin != nil || e.HostOwnActor != "" {
		t.Fatalf("sidecar = %q by %q for a spawn without the field", e.HostOwnLogin, e.HostOwnActor)
	}
}

func TestSidecarRestoresHostOwnLogin(t *testing.T) {
	l, _ := hostBoundLauncher(t, []string{"sh", "-c", "sleep 30"})
	h := ownLoginHarness(t, l)
	fb := newFakeBackend()
	h.m.backend = fb
	spec := ownSpec(t, launcher.OwnLoginGitHub)
	spec.Actor = "bearer"
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(h.path); err != nil {
		t.Fatal(err)
	}
	m2.SetBoxes(&fakeBoxes{})
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not restored")
	}
	want := []string{launcher.OwnLoginGitHub}
	if !slices.Equal(r.HostOwnLogin, want) || !slices.Equal(r.Info().HostOwnLogin, want) || r.hostOwnActor != "bearer" {
		t.Fatalf("restored HostOwnLogin = %q by %q", r.Info().HostOwnLogin, r.hostOwnActor)
	}
}

func TestSidecarWithoutHostOwnLoginReadsNone(t *testing.T) {
	var md sessionMeta
	if err := json.Unmarshal([]byte(`{"agent":"p","runtime":"host"}`), &md); err != nil {
		t.Fatal(err)
	}
	if md.HostOwnLogin != nil || md.HostOwnActor != "" {
		t.Fatalf("an old record reads %q by %q", md.HostOwnLogin, md.HostOwnActor)
	}
}

func TestRestartKeepsHostOwnLogin(t *testing.T) {
	l, _ := hostBoundLauncher(t, []string{"sh", "-c", "exit 0"})
	h := ownLoginHarness(t, l)
	spec := ownSpec(t, launcher.OwnLoginGitHub)
	spec.Actor = "bearer"
	old := exitedSession(t, h.m, spec)
	ns, err := h.m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(ns.ID) })
	if !slices.Equal(ns.HostOwnLogin, []string{launcher.OwnLoginGitHub}) || ns.GitHub != nil || ns.hostOwnActor != "bearer" {
		t.Fatalf("restarted: own=%q github=%v actor=%q", ns.HostOwnLogin, ns.GitHub, ns.hostOwnActor)
	}
	calls, _, _ := h.hooks.hostState()
	if len(calls) != 2 || calls[1].bs.GitHub != nil || calls[1].bs.AWS == nil {
		t.Fatalf("HostFor calls = %+v, want a second one without GitHub and with AWS", calls)
	}
}

func TestRestartDropsAKindTheProfileNoLongerBinds(t *testing.T) {
	var off atomic.Bool
	l, _ := hostBoundLauncher(t, []string{"sh", "-c", "exit 0"})
	l.ghOff = &off
	h := ownLoginHarness(t, l)
	old := exitedSession(t, h.m, ownSpec(t, launcher.OwnLoginGitHub))
	off.Store(true)
	ns, err := h.m.Restart(old.ID)
	if err != nil {
		t.Fatalf("a restart after the profile lost GitHub: %v", err)
	}
	t.Cleanup(func() { _ = h.m.Kill(ns.ID) })
	if ns.HostOwnLogin != nil || ns.GitHub != nil {
		t.Fatalf("restarted: own=%q github=%v", ns.HostOwnLogin, ns.GitHub)
	}

	// The reverse: GitHub gained since the spawn goes through the broker.
	off.Store(true)
	l2, _ := hostBoundLauncher(t, []string{"sh", "-c", "exit 0"})
	l2.ghOff = &off
	h2 := ownLoginHarness(t, l2)
	old2 := exitedSession(t, h2.m, ownSpec(t, launcher.OwnLoginAWS))
	if old2.GitHub != nil {
		t.Fatalf("GitHub bound while off: %v", old2.GitHub)
	}
	off.Store(false)
	ns2, err := h2.m.Restart(old2.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h2.m.Kill(ns2.ID) })
	if ns2.GitHub == nil || !slices.Equal(ns2.HostOwnLogin, []string{launcher.OwnLoginAWS}) {
		t.Fatalf("restarted: own=%q github=%v", ns2.HostOwnLogin, ns2.GitHub)
	}
}

func TestBoxSessionRecordsNoHostOwnLogin(t *testing.T) {
	l := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), Actor: "bearer",
		HostOwnLogin: []string{launcher.OwnLoginGitHub}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.HostOwnLogin != nil || s.Info().HostOwnLogin != nil {
		t.Fatalf("HostOwnLogin = %q on a box session", s.Info().HostOwnLogin)
	}
	if e := sidecarEntry(t, h.path, s.ID); e.HostOwnLogin != nil || e.HostOwnActor != "" {
		t.Fatalf("sidecar = %q by %q for a box session", e.HostOwnLogin, e.HostOwnActor)
	}
}

func TestHostOwnLoginAuditedBeforeStart(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	spec := ownSpec(t, launcher.OwnLoginGitHub, launcher.OwnLoginAWS)
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	want := HostOwnLoginRecord{SessionID: s.ID, Profile: spec.Agent, Cwd: spec.Cwd, Actor: "web",
		Kinds: []string{launcher.OwnLoginGitHub, launcher.OwnLoginAWS}}
	recs := auditOf(h).records()
	if len(recs) != 1 || !reflect.DeepEqual(recs[0], want) {
		t.Fatalf("audit rows = %+v, want [%+v]", recs, want)
	}
}

func TestHostOwnLoginAuditFailureRefusesSpawn(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	auditOf(h).err = errors.New("disk full")
	_, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub))
	if !errors.Is(err, ErrSpawnAudit) {
		t.Fatalf("err = %v, want ErrSpawnAudit", err)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
	calls, rels, _ := h.hooks.hostState()
	if len(calls) != 1 || len(rels) != 1 || rels[0].id != calls[0].id || !rels[0].remove {
		t.Fatalf("host socket calls=%+v releases=%+v, want it released with remove", calls, rels)
	}
}

func TestHostOwnLoginWithoutAuditorIsRefused(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := newDropsHarness(t, l)
	_, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub))
	if !errors.Is(err, ErrSpawnAudit) {
		t.Fatalf("err = %v, want ErrSpawnAudit", err)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
}

func TestNoHostOwnLoginRowWithoutAChoice(t *testing.T) {
	// A plain bound Host spawn.
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	// A Host spawn refused (the profile does not bind aws).
	l2, _ := hostBoundLauncher(t, envDumpArgv)
	l2.noAWS = true
	h2 := ownLoginHarness(t, l2)
	if _, err := h2.m.Spawn(ownSpec(t, launcher.OwnLoginAWS)); err == nil {
		t.Fatal("want a refusal")
	}
	// A Box spawn carrying the field.
	lb := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	hb := ownLoginHarness(t, lb)
	bs, err := hb.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), HostDropsAck: true,
		HostOwnLogin: []string{launcher.OwnLoginGitHub}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hb.m.Kill(bs.ID) })
	for name, hh := range map[string]*dropsHarness{"plain": h, "refused": h2, "box": hb} {
		if recs := auditOf(hh).records(); len(recs) != 0 {
			t.Errorf("%s: %d audit rows, want none", name, len(recs))
		}
	}
}

func TestRestartAuditsTheChoiceAgain(t *testing.T) {
	l, _ := hostBoundLauncher(t, []string{"sh", "-c", "exit 0"})
	h := ownLoginHarness(t, l)
	spec := ownSpec(t, launcher.OwnLoginGitHub)
	spec.Actor = "bearer"
	old := exitedSession(t, h.m, spec)
	ns, err := h.m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(ns.ID) })
	recs := auditOf(h).records()
	if len(recs) != 2 || recs[0].Actor != "bearer" || recs[1].Actor != "bearer" || recs[1].SessionID != ns.ID {
		t.Fatalf("audit rows = %+v, want two by bearer, the second for %s", recs, ns.ID)
	}
}
