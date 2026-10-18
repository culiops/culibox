package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// fakeDocker records argv and answers ls/inspect/create from a tiny in-memory table.
type fakeDocker struct {
	mu        sync.Mutex
	nets      map[string]string // name → inspect JSON
	calls     [][]string
	createErr error
	// onCreate, when set, runs instead of storing the network (a concurrent create).
	onCreate func(f *fakeDocker, name string)
	nextGW   string            // gateway handed to created networks; "" → 172.21.0.1
	boxes    map[string]string // container name → its address on any network inspected
}

func newFakeDocker() *fakeDocker { return &fakeDocker{nets: map[string]string{}} }

func inspectJSON(name string, internal bool, configs ...[2]string) string {
	return inspectJSONOpts(name, internal, nil, configs...)
}

// inspectJSONOpts is inspectJSON with the network's driver options (nil: none).
func inspectJSONOpts(name string, internal bool, opts map[string]string, configs ...[2]string) string {
	type cfg struct{ Subnet, Gateway string }
	var cs []cfg
	for _, c := range configs {
		cs = append(cs, cfg{c[0], c[1]})
	}
	b, _ := json.Marshal(map[string]any{"Name": name, "Internal": internal, "Options": opts, "IPAM": map[string]any{"Config": cs}})
	return string(b)
}

func (f *fakeDocker) Run(ctx context.Context, args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) == 6 && args[0] == "inspect" && args[1] == "--type" && args[2] == "container" {
		ip, ok := f.boxes[args[5]]
		if !ok {
			return "", fmt.Errorf("Error: No such container: %s", args[5])
		}
		return `{"agentbox-egress-DEV":{"IPAddress":"` + ip + `"}}`, nil
	}
	if len(args) < 3 || args[0] != "network" {
		return "", fmt.Errorf("fake docker: unexpected %v", args)
	}
	switch args[1] {
	case "ls":
		for _, a := range args {
			if strings.HasPrefix(a, "name=^") {
				name := strings.TrimSuffix(strings.TrimPrefix(a, "name=^"), "$")
				if _, ok := f.nets[name]; ok {
					return name, nil
				}
			}
		}
		return "", nil
	case "inspect":
		j, ok := f.nets[args[2]]
		if !ok {
			return "", fmt.Errorf("docker network: No such network: %s", args[2])
		}
		return j, nil
	case "create":
		name := args[len(args)-1]
		if f.onCreate != nil {
			f.onCreate(f, name)
		}
		if f.createErr != nil {
			return "", f.createErr
		}
		internal, subnet := false, "172.21.0.0/16"
		opts := map[string]string{}
		gw := f.nextGW
		if gw == "" {
			gw = "172.21.0.1"
		}
		for i, a := range args {
			if a == "--internal" {
				internal = true
			}
			if a == "-o" {
				k, v, _ := strings.Cut(args[i+1], "=")
				opts[k] = v
			}
			if a == "--subnet" {
				subnet = args[i+1]
				p := netip.MustParsePrefix(subnet)
				gw = p.Addr().Next().String()
			}
		}
		f.nets[name] = inspectJSONOpts(name, internal, opts, [2]string{subnet, gw})
		return "id123", nil
	}
	return "", fmt.Errorf("fake docker: unexpected %v", args)
}

func (f *fakeDocker) createCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 1 && c[1] == "create" {
			out = append(out, c)
		}
	}
	return out
}

