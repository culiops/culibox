package doctor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boximagecheck"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/datacompat"
	"github.com/culiops/culibox/internal/secretchain"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/vmshares"
)

// ClaudeBoxToken checks that every boxed claude agent in config.json names a
// Claude credential its secret sources resolve. On macOS Claude Code keeps its
// login in the Keychain, which a box cannot read, and agent-box deliberately
// does not copy it: the box gets a token made for it. Values are never printed.
// Only config.json agents are checked; UI-created profiles are not visible here.
//
// st is the encrypted store when a secret source names it (see StoreFor); nil
// otherwise. A locked store counts as "does not resolve", never a pass.
//
// An agent that names an account (not "default") also counts that account's own
// pasted token (account.OwnsStoreToken), read straight from acctStore, the
// encrypted store opened directly (see AccountStoreFor; nil when there is none),
// the way the launcher does when secret_sources has no store entry. accts is
// accounts.json (storerefs.ReadAccounts).
func ClaudeBoxToken(c config.Config, st credref.StoreGetter, acctStore credref.StoreGetter, accts []account.Account) Result {
	r, _ := claudeBoxToken(c, st, acctStore, accts)
	return r
}

// claudeBoxToken is ClaudeBoxToken; lockedOnly is true when the row is a WARN
// only because the store is locked (no agent is missing a credential or hit a
// Keychain failure).
func claudeBoxToken(c config.Config, st credref.StoreGetter, acctStore credref.StoreGetter, accts []account.Account) (r Result, lockedOnly bool) {
	const check = "boxed claude token"
	res, rerr := secretchain.New(c.SecretSources, st)
	var missing []string
	var locked []string   // agents whose credential failed only because the store is locked
	var unavailable error // the first Keychain failure met for an agent that found no other credential
	var unavailableAgents []string
	// secretAdapter: when the chain cannot be built every name is Absent (the
	// secret path is skipped); otherwise it maps what the chain resolves.
	secretAdapter := func(name string) (secretchain.Presence, error) {
		if rerr != nil {
			return secretchain.Absent, nil
		}
		_, ok, err := res.Resolve(name)
		if err == nil && !ok {
			return secretchain.Absent, nil
		}
		return presenceOf(err)
	}
	boxed := 0
	for _, a := range c.Agents {
		if filepath.Base(a.Bin) != "claude" || c.RuntimeFor(a) != config.RuntimeBox {
			continue
		}
		boxed++
		presence, unavail := secretchain.ClaudeBoxCredential(a, secretAdapter, func(name string) (secretchain.Presence, error) {
			i := slices.IndexFunc(accts, func(x account.Account) bool { return x.Name == name })
			if acctStore == nil || i < 0 || !accts[i].OwnsStoreToken() {
				return secretchain.Absent, nil
			}
			_, err := acctStore.Get(accts[i].TokenSecret)
			return presenceOf(err)
		})
		switch presence {
		case secretchain.Present:
		case secretchain.StoredUnavailable:
			// A Keychain failure is neither "locked" nor "missing": the token is
			// stored but unreadable, and the fix is the Keychain's.
			unavailableAgents = append(unavailableAgents, a.Name)
			if unavailable == nil {
				unavailable = unavail
			}
		case secretchain.StoredLocked:
			locked = append(locked, a.Name)
		default:
			missing = append(missing, a.Name)
		}
	}
	switch {
	case boxed == 0:
		return Result{Pass, check, "no boxed claude agent in config.json"}, false
	case len(unavailableAgents) > 0:
		return Result{Fail, check, "agent(s) " + strings.Join(unavailableAgents, ", ") +
			": the Claude token is in the store, but " + unavailable.Error()}, false
	case len(locked) > 0:
		return Result{Warn, check, "agent(s) " + strings.Join(locked, ", ") +
			`: the Claude token is in the store, which is locked — unlock it ("culibox store unlock")`}, len(missing) == 0
	case len(missing) > 0:
		return Result{Warn, check, "agent(s) " + strings.Join(missing, ", ") +
			" have no Claude token that resolves (account or CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY secret); a boxed claude will ask to /login. " +
			`Run "culibox init", or paste a setup-token under Settings → Agent accounts and set "account": "<name>" on the claude agent`}, false
	}
	return Result{Pass, check, fmt.Sprintf("%d boxed claude agent(s) have a credential", boxed)}, false
}

