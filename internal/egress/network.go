package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// InstanceLabel marks every docker object agent-box creates for one instance.
const InstanceLabel = "io.culiops.agent-box.instance"

const dockerTimeout = 30 * time.Second

// iccOption is the bridge driver option for inter-container traffic. Absent,
// a bridge network lets its containers reach each other.
const iccOption = "com.docker.network.bridge.enable_icc"

// Docker is the part of the docker CLI the network code needs. args are the
// arguments after "docker"; box.CLIClient satisfies it.
type Docker interface {
	Run(ctx context.Context, args []string) (string, error)
}

// Net is an egress network as docker reports it.
type Net struct {
	Name     string
	Gateway  netip.Addr
	Subnet   netip.Prefix
	Internal bool
	NoICC    bool // docker reports inter-container traffic explicitly off
}

// Want is what the platform needs from the network.
type Want struct {
	Internal bool   // Linux: true
	Subnet   string // darwin: the fixed egress subnet; "" = docker chooses
	NoICC    bool   // Linux: true, boxes on the network must not reach each other
}

// NetworkName is the per-instance network name; every byte outside
// [A-Za-z0-9_.-] becomes '-'.
func NetworkName(instance string) string {
	b := []byte(instance)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			b[i] = '-'
		}
	}
	return "agentbox-egress-" + string(b)
}

func run(ctx context.Context, d Docker, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	return d.Run(ctx, args)
}

type inspected struct {
	Internal bool
	Options  map[string]string
	IPAM     struct {
		Config []struct{ Subnet, Gateway string }
	}
}

// InspectNetwork reports the named network; ok is false when it is absent. A
// network with no IPv4 config is returned with zero Gateway and Subnet; callers
// that need them (EnsureNetwork) check.
func InspectNetwork(ctx context.Context, d Docker, name string) (Net, bool, error) {
	out, err := run(ctx, d, "network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return Net{}, false, err
	}
	found := false
	for _, l := range strings.Split(out, "\n") {
		// The filter is a regexp ('.' matches anything); only the exact name counts.
		if strings.TrimSpace(l) == name {
			found = true
		}
	}
	if !found {
		return Net{}, false, nil
	}
	out, err = run(ctx, d, "network", "inspect", name, "--format", "{{json .}}")
	if err != nil {
		return Net{}, false, err
	}
	var in inspected
	if err := json.Unmarshal([]byte(out), &in); err != nil {
		return Net{}, false, fmt.Errorf("egress: network %s: unreadable inspect output: %w", name, err)
	}
	n := Net{Name: name, Internal: in.Internal}
	// Only an explicit, parsable "false" counts as off.
	if v, ok := in.Options[iccOption]; ok {
		if on, perr := strconv.ParseBool(v); perr == nil && !on {
			n.NoICC = true
		}
	}
	for _, c := range in.IPAM.Config {
		p, perr := netip.ParsePrefix(c.Subnet)
		if perr != nil || !p.Addr().Unmap().Is4() {
			continue
		}
		n.Subnet = p.Masked()
		if g, gerr := netip.ParseAddr(c.Gateway); gerr == nil && g.Unmap().Is4() {
			n.Gateway = g.Unmap()
		}
		break
	}
	return n, true, nil
}

// EnsureNetwork returns the named network, creating it when absent, and refuses
// one that does not match w: an open network, or one on which boxes can reach
// each other, must never be used for boxes.
func EnsureNetwork(ctx context.Context, d Docker, name, instance string, w Want) (Net, error) {
	n, ok, err := InspectNetwork(ctx, d, name)
	if err != nil {
		return Net{}, err
	}
	if !ok {
		args := []string{"network", "create", "--driver", "bridge"}
		if w.Internal {
			args = append(args, "--internal")
		}
		if w.NoICC {
			args = append(args, "-o", iccOption+"=false")
		}
		if w.Subnet != "" {
			args = append(args, "--subnet", w.Subnet)
		}
		args = append(args, "--label", InstanceLabel+"="+instance, name)
		if _, cerr := run(ctx, d, args...); cerr != nil {
			if !strings.Contains(cerr.Error(), "already exists") {
				return Net{}, cerr
			}
			// A concurrent create won; use what it made, through the same checks.
			if n, ok, err = InspectNetwork(ctx, d, name); err != nil {
				return Net{}, err
			}
			if !ok {
				return Net{}, cerr
			}
		} else if n, ok, err = InspectNetwork(ctx, d, name); err != nil {
			return Net{}, err
		} else if !ok {
			return Net{}, fmt.Errorf("egress: network %s is missing right after create", name)
		}
	}
	if n.Internal != w.Internal {
		return Net{}, fmt.Errorf("egress network %s exists with internal=%v; agent-box will not use an open network for boxes. Remove it (docker network rm %s) and retry",
			name, n.Internal, name)
	}
	if w.NoICC && !n.NoICC {
		return Net{}, fmt.Errorf("egress network %s exists with inter-container traffic on (%s); agent-box will not use a network on which boxes can reach each other. Remove it (docker network rm %s) and retry",
			name, iccOption, name)
	}
	if w.Subnet != "" {
		want, perr := netip.ParsePrefix(w.Subnet)
		if perr != nil {
			return Net{}, fmt.Errorf("egress: configured subnet %q: %w", w.Subnet, perr)
		}
		if n.Subnet != want.Masked() {
			return Net{}, fmt.Errorf("egress network %s has subnet %s, want %s. Remove it (docker network rm %s) and retry",
				name, n.Subnet, want.Masked(), name)
		}
	}
	if !n.Subnet.IsValid() || !n.Gateway.IsValid() {
		return Net{}, fmt.Errorf("egress network %s has no IPv4 subnet and gateway", name)
	}
	return n, nil
}
