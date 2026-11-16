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

// initBoxStep is step 7, in two shapes on macOS.
//
// No VM yet: the workspace goes into the VM's folder list first, then `box
// setup` (VM, image, containment probe, token row) starts the VM with that list
// (setup reads it before it provisions), so the first boot already has the
// workspace and nothing is pending.
//
// A VM that is already set up (running or stopped): box setup runs first, which
// also starts a stopped VM, because applying a folder change refuses unless the
// VM runs. Then the workspace is added and, when the VM does not have it yet,
// init offers the restart that applies it and runs it with its output shown.
//
// Either way init reads the folder state back and stops unless the workspace
// is applied. A box that is already ready is "already done" unless the user
// asks for setup again.
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
	freshVM := darwin && runSetup && !d.vmSetUp(cfg, dataDir)
	if freshVM {
		if _, ok := initVMFolder(u, cfg, cfgFiles, dataDir, workspace, d, true); !ok {
			return false
		}
	}
	if runSetup {
		if rc := d.boxSetup(cfg, cfgFiles, u.out); rc != 0 {
			u.stopped("box setup failed (see the lines above); fix that, run `culibox box setup`, then culibox init again")
			return false
		}
	}
	if !darwin {
		u.say(initNoVMLine)
		u.done()
		return true
	}
	covered := ""
	if !freshVM {
		var ok bool
		if covered, ok = initVMFolder(u, cfg, cfgFiles, dataDir, workspace, d, false); !ok {
			return false
		}
	}
	state, err := d.shareState(cfg, cfgFiles, dataDir, workspace)
	if err != nil {
		u.stopped(fmt.Sprintf("the box VM's folder state could not be read (%v); run `culibox box share list`, then culibox init again", err))
		return false
	}
	applied := false
	if state == "pending" && !freshVM {
		if !initApplyShares(u, cfg, cfgFiles, dataDir, workspace, d) {
			return false
		}
		applied = true
		if state, err = d.shareState(cfg, cfgFiles, dataDir, workspace); err != nil {
			u.stopped(fmt.Sprintf("the box VM's folder state could not be read (%v); run `culibox box share list`, then culibox init again", err))
			return false
		}
	}
	if state != "applied" {
		u.stopped(fmt.Sprintf("the box VM does not have %s yet (folder state: %s); run `culibox box share apply` (restarts the box VM), then culibox init again", workspace, state))
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

// initApplyShares restarts a set-up VM so it takes its saved folder list, after
// saying so and asking. It reports false (with a stopped line that names a
// command that works) when the user declines or the apply fails: a refusal
// because boxes run, or a VM that is not running, is the apply's own message.
func initApplyShares(u *initUI, cfg config.Config, cfgFiles []string, dataDir, workspace string, d initDeps) bool {
	u.say("the box VM's folder list has changes the VM does not have yet (%s); taking them needs a restart of the box VM, which ends the sessions in it", workspace)
	if !u.ask("Restart the box VM now to apply them?", true) {
		u.stopped(fmt.Sprintf("the box VM does not have %s yet; run `culibox box share apply` (restarts the box VM), then culibox init again", workspace))
		return false
	}
	if rc := d.shareApply(cfg, cfgFiles, dataDir, u.out); rc != 0 {
		u.stopped("`culibox box share apply` did not finish (see the lines above); with boxes running, stop them or use `culibox box share apply -force` (ends them), then culibox init again")
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
// boxed claude token row PASS too (doctor prints it only on macOS). The one
// exception is a token row that is a WARN only because the store is locked
// while the claude-box token is in it (tokenStored, which never decrypts): a
// Linux store's unlock expires, and a locked store is no reason to rebuild. The
// image must be current: macOS reads doctor's "box image" row (present only
// while the VM runs), Linux asks the host's docker (imageCurrent).
func initBoxReadyFrom(goos string, rep doctor.BoxTierReport, tokenStored, imageCurrent func() bool) bool {
	rows := rep.Rows
	if goos != "darwin" {
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

// initShareState is whether the box VM has the workspace: "applied", or the
// state `culibox box share list` ends on ("pending", "not_set_up", ...). It reads
// the saved list against the folders the VM's written colima.yaml has and never
// starts the VM.
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
	v, err := svc.View()
	if err != nil {
		return "", err
	}
	if v.ListError != "" {
		return "", errors.New(v.ListError)
	}
	if v.State != "whole_home" {
		return v.State, nil
	}
	applied, _, _, err := svc.ctl.Applied()
	if err != nil {
		return "", err
	}
	return wholeHomeShareState(workspace, svc.env.Home, applied, svc.env.Fold), nil
}

// wholeHomeShareState settles a VM that shares the whole home folder: it has the
// workspace only when the workspace is under home or inside a folder the VM
// applied read & write. Anything else (say /Volumes/work) is pending.
func wholeHomeShareState(workspace, home string, applied []vmshares.Share, fold bool) string {
	ws := vmshares.Resolve(workspace)
	if vmshares.Within(ws, vmshares.Resolve(home), fold) {
		return "applied"
	}
	for _, a := range applied {
		if a.Mode == vmshares.RW && vmshares.Within(ws, a.Path, fold) {
			return "applied"
		}
	}
	return "pending"
}

// initShareApply is `culibox box share apply`, in-process, without -force: it
// refuses while a box runs. The output is the apply's own step lines.
func initShareApply(cfg config.Config, cfgFiles []string, home string, w io.Writer) int {
	merged, _ := mergeCredentials(cfg, home)
	return runBox(cliArgs{Cmd: "box", Sub: "share", ShareAction: "apply"}, merged, home, cfgFiles, w)
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
	return doctor.Run(w, doctor.Input{Config: merged, Source: src, Home: home, Commit: buildCommit(),
		CredErrors: cred.Problems(), ConfigFiles: cfgFiles, EnvNotice: notice, EnvErr: envErr})
}