// presenceOf maps a read error to a Presence: nil is Present, a locked store is
// StoredLocked, an unreadable Keychain is StoredUnavailable (with err), anything
// else Absent.
func presenceOf(err error) (secretchain.Presence, error) {
	switch {
	case err == nil:
		return secretchain.Present, nil
	case errors.Is(err, credref.ErrLocked):
		return secretchain.StoredLocked, nil
	case errors.Is(err, secretstore.ErrKeeperUnavailable):
		return secretchain.StoredUnavailable, err
	}
	return secretchain.Absent, nil
}

// boxFacts is everything doctor learned about the box tier. Gathering never
// starts the VM; interpreting is the pure boxTierResults.
type boxFacts struct {
	DockerErr error
	// DaemonErr: docker was found but its daemon cannot be used (Linux only;
	// box.ErrDockerNotRunning, box.ErrDockerDenied, or unclassified).
	DaemonErr error
	// RuntimeRan: the daemon answered and its runtimes were asked (Linux, or
	// the darwin VM while it runs). Runtime is the isolation runtime new boxes
	// get ("sysbox-runc" or "runc"); RuntimeErr: docker could not list them, so
	// a running CuliBox refuses boxed spawns until it can (#334).
	RuntimeRan bool
	Runtime    string
	RuntimeErr error
	ColimaErr  error  // darwin only
	MacOSMajor int    // darwin only; 0 = unknown
	ColimaVer  string // darwin only; "" = unknown
	SetUp      bool
	Drifted    bool
	// Drift classifies Drifted (boxvm.DriftMountsOnly: only the folder list
	// differs, which an apply fixes without a recreate). WholeHome: the VM still
	// shares the whole home folder. ShareCount: folders in the normalized list.
	// SharesErr: vm-shares.json could not be read.
	Drift      boxvm.Drift
	WholeHome  bool
	ShareCount int
	SharesErr  error
	// FoldersErr: the VM's own folder list (colima.yaml) could not be read.
	FoldersErr  error
	SocketErr   error
	Status      boxvm.Status
	StatusErr   error
	ServerArch  string
	ImageName   string // the configured box_image
	Image       boximage.ImageState
	ImageErr    error
	ProbeErr    error // only gathered when the VM runs; nil = blocked
	ProbeRan    bool
	UserContext error    // boxvm.CheckUserDockerContext result
	Stopped     []string // this instance's boxes that are not running
	// ProvisionDrifted: the drift includes the provision script (the VM's
	// firewall rules).
	ProvisionDrifted bool
	// FirewallRan: the VM runs and its live rules were asked for. FirewallReadErr:
	// they could not be read. FirewallErr: read, and not the rules the relay
	// needs (boxvm.CheckInputRules, boxvm.CheckDockerUserRules).
	FirewallRan     bool
	FirewallErr     error
	FirewallReadErr error
	// MountsRan: the VM runs and its real folders were compared with its
	// colima.yaml (boxvm.VerifyMounts); MountsErr: they differ or could not be read.
	MountsRan bool
	MountsErr error
	// Locks are the in-place guard locks recorded in the data root (#96,
	// darwin only); LocksErr: the records cannot be read. Running are this
	// instance's boxes docker lists, in any state, when RunningKnown (none
	// while the VM is down): the boxes that hold their locks.
	Locks        []box.GuardLock
	LocksErr     error
	Running      []string
	RunningKnown bool
	// Home is $HOME, so a printed shell command can name a path under it as
	// "$HOME"/'rest': doctor's output shows $HOME as "~", which a quoted
	// command would not expand.
	Home string
	// LegacyName is the pre-rename default image, read only when box_image is
	// unset and the default is missing (doctor never tags it: serve adopts it);
	// "" = not read. Legacy is what the daemon holds under it.
	LegacyName string
	Legacy     boximage.ImageState
	// ImageInfo is the running image as Describe read it (nil when the daemon was
	// not asked or could not say); ImageReport is what the update rows show, built
	// from it, the cache and the config (nil: no rows).
	ImageInfo   *boximage.ImageInfo
	ImageReport *boximagecheck.Report
}

