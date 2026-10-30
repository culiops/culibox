package boxvm

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   []string
	envs    map[string][]string // first arg (start, stop, ...) -> the env of the last such call
	running atomic.Bool
	startN  atomic.Int32
	// startCancelled is set when a start's context was cancelled under it.
	startCancelled atomic.Bool
	delay          time.Duration
	failOn         string
}

func (f *fakeRunner) Run(ctx context.Context, env []string, bin string, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.envs == nil {
		f.envs = map[string][]string{}
	}
	f.envs[args[0]] = env
	f.mu.Unlock()
	if f.failOn != "" && args[0] == f.failOn {
		return "boom", errors.New("exit status 1")
	}
	switch args[0] {
	case "status":
		if !f.running.Load() {
			return "colima [profile=agent-box] is not running", errors.New("exit status 1")
		}
		return `{"display_name":"colima [profile=agent-box]","driver":"macOS Virtualization.Framework","arch":"aarch64","runtime":"docker","mount_type":"virtiofs","docker_socket":"unix:///r/vm/agent-box/docker.sock"}`, nil
	case "start":
		f.startN.Add(1)
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			f.startCancelled.Store(true)
			return "", ctx.Err()
		}
		f.running.Store(true)
	case "stop", "delete":
		f.running.Store(false)
	}
	return "", nil
}

// shortRoot is a data root short enough for the VM's socket paths (t.TempDir's
// is not: CheckSocketPaths refuses it). Rooted at /tmp, which is a symlink on
// macOS, so Config resolution is exercised too.
func shortRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "bvm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func setUpVM(t *testing.T, r Runner) *VM {
	t.Helper()
	root := shortRoot(t)
	v := NewVM(Config{Root: root, Home: root, ColimaBin: "/c", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}, r)
	if err := v.Provision(context.Background(), false, io.Discard); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestStatusParsesRunningAndStopped(t *testing.T) {
	r := &fakeRunner{}
	v := setUpVM(t, r)
	r.running.Store(false)
	if st, err := v.Status(context.Background()); err != nil || st.Running {
		t.Fatalf("stopped: %+v %v", st, err)
	}
	r.running.Store(true)
	st, err := v.Status(context.Background())
	if err != nil || !st.Running || st.MountType != "virtiofs" || st.DockerSocket != "/r/vm/agent-box/docker.sock" {
		t.Fatalf("running: %+v %v", st, err)
	}
}

func TestEnsureRunningRefusesWhenNotSetUp(t *testing.T) {
	v := NewVM(Config{Root: shortRoot(t), Home: "/h", ColimaBin: "/c", DockerBin: "/d", BrokerPort: 9071, EgressPort: 9072}, &fakeRunner{})
	if _, err := v.EnsureRunning(context.Background()); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("want ErrNotSetUp, got %v", err)
	}
}

func TestEnsureRunningStartsOnceForConcurrentCallers(t *testing.T) {
	r := &fakeRunner{delay: 50 * time.Millisecond}
	v := setUpVM(t, r)
	r.running.Store(false)
	r.startN.Store(0)
	var wg sync.WaitGroup
	var started atomic.Int32
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := v.EnsureRunning(context.Background())
			if err != nil {
				t.Error(err)
			}
			if s {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	if r.startN.Load() != 1 || started.Load() != 1 {
		t.Fatalf("want exactly one start, got starts=%d started=%d", r.startN.Load(), started.Load())
	}
}

func TestEnsureRunningTimesOut(t *testing.T) {
	r := &fakeRunner{delay: time.Second}
	v := setUpVM(t, r)
	r.running.Store(false)
	v.startTimeout = 20 * time.Millisecond
	if _, err := v.EnsureRunning(context.Background()); err == nil {
		t.Fatal("a start past the timeout must fail")
	}
}

func TestCancelledCallerDoesNotKillTheSharedStart(t *testing.T) {
	r := &fakeRunner{delay: 150 * time.Millisecond}
	v := setUpVM(t, r)
	r.running.Store(false)
	r.startN.Store(0)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := v.EnsureRunning(ctx)
		errc <- err
	}()
	for r.startN.Load() == 0 { // the start is in flight
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("the cancelled caller must see its own cancellation")
	}

	// A later caller waits for the start that is still running, then is the
	// one handed started=true (its boot was never reconciled).
	started, err := v.EnsureRunning(context.Background())
	if err != nil || !started {
		t.Fatalf("the late caller must reconcile the boot: started=%v err=%v", started, err)
	}
	if r.startCancelled.Load() {
		t.Fatal("cancelling a caller killed colima start")
	}
	if r.startN.Load() != 1 {
		t.Fatalf("want one start, got %d", r.startN.Load())
	}
	if again, err := v.EnsureRunning(context.Background()); err != nil || again {
		t.Fatalf("started=true is handed out once: again=%v err=%v", again, err)
	}
}

