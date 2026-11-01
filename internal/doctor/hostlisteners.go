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
// can open raw TCP to every wildcard listener here, without passing the egress
// proxy. Measured from a box on 2026-11-01: 0.0.0.0 and [::] listeners answer;
// 127.0.0.1, a socket bound to one other address, and the host's other
// addresses do not. macOS is out of scope: there the gateway is the VM.
const (
	hostListenersCheck = "host listeners"
	tcpListenState     = "0A"
)

// wildcardPorts returns the sorted, unique ports with a listening socket on the
// wildcard address, from the text of /proc/net/tcp and /proc/net/tcp6. A row it
// cannot parse is skipped.
func wildcardPorts(tcp, tcp6 []byte) []int {
	var ports []int
	for _, table := range [][]byte{tcp, tcp6} {
		for _, line := range strings.Split(string(table), "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || f[3] != tcpListenState {
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

// CheckHostListeners reports the host services a Linux box can reach directly.
// It only describes: there is no root here to change the firewall with.
func CheckHostListeners() Result { return checkHostListeners(os.ReadFile) }

func checkHostListeners(read func(string) ([]byte, error)) Result {
	tcp, err := read("/proc/net/tcp")
	if err != nil {
		return Result{Warn, hostListenersCheck, fmt.Sprintf("could not read the listening sockets (%v), so what a box can reach on this host is unknown", err)}
	}
	tcp6, err := read("/proc/net/tcp6")
	if err != nil && !errors.Is(err, fs.ErrNotExist) { // no IPv6 in this kernel
		return Result{Warn, hostListenersCheck, fmt.Sprintf("could not read the listening sockets (%v), so what a box can reach on this host is unknown", err)}
	}
	ports := wildcardPorts(tcp, tcp6)
	if len(ports) == 0 {
		return Result{Pass, hostListenersCheck, "no service listens on all addresses, so a box cannot reach one directly"}
	}
	list := make([]string, len(ports))
	for i, p := range ports {
		list[i] = strconv.Itoa(p)
	}
	return Result{Warn, hostListenersCheck, fmt.Sprintf(
		"a box can connect directly to TCP ports %s on this host, because they listen on all addresses. The egress proxy does not see these connections. Bind a service to 127.0.0.1 if a box must not reach it",
		strings.Join(list, ", "))}
}