// boxTierResults turns gathered facts into doctor lines. Pure, so every row of
// the spec's error table is a unit test.
func boxTierResults(goos string, f boxFacts, token Result) []Result {
	const ck = "boxed tier"
	var out []Result
	stopped := func() {
		if len(f.Stopped) > 0 {
			out = append(out, Result{Warn, "stopped boxes", strings.Join(f.Stopped, ", ") +
				" (not running; the running daemon's 10-minute sweep removes this instance's stopped boxes)"})
		}
	}
	if goos != "darwin" {
		if f.DockerErr != nil {
			return []Result{{Warn, ck, "docker not found: host tier only (" + f.DockerErr.Error() + ")"}}
		}
		if f.DaemonErr != nil {
			// #154: the layer exists, but a boxed spawn is refused. The words match
			// the page's (box.DaemonReason); the remedy is doctor's to add.
			out = append(out, Result{Warn, ck, daemonRow(f.DaemonErr)})
			return append(out, token)
		}
		out = append(out, Result{Pass, ck, "docker found"})
		out = append(out, runtimeRows(f)...)
		out = append(out, imageUpdateRows(f.ImageReport)...)
		stopped()
		// The token does not depend on the tier's state, but a tier that ended on
		// docker not found has no token row, as on macOS.
		return append(out, token)
	}
	if f.DockerErr != nil {
		return []Result{{Fail, ck, "docker CLI not found; install it with `brew install colima docker` (" + f.DockerErr.Error() + ")"}}
	}
	// Whether the user's own docker context is still theirs does not depend on
	// the box VM (review focus 4), so this row is on every return below.
	var userCtx Result
	switch {
	case errors.Is(f.UserContext, boxvm.ErrContextUnchecked):
		// Unknown is not fine: say so rather than let silence read as a pass.
		userCtx = Result{Warn, "docker context", f.UserContext.Error()}
	case f.UserContext != nil:
		userCtx = Result{Fail, "docker context", f.UserContext.Error()}
	default:
		userCtx = Result{Pass, "docker context", "your docker CLI's current context is not the box VM"}
	}
	switch {
	case f.ColimaErr != nil:
		return []Result{{Fail, ck, f.ColimaErr.Error()}, userCtx}
	case f.MacOSMajor != 0 && f.MacOSMajor < 13:
		return []Result{{Fail, ck, fmt.Sprintf("macOS %d: the box VM needs macOS 13+ (vz + virtiofs)", f.MacOSMajor)}, userCtx}
	case f.SocketErr != nil:
		return []Result{{Fail, ck, f.SocketErr.Error()}, userCtx}
	case !f.SetUp:
		return []Result{{Warn, ck, "box VM not set up; run `culibox box setup`"}, userCtx, token}
	case f.StatusErr != nil:
		return []Result{{Fail, ck, "box VM status: " + f.StatusErr.Error()}, userCtx}
	}
	if f.ColimaVer != "" && !boxvm.VersionAtLeast(f.ColimaVer, 0, 8) {
		out = append(out, Result{Warn, "colima", "version " + f.ColimaVer + " is older than the tested 0.8; `brew upgrade colima`"})
	}
	if f.Drift == boxvm.DriftMountsOnly {
		// The folder list alone changed: "box VM folders" below says how to apply
		// it, and a recreate is not needed.
	} else if f.Drifted && f.ProvisionDrifted {
		out = append(out, Result{Fail, "box VM config", "its firewall rules differ from what this version and config write (set up by an older version, or broker_port or egress_port changed); run \"culibox box setup -recreate\""})
	} else if f.Drifted {
		out = append(out, Result{Warn, "box VM config", "changed since setup (VM size, mounts, extra_ca_certs or the provision script); run `culibox box setup -recreate`"})
	}
	out = append(out, userCtx)
	out = append(out, folderRows(f)...)
	if !f.Status.Running {
		out = append(out, Result{Pass, ck, "box VM stopped (starts on the first boxed spawn)"}, token)
		return out
	}
	out = append(out, Result{Pass, ck, fmt.Sprintf("box VM running (%s, %s)", f.Status.Driver, f.Status.MountType)})
	out = append(out, runtimeRows(f)...)
	if f.Status.MountType != "virtiofs" {
		out = append(out, Result{Warn, "box VM mounts", f.Status.MountType + " is about 3.5x slower than virtiofs; run `culibox box setup -recreate`"})
	}
	if f.ImageErr == nil && !f.Image.Present && f.LegacyName != "" && f.Legacy.Present {
		// An upgraded install: the next serve (or box setup) tags the old default
		// as the new one. Said as it is, not as a missing image.
		detail := "present as " + f.LegacyName + "; becomes " + f.ImageName + " on the next serve"
		switch fatal, msg := f.Legacy.CheckFor(f.LegacyName, f.ServerArch); {
		case fatal:
			out = append(out, Result{Fail, "box image", detail + ", but " + msg})
		case msg != "":
			out = append(out, Result{Warn, "box image", detail + "; " + msg})
		default:
			out = append(out, Result{Pass, "box image", detail})
		}
	} else if f.ImageErr != nil {
		out = append(out, Result{Fail, "box image", f.ImageErr.Error()})
	} else if fatal, msg := f.Image.CheckFor(f.ImageName, f.ServerArch); fatal {
		out = append(out, Result{Fail, "box image", msg})
	} else if msg != "" {
		out = append(out, Result{Warn, "box image", msg})
	} else {
		out = append(out, Result{Pass, "box image", "current, linux/" + f.ServerArch})
	}
	out = append(out, imageUpdateRows(f.ImageReport)...)
	if f.ProbeRan {
		if f.ProbeErr != nil {
			out = append(out, Result{Fail, "containment", f.ProbeErr.Error()})
		} else {
			out = append(out, Result{Pass, "containment", "a box cannot reach the Mac's loopback services"})
		}
	}
	if f.FirewallRan {
		switch {
		case f.FirewallReadErr != nil:
			// Relay.Ensure refuses every spawn while the rules cannot be read.
			out = append(out, Result{Fail, "box VM firewall", "could not read the rules (" + f.FirewallReadErr.Error() + "); every boxed spawn is refused until they can be read"})
		case f.FirewallErr != nil:
			out = append(out, Result{Fail, "box VM firewall", f.FirewallErr.Error()})
		default:
			out = append(out, Result{Pass, "box VM firewall", "the relay's INPUT rules and the gateway DROP are loaded, and no DOCKER-USER rule passes TCP"})
		}
	}
	stopped()
	return append(out, token)
}

