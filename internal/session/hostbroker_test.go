package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// #360: a bound profile on Host gets its bindings through the broker, on a
// socket and files of its own, when node is on the session PATH.

// sessionTools is a PATH dir holding only what envDumpArgv runs (env, sleep),
// plus a stand-in node and gh when asked: whether node is "installed" is the
// test's choice, never the machine's.
func sessionTools(t *testing.T, node, gh bool) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"env", "sleep"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]bool{"node": node, "gh": gh} {
		if want {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// hostBoundLauncher is the bound profile with node (and gh) on its PATH, and
// nothing else a Host run would drop.
func hostBoundLauncher(t *testing.T, argv []string, extraEnv ...string) (dropsLauncher, string) {
	tools := sessionTools(t, true, true)
	return dropsLauncher{tieredLauncher: tieredLauncher{argv: argv, runtime: config.RuntimeBox}, bound: true,
		guardrail: threeGuard, path: tools, extraEnv: extraEnv}, tools
}

func hostAckSpec(t *testing.T) launcher.SpawnSpec {
	spec := hostSpec(t)
	spec.HostDropsAck = true
	return spec
}

// envOfHostProcessKV is envOfHostProcess with the values.
func envOfHostProcessKV(t *testing.T, s *Session) map[string]string {
	t.Helper()
	out, cancel, _ := s.Subscribe()
	defer cancel()
	var b strings.Builder
	deadline := time.After(5 * time.Second)
	for !strings.Contains(b.String(), "ENDOFENV") {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatalf("session ended; output %q", b.String())
			}
			b.WriteString(string(chunk))
		case <-deadline:
			t.Fatalf("no env output; got %q", b.String())
		}
	}
	env := map[string]string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "="); ok && k != "" && !strings.Contains(k, " ") {
			env[k] = v
		}
	}
	return env
}

func TestHostBoundSpawnGetsBrokerEnvAndToken(t *testing.T) {
	l, tools := hostBoundLauncher(t, envDumpArgv)
	h := newDropsHarness(t, l)
	s, err := h.m.Spawn(hostAckSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })

	env := envOfHostProcessKV(t, s)
	dir := h.hooks.hostDirFor(s.ID)
	if env["AGENTBOX_BROKER"] != "unix://"+dir+"/s" || env["AGENTBOX_DIR"] != dir {
		t.Fatalf("broker env: AGENTBOX_BROKER=%q AGENTBOX_DIR=%q, want the dir %s", env["AGENTBOX_BROKER"], env["AGENTBOX_DIR"], dir)
	}
	if want := "tok-" + s.ID + "." + s.BrokerNonce; s.BrokerNonce == "" || env[brokerTokenEnv] != want {
		t.Fatalf("%s = %q, want %q", brokerTokenEnv, env[brokerTokenEnv], want)
	}
	if want := dir + "/bin" + string(os.PathListSeparator) + tools; env["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", env["PATH"], want)
	}
	// Egress does not apply on Host, and nothing was asked of a box.
	for _, bad := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "NO_PROXY", config.ContextPathsEnv, "GH_REPO"} {
		if _, ok := env[bad]; ok {
			t.Fatalf("host process has %s", bad)
		}
	}
	if h.eg.spawnCalls() != 0 || h.boxes.prepares != 0 || h.boxes.wraps != 0 {
		t.Fatalf("egress=%d prepares=%d wraps=%d for a host spawn", h.eg.spawnCalls(), h.boxes.prepares, h.boxes.wraps)
	}

	calls, rel, _ := h.hooks.hostState()
	if len(calls) != 1 || calls[0].id != s.ID || calls[0].realGH != filepath.Join(tools, "gh") {
		t.Fatalf("HostFor calls = %+v, want one for %s with the real gh %s", calls, s.ID, filepath.Join(tools, "gh"))
	}
	if !calls[0].bs.Any() || calls[0].bs.Hash() != s.Bindings().Hash() || len(calls[0].bs.EgressAllow) != 0 {
		t.Fatalf("HostFor bindings = %+v, want the session's, without egress", calls[0].bs)
	}
	if len(rel) != 0 {
		t.Fatalf("a live session released its host socket: %+v", rel)
	}

	if !s.HostBroker || s.Runtime != config.RuntimeHost || s.Box != "" || s.Exclusive {
		t.Fatalf("session: HostBroker=%v runtime=%s box=%q exclusive=%v", s.HostBroker, s.Runtime, s.Box, s.Exclusive)
	}
	if s.HostDir != dir {
		t.Fatalf("HostDir = %q, want %q", s.HostDir, dir)
	}
	in := s.Info()
	if !in.HostBroker || in.AWS == nil || in.GitHub == nil || len(in.Tokens) == 0 || in.HostDropped != nil || in.Egress != "" {
		t.Fatalf("Info: host_broker=%v aws=%v github=%v tokens=%d dropped=%q egress=%q",
			in.HostBroker, in.AWS != nil, in.GitHub != nil, len(in.Tokens), in.HostDropped, in.Egress)
	}
	if len(in.ContextPaths) != 0 || s.EgressNonce != "" {
		t.Fatalf("host session carries box-only state: context=%v egress nonce=%q", in.ContextPaths, s.EgressNonce)
	}
	md := sidecarEntry(t, h.path, s.ID)
	if !md.HostBroker || md.BrokerNonce != s.BrokerNonce || md.AWS == nil || md.GitHub == nil || len(md.Tokens) == 0 {
		t.Fatalf("sidecar: %+v", md)
	}
}

