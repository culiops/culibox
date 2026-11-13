package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/vmshares"
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
	rules   string // what `ssh … iptables -S` prints
	mounts  string // what `ssh … cat /proc/self/mounts` prints
	// hangSSH makes the named ssh command ("iptables" or "mounts") block until its context ends.
	hangSSH  string
	onSSH    func() // called on each ssh (lets a test order it against docker calls)
	calls    []string
	failStop bool // stop fails and the VM keeps running
	// fixedMounts is what the guest lists after a folder-mode remount
	// (`ssh … sh -c …`); remounts counts those calls.
	fixedMounts string
	remounts    int
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
	case "ssh":
		if strings.Contains(strings.Join(args, " "), "mount -o remount,") {
			f.remounts++
			if f.fixedMounts != "" {
				f.mounts = f.fixedMounts
			}
			return "", nil
		}
		kind := "iptables"
		if strings.Contains(strings.Join(args, " "), "/proc/self/mounts") {
			kind = "mounts"
		}
		if f.hangSSH == kind {
			f.mu.Unlock()
			<-ctx.Done()
			f.mu.Lock()
			return "", ctx.Err()
		}
		if kind == "mounts" {
			return f.mounts, nil
		}
		if f.onSSH != nil {
			f.onSSH()
		}
		return f.rules, nil
	case "start":
		f.running = true
	case "stop":
		if f.failStop {
			return "", errors.New("colima stop: hostagent did not exit")
		}
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
ps) case "$*" in *ca-scratch*) ;; *) cat "$d/ps" 2>/dev/null ;; esac ;;
context) [ -f "$d/context" ] || { echo "cannot connect" >&2; exit 1; }; cat "$d/context" ;;
run|exec) [ -f "$d/sleep" ] && exec sleep 5; cat "$d/probe" 2>/dev/null ;;
info) echo '{"runc":{}}' ;;
rm) ;;
inspect) cat "$d/inspect" 2>/dev/null ;;
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

