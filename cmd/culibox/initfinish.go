package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/doctor"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/vmshares"
)

// initConfigTarget is the file step 6 writes: -config when given, else the
// file config.Resolve read, else config.json in the data root.
func initConfigTarget(a cliArgs, src config.Source) string {
	if a.ConfigSet {
		if p, err := filepath.Abs(a.ConfigPath); err == nil {
			return p
		}
		return a.ConfigPath
	}
	if src.Path != "" {
		return src.Path
	}
	return filepath.Join(src.Root.Path, "config.json")
}

// initConfigStep is step 6: the config keys steps 3-5 settled, written once.
// A new file is shown in full and written on a yes (the default); an existing
// one is shown as a diff of its normalized form and changed only on an explicit
// yes. Nothing to change is "already done" with no write. The Claude token is
// not in e, so it can never reach the file or the output.
func initConfigStep(u *initUI, target string, e config.InitEdit) bool {
	u.begin("config")
	existing, err := os.ReadFile(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		existing = nil // PlanInitWrite: nil is "no file yet"
	case err != nil:
		u.stopped(fmt.Sprintf("%s cannot be read (%v); fix that, then run culibox init again", target, err))
		return false
	}
	out, skipped, err := config.PlanInitWrite(existing, e)
	if err != nil {
		u.stopped(fmt.Sprintf("init cannot edit %s: %v; fix the file by hand, then run culibox init again", target, err))
		return false
	}
	for _, name := range skipped {
		u.say("agent %s is linked to another account; init leaves it as it is", name)
	}
	var before []byte
	if existing != nil {
		if before, err = config.NormalizeJSON(existing); err != nil {
			u.stopped(fmt.Sprintf("init cannot read %s as JSON (%v); fix the file by hand, then run culibox init again", target, err))
			return false
		}
		if bytes.Equal(before, out) {
			u.already(target + " has these settings")
			return true
		}
	}
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		u.stopped(fmt.Sprintf("%s is a symlink and init never writes through one; run culibox init -config <the real file>, or make these changes by hand:\n%s", target, indentLines(lineDiff(before, out), "  ")))
		return false
	}
	if existing == nil {
		u.say("%s does not exist; init writes:", target)
		fmt.Fprint(u.out, indentLines(string(out), "    "))
		if !u.ask(fmt.Sprintf("Write %s?", target), true) {
			u.stopped(fmt.Sprintf("no config was written; create %s with the content above, then run culibox init again", target))
			return false
		}
	} else {
		u.say("changes to %s (init writes the keys sorted, two-space indented, so the left side is your file in that form):", target)
		fmt.Fprint(u.out, indentLines(lineDiff(before, out), "    "))
		if !u.ask("Apply?", false) {
			u.stopped(fmt.Sprintf("%s was left as it is; make the changes above by hand, then run culibox init again", target))
			return false
		}
	}
	if err := config.WriteFileAtomic(target, out); err != nil {
		// The last step, the directory sync, runs after the rename: then the new
		// file is in place and only its durability is in doubt.
		if b, rerr := os.ReadFile(target); rerr == nil && bytes.Equal(b, out) {
			u.say("wrote %s, but syncing its folder failed (%v); the file is in place", target, err)
			u.done()
			return true
		}
		u.stopped(fmt.Sprintf("writing %s failed (%v); fix that, then run culibox init again", target, err))
		return false
	}
	u.say("wrote %s", target)
	u.done()
	return true
}

// indentLines puts prefix before every line of s.
func indentLines(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.SplitAfter(s, "\n")
	var sb strings.Builder
	for _, l := range lines {
		if l != "" {
			sb.WriteString(prefix + l)
		}
	}
	if !strings.HasSuffix(s, "\n") {
		sb.WriteByte('\n')
	}
	return sb.String()
}