func has(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		ok := true
		for j := range want {
			if args[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestNetworkName(t *testing.T) {
	if NetworkName("DEV") != "agentbox-egress-DEV" || NetworkName("M3C IT/x") != "agentbox-egress-M3C-IT-x" {
		t.Fatal(NetworkName("M3C IT/x"))
	}
	if got := NetworkName("a.b_c-d"); got != "agentbox-egress-a.b_c-d" {
		t.Fatal(got)
	}
	if got := NetworkName("é\n;rm"); strings.ContainsAny(got, "\né;") {
		t.Fatal(got)
	}
}

func TestEnsureCreatesInternalWithInstanceLabel(t *testing.T) {
	f := newFakeDocker()
	n, err := EnsureNetwork(context.Background(), f, "agentbox-egress-DEV", "DEV", Want{Internal: true})
	if err != nil {
		t.Fatal(err)
	}
	cs := f.createCalls()
	if len(cs) != 1 {
		t.Fatalf("creates: %v", cs)
	}
	c := cs[0]
	if !has(c, "--internal") || !has(c, "--label", "io.culiops.agent-box.instance=DEV") || !has(c, "--driver", "bridge") || c[len(c)-1] != "agentbox-egress-DEV" {
		t.Fatalf("argv: %v", c)
	}
	if has(c, "--subnet") {
		t.Fatalf("linux must let docker choose the subnet: %v", c)
	}
	if !n.Internal || n.Gateway != netip.MustParseAddr("172.21.0.1") || n.Subnet != netip.MustParsePrefix("172.21.0.0/16") || n.Name != "agentbox-egress-DEV" {
		t.Fatalf("net: %+v", n)
	}
	// A second ensure finds it and does not create again.
	if _, err := EnsureNetwork(context.Background(), f, "agentbox-egress-DEV", "DEV", Want{Internal: true}); err != nil || len(f.createCalls()) != 1 {
		t.Fatalf("second ensure: err=%v creates=%d", err, len(f.createCalls()))
	}
}

func TestEnsureRefusesExistingOpenNetworkOnLinux(t *testing.T) {
	f := newFakeDocker()
	f.nets["agentbox-egress-DEV"] = inspectJSON("agentbox-egress-DEV", false, [2]string{"172.21.0.0/16", "172.21.0.1"})
	_, err := EnsureNetwork(context.Background(), f, "agentbox-egress-DEV", "DEV", Want{Internal: true})
	if err == nil || !strings.Contains(err.Error(), "docker network rm agentbox-egress-DEV") || !strings.Contains(err.Error(), "internal=false") {
		t.Fatalf("err: %v", err)
	}
	if len(f.createCalls()) != 0 {
		t.Fatal("must not create over an existing network")
	}
}

func TestEnsureRefusesExistingInternalNetworkWhenOpenWanted(t *testing.T) {
	f := newFakeDocker()
	f.nets["n"] = inspectJSON("n", true, [2]string{"172.29.72.0/24", "172.29.72.1"})
	if _, err := EnsureNetwork(context.Background(), f, "n", "DEV", Want{Subnet: "172.29.72.0/24"}); err == nil {
		t.Fatal("an internal network cannot serve darwin's non-internal design")
	}
}

func TestEnsureDarwinWantsNonInternalFixedSubnet(t *testing.T) {
	f := newFakeDocker()
	n, err := EnsureNetwork(context.Background(), f, "agentbox-egress-DEV", "DEV", Want{Subnet: "172.29.72.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	c := f.createCalls()[0]
	if !has(c, "--subnet", "172.29.72.0/24") || has(c, "--internal") {
		t.Fatalf("argv: %v", c)
	}
	if n.Internal || n.Subnet != netip.MustParsePrefix("172.29.72.0/24") || n.Gateway != netip.MustParseAddr("172.29.72.1") {
		t.Fatalf("net: %+v", n)
	}
	// An existing network on another subnet is refused.
	f2 := newFakeDocker()
	f2.nets["agentbox-egress-DEV"] = inspectJSON("agentbox-egress-DEV", false, [2]string{"172.30.0.0/16", "172.30.0.1"})
	_, err = EnsureNetwork(context.Background(), f2, "agentbox-egress-DEV", "DEV", Want{Subnet: "172.29.72.0/24"})
	if err == nil || !strings.Contains(err.Error(), "172.29.72.0/24") {
		t.Fatalf("err: %v", err)
	}
}

func TestEnsureRaceAlreadyExistsReinspects(t *testing.T) {
	f := newFakeDocker()
	f.createErr = fmt.Errorf("docker network: Error response from daemon: network with name x already exists")
	f.onCreate = func(f *fakeDocker, name string) {
		f.nets[name] = inspectJSON(name, true, [2]string{"172.22.0.0/16", "172.22.0.1"})
	}
	n, err := EnsureNetwork(context.Background(), f, "x", "DEV", Want{Internal: true})
	if err != nil {
		t.Fatal(err)
	}
	if n.Gateway != netip.MustParseAddr("172.22.0.1") {
		t.Fatalf("net: %+v", n)
	}
	// A create failure that is not a race is returned, never swallowed.
	g := newFakeDocker()
	g.createErr = fmt.Errorf("docker network: boom")
	if _, err := EnsureNetwork(context.Background(), g, "x", "DEV", Want{Internal: true}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err: %v", err)
	}
	// "already exists" but still absent afterwards: error, no loop.
	h := newFakeDocker()
	h.createErr = fmt.Errorf("already exists")
	if _, err := EnsureNetwork(context.Background(), h, "x", "DEV", Want{Internal: true}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestEnsureRefusesNetworkWithoutIPv4GatewayOrSubnet(t *testing.T) {
	for name, j := range map[string]string{
		"no config":  inspectJSON("x", true),
		"no gateway": inspectJSON("x", true, [2]string{"172.21.0.0/16", ""}),
		"no subnet":  inspectJSON("x", true, [2]string{"", "172.21.0.1"}),
		"v6 only":    inspectJSON("x", true, [2]string{"fd00::/64", "fd00::1"}),
	} {
		f := newFakeDocker()
		f.nets["x"] = j
		if _, err := EnsureNetwork(context.Background(), f, "x", "DEV", Want{Internal: true}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInspectPicksIPv4Config(t *testing.T) {
	f := newFakeDocker()
	f.nets["x"] = inspectJSON("x", true, [2]string{"fd00::/64", "fd00::1"}, [2]string{"172.21.0.0/16", "172.21.0.1"})
	n, ok, err := InspectNetwork(context.Background(), f, "x")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if n.Gateway != netip.MustParseAddr("172.21.0.1") || n.Subnet != netip.MustParsePrefix("172.21.0.0/16") || !n.Internal {
		t.Fatalf("net: %+v", n)
	}
}

func TestInspectAbsent(t *testing.T) {
	f := newFakeDocker()
	_, ok, err := InspectNetwork(context.Background(), f, "x")
	if err != nil || ok {
		t.Fatal(ok, err)
	}
	// The listing must be an exact-name filter, not a substring one.
	if c := f.calls[0]; !has(c, "--filter", "name=^x$") || !has(c, "--format", "{{.Name}}") {
		t.Fatalf("argv: %v", c)
	}
}

func TestInspectIgnoresOtherNamesFromLs(t *testing.T) {
	// A regex-ish filter can return siblings; only an exact name counts.
	d := dockerFunc(func(ctx context.Context, args []string) (string, error) {
		if args[1] == "ls" {
			return "xy\nx.z", nil
		}
		return "", fmt.Errorf("must not inspect")
	})
	if _, ok, err := InspectNetwork(context.Background(), d, "x"); err != nil || ok {
		t.Fatal(ok, err)
	}
}

type dockerFunc func(ctx context.Context, args []string) (string, error)

func (f dockerFunc) Run(ctx context.Context, args []string) (string, error) { return f(ctx, args) }

func TestListenAddrNeverUnspecified(t *testing.T) {
	for _, gw := range []netip.Addr{
		{}, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::"),
		netip.MustParseAddr("::ffff:0.0.0.0"), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("224.0.0.1"),
	} {
		if a, err := listenAddr("linux", Net{Gateway: gw}, 9072); err == nil {
			t.Fatalf("listenAddr accepted gateway %v as %q", gw, a)
		}
	}
	if a, err := listenAddr("linux", Net{Gateway: netip.MustParseAddr("172.21.0.1")}, 9072); err != nil || a != "172.21.0.1:9072" {
		t.Fatal(a, err)
	}
	if a, err := listenAddr("darwin", Net{Gateway: netip.MustParseAddr("172.29.72.1")}, 9072); err != nil || a != "127.0.0.1:9072" {
		t.Fatal(a, err)
	}
	// darwin ignores the gateway entirely, so even an unspecified one cannot widen the bind.
	if a, err := listenAddr("darwin", Net{Gateway: netip.MustParseAddr("0.0.0.0")}, 9072); err != nil || a != "127.0.0.1:9072" {
		t.Fatal(a, err)
	}
	// A gateway outside the network's own subnet is not this network's gateway.
	if _, err := listenAddr("linux", Net{Gateway: netip.MustParseAddr("93.184.216.34"), Subnet: netip.MustParsePrefix("172.21.0.0/16")}, 9072); err == nil {
		t.Fatal("accepted a gateway outside the subnet")
	}
	for _, p := range []int{-1, 65536} {
		if _, err := listenAddr("linux", Net{Gateway: netip.MustParseAddr("172.21.0.1")}, p); err == nil {
			t.Fatalf("accepted port %d", p)
		}
	}
	if _, err := listenAddr("plan9", Net{Gateway: netip.MustParseAddr("172.21.0.1")}, 9072); err == nil {
		t.Fatal("accepted an unsupported GOOS")
	}
}

const iccOpt = "com.docker.network.bridge.enable_icc"

// Spec correction 11: boxes on the egress network must not reach each other.
func TestEnsureCreatesNetworkWithICCOff(t *testing.T) {
	f := newFakeDocker()
	n, err := EnsureNetwork(context.Background(), f, "agentbox-egress-DEV", "DEV", Want{Internal: true, NoICC: true})
	if err != nil {
		t.Fatal(err)
	}
	c := f.createCalls()[0]
	if !has(c, "-o", iccOpt+"=false") || !has(c, "--internal") || c[len(c)-1] != "agentbox-egress-DEV" {
		t.Fatalf("argv: %v", c)
	}
	if !n.NoICC {
		t.Fatalf("net: %+v", n)
	}
	// Without the requirement no option is passed (darwin's design is separate).
	g := newFakeDocker()
	if _, err := EnsureNetwork(context.Background(), g, "x", "DEV", Want{Internal: true}); err != nil {
		t.Fatal(err)
	}
	if has(g.createCalls()[0], "-o") {
		t.Fatalf("argv: %v", g.createCalls()[0])
	}
}

func TestInspectReadsICC(t *testing.T) {
	cfg := [2]string{"172.21.0.0/16", "172.21.0.1"}
	for name, c := range map[string]struct {
		opts   map[string]string
		wantNo bool
	}{
		"absent (bridge default: on)": {nil, false},
		"other options only":          {map[string]string{"com.docker.network.bridge.name": "br0"}, false},
		"false":                       {map[string]string{iccOpt: "false"}, true},
		"true":                        {map[string]string{iccOpt: "true"}, false},
		"unparsable":                  {map[string]string{iccOpt: "nope"}, false},
	} {
		f := newFakeDocker()
		f.nets["x"] = inspectJSONOpts("x", true, c.opts, cfg)
		n, ok, err := InspectNetwork(context.Background(), f, "x")
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", name, ok, err)
		}
		if n.NoICC != c.wantNo {
			t.Errorf("%s: NoICC = %v, want %v", name, n.NoICC, c.wantNo)
		}
	}
}

func TestEnsureRefusesExistingNetworkWithICCOn(t *testing.T) {
	for name, opts := range map[string]map[string]string{
		"option absent": nil,
		"option true":   {iccOpt: "true"},
	} {
		f := newFakeDocker()
		f.nets["agentbox-egress-DEV"] = inspectJSONOpts("agentbox-egress-DEV", true, opts, [2]string{"172.21.0.0/16", "172.21.0.1"})
		_, err := EnsureNetwork(context.Background(), f, "agentbox-egress-DEV", "DEV", Want{Internal: true, NoICC: true})
		if err == nil || !strings.Contains(err.Error(), "docker network rm agentbox-egress-DEV") ||
			!strings.Contains(err.Error(), "inter-container") || !strings.Contains(err.Error(), "egress network agentbox-egress-DEV") {
			t.Fatalf("%s: err: %v", name, err)
		}
		if len(f.createCalls()) != 0 {
			t.Fatalf("%s: must not create over an existing network", name)
		}
	}
	// ICC off: accepted.
	f := newFakeDocker()
	f.nets["n"] = inspectJSONOpts("n", true, map[string]string{iccOpt: "false"}, [2]string{"172.21.0.0/16", "172.21.0.1"})
	if _, err := EnsureNetwork(context.Background(), f, "n", "DEV", Want{Internal: true, NoICC: true}); err != nil {
		t.Fatal(err)
	}
}

// m3: the internal=false refusal concerns every fenced box, not only bound ones.
func TestOpenNetworkRefusalSaysBoxes(t *testing.T) {
	f := newFakeDocker()
	f.nets["n"] = inspectJSON("n", false, [2]string{"172.21.0.0/16", "172.21.0.1"})
	_, err := EnsureNetwork(context.Background(), f, "n", "DEV", Want{Internal: true})
	if err == nil || strings.Contains(err.Error(), "bound boxes") || !strings.Contains(err.Error(), "for boxes") {
		t.Fatalf("err: %v", err)
	}
}