// freshDarwinLayer builds a darwin layer over dir and does NOT load the relay
// key, so a test can arrange the data directory before the first load (which
// darwinLayer has already done).
func freshDarwinLayer(t *testing.T, dir string) *boxLayer {
	t.Helper()
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}, dir, dir, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l
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
	// Every darwin test layer carries the relay key the daemon would have
	// loaded at start (#114).
	if err := l.loadRelayKey(); err != nil {
		t.Fatal(err)
	}
	if !notSetUp {
		vc := l.vm.Config()
		if err := os.MkdirAll(vc.ColimaHome(), 0o700); err != nil {
			t.Fatal(err)
		}
		// The VM as `box setup` leaves it: its colima.yaml, the stamp that is that
		// yaml's hash, and a running VM whose mounts are exactly the yaml's.
		y, err := vc.YAML()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(vc.YAMLPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vc.YAMLPath(), y, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(y)
		if err := os.WriteFile(vc.StampPath(), []byte(hex.EncodeToString(sum[:])+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var mb strings.Builder
		for _, m := range vc.Mounts() {
			mode := "ro"
			if m.Writable {
				mode = "rw"
			}
			fmt.Fprintf(&mb, "mount%d %s virtiofs %s,relatime 0 0\n", len(mb.String()), m.Location, mode)
		}
		fc.mounts = mb.String()
	}
	return l, fc, d
}

// #114: the macOS daemon keeps one relay key per data directory, in
// relay2.secret, so the relay and the listeners agree on it across restarts.
func TestDarwinLayerLoadsOneRelayKeyPerDataDir(t *testing.T) {
	// The docs name this file (#118: v1's relay.secret is retired).
	if relaySecretFile != "relay2.secret" {
		t.Fatalf("relaySecretFile = %q, want relay2.secret", relaySecretFile)
	}
	l, _, _ := darwinLayer(t, false)
	if l.relayKey.IsZero() {
		t.Fatal("relay key is zero: loadRelayKey did not set it")
	}
	path := filepath.Join(l.dataDir, relaySecretFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("relay2.secret mode = %v, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 32 || !bytes.Equal(b, l.relayKey[:]) {
		t.Fatalf("relay2.secret is %d bytes and does not equal the key", len(b))
	}

	// A second layer on the same root gets the same key.
	again, err := newBoxLayer(config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}, l.dataDir, l.dataDir, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.loadRelayKey(); err != nil {
		t.Fatal(err)
	}
	if again.relayKey != l.relayKey {
		t.Error("a second layer on the same data dir got another key")
	}

	// Another data dir gets its own.
	other, _, _ := darwinLayer(t, false)
	if other.relayKey == l.relayKey {
		t.Error("two data dirs share a relay key")
	}
}

// D11: Linux has no relay and no relay key, so it gets no relay2.secret, and the
// retirement of v1's file is darwin only: a relay.secret that sits in a Linux
// data dir is not CuliBox's to delete.
func TestLinuxLayerHasNoRelayKey(t *testing.T) {
	root := t.TempDir()
	planted := bytes.Repeat([]byte{7}, 32)
	old := filepath.Join(root, "relay.secret")
	if err := os.WriteFile(old, planted, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.loadRelayKey(); err != nil {
		t.Fatalf("loadRelayKey on linux: %v", err)
	}
	if !l.relayKey.IsZero() {
		t.Error("linux layer carries a relay key")
	}
	if _, err := os.Stat(filepath.Join(root, "relay2.secret")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("linux wrote relay2.secret (stat err %v)", err)
	}
	got, err := os.ReadFile(old)
	if err != nil || !bytes.Equal(got, planted) {
		t.Errorf("linux changed relay.secret (read err %v, %d bytes)", err, len(got))
	}
}

// #118 (D6): v1 sent the relay key in clear on every connection, so it may have
// leaked. It is never loaded again: the first load deletes the old file and the
// key lives in a new one, with a new random value.
func TestDarwinLayerRetiresTheV1RelaySecret(t *testing.T) {
	dir := shortRoot(t)
	planted := bytes.Repeat([]byte{7}, 32)
	old := filepath.Join(dir, "relay.secret") // the name already installed Macs have
	if err := os.WriteFile(old, planted, 0o600); err != nil {
		t.Fatal(err)
	}
	l := freshDarwinLayer(t, dir)
	if err := l.loadRelayKey(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("relay.secret still exists (stat err %v)", err)
	}
	path := filepath.Join(dir, "relay2.secret")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("relay2.secret mode = %v, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 32 || !bytes.Equal(b, l.relayKey[:]) {
		t.Fatalf("relay2.secret is %d bytes and does not equal the key", len(b))
	}
	if bytes.Equal(l.relayKey[:], planted) {
		t.Error("the key is the retired v1 key")
	}

	// A restart finds nothing to retire and keeps the key.
	again := freshDarwinLayer(t, dir)
	if err := again.loadRelayKey(); err != nil {
		t.Fatal(err)
	}
	if again.relayKey != l.relayKey {
		t.Error("the second load changed the key")
	}
}

// No relay.secret (a new install, or the second start) is the normal case: the
// retirement says nothing.
func TestLoadRelayKeyIgnoresAMissingRetiredFile(t *testing.T) {
	l := freshDarwinLayer(t, shortRoot(t))
	var err error
	logged := captureLog(t, func() { err = l.loadRelayKey() })
	if err != nil {
		t.Fatal(err)
	}
	if l.relayKey.IsZero() {
		t.Fatal("the key is zero")
	}
	if logged != "" {
		t.Errorf("loadRelayKey logged on a clean first start: %q", logged)
	}
}

// Deleting v1's file is best effort: a failure is logged (without any key) and
// the load of the new key carries on.
func TestLoadRelayKeyLogsAFailedRetirement(t *testing.T) {
	dir := shortRoot(t)
	old := filepath.Join(dir, "relay.secret")
	// A non-empty directory cannot be removed by os.Remove, as root too.
	if err := os.MkdirAll(filepath.Join(old, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	l := freshDarwinLayer(t, dir)
	var err error
	out := captureLog(t, func() { err = l.loadRelayKey() })
	if err != nil {
		t.Fatalf("a failed retirement must not fail the load: %v", err)
	}
	if l.relayKey.IsZero() {
		t.Fatal("the key is zero after a failed retirement")
	}
	for _, want := range []string{"relay: could not remove the retired", old, "it is no longer used"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, hex.EncodeToString(l.relayKey[:])) {
		t.Errorf("the log holds the key: %q", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "relay2.secret"))
	if err != nil || !bytes.Equal(b, l.relayKey[:]) {
		t.Errorf("relay2.secret was not created with the key (read err %v)", err)
	}
}

func TestLoadRelayKeyRefusesAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 file")
	}
	l, _, _ := darwinLayer(t, false)
	path := filepath.Join(l.dataDir, relaySecretFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	fresh, err := newBoxLayer(config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}, l.dataDir, l.dataDir, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.loadRelayKey(); err == nil {
		t.Fatal("an unreadable relay.secret must be an error")
	}
	if !fresh.relayKey.IsZero() {
		t.Error("the key must stay zero after a failed load")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("an unreadable relay.secret was overwritten")
	}
}

// The shared helper words its read error for the cookie secret and the layer's
// log line must not repeat that: the error has to name relay2.secret and what
// fixes it, for an unreadable file and for an all-zero one (where the fix is to
// delete the file, not to make it readable).
func TestLoadRelayKeyErrorsNameTheRelaySecretAndItsFix(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode 000 file")
		}
		dir := shortRoot(t)
		path := filepath.Join(dir, relaySecretFile)
		if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o600) })
		err := freshDarwinLayer(t, dir).loadRelayKey()
		if err == nil {
			t.Fatal("an unreadable relay.secret must be an error")
		}
		for _, want := range []string{"relay secret", path, "permission denied", "delete it (CuliBox creates a new one and replaces the relay once); fixing its permissions keeps the old key"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error lacks %q: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "cookie") {
			t.Errorf("error is worded for the cookie secret: %v", err)
		}
	})
	t.Run("all zero", func(t *testing.T) {
		dir := shortRoot(t)
		path := filepath.Join(dir, relaySecretFile)
		if err := os.WriteFile(path, make([]byte, 32), 0o600); err != nil {
			t.Fatal(err)
		}
		l := freshDarwinLayer(t, dir)
		err := l.loadRelayKey()
		if err == nil {
			t.Fatal("an all-zero relay.secret must be an error")
		}
		for _, want := range []string{"relay secret", path, "all zero", "delete it"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error lacks %q: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "cookie") || strings.Contains(err.Error(), "readable") {
			t.Errorf("error sends the user to the wrong fix: %v", err)
		}
		if !l.relayKey.IsZero() {
			t.Error("the key must stay zero after a failed load")
		}
		if b, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(b, make([]byte, 32)) {
			t.Errorf("the all-zero file was replaced (read err %v)", rerr)
		}
	})
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
	if o.Prepare == nil {
		t.Fatal("an unset box_image needs the adoption hook on linux too")
	}
	pinned, err := newBoxLayer(config.Config{DockerBin: "/bin/sh", BoxImage: "me/img:1"}, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := pinned.options(config.Config{DockerBin: "/bin/sh", BoxImage: "me/img:1"}).Prepare; p != nil {
		t.Fatal("a pinned box_image needs no hook on linux")
	}
	o.Prepare = nil
	if o.Verify != nil || o.Reachable != nil || o.Shares != nil || o.VMIDs != nil || len(o.DockerArgs) != 0 || o.LockGuards {
		t.Fatalf("linux options must carry no VM hooks: %+v", o)
	}
	if o.StateRoot != root || o.Image != config.DefaultBoxImage || o.Instance != "default" {
		t.Fatalf("linux options = %+v", o)
	}
	if l.vmStopped(context.Background()) {
		t.Fatal("linux has no VM to be stopped")
	}
}

