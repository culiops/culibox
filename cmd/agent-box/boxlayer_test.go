package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/config"
)

// fakeColima stands in for the colima binary. Its zero value answers every
// call as "not running"; nothing is started unless a test asks for it.
type fakeColima struct {
	mu      sync.Mutex
	running bool
	socket  string // docker_socket reported while running
	mount   string // mount_type reported while running; "" = virtiofs
	garbled bool   // status exits 0 with output that is not colima's JSON
	hang    bool   // status blocks until its context ends (a wedged hostagent)
	calls   []string
}

func (f *fakeColima) Run(ctx context.Context, _ []string, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "status":
		if f.hang {
			f.mu.Unlock()
			<-ctx.Done()
			f.mu.Lock()
			return "", ctx.Err()
		}
		if f.garbled {
			return "WARN something colima printed\n", nil
		}
		if !f.running {
			return "colima [profile=agent-box] is not running", errors.New("exit status 1")
		}
		mount := f.mount
		if mount == "" {
			mount = "virtiofs"
		}
		return fmt.Sprintf(`{"driver":"macOS Virtualization.Framework","arch":"aarch64","mount_type":%q,"docker_socket":"unix://%s"}`,
			mount, f.socket), nil
	case "start":
		f.running = true
	case "stop":
		f.running = false
	}
	return "", nil
}

func (f *fakeColima) called(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, sub+" ") || c == sub {
			return true
		}
	}
	return false
}