func TestAWaiterCancelledOnTheLockReturnsEarly(t *testing.T) {
	r := &fakeRunner{delay: 300 * time.Millisecond}
	v := setUpVM(t, r)
	r.running.Store(false)
	go v.EnsureRunning(context.Background()) // holds the lock for the start
	for r.startN.Load() < 2 {                // setUpVM's own start was 1
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, err := v.EnsureRunning(ctx); err == nil || time.Since(t0) > 200*time.Millisecond {
		t.Fatalf("a cancelled waiter must return early with an error: %v after %v", err, time.Since(t0))
	}
}

func TestAnErroredStartStillReconcilesOnceWhenTheVMComesUp(t *testing.T) {
	r := &fakeRunner{delay: time.Second}
	v := setUpVM(t, r)
	r.running.Store(false)
	v.startTimeout = 20 * time.Millisecond
	if _, err := v.EnsureRunning(context.Background()); err == nil {
		t.Fatal("a start past the timeout must fail")
	}
	r.running.Store(true) // the boot finished anyway
	started, err := v.EnsureRunning(context.Background())
	if err != nil || !started {
		t.Fatalf("the late boot must be reconciled once: started=%v err=%v", started, err)
	}
	if again, _ := v.EnsureRunning(context.Background()); again {
		t.Fatal("reconcile must be handed out once")
	}
}

func TestStartUsesTheSafeArgsAndEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://user-docker:2375")
	t.Setenv("DOCKER_CONTEXT", "user-context")
	r := &fakeRunner{}
	v := setUpVM(t, r)
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for _, c := range r.calls {
		if strings.HasPrefix(c, "start ") {
			found = c == strings.Join(v.Config().StartArgs(), " ")
		}
	}
	if !found {
		t.Fatalf("start must use StartArgs: %q", r.calls)
	}
	env := r.envs["start"]
	has := func(kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	if !has("COLIMA_HOME=" + v.Config().ColimaHome()) {
		t.Fatalf("start must run with COLIMA_HOME under the data root, env=%q", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "DOCKER_HOST=") || strings.HasPrefix(e, "DOCKER_CONTEXT=") {
			t.Fatalf("the user's docker endpoint leaked into colima's env: %q", e)
		}
	}
}

func TestNothingReachesColimaWithAnUnusableConfig(t *testing.T) {
	good := func(root string) Config {
		return Config{Root: root, Home: root, ColimaBin: "/c", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}
	}
	long := filepath.Join(shortRoot(t), strings.Repeat("d", 60)) // sockets past macOS's limit
	for name, cfg := range map[string]Config{
		"comma in the root": good(filepath.Join(shortRoot(t), "a,b")),
		"socket too long":   good(long),
		"relative colima":   {Root: shortRoot(t), Home: "/h", ColimaBin: "colima", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072},
	} {
		r := &fakeRunner{}
		v := NewVM(cfg, r)
		// A stamp so EnsureRunning gets past ErrNotSetUp: the config is what must stop it.
		if err := os.MkdirAll(v.Config().ColimaHome(), 0o700); err == nil {
			_ = os.WriteFile(v.Config().StampPath(), []byte("x\n"), 0o600)
		}
		ctx := context.Background()
		if _, err := v.Status(ctx); err == nil {
			t.Errorf("%s: Status must refuse", name)
		}
		if v.Running(ctx) {
			t.Errorf("%s: Running must be false", name)
		}
		if _, err := v.EnsureRunning(ctx); err == nil {
			t.Errorf("%s: EnsureRunning must refuse", name)
		}
		if err := v.Stop(ctx); err == nil {
			t.Errorf("%s: Stop must refuse", name)
		}
		if err := v.Provision(ctx, true, io.Discard); err == nil {
			t.Errorf("%s: Provision must refuse", name)
		}
		if len(r.calls) != 0 {
			t.Errorf("%s: colima was invoked: %q", name, r.calls)
		}
	}
}

func TestEnsureRunningDoesNotStartWhenAlreadyRunning(t *testing.T) {
	r := &fakeRunner{}
	v := setUpVM(t, r)
	r.running.Store(true)
	r.startN.Store(0)
	started, err := v.EnsureRunning(context.Background())
	if err != nil || started || r.startN.Load() != 0 {
		t.Fatalf("a running VM must not be started: started=%v err=%v starts=%d", started, err, r.startN.Load())
	}
}

func TestStatusOfACancelledContextIsAnErrorNotStopped(t *testing.T) {
	v := setUpVM(t, &fakeRunner{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Status(ctx); err == nil {
		t.Fatal("a cancelled context must not read as a stopped VM")
	}
}

func TestConfigIsSymlinkResolved(t *testing.T) {
	real := shortRoot(t)
	link := real + "-link"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	// The home is reached through a symlink and the data root sits inside it
	// (not yet created): without resolving, Mounts would see a different prefix.
	root := filepath.Join(link, "data")
	v := NewVM(Config{Root: root, Home: link, ColimaBin: "/c", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}, &fakeRunner{})
	c := v.Config()
	wantHome, _ := filepath.EvalSymlinks(real)
	if c.Home != wantHome || c.Root != filepath.Join(wantHome, "data") {
		t.Fatalf("not resolved: home=%q root=%q", c.Home, c.Root)
	}
	if m := c.Mounts(); len(m) != 1 {
		t.Fatalf("root inside the home must not add a second mount: %q", m)
	}
}

func TestProvisionWritesYAMLAndStampAndDetectsDrift(t *testing.T) {
	r := &fakeRunner{}
	v := setUpVM(t, r)
	if _, err := os.Stat(v.Config().YAMLPath()); err != nil {
		t.Fatal("yaml not written")
	}
	if !v.SetUp() || v.Drifted() {
		t.Fatal("fresh provision must be set up and not drifted")
	}
	c := v.Config()
	c.CPUs = 8
	v2 := NewVM(c, r)
	if !v2.Drifted() {
		t.Fatal("a config change must read as drift")
	}
	r.running.Store(true)
	if err := v2.Provision(context.Background(), false, io.Discard); err == nil || !strings.Contains(err.Error(), "-recreate") {
		t.Fatalf("drift on an existing VM must ask for -recreate: %v", err)
	}
}

func TestProvisionRecreateDeletesFirst(t *testing.T) {
	r := &fakeRunner{}
	v := setUpVM(t, r)
	if err := v.Provision(context.Background(), true, io.Discard); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	joined := strings.Join(r.calls, "|")
	del := strings.Index(joined, "delete --profile agent-box --force")
	if del < 0 {
		t.Fatalf("recreate must delete the VM: %s", joined)
	}
	// setUpVM's own first provision started the VM once; the recreate's start
	// must come after the delete, not before.
	if start := strings.LastIndex(joined, "start "); start < del {
		t.Fatalf("recreate must start after it deletes: %s", joined)
	}
}

func TestStateFilesAreOwnerOnly(t *testing.T) {
	v := setUpVM(t, &fakeRunner{})
	for _, p := range []string{v.Config().YAMLPath(), v.Config().StampPath()} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", filepath.Base(p), fi.Mode(), err)
		}
	}
}

func TestInputRulesRunsIptablesOverColimaSSH(t *testing.T) {
	r := &fakeRunner{}
	v := setUpVM(t, r)
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
	if _, err := v.InputRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(r.calls, "|"), "ssh --profile agent-box -- sudo iptables -w -S INPUT"; got != want {
		t.Fatalf("calls = %q, want %q (it must never start the VM)", got, want)
	}
	if !slices.Equal(r.envs["ssh"], v.env()) {
		t.Fatalf("ssh env = %v, want the pinned env", r.envs["ssh"])
	}
}