func TestBoxLayerOptionsFenceEveryBox(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh"}
	if o := (&boxLayer{goos: "linux"}).options(cfg); !o.EgressAll || !o.GuardInPlace {
		t.Errorf("linux must fence and guard every box: EgressAll=%v GuardInPlace=%v", o.EgressAll, o.GuardInPlace)
	}
	// #69: darwin fences every box too (option (c) relay). #68: and guards it in
	// place, as the Mac spike showed the read-only self-binds hold through virtiofs.
	if o := (&boxLayer{goos: "darwin"}).options(cfg); !o.EgressAll || !o.GuardInPlace {
		t.Errorf("darwin must fence and guard every box: EgressAll=%v GuardInPlace=%v", o.EgressAll, o.GuardInPlace)
	}
}

func TestBoxLayerOptionsCarryLimits(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh", BoxPidsLimit: 2048, BoxTmpSize: "2g", BoxMemory: "3g", BoxCPUs: 1.5}
	for _, goos := range []string{"linux", "darwin"} {
		o := (&boxLayer{goos: goos}).options(cfg)
		if o.PidsLimit != 2048 || o.TmpSize != "2g" || o.Memory != "3g" || o.CPUs != 1.5 {
			t.Errorf("%s: limits not carried: pids=%d tmp=%q mem=%q", goos, o.PidsLimit, o.TmpSize, o.Memory)
		}
	}
}

