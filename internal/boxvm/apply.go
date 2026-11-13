package boxvm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/vmshares"
)

// ErrNeedsRecreate means the VM differs from the config by more than its folder
// list; only `box setup -recreate` can fix that.
var ErrNeedsRecreate = errors.New("the box VM has other changes (VM size, extra_ca_certs or the provision script) that need `culibox box setup -recreate`")

// ErrStartTimeout means the VM did not come back after an apply.
var ErrStartTimeout = errors.New("The VM did not start within 2 minutes.")

// ErrMountsMismatch means the running VM's mounts differ from its colima.yaml.
var ErrMountsMismatch = errors.New("the box VM's folders do not match what was applied")

// Drift is how the VM differs from the config agent-box would write now.
type Drift int

const (
	DriftNone       Drift = iota
	DriftMountsOnly       // only the folder list differs: a stop and start applies it
	DriftOther            // size, extra_ca_certs or the provision script: needs a recreate
)

// DriftKind classifies the drift. A VM provisioned by v0.32 (whole home) whose
// only difference is the mount list reads as DriftMountsOnly: re-rendering the
// config with the VM's own applied mounts reproduces its stamp.
func (v *VM) DriftKind() (Drift, error) {
	b, err := os.ReadFile(v.Config().StampPath())
	if err != nil {
		return DriftOther, ErrNotSetUp
	}
	stamp := strings.TrimSpace(string(b))
	cfg := v.Config()
	y, err := cfg.YAML()
	if err != nil {
		return DriftOther, err
	}
	if yamlHash(y) == stamp {
		return DriftNone, nil
	}
	applied, err := v.AppliedMounts()
	if err != nil {
		return DriftOther, err
	}
	y2, err := renderYAML(cfg, applied)
	if err != nil {
		return DriftOther, err
	}
	if yamlHash(y2) == stamp {
		return DriftMountsOnly, nil
	}
	return DriftOther, nil
}

// AppliedMounts are the mounts in the VM's written colima.yaml (what the VM has
// or will have at its next start), cached by the file's mtime and size. A file
// that lacks the four data mounts and does not share the home folder is an
// error (ErrMountsIncomplete), never an applied list.
func (v *VM) AppliedMounts() ([]Mount, error) {
	cfg := v.Config()
	path := cfg.YAMLPath()
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	v.flagMu.Lock()
	defer v.flagMu.Unlock()
	if v.mountsCache != nil && v.mountsCache.mtime.Equal(fi.ModTime()) && v.mountsCache.size == fi.Size() {
		return append([]Mount(nil), v.mountsCache.mounts...), nil
	}
	ms, err := ReadMounts(path)
	if err != nil {
		return nil, err
	}
	if err := checkApplied(cfg, ms); err != nil {
		return nil, err
	}
	v.mountsCache = &mountsCache{fi.ModTime(), fi.Size(), ms}
	return append([]Mount(nil), ms...), nil
}

type mountsCache struct {
	mtime  time.Time
	size   int64
	mounts []Mount
}

// WholeHome reports whether the VM shares the home folder or a folder above it.
func (v *VM) WholeHome() (bool, error) {
	ms, err := v.AppliedMounts()
	if err != nil {
		return false, err
	}
	cfg := v.Config()
	for _, m := range ms {
		if vmshares.Within(cfg.Home, m.Location, cfg.Fold) {
			return true, nil
		}
	}
	return false, nil
}

// SetShares replaces the folder list the next apply writes.
func (v *VM) SetShares(s []vmshares.Share) {
	rs := make([]vmshares.Share, len(s))
	for i, sh := range s {
		rs[i] = vmshares.Share{Path: vmshares.Resolve(sh.Path), Mode: sh.Mode}
	}
	v.flagMu.Lock()
	v.cfg.Shares = rs
	v.flagMu.Unlock()
}

// ApplyStep names a slow step of ApplyMounts.
type ApplyStep int

const (
	StepStop ApplyStep = iota
	StepStart
)

// StepEvent reports a step's end (Done) and its error, if any.
type StepEvent struct {
	Done bool
	Err  error
}

// ApplyHooks lets a caller show progress. Step is called with Done=false when a
// step begins and Done=true when it ends.
type ApplyHooks struct {
	Step func(ApplyStep, StepEvent)
}

func (h ApplyHooks) step(s ApplyStep, e StepEvent) {
	if h.Step != nil {
		h.Step(s, e)
	}
}

const stopTimeout = 30 * time.Second