// fakeDocker is a /bin/sh docker CLI. It logs every argv, strips the global
// flags, and answers each subcommand from a file the test writes.
type fakeDocker struct {
	bin, dir string
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	dir := t.TempDir()
	d := &fakeDocker{bin: filepath.Join(dir, "docker"), dir: dir}
	script := `#!/bin/sh
d=` + dir + `
echo "$*" >> "$d/log"
while [ "$1" = --host ] || [ "$1" = --config ]; do shift 2; done
case "$1" in
version) cat "$d/arch" 2>/dev/null || echo arm64 ;;
image) cat "$d/image" 2>/dev/null || { echo "Error: No such image" >&2; exit 1; } ;;
ps) cat "$d/ps" 2>/dev/null ;;
context) [ -f "$d/context" ] || { echo "cannot connect" >&2; exit 1; }; cat "$d/context" ;;
run|exec) [ -f "$d/sleep" ] && exec sleep 5; cat "$d/probe" 2>/dev/null ;;
info) echo '{"runc":{}}' ;;
rm) ;;
esac
exit 0
`
	if err := os.WriteFile(d.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *fakeDocker) set(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *fakeDocker) log() string {
	b, _ := os.ReadFile(filepath.Join(d.dir, "log"))
	return string(b)
}

// resolved is p with symlinks resolved: the spelling the VM config uses.
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const blockedProbe = "DNS ok\nBLOCKED host.lima.internal ECONNREFUSED\nBLOCKED 192.168.5.2 ETIMEDOUT\n"

// shortRoot is a data root short enough for unix socket paths (104 bytes on
// macOS, 108 on Linux): t.TempDir embeds the test name and lives under
// GOTMPDIR, which the box image sets to a long path, so it overflows them.
func shortRoot(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "bl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// darwinLayer builds a darwin layer over the fakes, with the VM set up (its
// stamp written) unless notSetUp.
func darwinLayer(t *testing.T, notSetUp bool) (*boxLayer, *fakeColima, *fakeDocker) {
	t.Helper()
	root := shortRoot(t)
	d := newFakeDocker(t)
	fc := &fakeColima{}
	l, err := newBoxLayer(config.Config{DockerBin: d.bin, ColimaBin: "/bin/sh"}, root, root, "darwin", fc, nil)
	if err != nil {
		t.Fatal(err)
	}
	fc.socket = l.vm.Config().DockerSocket()
	if !notSetUp {
		vc := l.vm.Config()
		if err := os.MkdirAll(vc.ColimaHome(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vc.StampPath(), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return l, fc, d
}

func TestLinuxLayerHasNoVMAndNoDockerArgs(t *testing.T) {
	root := t.TempDir()
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.vm != nil || len(l.dockerArgs) != 0 {
		t.Fatalf("linux must be unchanged: vm=%v args=%v", l.vm, l.dockerArgs)
	}
	o := l.options(config.Config{DockerBin: "/bin/sh"})
	if o.Prepare != nil || o.Verify != nil || o.Reachable != nil || o.Shared != nil || len(o.DockerArgs) != 0 {
		t.Fatalf("linux options must carry no VM hooks: %+v", o)
	}
	if o.StateRoot != root || o.Image != config.DefaultBoxImage || o.Instance != "default" {
		t.Fatalf("linux options = %+v", o)
	}
	if l.vmStopped(context.Background()) {
		t.Fatal("linux has no VM to be stopped")
	}
}

func TestBoxLayerOptionsFenceLinuxOnly(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh"}
	if o := (&boxLayer{goos: "linux"}).options(cfg); !o.EgressAll || !o.GuardInPlace {
		t.Errorf("linux must fence and guard every box: EgressAll=%v GuardInPlace=%v", o.EgressAll, o.GuardInPlace)
	}
	if o := (&boxLayer{goos: "darwin"}).options(cfg); o.EgressAll || o.GuardInPlace {
		t.Errorf("darwin keeps its default network: EgressAll=%v GuardInPlace=%v", o.EgressAll, o.GuardInPlace)
	}
}

func TestBoxLayerOptionsCarryLimits(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh", BoxPidsLimit: 2048, BoxTmpSize: "2g", BoxMemory: "3g"}
	for _, goos := range []string{"linux", "darwin"} {
		o := (&boxLayer{goos: goos}).options(cfg)
		if o.PidsLimit != 2048 || o.TmpSize != "2g" || o.Memory != "3g" {
			t.Errorf("%s: limits not carried: pids=%d tmp=%q mem=%q", goos, o.PidsLimit, o.TmpSize, o.Memory)
		}
	}
}

func TestDarwinLayerPinsTheVM(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}
	root := t.TempDir()
	l, err := newBoxLayer(cfg, root, root, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root = resolved(t, root) // macOS: /var/folders → /private/var/folders
	if l.vm == nil || !strings.Contains(strings.Join(l.dockerArgs, " "), "--host unix://"+root+"/vm/agent-box/docker.sock") {
		t.Fatalf("darwin layer must pin the VM socket: %+v", l)
	}
}

func TestDarwinLayerWithoutColimaIsUnavailable(t *testing.T) {
	_, err := newBoxLayer(config.Config{DockerBin: "/bin/sh", ColimaBin: "colima-definitely-missing"}, t.TempDir(), t.TempDir(), "darwin", &fakeColima{}, nil)
	if err == nil || !strings.Contains(err.Error(), "brew install colima") {
		t.Fatalf("missing colima must explain the install: %v", err)
	}
}

// One slice of global docker flags reaches every docker caller: a caller that
// misses it talks to the user's own daemon, outside the VM's loopback block.
func TestDarwinLayerSharesOneDockerArgsSlice(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}
	root := t.TempDir()
	l, err := newBoxLayer(cfg, root, root, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := l.vm.Config().DockerArgs()
	o := l.options(cfg)
	cli, ok := o.Client.(box.CLIClient)
	if !ok {
		t.Fatalf("client is %T", o.Client)
	}
	for what, got := range map[string][]string{
		"layer":              l.dockerArgs,
		"CLIClient.Args":     cli.Args,
		"Options.DockerArgs": o.DockerArgs,
		"ImageProber.Args":   l.prober("img").Args,
		"boximage.Args":      l.img.Args,
		"layer cli.Args":     l.cli.Args,
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %q, want %q", what, got, want)
		}
	}
	if p := l.prober("img"); p.Docker != l.dockerBin || p.Image != "img" {
		t.Errorf("prober = %+v", p)
	}
	if o.Prepare == nil || o.Verify == nil || o.Reachable == nil {
		t.Errorf("darwin options must carry the VM hooks")
	}
}

// Every darwin path comes from the VM's symlink-resolved root and home: the VM
// mounts the resolved paths, so an unresolved one is invisible inside it.
func TestDarwinLayerUsesResolvedPaths(t *testing.T) {
	real := resolved(t, t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}
	l, err := newBoxLayer(cfg, link, link, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := l.options(cfg)
	if o.StateRoot != real {
		t.Errorf("StateRoot = %q, want the resolved %q", o.StateRoot, real)
	}
	if !reflect.DeepEqual(o.Shared, []string{real}) {
		t.Errorf("Shared = %q, want [%q]", o.Shared, real)
	}
	if got := strings.Join(l.dockerArgs, " "); !strings.Contains(got, "unix://"+real+"/vm/") || strings.Contains(got, link) {
		t.Errorf("docker args = %q, want the resolved root", got)
	}
}

func TestVerifyPassesABlockedBoxThroughThePinnedDaemon(t *testing.T) {
	l, _, d := darwinLayer(t, false)
	d.set(t, "probe", blockedProbe)
	if err := l.verify(context.Background(), "ab-box-1"); err != nil {
		t.Fatalf("a blocked box must pass: %v", err)
	}
	want := strings.Join(l.dockerArgs, " ") + " exec ab-box-1 node -e "
	if !strings.Contains(d.log(), want) {
		t.Fatalf("probe must exec in the box through the VM's daemon; log:\n%s", d.log())
	}
}

func TestVerifyRefusesAReachingBox(t *testing.T) {
	l, _, d := darwinLayer(t, false)
	d.set(t, "probe", "DNS ok\nREACHED host.lima.internal\n")
	err := l.verify(context.Background(), "ab-box-1")
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "containment check failed") {
		t.Fatalf("a reaching box must be refused as unavailable: %v", err)
	}
}

// The spawn holds box operations while Verify runs, so a hung exec must not
// hold them forever.
func TestVerifyHasADeadline(t *testing.T) {
	l, _, d := darwinLayer(t, false)
	d.set(t, "sleep", "")
	old := verifyTimeout
	verifyTimeout = 200 * time.Millisecond
	defer func() { verifyTimeout = old }()
	start := time.Now()
	err := l.verify(context.Background(), "ab-box-1")
	if err == nil || !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("a hung probe must fail closed: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("verify ignored its deadline: %s", time.Since(start))
	}
}

// A hung `colima status` runs under the VM's single-slot lock, so without a
// deadline it would block this spawn and every later boxed spawn forever.
func TestPrepareHasADeadline(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	fc.hang = true
	old := prepareVMTimeout
	prepareVMTimeout = 200 * time.Millisecond
	defer func() { prepareVMTimeout = old }()
	for i := 0; i < 2; i++ { // the second spawn must get the lock back
		start := time.Now()
		err := l.prepare(context.Background())
		if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "did not answer") {
			t.Fatalf("spawn %d: a hung VM check must fail as unavailable: %v", i, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("spawn %d: prepare ignored its deadline: %s", i, time.Since(start))
		}
	}
}

func TestPrepareNotSetUpIsUnavailable(t *testing.T) {
	l, fc, _ := darwinLayer(t, true)
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "box setup") {
		t.Fatalf("not set up must point at box setup: %v", err)
	}
	if fc.called("start") {
		t.Fatal("a VM that is not set up must not be started")
	}
}

// The first boxed spawn starts the VM; every box in a freshly started VM is a
// leftover from before it stopped, so reconcile runs once, right then.
func TestPrepareStartsTheVMAndReconcilesOnce(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	d.set(t, "ps", "ab-box-old\n")
	if err := l.prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !fc.called("start") {
		t.Fatal("prepare must start a stopped VM")
	}
	if n := strings.Count(d.log(), "rm -f ab-box-old"); n != 1 {
		t.Fatalf("leftover box removed %d times after the start, want 1; log:\n%s", n, d.log())
	}
	if err := l.prepare(context.Background()); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if n := strings.Count(d.log(), "rm -f ab-box-old"); n != 1 {
		t.Fatalf("a VM already running must not be reconciled again (%d removals)", n)
	}
}

func TestPrepareRefusesAMissingOrForeignImage(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "not built") {
		t.Fatalf("a missing image must fail the spawn: %v", err)
	}
	d.set(t, "image", "amd64|"+boximage.Hash()+"\n")
	err = l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "emulated") {
		t.Fatalf("a wrong-arch image must fail the spawn: %v", err)
	}
	d.set(t, "image", "arm64|0ld\n")
	if err := l.prepare(context.Background()); err != nil {
		t.Fatalf("a stale image still runs: %v", err)
	}
}

// The spawn path is strict: a registry image that is not on the daemon is a
// refusal that points at `box setup`, never a pull.
func TestPrepareMissingRemoteImageNeverPulls(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	l.image = "ghcr.io/culiops/agent-box@sha256:" + strings.Repeat("a", 64)
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "agent-box box setup") {
		t.Fatalf("a missing remote image must fail the spawn and name box setup: %v", err)
	}
	for _, line := range strings.Split(d.log(), "\n") {
		if strings.Contains(line, "pull") {
			t.Fatalf("spawn pulled an image: %s", d.log())
		}
	}
}