func TestHostBoundSpawnNeedsAck(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := newDropsHarness(t, l)
	_, err := h.m.Spawn(hostSpec(t))
	want := `profile "p" runs on Host with AWS, GitHub and token bindings as the default credentials, not a limit; confirm to start it there (host_drops_bindings: true)`
	if err == nil || err.Error() != want || !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("err = %v\nwant  %q", err, want)
	}
	h.untouched(t)
	if calls, _, _ := h.hooks.hostState(); len(calls) != 0 {
		t.Fatalf("a refused spawn asked for host files: %+v", calls)
	}

	l.guardrail = []string{"GitHub binding"}
	h = newDropsHarness(t, l)
	_, err = h.m.Spawn(hostSpec(t))
	want = `profile "p" runs on Host with a GitHub binding as the default credentials, not a limit; confirm to start it there (host_drops_bindings: true)`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %q", err, want)
	}
}

func TestHostBoundSpawnStripsDaemonCreds(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv,
		"GH_TOKEN=DAEMON-gh", "GITHUB_TOKEN=DAEMON-gh2", "AWS_PROFILE=DAEMON-profile", "AWS_ACCESS_KEY_ID=DAEMON-key",
		"AWS_SECRET_ACCESS_KEY=DAEMON-secret", "GRAFANA_SA_TOKEN=DAEMON-grafana", brokerTokenEnv+"=DAEMON-tok",
		"AGENTBOX_DIR=DAEMON-dir", "GIT_CONFIG_COUNT=DAEMON-count", "AWS_CONFIG_FILE=DAEMON-cfg")
	h := newDropsHarness(t, l)
	s, err := h.m.Spawn(hostAckSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	env := envOfHostProcessKV(t, s)
	for _, gone := range []string{"GH_TOKEN", "GITHUB_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GRAFANA_SA_TOKEN", "GIT_CONFIG_COUNT"} {
		if v, ok := env[gone]; ok {
			t.Fatalf("%s=%q reached the Host session", gone, v)
		}
	}
	for k, v := range env {
		if strings.Contains(v, "DAEMON-") {
			t.Fatalf("%s=%q: a daemon value reached the Host session", k, v)
		}
	}
	if env["AWS_PROFILE"] != "agentbox" || env[brokerTokenEnv] != "tok-"+s.ID+"."+s.BrokerNonce {
		t.Fatalf("AWS_PROFILE=%q token=%q, want the broker's", env["AWS_PROFILE"], env[brokerTokenEnv])
	}
}

