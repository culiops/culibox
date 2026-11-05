package doctor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/secretchain"
	"github.com/culiops/culibox/internal/secretstore"
)

// ClaudeBoxToken checks that every boxed claude agent in config.json names a
// Claude credential its secret sources resolve. On macOS Claude Code keeps its
// login in the Keychain, which a box cannot read, and agent-box deliberately
// does not copy it: the box gets a token made for it. Values are never printed.
// Only config.json agents are checked; UI-created profiles are not visible here.
//
// st is the encrypted store when a secret source names it (see StoreFor); nil
// otherwise. A locked store counts as "does not resolve", never a pass.
func ClaudeBoxToken(c config.Config, st credref.StoreGetter) Result {
	const check = "boxed claude token"
	res, rerr := secretchain.New(c.SecretSources, st)
	var missing []string
	var locked []string   // agents whose credential failed only because the store is locked
	var unavailable error // the first Keychain failure met for an agent that found no other credential
	var unavailableAgents []string
	boxed := 0
	for _, a := range c.Agents {
		if filepath.Base(a.Bin) != "claude" || c.RuntimeFor(a) != config.RuntimeBox {
			continue
		}
		boxed++
		found, isLocked := false, false
		var unavail error
		for _, name := range a.Secrets {
			if name != "CLAUDE_CODE_OAUTH_TOKEN" && name != "ANTHROPIC_API_KEY" {
				continue
			}
			if rerr == nil {
				_, ok, err := res.Resolve(name)
				switch {
				case err == nil && ok:
					found = true
				case errors.Is(err, credref.ErrLocked):
					isLocked = true
				case errors.Is(err, secretstore.ErrKeeperUnavailable) && unavail == nil:
					unavail = err
				}
			}
		}
		switch {
		case found:
		case unavail != nil:
			// A Keychain failure is neither "locked" nor "missing": the token is
			// stored but unreadable, and the fix is the Keychain's.
			unavailableAgents = append(unavailableAgents, a.Name)
			if unavailable == nil {
				unavailable = unavail
			}
		case isLocked:
			locked = append(locked, a.Name)
		default:
			missing = append(missing, a.Name)
		}
	}
	switch {
	case boxed == 0:
		return Result{Pass, check, "no boxed claude agent in config.json"}
	case len(unavailableAgents) > 0:
		return Result{Fail, check, "agent(s) " + strings.Join(unavailableAgents, ", ") +
			": the Claude token is in the store, but " + unavailable.Error()}
	case len(locked) > 0:
		return Result{Warn, check, "agent(s) " + strings.Join(locked, ", ") +
			`: the Claude token is in the store, which is locked — unlock it ("agent-box store unlock")`}
	case len(missing) > 0:
		return Result{Warn, check, "agent(s) " + strings.Join(missing, ", ") +
			" have no CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY secret that resolves; a boxed claude will ask to /login. " +
			"Run `claude setup-token`, store it as a secret source, and list it in the agent's secrets"}
	}
	return Result{Pass, check, fmt.Sprintf("%d boxed claude agent(s) have a credential", boxed)}
}