// Review focus 3: with the VM stopped, the boot pass and the daily reclaim
// neither start the VM nor touch docker, and the sweep skips.
func TestStoppedVMIsLeftAloneByBackgroundPasses(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	ctx := context.Background()
	if !l.vmStopped(ctx) {
		t.Fatal("a VM that is not running is stopped")
	}
	l.bootReconcile(ctx)
	l.dailyReclaim(ctx)
	rep, err := l.boxes.Sweep(ctx, nil)
	if err != nil || !rep.Skipped {
		t.Fatalf("sweep with the VM stopped = %+v, %v; want skipped", rep, err)
	}
	if fc.called("start") {
		t.Fatal("a background pass started the VM")
	}
	if strings.Contains(d.log(), " ps ") || strings.Contains(d.log(), " rm ") {
		t.Fatalf("a background pass reached docker with the VM stopped:\n%s", d.log())
	}
}

func TestRunningVMIsReconciledAtBoot(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	d.set(t, "ps", "ab-box-old\n")
	l.bootReconcile(context.Background())
	if !strings.Contains(d.log(), "rm -f ab-box-old") {
		t.Fatalf("boot reconcile must reap orphans in a running VM:\n%s", d.log())
	}
	if fc.called("start") {
		t.Fatal("boot reconcile must never start the VM")
	}
}