// initBoxStep is step 7. On macOS the order depends on the VM.
//
// No VM yet: the workspace goes into the VM's folder list first, then `box
// setup` (VM, image, containment probe, token row) starts the VM with that list
// (setup reads it before it provisions), so the first boot already has the
// workspace and nothing is pending.
//
// A VM that is set up but STOPPED (a reboot, `box stop`): `box setup` cannot
// apply a changed folder list to a VM that is not running (its guard refuses
// without -force, because "not running" may hide a running box), so init does
// it: it adds the folder, asks, and applies with -force, which is safe because
// the VM is provably down (colima says so and nothing answers on its docker
// socket) and which starts it with the list. Then setup runs against a running
// VM.
//
// A running VM: box setup runs first (it also restarts the VM itself when only
// the folder list differs, unless a box runs), then the folder is added and,
// when the VM does not have it yet, init asks and applies it.
//
// Either way init reads back whether the VM has the workspace and stops unless
// it does. A box that is already ready is "already done" unless the user asks
// for setup again.
func initBoxStep(u *initUI, cfg config.Config, cfgFiles []string, dataDir, workspace string, d initDeps) bool {
	u.begin("box setup")
	darwin := d.goos == "darwin"
	runSetup := true
	if d.boxReady(cfg, cfgFiles, dataDir) {
		if darwin {
			u.say("the box is ready: the VM runs, the image is current and doctor's box rows pass")
		} else {
			u.say("the box is ready: the image is current and the boxed claude token resolves")
		}
		if !u.ask("Run box setup again?", false) {
			if !darwin {
				u.say(initNoVMLine)
				u.already("the box is ready")
				return true
			}
			runSetup = false
		}
	}
	existing := darwin && d.vmSetUp(cfg, dataDir)
	fresh := darwin && runSetup && !existing
	stoppedVM := existing && runSetup && d.vmStopped(cfg, dataDir)
	covered, applied := "", false
	readState := func() (string, bool) {
		state, err := d.shareState(cfg, cfgFiles, dataDir, workspace)
		if err != nil {
			u.stopped(fmt.Sprintf("the box VM's folder state could not be read (%v); run `culibox box share list`, then culibox init again", err))
			return "", false
		}
		return state, true
	}
	if fresh || stoppedVM {
		var ok bool
		if covered, ok = initVMFolder(u, cfg, cfgFiles, dataDir, workspace, d, fresh); !ok {
			return false
		}
	}
	if stoppedVM {
		state, ok := readState()
		if !ok {
			return false
		}
		if state != "applied" {
			if !initApplyShares(u, cfg, cfgFiles, dataDir, workspace, d, true) {
				return false
			}
			applied = true
		}
	}
	if runSetup {
		if rc := d.boxSetup(cfg, cfgFiles, u.out); rc != 0 {
			if existing {
				u.stopped("box setup failed (see the lines above); fix that. When it says boxes run or the VM state is unknown, stop the boxes or run `culibox box setup -force` (ends the running boxes and their sessions). Then culibox init again")
			} else {
				u.stopped("box setup failed (see the lines above); fix that, run `culibox box setup`, then culibox init again")
			}
			return false
		}
	}
	if !darwin {
		u.say(initNoVMLine)
		u.done()
		return true
	}
	if !fresh && !stoppedVM {
		var ok bool
		if covered, ok = initVMFolder(u, cfg, cfgFiles, dataDir, workspace, d, false); !ok {
			return false
		}
	}
	state, ok := readState()
	if !ok {
		return false
	}
	if state == "pending" && !fresh && !stoppedVM {
		if !initApplyShares(u, cfg, cfgFiles, dataDir, workspace, d, false) {
			return false
		}
		applied = true
		if state, ok = readState(); !ok {
			return false
		}
	}
	if state != "applied" {
		u.stopped(fmt.Sprintf("the box VM does not have %s yet (state: %s); run `culibox box share apply` (restarts the box VM), then culibox init again", workspace, state))
		return false
	}
	if !runSetup && !applied && covered != "" {
		u.already("the box is ready and the VM sees " + workspace)
		return true
	}
	u.say("the box VM has %s: its folder list is applied", workspace)
	u.done()
	return true
}