// boxFacts is everything doctor learned about the box tier. Gathering never
// starts the VM; interpreting is the pure boxTierResults.
type boxFacts struct {
	DockerErr   error
	ColimaErr   error  // darwin only
	MacOSMajor  int    // darwin only; 0 = unknown
	ColimaVer   string // darwin only; "" = unknown
	SetUp       bool
	Drifted     bool
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
	// firewall rules), so every boxed spawn is refused until the VM is recreated.
	ProvisionDrifted bool
	// FirewallRan: the VM runs and its live rules were asked for. FirewallReadErr:
	// they could not be read. FirewallErr: read, and not the rules the relay
	// needs (boxvm.CheckInputRules, boxvm.CheckDockerUserRules).
	FirewallRan     bool
	FirewallErr     error
	FirewallReadErr error
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
		out = append(out, Result{Pass, ck, "docker found"})
		stopped()
		return out
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
		return []Result{{Warn, ck, "box VM not set up; run `agent-box box setup`"}, userCtx, token}
	case f.StatusErr != nil:
		return []Result{{Fail, ck, "box VM status: " + f.StatusErr.Error()}, userCtx}
	}
	if f.ColimaVer != "" && !boxvm.VersionAtLeast(f.ColimaVer, 0, 8) {
		out = append(out, Result{Warn, "colima", "version " + f.ColimaVer + " is older than the tested 0.8; `brew upgrade colima`"})
	}
	if f.Drifted && f.ProvisionDrifted {
		out = append(out, Result{Fail, "box VM config", "its firewall rules differ from what this version and config write (set up by an older version, or broker_port or egress_port changed); run \"agent-box box setup -recreate\" (every boxed spawn is refused until then)"})
	} else if f.Drifted {
		out = append(out, Result{Warn, "box VM config", "changed since setup (VM size, mounts, extra_ca_certs or the provision script); run `agent-box box setup -recreate`"})
	}
	out = append(out, userCtx)
	if !f.Status.Running {
		out = append(out, Result{Pass, ck, "box VM stopped (starts on the first boxed spawn)"}, token)
		return out
	}
	out = append(out, Result{Pass, ck, fmt.Sprintf("box VM running (%s, %s)", f.Status.Driver, f.Status.MountType)})
	if f.Status.MountType != "virtiofs" {
		out = append(out, Result{Warn, "box VM mounts", f.Status.MountType + " is about 3.5x slower than virtiofs; run `agent-box box setup -recreate`"})
	}
	if f.ImageErr != nil {
		out = append(out, Result{Fail, "box image", f.ImageErr.Error()})
	} else if fatal, msg := f.Image.CheckFor(f.ImageName, f.ServerArch); fatal {
		out = append(out, Result{Fail, "box image", msg})
	} else if msg != "" {
		out = append(out, Result{Warn, "box image", msg})
	} else {
		out = append(out, Result{Pass, "box image", "current, linux/" + f.ServerArch})
	}
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
			out = append(out, Result{Warn, "box VM firewall", "could not read the rules (" + f.FirewallReadErr.Error() + ")"})
		case f.FirewallErr != nil:
			out = append(out, Result{Fail, "box VM firewall", f.FirewallErr.Error()})
		default:
			out = append(out, Result{Pass, "box VM firewall", "the relay's INPUT rules and the gateway DROP are loaded, and no DOCKER-USER rule passes TCP"})
		}
	}
	stopped()
	return append(out, token)
}

// gatherBoxFacts asks the real world, with a 60 s overall budget. It never
// starts the VM: a stopped VM is a normal state, reported as such.
func gatherBoxFacts(c config.Config, dataDir, home, goos string) boxFacts {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var f boxFacts
	bin, err := box.LookDocker(c.DockerBin)
	if err != nil {
		f.DockerErr = err
		return f
	}
	instance := box.LabelInstance + "=" + c.BoxInstance()
	if goos != "darwin" {
		f.Stopped = stoppedBoxes(ctx, box.CLIClient{Bin: bin}, instance)
		return f
	}
	// Needs only the user's docker CLI, so it is checked even when colima is
	// missing: doctor reports it on every darwin path.
	f.UserContext = boxvm.CheckUserDockerContext(ctx, bin)
	colima, err := boxvm.LookColima(c.ColimaBin)
	if err != nil {
		f.ColimaErr = err
		return f
	}
	hostCPUs, hostMem := boxvm.HostSize()
	vm := boxvm.NewVM(boxvm.ConfigFor(c, dataDir, home, colima, bin, hostCPUs, hostMem), boxvm.ExecRunner{})
	vc := vm.Config() // symlink-resolved: the paths the VM really uses
	f.MacOSMajor = boxvm.MacOSMajor(ctx)
	f.ColimaVer = boxvm.ColimaVersion(ctx, vc)
	f.SocketErr = vc.CheckSocketPaths()
	f.SetUp, f.Drifted = vm.SetUp(), vm.Drifted()
	f.ProvisionDrifted = f.Drifted && vm.ProvisionDrifted()
	if !f.SetUp {
		return f
	}
	f.Status, f.StatusErr = vm.Status(ctx)
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
	cli := box.CLIClient{Bin: bin, Args: vc.DockerArgs()}
	img := boximage.Docker{Bin: bin, Args: vc.DockerArgs()}
	if f.ServerArch, err = img.ServerArch(ctx); err != nil {
		f.ImageErr = err
		return f
	}
	image := c.BoxImageOrDefault()
	f.ImageName = image
	f.Image, f.ImageErr = img.Inspect(ctx, image)
	if f.ImageErr == nil && f.Image.Present {
		f.ProbeRan = true
		f.ProbeErr = boxvm.ProbeLoopback(ctx, boxvm.ThrowawayProbe(cli, image))
	}
	f.Stopped = stoppedBoxes(ctx, cli, instance)
	return f
}

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