func runVM(t *testing.T, l *boxLayer, a cliArgs) (int, string) {
	t.Helper()
	var out bytes.Buffer
	rc := runBoxVM(context.Background(), a, config.Config{}, l, &out)
	return rc, out.String()
}

func TestBoxStopRefusesRunningBoxesUnlessForced(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	d.set(t, "ps", "ab-box-1\trunning\t/Users/a/p\t2026-10-15 10:00:00 +0000 UTC\nab-box-2\texited\t/Users/a/q\t\n")
	rc, out := runVM(t, l, cliArgs{Sub: "stop"})
	if rc != 1 || !strings.Contains(out, "refusing: 1 box(es) running") || !strings.Contains(out, "ab-box-1") {
		t.Fatalf("stop with a running box = %d:\n%s", rc, out)
	}
	if fc.called("stop") {
		t.Fatal("a refused stop must not stop the VM")
	}
	rc, out = runVM(t, l, cliArgs{Sub: "stop", Force: true})
	if rc != 0 || !fc.called("stop") {
		t.Fatalf("forced stop = %d:\n%s", rc, out)
	}
}

func TestBoxStopFailsClosedWhenBoxesCannotBeListed(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	d.set(t, "ps", "") // replaced below by a failing docker
	if err := os.WriteFile(d.bin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rc, out := runVM(t, l, cliArgs{Sub: "stop"})
	if rc != 1 || fc.called("stop") {
		t.Fatalf("an unlistable VM must not be stopped: %d\n%s", rc, out)
	}
}

// A VM whose state cannot be read may be running boxes: without -force, stop
// refuses rather than reading the error as "not running".
func TestBoxStopRefusesAnUnreadableVMUnlessForced(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	fc.garbled = true
	rc, out := runVM(t, l, cliArgs{Sub: "stop"})
	if rc != 1 || !strings.Contains(out, "refusing") || !strings.Contains(out, "unreadable output") {
		t.Fatalf("stop with an unreadable VM state = %d:\n%s", rc, out)
	}
	if fc.called("stop") {
		t.Fatal("a refused stop must not stop the VM")
	}
	rc, out = runVM(t, l, cliArgs{Sub: "stop", Force: true})
	if rc != 0 || !fc.called("stop") {
		t.Fatalf("forced stop = %d:\n%s", rc, out)
	}
}

// colima status exits non-zero for a stopped VM, but also for other failures
// while the VM runs; Status reads both as "not running". So without -force,
// stop never calls colima stop on that reading: if the reading lied, nothing
// is stopped and no session ends.
func TestBoxStopOnANotRunningVMStopsNothing(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	rc, out := runVM(t, l, cliArgs{Sub: "stop"})
	if rc != 0 || !strings.Contains(out, "not running") {
		t.Fatalf("stop of a not-running VM = %d:\n%s", rc, out)
	}
	if fc.called("stop") {
		t.Fatal("stop must not call colima stop on a VM that reads as not running")
	}
	rc, out = runVM(t, l, cliArgs{Sub: "stop", Force: true})
	if rc != 0 || !fc.called("stop") {
		t.Fatalf("forced stop = %d:\n%s", rc, out)
	}
}

// -recreate deletes the VM, which ends every session in it, so it takes the
// same live-box guard as stop.
func TestBoxSetupRecreateRefusesRunningBoxesUnlessForced(t *testing.T) {
	l, fc, d := readySetup(t, "default")
	fc.running = true
	d.set(t, "ps", "ab-box-1\trunning\t/Users/a/p\t2026-10-15 10:00:00 +0000 UTC\n")
	rc, out := runVM(t, l, cliArgs{Sub: "setup", Recreate: true})
	if rc != 1 || !strings.Contains(out, "refusing: 1 box(es) running") {
		t.Fatalf("recreate with a running box = %d:\n%s", rc, out)
	}
	if fc.called("delete") || fc.called("start") {
		t.Fatal("a refused recreate must not touch the VM")
	}
	rc, out = runVM(t, l, cliArgs{Sub: "setup", Recreate: true, Force: true})
	if rc != 0 || !fc.called("delete") {
		t.Fatalf("forced recreate = %d:\n%s", rc, out)
	}
}

func TestBoxSetupRecreateRefusesAnUnreadableVMUnlessForced(t *testing.T) {
	l, fc, _ := readySetup(t, "default")
	fc.garbled = true
	rc, out := runVM(t, l, cliArgs{Sub: "setup", Recreate: true})
	if rc != 1 || !strings.Contains(out, "refusing") || fc.called("delete") {
		t.Fatalf("recreate with an unreadable VM = %d:\n%s", rc, out)
	}
}

// A "not running" reading is also what Status returns for any non-zero
// `colima status` exit while the VM runs, so its boxes cannot be listed:
// -recreate (which deletes the VM) needs -force on that reading, as stop does.
func TestBoxSetupRecreateOfANotRunningVMNeedsForce(t *testing.T) {
	l, fc, _ := readySetup(t, "default")
	rc, out := runVM(t, l, cliArgs{Sub: "setup", Recreate: true})
	if rc != 1 || !strings.Contains(out, "refusing") || !strings.Contains(out, "-force") {
		t.Fatalf("recreate of a not-running VM without -force = %d:\n%s", rc, out)
	}
	if fc.called("delete") || fc.called("start") {
		t.Fatal("a refused recreate must not touch the VM")
	}
	rc, out = runVM(t, l, cliArgs{Sub: "setup", Recreate: true, Force: true})
	if rc != 0 || !fc.called("delete") {
		t.Fatalf("forced recreate of a not-running VM = %d:\n%s", rc, out)
	}
}

func TestBoxStatusReportsVMImageAndBoxes(t *testing.T) {
	l, fc, d := darwinLayer(t, true)
	rc, out := runVM(t, l, cliArgs{Sub: "status"})
	if rc != 0 || !strings.Contains(out, "not set up") {
		t.Fatalf("status not set up = %d:\n%s", rc, out)
	}
	l, fc, d = darwinLayer(t, false)
	rc, out = runVM(t, l, cliArgs{Sub: "status"})
	if rc != 0 || !strings.Contains(out, "stopped (starts on the first boxed spawn)") {
		t.Fatalf("status stopped = %d:\n%s", rc, out)
	}
	fc.running = true
	d.set(t, "image", "arm64|0ld\n")
	d.set(t, "ps", "ab-box-1\trunning\t/Users/a/p\t\n")
	rc, out = runVM(t, l, cliArgs{Sub: "status"})
	for _, want := range []string{"box VM: running", "virtiofs", "WARN image", "older than this agent-box", "ab-box-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status running lacks %q:\n%s", want, out)
		}
	}
	if rc != 0 {
		t.Errorf("a stale image is a warning, not a failure: rc %d", rc)
	}
	if fc.called("start") {
		t.Fatal("status must never start the VM")
	}
}