// initApplyShares has the VM take its saved folder list, after saying so and
// asking. force is only for a VM that is provably stopped (nothing can run in
// it, so nothing ends): `box share apply` then starts it with the list. It
// reports false, with a stopped line naming a command that works from that
// state, when the user declines or the apply fails.
func initApplyShares(u *initUI, cfg config.Config, cfgFiles []string, dataDir, workspace string, d initDeps, force bool) bool {
	if force {
		u.say("the box VM is stopped and does not have %s yet; applying the saved folder list starts it with the list", workspace)
	} else {
		u.say("the box VM does not have %s yet; taking it needs a restart of the box VM, which ends the sessions in it, and applies the whole saved folder list", workspace)
	}
	if !u.ask("Apply it now?", true) {
		if force {
			// Not "run apply -force": by the time the user does, the VM may run boxes.
			u.stopped(fmt.Sprintf("the box VM does not have %s yet; run culibox init again, which checks the VM first (`culibox box share apply -force` is only safe while the VM is still stopped)", workspace))
		} else {
			u.stopped(fmt.Sprintf("the box VM does not have %s yet; run `culibox box share apply`, then culibox init again", workspace))
		}
		return false
	}
	// The prompt can wait for minutes, and `culibox serve` may start the VM for a
	// boxed spawn meanwhile. -force skips the live-box guard and would stop that
	// VM, so check again right before it and fall back to the guarded apply.
	if force && !d.vmStopped(cfg, dataDir) {
		force = false
		u.say("the box VM has started since the check; applying with the guard that refuses while a box runs")
	}
	if rc := d.shareApply(cfg, cfgFiles, dataDir, force, u.out); rc != 0 {
		if force {
			u.stopped("`culibox box share apply -force` did not finish (see the lines above); fix that, then run culibox init again, which checks the VM first")
		} else {
			u.stopped("`culibox box share apply` did not finish (see the lines above); with boxes running, stop them or use `culibox box share apply -force` (ends the running boxes and their sessions), then culibox init again")
		}
		return false
	}
	return true
}

const initNoVMLine = "no VM folders to set: the box VM is macOS only; on Linux boxes run on this machine's docker and mount the workspace directly"

// initVMFolder makes sure the box VM's folder list covers the workspace. It
// returns the folder that already covered it ("" when it was added).
// beforeSetup is true when there is no VM yet: `box setup` then starts it with
// the list, so nothing is left to apply.
func initVMFolder(u *initUI, cfg config.Config, cfgFiles []string, dataDir, workspace string, d initDeps, beforeSetup bool) (string, bool) {
	covered, err := d.shareAdd(cfg, cfgFiles, dataDir, workspace)
	if err != nil {
		u.stopped(fmt.Sprintf("%s could not be added to the box VM's folders (%v); fix that, run `culibox box share add %s`, then culibox init again", workspace, err, shellQuote(workspace)))
		return "", false
	}
	if covered != "" {
		if !sameFolder(covered, workspace) {
			u.say("the box VM sees %s through %s", workspace, covered)
		}
		return covered, true
	}
	if beforeSetup {
		u.say("added %s to the box VM's folders; box setup below starts the VM with it", workspace)
	} else {
		u.say("added %s to the box VM's folders", workspace)
	}
	return "", true
}

// sameFolder is true for two spellings of one folder: the same path, a case
// difference (APFS), or a symlink. shareAdd answers with the listed spelling.
func sameFolder(a, b string) bool {
	return strings.EqualFold(a, b) || vmshares.Resolve(a) == vmshares.Resolve(b) ||
		strings.EqualFold(vmshares.Resolve(a), vmshares.Resolve(b))
}

// initFinishStep is step 8: doctor, then how to start CuliBox and the URL to
// open (creating login.token when absent, as serve would). Its exit code is
// doctor's: 0 when no row failed.
func initFinishStep(u *initUI, cfg config.Config, src config.Source, cfgFiles []string, dataDir string, d initDeps) int {
	u.begin("doctor and login URL")
	rc := d.doctor(cfg, src, cfgFiles, u.out)
	login, err := auth.ResolveLogin(cfg, dataDir)
	if err != nil {
		u.say("the login cannot be prepared: %v", err)
		rc = 1
	} else {
		u.say("Start CuliBox: culibox serve")
		if login.Mode == auth.ModePassword {
			// The token-mode URL without a token is the login page itself.
			url, hint := loginURLFor(cfg, "")
			u.say("Then open: %s and log in with your password (from $%s)", strings.TrimSuffix(url, "#token="), cfg.PasswordEnv)
			initHint(u, hint)
		} else {
			url, hint := loginURLFor(cfg, login.Secret)
			u.say("Then open: %s", url)
			initHint(u, hint)
		}
	}
	if rc != 0 {
		u.stopped("doctor found a problem (the FAIL lines above); fix it, check with `culibox doctor`, then start CuliBox with culibox serve")
		return 1
	}
	u.done()
	return 0
}

func initHint(u *initUI, hint string) {
	if hint != "" {
		u.say("(%s)", hint)
	}
}

