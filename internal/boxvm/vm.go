package boxvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/vmshares"
)

// Runner runs colima. A fake drives every unit test: no Mac, no VM.
type Runner interface {
	Run(ctx context.Context, env []string, bin string, args ...string) (string, error)
}

// ExecRunner runs the real binary.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, env []string, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), args[0], err, firstLine(string(out)))
	}
	return string(out), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Status is what `colima status --json` reports for the profile.
type Status struct {
	Running bool
	// Stopped: colima itself answered that the VM is not running. A status
	// that failed any other way reads as neither: a box may still run.
	Stopped                               bool
	Driver, MountType, Arch, DockerSocket string
}

// ErrNotSetUp means `agent-box box setup` has not provisioned the VM yet.
var ErrNotSetUp = errors.New("the box VM is not set up; run `culibox box setup`")

// VM is agent-box's colima profile. sem (a one-slot lock a waiter can abandon
// when its context ends) serialises start/stop, so concurrent boxed spawns
// trigger one start, not five.
type VM struct {
	cfg          Config
	r            Runner
	sem          chan struct{}
	startTimeout time.Duration

	// needsReconcile is set when a start is attempted and cleared when some
	// caller is handed started=true. A start that errored but then came up, or
	// whose caller went away, still gets exactly one reconcile.
	flagMu         sync.Mutex
	needsReconcile bool
	mountsCache    *mountsCache
}

// NewVM resolves the config's Root and Home through symlinks, so the mount
// list, Shared answers and the paths the VM sees all use one spelling of the
// directories: a data root reached through a symlinked home would otherwise get
// a second virtiofs mount. Config() returns the resolved form; callers (box
// homes, the docker mounts) must build their paths from it.
func NewVM(cfg Config, r Runner) *VM {
	cfg.Root = vmshares.Resolve(cfg.Root)
	cfg.Home = vmshares.Resolve(cfg.Home)
	if len(cfg.Shares) > 0 {
		rs := make([]vmshares.Share, len(cfg.Shares))
		for i, sh := range cfg.Shares {
			rs[i] = vmshares.Share{Path: vmshares.Resolve(sh.Path), Mode: sh.Mode}
		}
		cfg.Shares = rs
	}
	return &VM{cfg: cfg, r: r, sem: make(chan struct{}, 1), startTimeout: 120 * time.Second}
}

func (v *VM) Config() Config {
	v.flagMu.Lock()
	defer v.flagMu.Unlock()
	return v.cfg
}