func readySetup(t *testing.T, ctxName string) (*boxLayer, *fakeColima, *fakeDocker) {
	l, fc, d := darwinLayer(t, true)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	d.set(t, "probe", blockedProbe)
	if ctxName != "" {
		d.set(t, "context", ctxName+"\n")
	}
	return l, fc, d
}

func TestBoxSetupProvisionsProbesAndChecksContext(t *testing.T) {
	l, fc, d := readySetup(t, "default")
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 0 {
		t.Fatalf("setup = %d:\n%s", rc, out)
	}
	for _, want := range []string{"PASS box VM", "PASS image", "PASS containment", "PASS docker context", "boxed claude token"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output lacks %q:\n%s", want, out)
		}
	}
	if !l.vm.SetUp() {
		t.Error("setup must stamp the VM")
	}
	// The containment probe is a throwaway container on the VM's daemon.
	if !strings.Contains(d.log(), strings.Join(l.dockerArgs, " ")+" run --rm --cap-drop=ALL") {
		t.Errorf("probe must run through the pinned daemon:\n%s", d.log())
	}
	// Review focus 4: the user-context check reads the USER's docker CLI, so it
	// must not carry our --host/--config (which would always answer "ours is fine").
	sawContext := false
	for _, line := range strings.Split(d.log(), "\n") {
		if strings.Contains(line, "context show") {
			sawContext = true
			if line != "context show" {
				t.Errorf("context check ran as %q, want plain `docker context show`", line)
			}
		}
	}
	if !sawContext {
		t.Error("setup never checked the user's docker context")
	}
	for _, c := range fc.calls {
		if strings.HasPrefix(c, "start") && !strings.Contains(c, "--activate=false") {
			t.Errorf("colima start without --activate=false: %q", c)
		}
	}
}