// The real seams behind initDeps' box fields. None of them starts the VM.

// initBoxTimeout bounds init's own docker questions (image presence).
const initBoxTimeout = 30 * time.Second

// initVMSetUp is the box VM's "set up" state, as `box status` reads it: on
// macOS only, false when colima (or docker) is not installed, else the setup
// stamp.
func initVMSetUp(cfg config.Config, dataDir, home, goos string) bool {
	if goos != "darwin" {
		return false
	}
	if _, err := boxvm.LookColima(cfg.ColimaBin); err != nil {
		return false
	}
	l, err := newBoxLayer(cfg, dataDir, home, goos, boxvm.ExecRunner{}, nil)
	if err != nil {
		return false
	}
	return l.vm.SetUp()
}

// initBoxReady says whether box setup has nothing left to do (see
// initBoxReadyFrom). macOS also needs the VM set up, checked first because it
// is cheap.
func initBoxReady(cfg config.Config, cfgFiles []string, dataDir, home, goos string) bool {
	if goos == "darwin" && !initVMSetUp(cfg, dataDir, home, goos) {
		return false
	}
	rep := doctor.BoxTier(doctor.Input{Config: cfg, Home: home, ConfigFiles: cfgFiles})
	return initBoxReadyFrom(goos, rep,
		func() bool { return initTokenStored(dataDir, newKeeper(dataDir)) },
		func() bool { return initImageCurrent(cfg) })
}

// initBoxReadyFrom reads doctor's box-tier report: every row PASS, and the
// boxed claude token row PASS too (doctor's rows carry it on both OSes unless the
// tier ended early; Linux adds it, see below). The one
// exception is a token row that is a WARN only because the store is locked
// while the claude-box token is in it (tokenStored, which never decrypts): a
// Linux store's unlock expires, and a locked store is no reason to rebuild. The
// image must be current: macOS reads doctor's "box image" row (present only
// while the VM runs), Linux asks the host's docker (imageCurrent).
func initBoxReadyFrom(goos string, rep doctor.BoxTierReport, tokenStored, imageCurrent func() bool) bool {
	rows := rep.Rows
	if goos != "darwin" {
		// A report that ended early lacks the token row; one that has it holds it a
		// second time, which changes nothing: the same row gets the same verdict.
		rows = append(slices.Clone(rows), rep.Token)
	}
	image := false
	for _, r := range rows {
		switch {
		case r.Status == doctor.Pass:
			image = image || r.Check == "box image"
		case r.Check == rep.Token.Check && rep.TokenLockedOnly && tokenStored():
		default:
			return false
		}
	}
	if goos == "darwin" {
		return image
	}
	return imageCurrent()
}

// initTokenStored says whether the store holds the claude-box token, by the
// same list step 5 reads (no passphrase, no decryption).
func initTokenStored(dataDir string, k secretstore.Keeper) bool {
	ok, err := storeHasID(secretstore.Open(dataDir, k, nil), account.TokenID(initAccountName))
	return err == nil && ok
}

// initImageCurrent says whether the box image is present, built for the
// daemon's architecture and from this binary's Dockerfile (Linux: the host's
// docker).
func initImageCurrent(cfg config.Config) bool {
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), initBoxTimeout)
	defer cancel()
	d := boximage.Docker{Bin: bin}
	arch, err := d.ServerArch(ctx)
	if err != nil {
		return false
	}
	image := cfg.BoxImageOrDefault()
	st, err := d.Inspect(ctx, image)
	if err != nil {
		return false
	}
	fatal, msg := st.CheckFor(image, arch)
	return !fatal && msg == ""
}

// initShareAdd adds path to the box VM's folder list read-write, as `box share
// add` does, and returns the folder that already covered it ("" = added).
func initShareAdd(cfg config.Config, cfgFiles []string, dataDir, path, home, goos string) (string, error) {
	if goos != "darwin" {
		return "", errors.New("the box VM exists only on macOS")
	}
	l, err := newBoxLayer(cfg, dataDir, home, goos, boxvm.ExecRunner{}, protectedDirsOf(cfg, dataDir, cfgFiles))
	if err != nil {
		return "", err
	}
	svc, err := newCLIShareSvc(context.Background(), cfg, l)
	if err != nil {
		return "", err
	}
	return svc.AddWorkspace(path)
}

