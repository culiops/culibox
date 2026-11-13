package doctor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/egress"
)

// On Linux the egress proxy listens on TCP at the box network's gateway address
// (egress.listenAddr). While CuliBox is stopped, another account on the host can
// bind that address and port; boxes that still run would then send their proxy
// connections to it, and it could act as their proxy (#131). When CuliBox starts
// and finds the port taken, the daemon logs egress.LinuxBindConflict; this check
// is the same finding from outside, and it also catches the window while CuliBox
// is down. It is read-only like every check here: it never binds the port, it
// reads /proc/net/tcp, whose rows carry the owning uid. A listener of your own
// account is not a finding (it is CuliBox, or something you started). macOS is
// out of scope: the relay proves a key before it forwards anything (#118).
//
// Rows it matches, on the port: LISTEN sockets bound to the gateway or to all
// IPv4 addresses, in the v4 table or as the v4-mapped form in the v6 table. A
// plain [::] socket is not matched: whether it blocks an IPv4 bind depends on
// bindv6only, and a false FAIL is worse than missing that case, which the
// "host listeners" check already lists.
const egressPortCheck = "egress port"

// CheckEgressPort is the Linux "egress port" row: it reads the egress network's
// gateway from docker and the listening sockets from /proc.
func CheckEgressPort(c config.Config) Result {
	bin, err := box.LookDocker(c.DockerBin)
	if err != nil {
		return Result{Warn, egressPortCheck, "docker not found, so the egress proxy's address is unknown (" + err.Error() + ")"}
	}
	d := box.CLIClient{Bin: bin}
	name := egress.NetworkName(c.BoxInstance())
	inspect := func(ctx context.Context) (egress.Net, bool, error) { return egress.InspectNetwork(ctx, d, name) }
	return checkEgressPort(context.Background(), c.EgressPortOrDefault(), inspect, os.ReadFile, os.Getuid())
}

func checkEgressPort(ctx context.Context, port int, inspect func(context.Context) (egress.Net, bool, error), read func(string) ([]byte, error), uid int) Result {
	n, ok, err := inspect(ctx)
	if err != nil {
		return Result{Warn, egressPortCheck, fmt.Sprintf("could not read the egress network (%v), so the proxy's address is unknown", err)}
	}
	if !ok {
		return Result{Pass, egressPortCheck, "the egress network is not created yet; its address is checked once it exists"}
	}
	gw := n.Gateway.Unmap()
	if !gw.Is4() {
		return Result{Warn, egressPortCheck, "the egress network has no gateway address, so the proxy's address is unknown"}
	}
	var tables [2][]byte
	for i, name := range []string{"tcp", "tcp6"} {
		b, err := read("/proc/net/" + name)
		// Only the v6 table can be absent, on a kernel without IPv6.
		if err != nil && (i == 0 || !errors.Is(err, fs.ErrNotExist)) {
			return Result{Warn, egressPortCheck, fmt.Sprintf("could not read the listening sockets (%v), so whether another account holds the egress port is unknown", err)}
		}
		tables[i] = b
	}
	var others []string
	where := ""
	for _, h := range portHolders(tables[0], tables[1], gw, port) {
		if h.uid != uid {
			others = append(others, strconv.Itoa(h.uid))
			if where == "" {
				where = h.where
			}
		}
	}
	if len(others) == 0 {
		return Result{Pass, egressPortCheck, fmt.Sprintf("no other account listens on port %d of %s, the egress proxy's address", port, gw)}
	}
	slices.Sort(others)
	others = slices.Compact(others)
	return Result{Fail, egressPortCheck, fmt.Sprintf(
		"another account (uid %s) has a socket listening on port %d, on %s, which is the egress proxy's address. The proxy cannot start while it is there, and a box that is still running would send its proxy connections to it. Stop it; see docs/SECURITY.md (Services on this host)",
		strings.Join(others, ", "), port, where)}
}

type holder struct {
	uid   int
	where string // how the socket is bound, for the message
}

// portHolders returns the LISTEN sockets on port that would block a bind to
// gw:port: their owners and how each is bound.
func portHolders(v4, v6 []byte, gw netip.Addr, port int) []holder {
	b := gw.As4()
	w4 := binary.NativeEndian.Uint32(b[:])
	mapped := binary.NativeEndian.Uint32([]byte{0, 0, 0xff, 0xff})
	hex4 := func(w uint32) string { return fmt.Sprintf("%08X", w) }
	all := "all addresses, which include the gateway " + gw.String()
	match := map[string]string{
		hex4(w4): "the gateway " + gw.String(),
		hex4(0):  all,
		// the v6 table's form of the same two (the first 8 bytes are zero)
		"0000000000000000" + hex4(mapped) + hex4(w4): "the gateway " + gw.String(),
		"0000000000000000" + hex4(mapped) + hex4(0):  all,
	}
	var hs []holder
	for _, table := range [][]byte{v4, v6} {
		for _, line := range strings.Split(string(table), "\n") {
			f := strings.Fields(line)
			if len(f) < 8 || f[3] != tcpListenState {
				continue
			}
			addr, portHex, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			p, err := strconv.ParseUint(portHex, 16, 16)
			if err != nil || int(p) != port {
				continue
			}
			at, ok := match[strings.ToUpper(addr)]
			if !ok {
				continue
			}
			uid, err := strconv.Atoi(f[7])
			if err != nil {
				continue
			}
			hs = append(hs, holder{uid, at})
		}
	}
	return hs
}