// #72: on macOS the context-path identity is read inside the VM (a Mac inode is
// not the one virtiofs shows the box), through one `colima ssh` that never
// starts the VM. Linux records it on the host and carries no hook (see
// TestLinuxLayerHasNoVMAndNoDockerArgs).
func TestDarwinLayerReadsContextIdentitiesInTheVM(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	o := l.options(config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"})
	if o.VMIDs == nil {
		t.Fatal("darwin options must carry the VM identity hook")
	}
	// #96: the guard locks wherever the OS has the file flags (a real Mac).
	if o.LockGuards != box.GuardLocksSupported() {
		t.Fatalf("LockGuards = %v, want %v on this OS", o.LockGuards, box.GuardLocksSupported())
	}
	fc.mu.Lock()
	fc.rules = "ctxid 44:393\nctxid 16777232:7\n"
	fc.calls = nil
	fc.mu.Unlock()
	ids, err := o.VMIDs(context.Background(), []string{"/Users/u/docs", "/Users/u/Tài liệu $x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []box.FileID{{Dev: 44, Ino: 393}, {Dev: 16777232, Ino: 7}}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %+v, want %+v", ids, want)
	}
	fc.mu.Lock()
	calls := append([]string(nil), fc.calls...)
	fc.mu.Unlock()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "ssh --profile agent-box -- sh -c ") {
		t.Fatalf("colima calls = %q, want exactly one ssh and no start", calls)
	}
	if strings.Contains(calls[0], "Tài") || strings.Contains(calls[0], "$x") {
		t.Fatalf("a path reached the VM shell in the clear: %q", calls[0])
	}
	// A VM answer that is not one identity per path is an error, never a guess.
	fc.mu.Lock()
	fc.rules = "ctxid 44:393\n"
	fc.mu.Unlock()
	if ids, err := o.VMIDs(context.Background(), []string{"/a", "/b"}); err == nil || ids != nil {
		t.Fatalf("a short VM answer must fail closed: ids=%v err=%v", ids, err)
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

// An existing VM keeps its size, so an upgrade never reads as drift; `box setup
// -recreate` is when the host-derived size applies.
func TestDarwinLayerVMSizeKeepsExistingUntilRecreate(t *testing.T) {
	old := hostSize
	hostSize = func() (int, uint64) { return 10, 32 << 30 }
	t.Cleanup(func() { hostSize = old })
	cfg := config.Config{DockerBin: "/bin/sh", ColimaBin: "/bin/sh"}
	root := t.TempDir()

	l, err := newBoxLayer(cfg, root, root, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := l.vm.Config(); c.CPUs != 10 || c.MemoryGiB != 16 {
		t.Fatalf("first setup sizes from the host: %d/%d", c.CPUs, c.MemoryGiB)
	}

	// as an older agent-box left it: 4/4, stamped
	vmDir := filepath.Join(root, "vm")
	if err := os.MkdirAll(filepath.Join(vmDir, boxvm.Profile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, boxvm.Profile, "colima.yaml"), []byte("cpu: 4\nmemory: 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, boxvm.Profile+".stamp"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err = newBoxLayer(cfg, root, root, "darwin", &fakeColima{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := l.vm.Config(); c.CPUs != 4 || c.MemoryGiB != 4 {
		t.Fatalf("an existing VM keeps its size: %d/%d", c.CPUs, c.MemoryGiB)
	}
	l, err = newBoxLayerFor(cfg, root, root, "darwin", &fakeColima{}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if c := l.vm.Config(); c.CPUs != 10 || c.MemoryGiB != 16 {
		t.Fatalf("recreate sizes from the host again: %d/%d", c.CPUs, c.MemoryGiB)
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
	// No colima.yaml yet: nothing applied, and no error (Prepare meets
	// ErrNotSetUp first).
	v, verr := o.Shares()
	if verr != nil || len(v.Applied) != 0 {
		t.Errorf("Shares = %+v, %v; want nothing applied and no error", v, verr)
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
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "culibox box setup") {
		t.Fatalf("a missing remote image must fail the spawn and name box setup: %v", err)
	}
	for _, line := range strings.Split(d.log(), "\n") {
		if strings.Contains(line, "pull") {
			t.Fatalf("spawn pulled an image: %s", d.log())
		}
	}
}

// Review focus 3: with the VM stopped, the boot pass and the periodic reclaim
// neither start the VM nor touch docker, and the sweep skips.
func TestStoppedVMIsLeftAloneByBackgroundPasses(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	ctx := context.Background()
	if !l.vmStopped(ctx) {
		t.Fatal("a VM that is not running is stopped")
	}
	l.bootReconcile(ctx)
	l.periodicReclaim(ctx)
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

// Review #96 (2): the boot pass lifts the guard locks only when colima says
// the VM is stopped. A status that cannot be read is not "stopped": a box may
// run, so its locks stay for the next Reconcile or Sweep.
func TestBootSweepsGuardLocksOnlyOnAConfirmedStop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		garbled   bool
		sockAlive bool
		wantSweep bool
	}{
		{"stopped", false, false, true},
		{"status unreadable", true, false, false},
		// #247 (1): colima also says "is not running" when it cannot read the
		// lima instance; while the VM's docker socket still answers, that is
		// not a confirmed stop.
		{"docker socket answers", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, fc, _ := darwinLayer(t, false)
			fc.garbled = tc.garbled
			if tc.sockAlive {
				sock := l.vm.Config().DockerSocket()
				if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
					t.Fatal(err)
				}
				ln, err := net.Listen("unix", sock)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { ln.Close() })
			}
			swept := 0
			l.sweepLocksNoBoxes = func() { swept++ }
			if l.bootReconcile(context.Background()) {
				t.Fatal("a VM that is not known to run is not reconciled")
			}
			if got := swept == 1; got != tc.wantSweep {
				t.Fatalf("swept %d times, want sweep=%v", swept, tc.wantSweep)
			}
		})
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
	for _, want := range []string{"box VM: running", "virtiofs", "WARN image", "older than this CuliBox", "ab-box-1"} {
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
		filepath.Join(root, "profiles.json"), "", "")
	if len(prot) != 2 || prot[0].What != "CuliBox's data directory" || prot[1].What != "CuliBox's config directory" {
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
		filepath.Join(root, "profiles.json"), "", "")
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, home, "linux", nil, prot)
	if err != nil {
		t.Fatal(err)
	}
	for ws, want := range map[string]string{
		dots:     "it is CuliBox's config directory",
		xdgDir:   "it is CuliBox's config directory",
		profDots: "it is CuliBox's profiles directory",
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
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json")}, filepath.Join(home, "profiles.json"), "", "")
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
	if reason, bad := l.boxes.Unboxable(filepath.Join(root, "vm")); !bad || !strings.Contains(reason, "inside CuliBox's data directory") {
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
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json")}, filepath.Join(root, "profiles.json"), personal, "")
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

// A custom bundle_source is the bundle's Source: its files' own @-imports are
// followed, so no box may write it. It gets the same protection as ~/.claude,
// and the same directory under two spellings is listed once.
func TestProtectedDirsIncludeACustomBundleSource(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "data")
	personal := filepath.Join(home, ".claude")
	custom := filepath.Join(home, "cfg", "claude")
	proj := filepath.Join(home, "proj")
	for _, d := range []string{root, personal, custom, proj} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prot := protectedDirs(root, []string{filepath.Join(root, "config.json")}, filepath.Join(root, "profiles.json"), personal, custom)
	l, err := newBoxLayer(config.Config{DockerBin: "/bin/sh"}, root, home, "linux", nil, prot)
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range []string{custom, filepath.Join(home, "cfg"), filepath.Join(custom, "context")} {
		if reason, bad := l.boxes.Unboxable(ws); !bad || !strings.Contains(reason, "personal config directory") {
			t.Errorf("%s must be refused: %q %v", ws, reason, bad)
		}
	}
	if reason, bad := l.boxes.Unboxable(proj); bad {
		t.Fatalf("a sibling project must stay boxable: %q", reason)
	}

	// The default source spelled through a link is the same directory: one row.
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(personal, alias); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range protectedDirs(root, nil, filepath.Join(root, "profiles.json"), personal, alias) {
		if d.What == "the personal config directory" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("personal config rows = %d, want 1", n)
	}
	// "" (bundle off) adds nothing.
	if got, want := len(protectedDirs(root, nil, filepath.Join(root, "profiles.json"), personal, "")),
		len(protectedDirs(root, nil, filepath.Join(root, "profiles.json"), personal, personal)); got != want {
		t.Fatalf("off vs same source: %d rows vs %d", got, want)
	}
}

func TestBundleSourceOf(t *testing.T) {
	if got := bundleSourceOf(config.Config{BundleSource: "off"}); got != "" {
		t.Errorf("off = %q", got)
	}
	if got := bundleSourceOf(config.Config{}); got != bundle.DefaultSource() {
		t.Errorf("default = %q, want %q", got, bundle.DefaultSource())
	}
	if got := bundleSourceOf(config.Config{BundleSource: "/x/cfg"}); got != "/x/cfg" {
		t.Errorf("custom = %q", got)
	}
}

// Applied is the written colima.yaml minus the data-root mounts; Listed is the
// user's list, applied or not.
func TestDarwinLayerShareViewFromYAML(t *testing.T) {
	l, _, _ := darwinLayer(t, false)
	vc := l.vm.Config()
	h := resolved(t, t.TempDir())
	a, b := filepath.Join(h, "a"), filepath.Join(h, "b")
	writeColimaYAML(t, l, append(boxvm.DataMounts(vc.Root), boxvm.Mount{Location: a, Writable: true}))
	l.vm.SetShares([]vmshares.Share{{Path: a, Mode: vmshares.RW}, {Path: b, Mode: vmshares.RO}})
	v, err := l.shareView()
	if err != nil {
		t.Fatal(err)
	}
	if want := []box.VMShare{{Path: a, Writable: true}}; !reflect.DeepEqual(v.Applied, want) {
		t.Errorf("Applied = %+v, want %+v", v.Applied, want)
	}
	if want := []box.VMShare{{Path: a, Writable: true}, {Path: b}}; !reflect.DeepEqual(v.Listed, want) {
		t.Errorf("Listed = %+v, want %+v", v.Listed, want)
	}
	if !v.Fold || v.Home != vc.Home {
		t.Errorf("Fold = %v, Home = %q; want fold and %q", v.Fold, v.Home, vc.Home)
	}
	if err := v.Check(filepath.Join(b, "x"), false); err == nil {
		t.Error("a listed-only folder must not pass the check")
	}
}

func TestDarwinLayerShareViewCorruptYAMLFailsClosed(t *testing.T) {
	l, _, _ := darwinLayer(t, false)
	vc := l.vm.Config()
	if err := os.MkdirAll(vc.ProfileDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vc.YAMLPath(), []byte("mounts:\n  - hand edited\nprovision:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.shareView(); err == nil {
		t.Fatal("a mounts block that cannot be read must fail closed")
	}
}

func writeColimaYAML(t *testing.T, l *boxLayer, ms []boxvm.Mount) {
	t.Helper()
	vc := l.vm.Config()
	if err := os.MkdirAll(vc.ProfileDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("cpu: 2\nmounts:\n")
	for _, m := range ms {
		fmt.Fprintf(&sb, "  - location: %q\n    writable: %v\n", m.Location, m.Writable)
	}
	sb.WriteString("provision:\n")
	if err := os.WriteFile(vc.YAMLPath(), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A bad share list at boot gives an empty list, a good one is normalised.
func TestBootSharesNormalises(t *testing.T) {
	l, _, _ := darwinLayer(t, false)
	if got := bootShares(l.dataDir, l.vm.Config(), nil); len(got) != 0 {
		t.Fatalf("no file: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(l.dataDir, vmshares.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := bootShares(l.dataDir, l.vm.Config(), nil); len(got) != 0 {
		t.Fatalf("corrupt file: %+v", got)
	}
}

// M4: `box` needs the config files only for the macOS VM's protected list, so
// failing to name them is fatal only on darwin.
func TestBoxConfigFilesFatalOnlyOnDarwin(t *testing.T) {
	boom := errors.New("boom")
	if files, err := boxConfigFiles("linux", nil, boom); err != nil || files != nil {
		t.Fatalf("linux: %v %v", files, err)
	}
	if _, err := boxConfigFiles("darwin", nil, boom); !errors.Is(err, boom) {
		t.Fatalf("darwin: %v", err)
	}
	if files, err := boxConfigFiles("darwin", []string{"/c"}, nil); err != nil || len(files) != 1 {
		t.Fatalf("darwin ok: %v %v", files, err)
	}
}

// adoptDocker is a docker whose daemon holds the images named in files
// img-<ref with / and : as _>, and fails every inspect while the file "down"
// exists. It logs argv.
func adoptDocker(t *testing.T, present ...string) *fakeDocker {
	t.Helper()
	d := newFakeDocker(t)
	script := `#!/bin/sh
d=` + d.dir + `
echo "$*" >> "$d/log"
case "$1" in
image)
  [ -f "$d/down" ] && { echo "Cannot connect to the Docker daemon" >&2; exit 1; }
  for a; do ref=$a; done
  f="$d/img-$(echo "$ref" | tr '/:' '__')"
  [ -f "$f" ] && cat "$f" || { echo "Error: No such image: $ref" >&2; exit 1; } ;;
tag) touch "$d/tagged" ;;
esac
exit 0
`
	if err := os.WriteFile(d.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, ref := range present {
		d.set(t, "img-"+strings.NewReplacer("/", "_", ":", "_").Replace(ref), "arm64|"+boximage.Hash()+"\n")
	}
	return d
}

func TestAdoptImageOnlyWhenUnset(t *testing.T) {
	d := adoptDocker(t, config.LegacyDefaultBoxImage)
	l, err := newBoxLayer(config.Config{DockerBin: d.bin, BoxImage: "me/img:1"}, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.adoptImage(context.Background())
	if err := l.boxes.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := d.log(); strings.Contains(got, "image") || strings.Contains(got, "tag") {
		t.Fatalf("a pinned box_image must never inspect or tag: %s", got)
	}
	// Unset: the same layer shape adopts, through the manager's Prepare hook.
	d = adoptDocker(t, config.LegacyDefaultBoxImage)
	l, err = newBoxLayer(config.Config{DockerBin: d.bin}, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.boxes.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := "tag " + config.LegacyDefaultBoxImage + " " + config.DefaultBoxImage; !strings.Contains(d.log(), want) {
		t.Fatalf("Prepare did not adopt (%q):\n%s", want, d.log())
	}
	for _, line := range strings.Split(d.log(), "\n") {
		if strings.Contains(line, "rmi") || strings.Contains(line, "image rm") {
			t.Fatalf("the old tag must be kept: %s", d.log())
		}
	}
}

// A daemon that is not ready is no answer: the next call tries again, and once
// it has an answer the layer stops asking.
func TestAdoptImageRetriesAfterError(t *testing.T) {
	d := adoptDocker(t, config.LegacyDefaultBoxImage)
	d.set(t, "down", "")
	l, err := newBoxLayer(config.Config{DockerBin: d.bin}, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.adoptImage(context.Background())
	if _, err := os.Stat(filepath.Join(d.dir, "tagged")); err == nil {
		t.Fatal("tagged while the daemon was down")
	}
	if err := os.Remove(filepath.Join(d.dir, "down")); err != nil {
		t.Fatal(err)
	}
	l.adoptImage(context.Background())
	if _, err := os.Stat(filepath.Join(d.dir, "tagged")); err != nil {
		t.Fatalf("the second call must adopt: %v\n%s", err, d.log())
	}
	before := d.log()
	l.adoptImage(context.Background())
	if d.log() != before {
		t.Fatalf("an answered layer must not ask again:\n%s", d.log())
	}
}

// Darwin keeps its own Prepare (which adopts before it inspects), and it is the
// only one.
func TestPrepareAdoptsTheLegacyDefaultBeforeInspect(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	fc.running = true
	d := adoptDocker(t, config.LegacyDefaultBoxImage)
	l.img = boximage.Docker{Bin: d.bin}
	if err := l.prepare(context.Background()); err == nil {
		t.Log("the fake's tag does not create the image, so the spawn is still refused")
	}
	if want := "tag " + config.LegacyDefaultBoxImage + " " + config.DefaultBoxImage; !strings.Contains(d.log(), want) {
		t.Fatalf("prepare did not adopt:\n%s", d.log())
	}
	// Compare whole lines: "tag " as a bare substring could match inside
	// another argument.
	tagAt, inspectAt := -1, -1
	for i, line := range strings.Split(d.log(), "\n") {
		if tagAt < 0 && strings.HasPrefix(line, "tag ") {
			tagAt = i
		}
		if strings.HasPrefix(line, "image inspect") {
			inspectAt = i
		}
	}
	if tagAt < 0 || tagAt > inspectAt {
		t.Fatalf("the tag must come before the final inspect:\n%s", d.log())
	}
}