// initShareState is whether the box VM has the workspace: "applied" when a
// folder the VM's written colima.yaml mounts read & write covers it, else
// "pending" (the VM is set up but lacks it) or "not_set_up". Only the
// workspace's own row counts: another unapplied change does not make it
// pending. It never starts the VM.
func initShareState(cfg config.Config, cfgFiles []string, dataDir, workspace, home, goos string) (string, error) {
	if goos != "darwin" {
		return "", errors.New("the box VM exists only on macOS")
	}
	l, err := newBoxLayer(cfg, dataDir, home, goos, boxvm.ExecRunner{}, protectedDirsOf(cfg, dataDir, cfgFiles))
	if err != nil {
		return "", err
	}
	svc, err := newCLIShareSvc(context.Background(), cfg, l)
	if err != nil {
		return "", err
	}
	if svc.corrupt != nil {
		return "", svc.corrupt
	}
	applied, _, setUp, err := svc.ctl.Applied()
	if err != nil {
		return "", err
	}
	return workspaceShareState(setUp, workspace, applied, svc.env.Fold), nil
}

// workspaceShareState is initShareState's answer from what it read: not set up,
// applied (a read & write mount holds the workspace) or pending.
func workspaceShareState(setUp bool, workspace string, applied []vmshares.Share, fold bool) string {
	switch {
	case !setUp:
		return "not_set_up"
	case workspaceApplied(workspace, applied, fold):
		return "applied"
	}
	return "pending"
}

// workspaceApplied is true when a folder the VM applied read & write holds the
// workspace (a whole-home VM applies the home folder, so it holds one under
// home). A read-only folder does not: a workspace needs to be written.
func workspaceApplied(workspace string, applied []vmshares.Share, fold bool) bool {
	ws := vmshares.Resolve(workspace)
	for _, a := range applied {
		if a.Mode == vmshares.RW && vmshares.Within(ws, vmshares.Resolve(a.Path), fold) {
			return true
		}
	}
	return false
}

// initVMStopped is true when the box VM is set up and provably not running
// (Status.Stopped needs colima's answer and a dead docker socket). A status
// that failed any other way is false: a box may still run.
func initVMStopped(cfg config.Config, dataDir, home, goos string) bool {
	if goos != "darwin" {
		return false
	}
	if _, err := boxvm.LookColima(cfg.ColimaBin); err != nil {
		return false
	}
	l, err := newBoxLayer(cfg, dataDir, home, goos, boxvm.ExecRunner{}, nil)
	if err != nil || !l.vm.SetUp() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), initBoxTimeout)
	defer cancel()
	return vmIsStopped(ctx, l.vm.Status)
}

// vmIsStopped is true only when status says the VM is stopped: a failed read, a
// running VM, and a status that is neither (colima said "not running" but the
// docker socket still answers) are all "a box may still run".
func vmIsStopped(ctx context.Context, status func(context.Context) (boxvm.Status, error)) bool {
	st, err := status(ctx)
	return err == nil && st.Stopped
}

// initShareApply is `culibox box share apply [-force]`, in-process. Without
// -force it refuses while a box runs. The output is the apply's own step lines.
func initShareApply(cfg config.Config, cfgFiles []string, home string, force bool, w io.Writer) int {
	merged, _ := mergeCredentials(cfg, home)
	return runBox(cliArgs{Cmd: "box", Sub: "share", ShareAction: "apply", Force: force}, merged, home, cfgFiles, w)
}

// initBoxSetup is `culibox box setup`, in-process, with the config main would
// give it (web-defined credentials merged in).
func initBoxSetup(cfg config.Config, cfgFiles []string, home string, w io.Writer) int {
	merged, _ := mergeCredentials(cfg, home)
	return runBox(cliArgs{Cmd: "box", Sub: "setup"}, merged, home, cfgFiles, w)
}

// initDoctor is `culibox doctor` with the Input main builds for it.
func initDoctor(cfg config.Config, src config.Source, cfgFiles []string, home string, w io.Writer) int {
	_, notice, envErr := config.HomeEnv(os.Getenv)
	merged, cred := mergeCredentials(cfg, home)
	return doctor.Run(w, doctor.Input{Config: merged, Source: src, Home: home, XDGConfig: xdgConfigHome(), Commit: buildCommit(),
		CredErrors: cred.Problems(), ConfigFiles: cfgFiles, EnvNotice: notice, EnvErr: envErr})
}