// runtimeRows is the isolation runtime Docker offers new boxes, as asked now
// (#334): doctor never reads a value a running CuliBox cached. None when the
// runtimes were not asked (daemon down, VM stopped): the rows above say why.
func runtimeRows(f boxFacts) []Result {
	const ck = "isolation runtime"
	switch {
	case !f.RuntimeRan:
		return nil
	case f.RuntimeErr != nil:
		msg, _, _ := strings.Cut(f.RuntimeErr.Error(), "\n")
		return []Result{{Warn, ck, "could not read Docker's runtimes (" + msg + "): boxed sessions are refused until it can; run `docker info`"}}
	case f.Runtime == box.RuntimeSysbox:
		return []Result{{Pass, ck, box.RuntimeSysbox + ": new boxes run under sysbox (a CuliBox started before sysbox was installed keeps runc until it restarts)"}}
	}
	return []Result{{Pass, ck, box.RuntimeRunc + " (Docker's default; sysbox is not installed)"}}
}

// readRuntime asks the daemon which isolation runtime new boxes get, bounded
// like the daemon probe.
func readRuntime(ctx context.Context, cli box.CLIClient) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, box.ProbeTimeout)
	defer cancel()
	ok, err := cli.HasRuntime(pctx, box.RuntimeSysbox)
	if err != nil {
		return "", err
	}
	if ok {
		return box.RuntimeSysbox, nil
	}
	return box.RuntimeRunc, nil
}

