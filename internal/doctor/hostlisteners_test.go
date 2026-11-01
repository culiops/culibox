package doctor

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const procTCPHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// Rows as the kernel prints them: 0A is LISTEN, 01 is ESTABLISHED.
const procTCP = procTCPHeader +
	"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 1 1\n" + // 0.0.0.0:22
	"   1: 0100007F:0CEA 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 2 1\n" + // 127.0.0.1:3306
	"   2: 7976644B:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 3 1\n" + // a specific IP
	"   3: 00000000:0BB8 0100007F:D431 01 00000000:00000000 00:00000000 00000000 0 0 4 1\n" // established, port 3000

const procTCP6 = procTCPHeader +
	"   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 5 1\n" + // [::]:22, same port as v4
	"   1: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 6 1\n" + // [::]:8080
	"   2: 0000000000000000FFFF000000000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 7 1\n" + // ::ffff:0.0.0.0:8081
	"   3: 00000000000000000000000001000000:1F92 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 8 1\n" // [::1]:8082

func TestWildcardPortsKeepsOnlyListeningWildcardSockets(t *testing.T) {
	got := wildcardPorts([]byte(procTCP), []byte(procTCP6))
	want := []int{22, 8080, 8081}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestWildcardPortsIgnoresGarbage(t *testing.T) {
	bad := procTCPHeader + "nonsense\n   0: 00000000 00000000:0000 0A\n   1: 00000000:ZZZZ 00000000:0000 0A\n"
	if got := wildcardPorts([]byte(bad), nil); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func fakeProc(tcp, tcp6 string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		switch p {
		case "/proc/net/tcp":
			return []byte(tcp), nil
		case "/proc/net/tcp6":
			if tcp6 == "" {
				return nil, os.ErrNotExist // an IPv6-less kernel has no such file
			}
			return []byte(tcp6), nil
		}
		return nil, errors.New("unexpected path " + p)
	}
}

func TestCheckHostListenersWarnsWithPortsAndFix(t *testing.T) {
	r := checkHostListeners(fakeProc(procTCP, procTCP6))
	if r.Status != Warn {
		t.Fatalf("status = %s, want WARN: %s", r.Status, r.Detail)
	}
	for _, want := range []string{"22, 8080, 8081", "127.0.0.1"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, r.Detail)
		}
	}
}

func TestCheckHostListenersPassesWhenNoneExposed(t *testing.T) {
	if r := checkHostListeners(fakeProc(procTCPHeader, "")); r.Status != Pass {
		t.Fatalf("status = %s: %s", r.Status, r.Detail)
	}
}

// Unreadable /proc is not evidence of a clean host: it must not read as a pass.
func TestCheckHostListenersUnreadableIsNotAPass(t *testing.T) {
	r := checkHostListeners(func(string) ([]byte, error) { return nil, os.ErrPermission })
	if r.Status != Warn {
		t.Fatalf("status = %s, want WARN: %s", r.Status, r.Detail)
	}
}
