package doctor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/egress"
)

// row is a /proc/net/tcp line: 0A is LISTEN. The address is printed as the
// kernel prints it, a host-order word.
func egpTCPRow(addr netip.Addr, port, uid int, state string) string {
	b := addr.As4()
	return fmt.Sprintf("   0: %08X:%04X 00000000:0000 %s 00000000:00000000 00:00000000 00000000 %d 0 1 1\n",
		binary.NativeEndian.Uint32(b[:]), port, state, uid)
}

// egpTCP6Mapped is a v4-mapped row (what a dual-stack socket bound to ::ffff:a.b.c.d shows).
func egpTCP6Mapped(addr netip.Addr, port, uid int) string {
	b := addr.As4()
	return fmt.Sprintf("   0: 0000000000000000%08X%08X:%04X 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 %d 0 1 1\n",
		binary.NativeEndian.Uint32([]byte{0, 0, 0xff, 0xff}), binary.NativeEndian.Uint32(b[:]), port, uid)
}

var (
	egpGW    = netip.MustParseAddr("172.21.0.1")
	egpNetOK = func(context.Context) (egress.Net, bool, error) {
		return egress.Net{Name: "n", Gateway: egpGW}, true, nil
	}
	egpNoProc = func(string) ([]byte, error) { return []byte(procTCPHeader), nil }
)

func egpProcWith(v4, v6 string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		switch name {
		case "/proc/net/tcp":
			return []byte(procTCPHeader + v4), nil
		case "/proc/net/tcp6":
			return []byte(procTCPHeader + v6), nil
		}
		return nil, errors.New("unexpected " + name)
	}
}

// #131: a listener on the gateway's egress port that another account owns is a
// FAIL, whether it is bound to the gateway itself, to every address, or to the
// v4-mapped form of either.
func TestEgressPortFailsOnAnotherAccountsListener(t *testing.T) {
	cases := map[string]func(*testing.T) func(string) ([]byte, error){
		"gateway": func(*testing.T) func(string) ([]byte, error) {
			return egpProcWith(egpTCPRow(egpGW, 9072, 1234, "0A"), "")
		},
		"wildcard": func(*testing.T) func(string) ([]byte, error) {
			return egpProcWith(egpTCPRow(netip.IPv4Unspecified(), 9072, 1234, "0A"), "")
		},
		"mapped gateway": func(*testing.T) func(string) ([]byte, error) {
			return egpProcWith("", egpTCP6Mapped(egpGW, 9072, 1234))
		},
		"mapped wildcard": func(*testing.T) func(string) ([]byte, error) {
			return egpProcWith("", egpTCP6Mapped(netip.IPv4Unspecified(), 9072, 1234))
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			r := checkEgressPort(context.Background(), 9072, egpNetOK, mk(t), 1000)
			if r.Status != Fail || r.Check != egressPortCheck {
				t.Fatalf("got %+v, want a FAIL", r)
			}
			for _, want := range []string{"9072", "172.21.0.1", "another account", "1234"} {
				if !strings.Contains(r.Detail, want) {
					t.Errorf("detail lacks %q: %s", want, r.Detail)
				}
			}
		})
	}
}

func TestEgressPortPassesWhenTheListenerIsYoursOrAbsentOrElsewhere(t *testing.T) {
	cases := map[string]func(string) ([]byte, error){
		"own account":      egpProcWith(egpTCPRow(egpGW, 9072, 1000, "0A"), ""),
		"nothing":          egpNoProc,
		"other port":       egpProcWith(egpTCPRow(egpGW, 9073, 1234, "0A"), ""),
		"other address":    egpProcWith(egpTCPRow(netip.MustParseAddr("127.0.0.1"), 9072, 1234, "0A"), ""),
		"not listening":    egpProcWith(egpTCPRow(egpGW, 9072, 1234, "01"), ""), // an established connection
		"v6 wildcard only": egpProcWith("", "   0: 00000000000000000000000000000000:2370 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 1234 0 1 1\n"),
	}
	for name, read := range cases {
		t.Run(name, func(t *testing.T) {
			if r := checkEgressPort(context.Background(), 9072, egpNetOK, read, 1000); r.Status != Pass {
				t.Fatalf("got %+v, want PASS", r)
			}
		})
	}
}