func TestBoxSetupRefusesASwitchedDockerContext(t *testing.T) {
	l, _, _ := readySetup(t, "colima-agent-box")
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 1 || !strings.Contains(out, "FAIL docker context") {
		t.Fatalf("a switched context must fail setup = %d:\n%s", rc, out)
	}
}

func TestBoxSetupWarnsWhenTheContextCannotBeChecked(t *testing.T) {
	l, _, _ := readySetup(t, "") // docker context show fails
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 0 || !strings.Contains(out, "WARN docker context") {
		t.Fatalf("an unchecked context warns and proceeds = %d:\n%s", rc, out)
	}
}

func TestBoxSetupFailsAReachingProbe(t *testing.T) {
	l, _, d := readySetup(t, "default")
	d.set(t, "probe", "DNS ok\nREACHED host.lima.internal\n")
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 1 || !strings.Contains(out, "FAIL containment") {
		t.Fatalf("a reaching probe must fail setup = %d:\n%s", rc, out)
	}
}

func TestBoxSetupFailsAForeignSocket(t *testing.T) {
	l, fc, _ := readySetup(t, "default")
	fc.socket = "/Users/a/.colima/default/docker.sock"
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 1 || !strings.Contains(out, "docker socket is") {
		t.Fatalf("a VM on another socket must fail setup = %d:\n%s", rc, out)
	}
}

var _ boxvm.Runner = (*fakeColima)(nil)

