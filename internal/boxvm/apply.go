package boxvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/vmshares"
)

// ErrNeedsRecreate means the VM differs from the config by more than its folder
// list; only `box setup -recreate` can fix that.
var ErrNeedsRecreate = errors.New("the box VM has other changes (VM size, extra_ca_certs or the provision script) that need `agent-box box setup -recreate`")

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
// or will have at its next start), cached by the file's mtime and size.
func (v *VM) AppliedMounts() ([]Mount, error) {
	path := v.Config().YAMLPath()
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
// colima.yaml, start. It never deletes the VM. The stamp is written only after
// a successful start; the yaml is written before it, so a failed start retries
// with the new list.
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
	if err := writeOwnerOnly(cfg.StampPath(), []byte(yamlHash(y)+"\n")); err != nil {
		h.step(StepStart, StepEvent{Done: true, Err: err})
		return err
	}
	h.step(StepStart, StepEvent{Done: true})
	return nil
}

// VerifyMounts checks the running VM's real virtiofs mounts against its
// colima.yaml: the same paths, the same modes, nothing more.
func (v *VM) VerifyMounts(ctx context.Context) error {
	want, err := v.AppliedMounts()
	if err != nil {
		return err
	}
	cfg := v.Config()
	out, err := v.r.Run(ctx, v.env(), cfg.ColimaBin, "ssh", "--profile", Profile, "--", "cat", "/proc/self/mounts")
	if err != nil {
		return fmt.Errorf("reading the box VM's mounts: %w", err)
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
	var problems []string
	seen := map[string]bool{}
	for _, m := range want {
		seen[m.Location] = true
		w, ok := got[m.Location]
		switch {
		case !ok:
			problems = append(problems, "missing "+m.Location)
		case w != m.Writable:
			problems = append(problems, fmt.Sprintf("%s is %s, expected %s", m.Location, modeName(w), modeName(m.Writable)))
		}
	}
	for p := range got {
		if !seen[p] {
			problems = append(problems, "extra "+p)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%w: %s", ErrMountsMismatch, strings.Join(problems, "; "))
	}
	return nil
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