// ApplyMounts makes the VM share the current folder list: stop, rewrite
// colima.yaml, start. It never deletes the VM. The yaml and its stamp are
// written before the start, so a failed or timed-out start is retried with the
// new list by calling ApplyMounts again.
func (v *VM) ApplyMounts(ctx context.Context, h ApplyHooks) error {
	if !v.SetUp() {
		return ErrNotSetUp
	}
	if err := v.preflight(); err != nil {
		return err
	}
	if err := v.lock(ctx); err != nil {
		return err
	}
	defer v.unlock()
	unlockApply, err := lockApply(v.Config().ApplyLockPath())
	if err != nil {
		return err
	}
	defer unlockApply()
	kind, err := v.DriftKind()
	if err != nil {
		return err
	}
	if kind == DriftOther {
		return ErrNeedsRecreate
	}
	cfg := v.Config()
	y, err := cfg.YAML()
	if err != nil {
		return err
	}
	// Before the stop: a list colima would refuse must not take the VM down.
	if err := checkMountsDisjoint(cfg.Mounts(), cfg.Fold); err != nil {
		return err
	}
	st, err := v.Status(ctx)
	if err != nil {
		return err
	}
	h.step(StepStop, StepEvent{})
	if st.Running {
		sctx, cancel := context.WithTimeout(ctx, stopTimeout)
		_, err := v.r.Run(sctx, v.env(), cfg.ColimaBin, "stop", "--profile", Profile)
		cancel()
		if err != nil {
			err = fmt.Errorf("stopping the box VM: %w", err)
			h.step(StepStop, StepEvent{Done: true, Err: err})
			return err
		}
	}
	h.step(StepStop, StepEvent{Done: true})
	if err := EnsureDataMountDirs(cfg.Root); err != nil {
		return err
	}
	if err := writeOwnerOnly(cfg.YAMLPath(), y); err != nil {
		return err
	}
	// The stamp is the hash of the YAML agent-box wrote for the VM, which colima
	// uses at its next start. Writing it before the start means a failed start
	// is retried by another ApplyMounts, not read as other drift.
	if err := writeOwnerOnly(cfg.StampPath(), []byte(yamlHash(y)+"\n")); err != nil {
		return err
	}
	h.step(StepStart, StepEvent{})
	v.markReconcile()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v.startTimeout)
	defer cancel()
	_, err = v.r.Run(sctx, v.env(), cfg.ColimaBin, cfg.StartArgs()...)
	if err != nil {
		if errors.Is(sctx.Err(), context.DeadlineExceeded) {
			err = ErrStartTimeout
		} else {
			err = fmt.Errorf("starting the box VM: %w", err)
		}
		h.step(StepStart, StepEvent{Done: true, Err: err})
		return err
	}
	h.step(StepStart, StepEvent{Done: true})
	return nil
}

// VerifyMounts checks the running VM's real virtiofs mounts against its
// colima.yaml: the same paths, the same modes, nothing more.
func (v *VM) VerifyMounts(ctx context.Context) error {
	d, err := v.diffMounts(ctx)
	if err != nil {
		return err
	}
	return d.err()
}

// mountDiff is how the VM's real virtiofs mounts differ from colima.yaml.
type mountDiff struct {
	problems []string
	// modeOnly are the folders the VM has at the wrong mode (Writable is the
	// wanted one); other is true when anything is missing or extra.
	modeOnly []Mount
	other    bool
}

func (d mountDiff) err() error {
	if len(d.problems) == 0 {
		return nil
	}
	p := append([]string(nil), d.problems...)
	sort.Strings(p)
	return fmt.Errorf("%w: %s", ErrMountsMismatch, strings.Join(p, "; "))
}

func (v *VM) diffMounts(ctx context.Context) (mountDiff, error) {
	var d mountDiff
	want, err := v.AppliedMounts()
	if err != nil {
		return d, err
	}
	cfg := v.Config()
	out, err := v.r.Run(ctx, v.env(), cfg.ColimaBin, "ssh", "--profile", Profile, "--", "cat", "/proc/self/mounts")
	if err != nil {
		return d, fmt.Errorf("reading the box VM's mounts: %w", err)
	}
	got := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 || f[2] != "virtiofs" {
			continue
		}
		opt, _, _ := strings.Cut(f[3], ",")
		got[unescapeMount(f[1])] = opt == "rw"
	}
	seen := map[string]bool{}
	for _, m := range want {
		seen[m.Location] = true
		w, ok := got[m.Location]
		switch {
		case !ok:
			d.problems = append(d.problems, "missing "+m.Location)
			d.other = true
		case w != m.Writable:
			d.problems = append(d.problems, fmt.Sprintf("%s is %s, expected %s", m.Location, modeName(w), modeName(m.Writable)))
			d.modeOnly = append(d.modeOnly, m)
		}
	}
	for p := range got {
		if !seen[p] {
			d.problems = append(d.problems, "extra "+p)
			d.other = true
		}
	}
	return d, nil
}