// lock takes the start/stop lock, or gives up when ctx ends first.
func (v *VM) lock(ctx context.Context) error {
	select {
	case v.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (v *VM) unlock() { <-v.sem }

func (v *VM) markReconcile() {
	v.flagMu.Lock()
	v.needsReconcile = true
	v.flagMu.Unlock()
}

// claimReconcile hands started=true to exactly one caller per attempted start.
func (v *VM) claimReconcile() bool {
	v.flagMu.Lock()
	defer v.flagMu.Unlock()
	c := v.needsReconcile
	v.needsReconcile = false
	return c
}

// preflight refuses a config that cannot be carried safely, before any colima
// invocation: a root docker or YAML would misparse, or a socket path past the
// platform limit.
func (v *VM) preflight() error {
	if err := v.Config().Validate(); err != nil {
		return err
	}
	return v.Config().CheckSocketPaths()
}

func (v *VM) env() []string { return v.Config().Env(os.Environ()) }

// SetUp reports whether box setup has provisioned this VM.
func (v *VM) SetUp() bool {
	_, err := os.Stat(v.Config().StampPath())
	return err == nil
}

func yamlHash(y []byte) string {
	sum := sha256.Sum256(y)
	return hex.EncodeToString(sum[:])
}

// Drifted reports whether the config agent-box would write now differs from
// the one the VM was provisioned with (cpus, memory, mounts, extra_ca_certs, provision script).
func (v *VM) Drifted() bool {
	y, err := v.Config().YAML()
	if err != nil {
		return true
	}
	b, err := os.ReadFile(v.Config().StampPath())
	return err != nil || strings.TrimSpace(string(b)) != yamlHash(y)
}

// provisionHash is the hash of the base provision script (the egress and broker
// INPUT rules), without the user's extra CA block.
func (v *VM) provisionHash() string {
	return yamlHash([]byte(provisionScript(v.Config().BrokerPort, v.Config().EgressPort)))
}

// ProvisionDrifted reports whether the VM was provisioned with a different
// provision script than agent-box would write now. A VM set up before the
// script was stamped has no stamp and reads as drifted: that is the VM that
// predates egress control for all boxes. A changed port or a newer script (#113)
// drifts it too. Fails closed on an unreadable stamp.
func (v *VM) ProvisionDrifted() bool {
	b, err := os.ReadFile(v.Config().ProvisionStampPath())
	return err != nil || strings.TrimSpace(string(b)) != v.provisionHash()
}

// Status asks colima. A non-zero exit means "not running" (colima reports a
// stopped or never-created profile that way); a cancelled context is an error.
// Stopped needs two independent signals: colima's "is not running" answer,
// which colima also gives when it cannot read the lima instance (so alone it
// may hide a running VM), and nothing listening on the VM's docker socket
// (socketDead). Any other failure leaves it false: boxes may still run.
func (v *VM) Status(ctx context.Context) (Status, error) {
	if err := v.preflight(); err != nil {
		return Status{}, err
	}
	out, err := v.r.Run(ctx, v.env(), v.Config().ColimaBin, "status", "--profile", Profile, "--json")
	if ctx.Err() != nil {
		return Status{}, ctx.Err()
	}
	if err != nil {
		return Status{Stopped: strings.Contains(out, "is not running") && socketDead(v.Config().DockerSocket())}, nil
	}
	var s struct {
		Driver       string `json:"driver"`
		Arch         string `json:"arch"`
		MountType    string `json:"mount_type"`
		DockerSocket string `json:"docker_socket"`
	}
	if err := json.Unmarshal([]byte(lastLine(out)), &s); err != nil {
		return Status{}, fmt.Errorf("colima status: unreadable output: %w", err)
	}
	return Status{Running: true, Driver: s.Driver, Arch: s.Arch, MountType: s.MountType,
		DockerSocket: strings.TrimPrefix(s.DockerSocket, "unix://")}, nil
}

// socketDead reports whether nothing serves the unix socket p: it does not
// exist, or a connect is refused (a file left behind). lima's host agent
// serves the VM's docker socket and, with vmType vz, runs the VM itself, so a
// dead socket means no box is reachable or running. Any other answer (it
// connects, a permission error, a timeout) is not dead.
func socketDead(p string) bool {
	c, err := net.DialTimeout("unix", p, 2*time.Second)
	if err == nil {
		c.Close()
		return false
	}
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// FilterRules returns the VM's live filter table (`iptables -S`, every chain:
// the relay checks INPUT and DOCKER-USER). It runs over `colima ssh`, which
// fails on a stopped VM and never starts one: the relay may only follow rules
// that are loaded now, not rules a script wrote on some earlier boot.
func (v *VM) FilterRules(ctx context.Context) (string, error) {
	if err := v.preflight(); err != nil {
		return "", err
	}
	out, err := v.r.Run(ctx, v.env(), v.Config().ColimaBin, "ssh", "--profile", Profile, "--", "sudo", "iptables", "-w", "-S")
	if err != nil {
		return "", fmt.Errorf("reading the box VM's firewall rules: %w", err)
	}
	return out, nil
}

// Running is Status without the error, for the sweep's reachability check.
func (v *VM) Running(ctx context.Context) bool {
	st, err := v.Status(ctx)
	return err == nil && st.Running
}

// EnsureRunning starts the VM if it is stopped. started is true for exactly
// one caller per attempted start, so that caller alone reconciles boxes.
//
// The start is shared work: it runs under a context detached from the caller's
// (bounded by startTimeout), so a request cancelled mid-boot (a closed tab)
// bounds only its own wait. Killing `colima start` would orphan limactl's
// children and skip colima's post-boot docker setup. A caller that goes away
// leaves the start running; the next caller waits for it, finds the VM up and
// is handed started=true.
func (v *VM) EnsureRunning(ctx context.Context) (started bool, err error) {
	if !v.SetUp() {
		return false, ErrNotSetUp
	}
	if err := v.preflight(); err != nil {
		return false, err
	}
	if err := v.lock(ctx); err != nil {
		return false, err
	}
	st, err := v.Status(ctx)
	if err != nil {
		v.unlock()
		return false, err
	}
	if st.Running {
		v.unlock()
		return v.claimReconcile(), nil
	}
	v.markReconcile()
	cfg := v.Config()
	done := make(chan error, 1)
	go func() {
		defer v.unlock() // the lock lasts as long as the start, not the caller
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v.startTimeout)
		defer cancel()
		_, err := v.r.Run(sctx, cfg.Env(os.Environ()), cfg.ColimaBin, cfg.StartArgs()...)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return false, fmt.Errorf("starting the box VM: %w", err)
		}
		return v.claimReconcile(), nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Stop stops the VM. Callers check for running boxes first (box stop -force).
func (v *VM) Stop(ctx context.Context) error {
	if err := v.preflight(); err != nil {
		return err
	}
	if err := v.lock(ctx); err != nil {
		return err
	}
	defer v.unlock()
	_, err := v.r.Run(ctx, v.env(), v.Config().ColimaBin, "stop", "--profile", Profile)
	return err
}

// Provision writes the profile's colima.yaml, starts the VM and records a stamp
// of the config it started with. On drift it refuses unless recreate is set,
// because colima fixes vmType, mountType and mounts at creation: changing them
// means deleting the VM (its images and cache; box homes live on the host).
func (v *VM) Provision(ctx context.Context, recreate bool, w io.Writer) error {
	if err := v.preflight(); err != nil {
		return err
	}
	if err := v.lock(ctx); err != nil {
		return err
	}
	defer v.unlock()
	cfg := v.Config()
	y, err := cfg.YAML()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(cfg.StampPath()); statErr == nil && v.Drifted() && !recreate {
		return errors.New("the box VM was set up with a different configuration (VM size, mounts, extra_ca_certs or the provision script); run `culibox box setup -recreate` (deletes the VM's images and cache, not box homes)")
	}
	if recreate {
		fmt.Fprintln(w, "deleting the box VM …")
		if _, err := v.r.Run(ctx, cfg.Env(os.Environ()), cfg.ColimaBin, "delete", "--profile", Profile, "--force"); err != nil {
			return err
		}
		_ = os.Remove(cfg.StampPath())
		_ = os.Remove(cfg.ProvisionStampPath())
	}
	if err := os.MkdirAll(cfg.ProfileDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DockerConfigDir(), 0o700); err != nil {
		return err
	}
	// virtiofs needs every mount source to exist before the VM starts.
	if err := EnsureDataMountDirs(cfg.Root); err != nil {
		return err
	}
	if err := writeOwnerOnly(cfg.YAMLPath(), y); err != nil {
		return err
	}
	st, err := v.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Running {
		fmt.Fprintln(w, "starting the box VM (first start downloads an image; a few minutes) …")
		sctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		if _, err := v.r.Run(sctx, cfg.Env(os.Environ()), cfg.ColimaBin, cfg.StartArgs()...); err != nil {
			return fmt.Errorf("starting the box VM: %w", err)
		}
	}
	if err := writeOwnerOnly(cfg.ProvisionStampPath(), []byte(v.provisionHash()+"\n")); err != nil {
		return err
	}
	return writeOwnerOnly(cfg.StampPath(), []byte(yamlHash(y)+"\n"))
}

// writeOwnerOnly replaces path atomically with mode 0600. The directory is
// agent-box's own (under the data root, 0700), created above.
func writeOwnerOnly(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
