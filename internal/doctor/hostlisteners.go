package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
)

// A Linux box sits on an --internal docker network: it has no route off the
// bridge, but the bridge's gateway IP is this host, and the kernel delivers a
// connection to that IP to any socket bound to the wildcard address. So a box
// may open raw TCP to a wildcard listener here, without passing the egress proxy. Measured from a box on 2026-11-01: 0.0.0.0 and [::] listeners answer;
// 127.0.0.1, a socket bound to one other address, and the host's other
// addresses do not. A socket bound to the gateway IP itself does answer, but
// that network is created lazily, so this check looks only at wildcards. /proc
// cannot tell a reachable wildcard from a Docker-published port (DNAT sends it to
// the container's network, and the box times out) or an IPv6-only socket, hence
// "may". macOS is out of scope: there a bound box's gateway is the VM.
const (
	hostListenersCheck = "host listeners"
	tcpListenState     = "0A"
	udpUnboundState    = "07" // an unconnected UDP socket
)

// wildcardPorts returns the sorted, unique ports with a socket in state on the
// wildcard address, from the text of two /proc/net tables (v4 and v6). A row it
// cannot parse is skipped.
func wildcardPorts(state string, v4, v6 []byte) []int {
	var ports []int
	for _, table := range [][]byte{v4, v6} {
		for _, line := range strings.Split(string(table), "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || f[3] != state {
				continue
			}
			addr, portHex, ok := strings.Cut(f[1], ":")
			if !ok || !isWildcardHex(addr) {
				continue
			}
			p, err := strconv.ParseUint(portHex, 16, 16)
			if err != nil {
				continue
			}
			ports = append(ports, int(p))
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}

// isWildcardHex reports 0.0.0.0, :: or ::ffff:0.0.0.0 as the kernel prints them.
func isWildcardHex(addr string) bool {
	return addr == strings.Repeat("0", 8) || addr == strings.Repeat("0", 32) ||
		addr == "0000000000000000FFFF000000000000"
}

// CheckHostListeners reports the host services a Linux box may reach directly.
// It only describes: there is no root here to change the firewall with.
func CheckHostListeners() Result { return checkHostListeners(os.ReadFile) }

func checkHostListeners(read func(string) ([]byte, error)) Result {
	var tables [4][]byte
	for i, name := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := read("/proc/net/" + name)
		// Only the v6 tables can be absent, on a kernel without IPv6.
		if err != nil && (i%2 == 0 || !errors.Is(err, fs.ErrNotExist)) {
			return Result{Warn, hostListenersCheck, fmt.Sprintf("could not read the listening sockets (%v), so what a box can reach on this host is unknown", err)}
		}
		tables[i] = b
	}
	var parts []string
	if p := wildcardPorts(tcpListenState, tables[0], tables[1]); len(p) > 0 {
		parts = append(parts, "TCP ports "+joinPorts(p))
	}
	if p := wildcardPorts(udpUnboundState, tables[2], tables[3]); len(p) > 0 {
		parts = append(parts, "UDP ports "+joinPorts(p))
	}
	if len(parts) == 0 {
		return Result{Pass, hostListenersCheck, "no TCP or UDP service listens on all addresses"}
	}
	return Result{Warn, hostListenersCheck, fmt.Sprintf(
		"%s listen on all addresses, so a box may reach them directly, unseen by the egress proxy. Docker-published ports and IPv6-only sockets are listed too, though a box usually cannot reach them, and outbound-only UDP sockets can appear. Bind a service to 127.0.0.1 if a box must not reach it",
		strings.Join(parts, " and "))}
}

func joinPorts(ports []int) string {
	list := make([]string, len(ports))
	for i, p := range ports {
		list[i] = strconv.Itoa(p)
	}
	return strings.Join(list, ", ")
}