func TestHostSpawnWithoutNodeDropsBindings(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	l.path = sessionTools(t, false, true) // gh, but no node
	h := newDropsHarness(t, l)
	s, err := h.m.Spawn(hostAckSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	env := envOfHostProcessKV(t, s)
	for _, bad := range []string{"AGENTBOX_BROKER", brokerTokenEnv, "AGENTBOX_DIR", "AWS_CONFIG_FILE", "GH_CONFIG_DIR"} {
		if v, ok := env[bad]; ok {
			t.Fatalf("%s=%q in a Host session without node", bad, v)
		}
	}
	if env["PATH"] != l.path {
		t.Fatalf("PATH = %q, want the session's own %q", env["PATH"], l.path)
	}
	want := []string{"AWS binding", "GitHub binding", "token binding", nodeMissingItem}
	if !slices.Equal(s.HostDropped, want) || !slices.Equal(s.Info().HostDropped, want) {
		t.Fatalf("HostDropped = %q, want %q", s.Info().HostDropped, want)
	}
	if s.HostBroker || s.BrokerNonce != "" || s.Bindings().Any() {
		t.Fatalf("session holds broker state: host=%v nonce=%q bindings=%+v", s.HostBroker, s.BrokerNonce, s.Bindings())
	}
	if calls, _, _ := h.hooks.hostState(); len(calls) != 0 {
		t.Fatalf("HostFor called without node: %+v", calls)
	}
	h.noBoxSideEffects(t)
	h.hooks.mu.Lock()
	n := len(h.hooks.probeCalls)
	h.hooks.mu.Unlock()
	if n != 0 {
		t.Fatalf("base roles probed for bindings that were dropped: %d", n)
	}
}

func TestHostSessionEndRemovesHostDir(t *testing.T) {
	t.Run("kill", func(t *testing.T) {
		l, _ := hostBoundLauncher(t, []string{"sh", "-c", "sleep 30"})
		h := newDropsHarness(t, l)
		s, err := h.m.Spawn(hostAckSpec(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.m.Kill(s.ID); err != nil {
			t.Fatal(err)
		}
		if _, rel, _ := h.hooks.hostState(); !slices.Equal(rel, []hostRelease{{id: s.ID, remove: true}}) {
			t.Fatalf("releases = %+v, want one removing release for %s", rel, s.ID)
		}
		if !h.hooks.forgotten(s.ID) {
			t.Fatal("broker state of the ended Host session was not forgotten")
		}
	})
	t.Run("exit", func(t *testing.T) {
		l, _ := hostBoundLauncher(t, []string{"sh", "-c", "exit 0"})
		h := newDropsHarness(t, l)
		s, err := h.m.Spawn(hostAckSpec(t))
		if err != nil {
			t.Fatal(err)
		}
		waitStatus(t, s, statusExited, 5*time.Second)
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, rel, _ := h.hooks.hostState(); len(rel) > 0 {
				if !slices.Equal(rel, []hostRelease{{id: s.ID, remove: true}}) {
					t.Fatalf("releases = %+v", rel)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("an exited Host session kept its host socket")
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Killing the retained session afterwards releases nothing more.
		_ = h.m.Kill(s.ID)
		if _, rel, _ := h.hooks.hostState(); len(rel) != 1 {
			t.Fatalf("releases = %+v, want exactly one", rel)
		}
	})
}

func TestHostSpawnFailureReleasesHost(t *testing.T) {
	l, _ := hostBoundLauncher(t, []string{filepath.Join(t.TempDir(), "no-such-agent")})
	h := newDropsHarness(t, l)
	_, err := h.m.Spawn(hostAckSpec(t))
	if err == nil {
		t.Fatal("a spawn of a missing binary started")
	}
	calls, rel, _ := h.hooks.hostState()
	if len(calls) != 1 {
		t.Fatalf("HostFor calls = %+v, want one", calls)
	}
	if !slices.Equal(rel, []hostRelease{{id: calls[0].id, remove: true}}) {
		t.Fatalf("releases = %+v, want the failed spawn's socket given back and its dir removed", rel)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a failed spawn", n)
	}
}

// A HostFor failure (a socket path over the OS limit) refuses the start with the
// reason: the user chose a bound profile, so it never runs without its bindings
// silently.
func TestHostForErrorRefusesSpawn(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := newDropsHarness(t, l)
	h.hooks.hostErr = errors.New("broker: the socket path /very/long/state/broker/h/0123456789ab/s is 120 bytes; a unix socket path holds at most 107. Use a shorter data directory")
	_, err := h.m.Spawn(hostAckSpec(t))
	if err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "/very/long/state/broker/h/0123456789ab/s") ||
		!strings.HasPrefix(err.Error(), `profile "p": `) {
		t.Fatalf("err = %v, want a refusal naming the path", err)
	}
	if _, rel, _ := h.hooks.hostState(); len(rel) != 0 {
		t.Fatalf("a failed HostFor holds no reference, yet released: %+v", rel)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
}

func TestReattachHostSessionReopensItsSocket(t *testing.T) {
	l, _ := hostBoundLauncher(t, []string{"sh", "-c", "sleep 30"})
	h := newDropsHarness(t, l)
	fb := newFakeBackend()
	h.m.backend = fb
	s, err := h.m.Spawn(hostAckSpec(t))
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
	order := &callLog{}
	hooks2 := &fakeBrokerHooks{order: order}
	m2.SetBroker(hooks2)
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not restored")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	calls, rel, sweeps := hooks2.hostState()
	if len(calls) != 1 || calls[0].id != s.ID || calls[0].bs.Hash() != s.Bindings().Hash() || calls[0].realGH != "" {
		t.Fatalf("HostFor on reattach = %+v, want one for %s with its bindings", calls, s.ID)
	}
	if !r.HostBroker || r.BrokerNonce != s.BrokerNonce || r.HostDir != hooks2.hostDirFor(s.ID) {
		t.Fatalf("restored: HostBroker=%v nonce=%q (want %q) dir=%q", r.HostBroker, r.BrokerNonce, s.BrokerNonce, r.HostDir)
	}
	if len(rel) != 0 {
		t.Fatalf("reattach released: %+v", rel)
	}
	if len(sweeps) != 1 || !slices.Equal(sweeps[0], []string{s.ID}) {
		t.Fatalf("sweeps = %q, want one keeping %s", sweeps, s.ID)
	}
	if got := order.got(); slices.Index(got, "sweep") < slices.Index(got, "hostfor") {
		t.Fatalf("call order %q: the sweep must follow the reattach", got)
	}
	// Its end removes the dir.
	if err := m2.Kill(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, rel, _ := hooks2.hostState(); !slices.Equal(rel, []hostRelease{{id: s.ID, remove: true}}) {
		t.Fatalf("releases after the restored session ended = %+v", rel)
	}
}

// A daemon with no surviving sessions still sweeps what a crash left.
func TestReconcileSweepsHostDirsWithNothingLive(t *testing.T) {
	for _, backend := range []bool{true, false} {
		m := NewManager(tieredLauncher{argv: []string{"sh"}}, nil, 1<<16, time.Minute)
		if backend {
			m.backend = newFakeBackend()
		}
		hooks := &fakeBrokerHooks{}
		m.SetBroker(hooks)
		if err := m.Reconcile(); err != nil {
			t.Fatal(err)
		}
		if _, _, sweeps := hooks.hostState(); len(sweeps) != 1 || len(sweeps[0]) != 0 {
			t.Fatalf("backend=%v: sweeps = %q, want one keeping nothing", backend, sweeps)
		}
	}
}

// The box_only lock still refuses Host, ack or not, before anything is asked of
// the broker.
func TestBoxOnlyStillRefusesHost(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	l.hostBlock = "this profile is set to box only"
	h := newDropsHarness(t, l)
	_, err := h.m.Spawn(hostAckSpec(t))
	if err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "set to box only") {
		t.Fatalf("err = %v, want the lock refusal", err)
	}
	h.untouched(t)
	if calls, _, _ := h.hooks.hostState(); len(calls) != 0 {
		t.Fatalf("HostFor called for a locked profile: %+v", calls)
	}
}

// No broker running: a Host spawn of a bound profile is refused, never started
// without the bindings it was confirmed with.
func TestHostBoundSpawnRefusedWithoutBroker(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	m := NewManager(l, nil, 1<<16, time.Minute)
	_, err := m.Spawn(hostAckSpec(t))
	if err == nil || !strings.Contains(err.Error(), "credential broker is not running") {
		t.Fatalf("err = %v, want the broker refusal", err)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
}

// A box spawn of the same profile is unchanged: no Host socket.
func TestBoxSpawnNeverAsksForHostFiles(t *testing.T) {
	l, _ := hostBoundLauncher(t, []string{"sh", "-c", "sleep 30"})
	h := newDropsHarness(t, l)
	s, err := h.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), HostDropsAck: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if calls, _, _ := h.hooks.hostState(); len(calls) != 0 || s.HostBroker || s.HostDir != "" {
		t.Fatalf("box spawn: HostFor=%+v HostBroker=%v HostDir=%q", calls, s.HostBroker, s.HostDir)
	}
	if got := envNamed(h.boxes.lastEnv, "PATH"); len(got) != 1 || strings.Contains(got[0], "broker/h") {
		t.Fatalf("box PATH = %q", got)
	}
}