// daemonRow words a Linux docker whose daemon cannot be used: the page's
// sentence first, then what the user can do about it. Doctor prints on the
// user's own terminal, so the unclassified case may say what docker answered.
func daemonRow(err error) string {
	const lead = "docker found, but "
	const effect = ": boxed sessions cannot start"
	switch {
	case errors.Is(err, box.ErrDockerNotRunning):
		return lead + box.DaemonReason(err) + effect + " until it is (start the Docker service, for example `sudo systemctl start docker`)"
	case errors.Is(err, box.ErrDockerDenied):
		return lead + box.DaemonReason(err) + effect + " (add your user to the docker group and sign in again, or use rootless Docker)"
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return lead + "the Docker daemon did not answer" + effect + "; run `docker info` to see why (" + msg + ")"
}

// folderRows reports the box VM's folder list (darwin only; the caller gates it).
// One "box VM folders" row: a whole-home share wins, then a changed list, then
// a pass. A vm-shares.json that cannot be read is its own row, so it is never
// hidden behind the whole-home warning.
func folderRows(f boxFacts) []Result {
	const ck = "box VM folders"
	var out []Result
	switch {
	case f.MountsRan && f.MountsErr != nil:
		// Every boxed spawn is refused until this is fixed: it outranks the rest.
		out = append(out, Result{Fail, ck, "the box VM's folders do not match its folder list (" + f.MountsErr.Error() + "); run `culibox box share apply`"})
	case f.WholeHome && f.SharesErr != nil:
		// Applying would drop the user's folders, so no apply advice.
		out = append(out, Result{Warn, ck, "the box VM shares your whole home folder; fix vm-shares.json before applying a folder list"})
	case f.WholeHome:
		out = append(out, Result{Warn, ck, vmshares.WholeHomeLine(f.ShareCount)})
	case f.FoldersErr != nil:
		out = append(out, Result{Warn, ck, "cannot read the box VM's folder list (" + f.FoldersErr.Error() + "); run `culibox box setup`"})
	case f.SharesErr != nil:
		// No row here: the list row below carries the warning, and "0 folders"
		// or apply advice would be wrong for a list we cannot read.
	case f.Drift == boxvm.DriftMountsOnly:
		out = append(out, Result{Warn, ck, "the folder list changed since the box VM started; apply it in Settings › VM folders (or `culibox box share apply`)"})
	default:
		out = append(out, Result{Pass, ck, fmt.Sprintf("the box VM sees %d folders and CuliBox data", f.ShareCount)})
	}
	if f.SharesErr != nil {
		out = append(out, Result{Warn, ck + " list", "vm-shares.json cannot be read (" + f.SharesErr.Error() + "); fix or move it away; until then the folder list cannot change"})
	}
	return out
}

// gatherBoxFacts is gatherFacts without the running image's description, which
// only the doctor report shows.
func gatherBoxFacts(c config.Config, dataDir, home, goos string, protected []box.ProtectedDir) boxFacts {
	return gatherFacts(c, dataDir, home, goos, protected, false)
}

// gatherFacts asks the real world, with a 60 s overall budget. It never
// starts the VM: a stopped VM is a normal state, reported as such.
// protected is the directory list no VM share may overlap (protectedFor).
// describe also reads the running image's labels and layers (Linux: once the
// daemon answered; macOS: once the VM runs and its image was read), for the
// image update rows.
func gatherFacts(c config.Config, dataDir, home, goos string, protected []box.ProtectedDir, describe bool) boxFacts {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := boxFacts{Home: home}
	bin, err := box.LookDocker(c.DockerBin)
	if err != nil {
		f.DockerErr = err
		return f
	}
	instance := box.LabelInstance + "=" + c.BoxInstance()
	if goos != "darwin" {
		// Linux (#154): ask the daemon before listing its boxes. Bounded: a daemon
		// that never answers must not use up the 60 s budget. macOS is not asked:
		// doctor never starts the box VM, and a stopped VM is a normal state.
		cli := box.CLIClient{Bin: bin}
		pctx, pcancel := context.WithTimeout(ctx, box.ProbeTimeout)
		f.DaemonErr = cli.Ping(pctx)
		pcancel()
		if f.DaemonErr == nil {
			f.RuntimeRan = true
			f.Runtime, f.RuntimeErr = readRuntime(ctx, cli)
			f.Stopped = stoppedBoxes(ctx, cli, instance)
			if describe {
				f.ServerArch, f.ImageInfo = describeImage(ctx, boximage.Docker{Bin: bin}, c.BoxImageOrDefault())
			}
		}
		return f
	}
	// Needs only the user's docker CLI, so it is checked even when colima is
	// missing: doctor reports it on every darwin path.
	f.UserContext = boxvm.CheckUserDockerContext(ctx, bin)
	f.Locks, f.LocksErr = box.ReadGuardLocks(dataDir)
	colima, err := boxvm.LookColima(c.ColimaBin)
	if err != nil {
		f.ColimaErr = err
		return f
	}
	hostCPUs, hostMem := boxvm.HostSize()
	cfg := boxvm.ConfigFor(c, dataDir, home, colima, bin, hostCPUs, hostMem)
	cfg.Fold = goos == "darwin"
	vm := boxvm.NewVM(cfg, boxvm.ExecRunner{})
	vc := vm.Config() // symlink-resolved: the paths the VM really uses
	f.MacOSMajor = boxvm.MacOSMajor(ctx)
	f.ColimaVer = boxvm.ColimaVersion(ctx, vc)
	f.SocketErr = vc.CheckSocketPaths()
	f.SetUp = vm.SetUp()
	if !f.SetUp {
		f.RunningKnown = true // no VM: no box runs
		return f
	}
	// The folder list must be on the VM before any drift is computed, or every
	// VM with an applied list reads as drifted.
	list, _, lerr := vmshares.Load(dataDir)
	f.SharesErr = lerr
	if lerr == nil {
		list, _ = vmshares.Normalize(list, vmshares.Env{Home: vc.Home, DataRoot: vc.Root, Fold: vc.Fold, Protected: protected})
		vm.SetShares(list)
		f.ShareCount = len(list)
	}
	f.Drifted = vm.Drifted()
	f.ProvisionDrifted = f.Drifted && vm.ProvisionDrifted()
	f.WholeHome = false
	if wh, err := vm.WholeHome(); err != nil {
		f.FoldersErr = err
	} else {
		f.WholeHome = wh
	}
	if d, err := vm.DriftKind(); err != nil {
		f.FoldersErr = err
	} else {
		f.Drift = d
	}
	f.Status, f.StatusErr = vm.Status(ctx)
	if noBoxRuns(f.Status, f.StatusErr) {
		f.RunningKnown = true // the VM is down: no box runs
	}
	if f.StatusErr != nil || !f.Status.Running {
		return f
	}
	// The same live check the relay runs before every start (relay.go Ensure).
	f.FirewallRan = true
	if rules, err := vm.FilterRules(ctx); err != nil {
		f.FirewallReadErr = err
	} else if f.FirewallErr = boxvm.CheckInputRules(rules, vc.EgressPort, vc.BrokerPort); f.FirewallErr == nil {
		f.FirewallErr = boxvm.CheckDockerUserRules(rules)
	}
	// The same check a spawn runs before it trusts the VM's folders.
	f.MountsRan = true
	f.MountsErr = vm.VerifyMounts(ctx)
	cli := box.CLIClient{Bin: bin, Args: vc.DockerArgs()}
	f.Running, f.RunningKnown = runningBoxes(ctx, cli, instance)
	// The VM's own daemon, as the boxes see it (#334: the same check as Linux).
	f.RuntimeRan = true
	f.Runtime, f.RuntimeErr = readRuntime(ctx, cli)
	img := boximage.Docker{Bin: bin, Args: vc.DockerArgs()}
	if f.ServerArch, err = img.ServerArch(ctx); err != nil {
		f.ImageErr = err
		return f
	}
	image := c.BoxImageOrDefault()
	f.ImageName = image
	f.Image, f.ImageErr = img.Inspect(ctx, image)
	if f.ImageErr == nil && !f.Image.Present && c.BoxImage == "" {
		// Read-only: look, never tag. A failed look leaves the plain "not set up".
		if old, err := img.Inspect(ctx, config.LegacyDefaultBoxImage); err == nil {
			f.LegacyName, f.Legacy = config.LegacyDefaultBoxImage, old
		}
	}
	if describe && f.ImageErr == nil {
		_, f.ImageInfo = describeImage(ctx, img, image)
	}
	if f.ImageErr == nil && f.Image.Present {
		f.ProbeRan = true
		f.ProbeErr = boxvm.ProbeLoopback(ctx, boxvm.ThrowawayProbe(cli, image))
	}
	f.Stopped = stoppedBoxes(ctx, cli, instance)
	return f
}

// describeImage asks the daemon for its arch and the image's facts, bounded like
// the daemon probe. Any failure leaves the facts unknown (nil): the update rows
// are then left out, and the rows above say what is wrong with docker.
func describeImage(ctx context.Context, d boximage.Docker, image string) (string, *boximage.ImageInfo) {
	dctx, cancel := context.WithTimeout(ctx, box.ProbeTimeout)
	defer cancel()
	arch, err := d.ServerArch(dctx)
	if err != nil {
		return "", nil
	}
	info, err := d.Describe(dctx, image)
	if err != nil {
		return arch, nil
	}
	return arch, &info
}

// imageCheckTimeout bounds the live registry check the CLI doctor runs.
const imageCheckTimeout = 15 * time.Second

// imageChecker is the anonymous check for the CLI doctor. A variable so a test can
// hand in a client that fails the test when it is used.
var imageChecker = func() boximagecheck.Checker { return boximagecheck.NewChecker(imageCheckTimeout) }

// imageReport compares the running image (f.ImageInfo) with the image for this
// version. Only the CLI doctor (in.LiveImageCheck) may ask the network, and then
// only when the cache is due or holds a failure, since a user runs doctor to
// confirm a fix; everyone else reads the cache the server keeps. The answer is
// used even when it could not be saved (the data dir may not exist yet). nil when
// the daemon gave no facts.
func imageReport(in Input, dataDir string, f boxFacts) *boximagecheck.Report {
	if f.ImageInfo == nil {
		return nil
	}
	c := in.Config
	ii := boximagecheck.Inputs{
		Configured: c.BoxImageOrDefault(),
		Explicit:   c.BoxImage != "",
		Published:  boximage.Published(),
		CheckOn:    c.ImageCheckOn(),
		Arch:       f.ServerArch,
		Info:       f.ImageInfo,
	}
	if in.LiveImageCheck {
		ctx, cancel := context.WithTimeout(context.Background(), imageCheckTimeout)
		defer cancel()
		ii.Cache, _ = boximagecheck.RunIfDue(ctx, imageChecker(), dataDir, ii, true)
	} else if cache, err := boximagecheck.LoadCache(dataDir); err == nil {
		ii.Cache = cache
	}
	r := boximagecheck.Evaluate(ii)
	return &r
}

// imageUpdateRows says whether the image that runs is the one CI published for
// this version, and the weekly scan's count when it is known. Pure: every value
// comes from the validated report (doctor prints on a terminal), never from raw
// error text. No row for an image the daemon lacks (the image row says so) or
// facts it could not give.
func imageUpdateRows(r *boximagecheck.Report) []Result {
	if r == nil {
		return nil
	}
	const ck, scanCk = "box image update", "box image scan"
	var out []Result
	switch r.Verdict {
	case boximagecheck.Current, boximagecheck.CurrentLocal:
		lead := "current: "
		if r.Verdict == boximagecheck.CurrentLocal {
			lead = "current (built here from this version's Dockerfile): "
		}
		detail := lead + r.Configured
		if r.Info != nil {
			if r.Info.ClaudeCode != "" {
				detail += ", Claude Code " + r.Info.ClaudeCode
			}
			if !r.Info.Created.IsZero() {
				detail += ", built " + r.Info.Created.UTC().Format(time.DateOnly)
			}
		}
		out = append(out, Result{Pass, ck, detail})
	case boximagecheck.Update:
		detail := "an image for this version exists: " + r.Want
		var facts []string
		if r.WantClaudeCode != "" {
			facts = append(facts, "Claude Code "+r.WantClaudeCode)
		}
		if !r.WantCreated.IsZero() {
			facts = append(facts, "built "+r.WantCreated.UTC().Format(time.DateOnly))
		}
		if len(facts) > 0 {
			detail += " (" + strings.Join(facts, ", ") + ")"
		}
		detail += "; "
		if r.DownloadMB != "" {
			detail += "about " + r.DownloadMB + " MB to download; "
		}
		if r.ConfigLine != "" {
			detail += "set " + r.ConfigLine + " in the config, then "
		}
		out = append(out, Result{Warn, ck, detail + "run `" + r.SetupCmd + "`"})
	case boximagecheck.StaleUnknown:
		why := r.CheckErr
		switch {
		case !r.CheckOn:
			why = "check off: image_check is false"
		case why == "":
			why = "not checked yet"
		}
		out = append(out, Result{Warn, ck, "built from an older Dockerfile than this CuliBox; whether CI published this version's image is unknown (" + why + ")"})
	}
	if s := r.Scan; s != nil {
		if s.Count > 0 {
			out = append(out, Result{Warn, scanCk, fmt.Sprintf("%d fixable HIGH/CRITICAL in the published images (https://github.com/culiops/culibox/issues/%d)", s.Count, s.Number)})
		} else {
			out = append(out, Result{Pass, scanCk, "no fixable HIGH/CRITICAL in the published images"})
		}
	}
	return out
}

// noBoxRuns reports whether colima itself said the VM is not running, so no box
// can run. Any other failed status leaves it unknown.
func noBoxRuns(st boxvm.Status, err error) bool { return err == nil && st.Stopped }

// runningBoxes names this instance's boxes that docker still lists, in any
// state; ok is false when the list cannot be read. They are the boxes that
// hold their guard locks: the lock sweep keeps the locks of a listed box
// whatever its state (a paused or exited box may run again), so doctor must
// not call those leftovers.
func runningBoxes(ctx context.Context, c box.CLIClient, label string) ([]string, bool) {
	bs, err := c.ListBoxes(ctx, label)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out, true
}

// guardLockRows reports the in-place guard locks (#96, darwin only): files
// CuliBox keeps locked on the Mac while a box runs, and locks left behind by a
// box that is gone. No locks, no row.
func guardLockRows(goos string, f boxFacts) []Result {
	const ck = "in-place locks"
	if goos != "darwin" {
		return nil
	}
	var out []Result
	if errors.Is(f.LocksErr, datacompat.ErrNewer) {
		// Left in place, not moved aside: the records belong to the newer CuliBox,
		// which unlocks them; this build must not act on them.
		out = append(out, Result{Warn, ck, "the lock records are from a newer CuliBox and are left as they are, so every in-place box " +
			"that needs a lock is refused (" + f.LocksErr.Error() + ")"})
	} else if f.LocksErr != nil {
		out = append(out, Result{Warn, ck, "the lock records cannot be read (" + f.LocksErr.Error() + "); a guarded file CuliBox " +
			"locked may stay locked: check with `ls -lO` and unlock it with `chflags nouchg <file>`"})
	}
	type group struct {
		ws, class string
		files     []string
		sessions  []string
	}
	var groups []*group
	find := func(ws, class string) *group {
		for _, g := range groups {
			if g.ws == ws && g.class == class {
				return g
			}
		}
		g := &group{ws: ws, class: class}
		groups = append(groups, g)
		return g
	}
	notOurs := map[string][]string{} // workspace -> files held by a running box of this instance
	var notOursWS []string
	for _, l := range f.Locks {
		if !l.Ours {
			// Not CuliBox's to lift. But while a box of this instance holds the
			// file, say so: the flag may belong to another instance (docs/MACOS.md).
			if f.RunningKnown && slices.ContainsFunc(l.Boxes, func(b string) bool { return slices.Contains(f.Running, b) }) {
				if _, ok := notOurs[l.Workspace]; !ok {
					notOursWS = append(notOursWS, l.Workspace)
				}
				notOurs[l.Workspace] = append(notOurs[l.Workspace], l.File)
			}
			continue
		}
		class := "leftover"
		switch {
		case !f.RunningKnown:
			class = "unknown"
		case slices.ContainsFunc(l.Boxes, func(b string) bool { return slices.Contains(f.Running, b) }):
			class = "current"
		}
		g := find(l.Workspace, class)
		g.files = append(g.files, l.File)
		for _, s := range l.Sessions {
			if !slices.Contains(g.sessions, s.ID) {
				g.sessions = append(g.sessions, s.ID)
			}
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].ws < groups[j].ws })
	for _, g := range groups {
		sort.Strings(g.files)
		sort.Strings(g.sessions)
		files := strings.Join(g.files, ", ")
		switch g.class {
		case "current":
			who := "a box spawned in place runs"
			if len(g.sessions) > 0 {
				who = "its session runs (" + strings.Join(g.sessions, ", ") + ")"
			}
			out = append(out, Result{Pass, ck, g.ws + ": " + files + " locked on your Mac while " + who +
				"; macOS cannot otherwise stop the box changing them through another letter case"})
		case "unknown":
			out = append(out, Result{Warn, ck, g.ws + ": " + files + " locked by CuliBox; whether the box holding the lock " +
				"still runs cannot be checked"})
		default:
			var paths []string
			for _, n := range g.files {
				paths = append(paths, shPath(f.Home, filepath.Join(g.ws, n)))
			}
			out = append(out, Result{Warn, ck, g.ws + ": " + files + " still locked, but no box holding the lock runs; " +
				"CuliBox lifts it when it next starts (or the box VM does), or unlock it now: chflags nouchg " + strings.Join(paths, " ")})
		}
	}
	sort.Strings(notOursWS)
	for _, ws := range notOursWS {
		names := notOurs[ws]
		sort.Strings(names)
		verb := "was"
		if len(names) > 1 {
			verb = "were"
		}
		out = append(out, Result{Warn, ck, ws + ": " + strings.Join(names, ", ") + " " + verb +
			" already locked when CuliBox came to lock it (by you, or by another CuliBox instance on this Mac); " +
			"this instance will not unlock it. If another CuliBox instance is running boxes in this folder, " +
			"ending its box unlocks the file while this one still runs (see docs/MACOS.md)"})
	}
	return out
}

// shPath quotes an absolute path for a pasted command. Under home it is
// "$HOME"/'rest': doctor shows $HOME as "~" in its output, and '~/x' inside
// quotes would not expand. Anything else is single-quoted as it is.
func shPath(home, p string) string {
	if home != "" && home != "/" {
		h := filepath.Clean(home)
		if rest, ok := strings.CutPrefix(p, h+"/"); ok {
			return `"$HOME"/` + shQuote(rest)
		}
	}
	return shQuote(p)
}

// shQuote single-quotes s for a POSIX shell (a quote in s closes the quotes,
// is escaped and reopens them), so a printed line is safe to paste whatever
// the folder is called.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// stoppedBoxes names this instance's boxes that are not running (label is
// "key=value"). A listing that fails is "unknown", not "none": it reports nothing.
func stoppedBoxes(ctx context.Context, c box.CLIClient, label string) []string {
	bs, err := c.ListBoxes(ctx, label)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range bs {
		if b.State != "running" {
			out = append(out, b.Name)
		}
	}
	return out
}