// ReconcileMounts is VerifyMounts for a VM that was just started. After a mode
// switch (rw to ro or back) the guest still mounts a folder at its old mode:
// it mounts from the /etc/fstab the previous boot left, cloud-init rewrites
// that file a moment later, and lima's `mount -a` skips a folder that is
// already mounted (#236). The Mac side already has the new mode, so when the
// mode is the ONLY difference this remounts each such folder in the guest and
// verifies again. A missing or extra folder is never touched: it fails as
// VerifyMounts does, and so does a remount that fails or does not hold.
func (v *VM) ReconcileMounts(ctx context.Context) error {
	d, err := v.diffMounts(ctx)
	if err != nil {
		v.markReconcile() // no verdict yet: the next spawn gets the chance
		return err
	}
	if len(d.problems) == 0 {
		return nil
	}
	if d.other || len(d.modeOnly) == 0 {
		return d.err()
	}
	// Only one caller per start is handed started=true, so a remount that is
	// cut off (the spawn's request ends) or fails is re-armed for the next
	// spawn; the remount itself outlives the request, bounded by its own timeout.
	err = v.remountModes(context.WithoutCancel(ctx), d)
	if err != nil {
		v.markReconcile()
	}
	return err
}

// reconcileTimeout bounds the remount and the check after it.
var reconcileTimeout = 60 * time.Second

// remountModes remounts the mode-only mismatches under the apply lock (an
// apply that is stopping or starting the VM must not be raced). A lock held
// elsewhere skips the remount and fails closed with the mismatch the check
// found, plus the reason; the folders are never trusted unverified.
func (v *VM) remountModes(ctx context.Context, d mountDiff) error {
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	unlock, err := lockApply(v.Config().ApplyLockPath())
	if err != nil {
		if errors.Is(err, ErrApplyLocked) {
			return fmt.Errorf("%w (not fixing the folder modes: %v)", d.err(), err)
		}
		return fmt.Errorf("%w (fixing the folder modes in the VM failed: %v)", d.err(), err)
	}
	defer unlock()
	if _, err := v.r.Run(ctx, v.env(), v.Config().ColimaBin, "ssh", "--profile", Profile, "--", "sh", "-c", remountScript(d.modeOnly)); err != nil {
		return fmt.Errorf("%w (fixing the folder modes in the VM failed: %v)", d.err(), err)
	}
	if err := v.VerifyMounts(ctx); err != nil {
		return fmt.Errorf("%w (after fixing the folder modes in the VM)", err)
	}
	return nil
}

// remountScript builds the guest command that remounts each folder at its
// wanted mode. A path travels base64-encoded (an alphabet a shell reads as
// plain text, so no quote or space in it can be interpreted, however lima
// joins the argv), the same way PathIDs sends paths; the mode is the literal
// ro or rw chosen here, never input. `sudo -n` never prompts: lima's guest
// user has passwordless sudo. `--` ends options for a path that starts with -.
func remountScript(ms []Mount) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, m := range ms {
		mode := "ro"
		if m.Writable {
			mode = "rw"
		}
		enc := base64.StdEncoding.EncodeToString([]byte(m.Location))
		fmt.Fprintf(&b, "p=$(printf %%s %s | base64 -d; echo x); p=${p%%x}\nsudo -n mount -o remount,%s -- \"$p\"\n", enc, mode)
	}
	return b.String()
}

func modeName(w bool) string {
	if w {
		return "rw"
	}
	return "ro"
}

// unescapeMount decodes the octal escapes (\040 for a space) /proc/mounts uses.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ErrApplyLocked means another process (the daemon, or a `box share apply`
// from a terminal) is applying the folder list now.
var ErrApplyLocked = errors.New("another CuliBox is applying the box VM's folder list; try again when it finishes")

// ApplyLockPath is the advisory lock every process holds for an apply's whole
// stop, write and start. v.lock is per process; this one spans processes.
func (c Config) ApplyLockPath() string { return filepath.Join(c.ColimaHome(), "agent-box.apply.lock") }

// lockApply takes the cross-process apply lock without waiting: an flock on
// the lock file, released when unlock runs or the process dies.
func lockApply(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrApplyLocked
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		f.Close()
	}, nil
}

// ApplyLockHeld reports whether an apply holds the cross-process lock now
// (this process's own included: the caller checks its own job first).
func (v *VM) ApplyLockHeld() bool {
	unlock, err := lockApply(v.Config().ApplyLockPath())
	if err != nil {
		return errors.Is(err, ErrApplyLocked)
	}
	unlock()
	return false
}

// ErrMountsOverlap means two of the VM's mounts would nest or repeat once links
// are followed: colima refuses to start with that, so the apply stops first.
var ErrMountsOverlap = errors.New("two of the box VM's folders overlap once links are followed, and colima cannot start with one inside another; the VM was not touched. Remove one of them in Settings › VM folders (or `culibox box share rm`)")

// checkMountsDisjoint refuses a mount list where one location (resolved now)
// equals or lies inside another.
func checkMountsDisjoint(ms []Mount, fold bool) error {
	locs := make([]string, len(ms))
	for i, m := range ms {
		locs[i] = vmshares.Resolve(m.Location)
	}
	for i := range locs {
		for j := range locs {
			if i != j && vmshares.Within(locs[i], locs[j], fold) {
				return fmt.Errorf("%w: %s is inside %s", ErrMountsOverlap, ms[i].Location, ms[j].Location)
			}
		}
	}
	return nil
}