// One listener of yours does not hide another account's on the same port.
func TestEgressPortFailsEvenWhenYourOwnListenerIsAlsoThere(t *testing.T) {
	read := egpProcWith(egpTCPRow(egpGW, 9072, 1000, "0A")+egpTCPRow(netip.IPv4Unspecified(), 9072, 77, "0A"), "")
	if r := checkEgressPort(context.Background(), 9072, egpNetOK, read, 1000); r.Status != Fail || !strings.Contains(r.Detail, "77") {
		t.Fatalf("got %+v", r)
	}
}

// No network yet: the address is not known, so nothing to look at; docker or
// /proc unreadable: a WARN, never a PASS that was not checked.
func TestEgressPortUnknownStates(t *testing.T) {
	absent := func(context.Context) (egress.Net, bool, error) { return egress.Net{}, false, nil }
	if r := checkEgressPort(context.Background(), 9072, absent, egpNoProc, 1000); r.Status != Pass || !strings.Contains(r.Detail, "not created yet") {
		t.Errorf("absent network: %+v", r)
	}
	broken := func(context.Context) (egress.Net, bool, error) { return egress.Net{}, false, errors.New("daemon down") }
	if r := checkEgressPort(context.Background(), 9072, broken, egpNoProc, 1000); r.Status != Warn {
		t.Errorf("docker error: %+v", r)
	}
	noGw := func(context.Context) (egress.Net, bool, error) { return egress.Net{Name: "n"}, true, nil }
	if r := checkEgressPort(context.Background(), 9072, noGw, egpNoProc, 1000); r.Status != Warn {
		t.Errorf("no gateway: %+v", r)
	}
	unreadable := func(string) ([]byte, error) { return nil, errors.New("denied") }
	if r := checkEgressPort(context.Background(), 9072, egpNetOK, unreadable, 1000); r.Status != Warn {
		t.Errorf("unreadable /proc: %+v", r)
	}
}

// The row format is the kernel's own: a real listener on this machine is found
// by its address and port, with this account's uid, so the host-order word and
// the uid column are read right (the fixtures above are written by the same
// reading and would not show a wrong one).
func TestPortHoldersReadsARealListener(t *testing.T) {
	v4, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		t.Skip("no /proc/net/tcp")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	v4, _ = os.ReadFile("/proc/net/tcp")
	v6, _ := os.ReadFile("/proc/net/tcp6")
	hs := portHolders(v4, v6, netip.MustParseAddr("127.0.0.1"), port)
	if len(hs) != 1 || hs[0].uid != os.Getuid() {
		t.Fatalf("holders = %+v, want one owned by uid %d", hs, os.Getuid())
	}
	if hs := portHolders(v4, v6, netip.MustParseAddr("127.0.0.2"), port); len(hs) != 0 {
		t.Fatalf("another address matched: %+v", hs)
	}
}

// CheckEgressPort reads the gateway through the docker CLI and the sockets from
// /proc: with the network's gateway on this machine and a socket of this account
// on the port, it passes.
func TestCheckEgressPortThroughDockerAndProc(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc/net/tcp")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	bin := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \"$2\" in\nls) echo agentbox-egress-DEV;;\ninspect) echo '{\"Internal\":true,\"IPAM\":{\"Config\":[{\"Subnet\":\"127.0.0.0/8\",\"Gateway\":\"127.0.0.1\"}]}}';;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := config.Config{DockerBin: bin, EnvLabel: "DEV", EgressPort: port}
	r := CheckEgressPort(c)
	if r.Status != Pass || !strings.Contains(r.Detail, "127.0.0.1") {
		t.Fatalf("got %+v", r)
	}
}