// The serve path protects the data root and the directories of every config
// file and of the profiles file, deduped, and the box manager refuses a
// workspace over a config dir that is outside the data root.
func TestProtectedDirsReachTheBoxManager(t *testing.T) {
	root := t.TempDir()
	cfgDir := t.TempDir()
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json"), filepath.Join(cfgDir, "config.json")},
		filepath.Join(root, "profiles.json"), "")
	if len(prot) != 2 || prot[0].What != "agent-box's data directory" || prot[1].What != "agent-box's config directory" {
		t.Fatalf("want data root + the outside config dir, deduped: %+v", prot)
	}
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, t.TempDir(), "linux", nil, prot)
	if err != nil {
		t.Fatal(err)
	}
	if reason, bad := l.boxes.Unboxable(cfgDir); !bad || !strings.Contains(reason, "config directory") {
		t.Fatalf("a workspace at the config dir must be refused: %q %v", reason, bad)
	}
}

// load and store.Open read through a symlinked config.json / profiles.json, so
// the link targets' directories (outside the data root, e.g. ~/dotfiles) are
// protected too, along with the directories holding the links.
func TestProtectedDirsFollowSymlinkedConfigAndProfiles(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "data")
	xdgDir := filepath.Join(home, "xdg", "agent-box")
	dots := filepath.Join(home, "dotfiles")
	profDots := filepath.Join(home, "profdots")
	for _, d := range []string{root, xdgDir, dots, profDots} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(dots, "config.json"), filepath.Join(profDots, "profiles.json")} {
		if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dots, "config.json"), filepath.Join(xdgDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(profDots, "profiles.json"), filepath.Join(root, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json"), filepath.Join(xdgDir, "config.json")},
		filepath.Join(root, "profiles.json"), "")
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, home, "linux", nil, prot)
	if err != nil {
		t.Fatal(err)
	}
	for ws, want := range map[string]string{
		dots:     "it is agent-box's config directory",
		xdgDir:   "it is agent-box's config directory",
		profDots: "it is agent-box's profiles directory",
	} {
		if reason, bad := l.boxes.Unboxable(ws); !bad || !strings.Contains(reason, want) {
			t.Errorf("%s: want %q, got %q %v", ws, want, reason, bad)
		}
	}
	proj := filepath.Join(dots, "proj")
	if err := os.Mkdir(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if reason, bad := l.boxes.Unboxable(proj); bad {
		t.Fatalf("a subdir of a config dir must stay boxable: %q", reason)
	}
}

// profiles_path directly in $HOME protects $HOME itself (already refused) but
// must not make every project under $HOME unboxable.
func TestProfilesInHomeLeavesProjectsBoxable(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "data") // outside home, so home's refusal is the profiles dir's
	proj := filepath.Join(home, "proj")
	for _, d := range []string{root, proj} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json")}, filepath.Join(home, "profiles.json"), "")
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, home, "linux", nil, prot)
	if err != nil {
		t.Fatal(err)
	}
	if reason, bad := l.boxes.Unboxable(proj); bad {
		t.Fatalf("~/proj must stay boxable with profiles.json in $HOME: %q", reason)
	}
	if reason, bad := l.boxes.Unboxable(home); !bad || !strings.Contains(reason, "profiles directory") {
		t.Fatalf("$HOME itself must be refused as the profiles dir: %q %v", reason, bad)
	}
	if reason, bad := l.boxes.Unboxable(filepath.Join(root, "vm")); !bad || !strings.Contains(reason, "inside agent-box's data directory") {
		t.Fatalf("inside the data root must stay refused: %q %v", reason, bad)
	}
}

// The default account's config dir holds hooks and scripts that host sessions
// execute, and settings.json that inheriting accounts copy from. A box mounted
// over it, or anywhere inside it, could plant code that later runs on the host.
func TestProtectedDirsIncludeThePersonalConfigDir(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "data")
	personal := filepath.Join(home, ".claude")
	proj := filepath.Join(home, "proj")
	for _, d := range []string{root, filepath.Join(personal, "hooks"), proj} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json")}, filepath.Join(root, "profiles.json"), personal)
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, home, "linux", nil, prot)
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range []string{personal, filepath.Join(personal, "hooks")} {
		if reason, bad := l.boxes.Unboxable(ws); !bad || !strings.Contains(reason, "personal config directory") {
			t.Errorf("%s must be refused: %q %v", ws, reason, bad)
		}
	}
	if reason, bad := l.boxes.Unboxable(proj); bad {
		t.Fatalf("a sibling project of ~/.claude must stay boxable: %q", reason)
	}
}
