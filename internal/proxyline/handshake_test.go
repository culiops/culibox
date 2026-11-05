package proxyline

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

// nonceOf returns a 32-byte nonce made of b.
func nonceOf(b byte) *[32]byte {
	var n [32]byte
	for i := range n {
		n[i] = b
	}
	return &n
}

func TestProofKnownAnswer(t *testing.T) {
	k, nR, nL := keyOf(0x11), nonceOf(0xaa), nonceOf(0xbb)
	const port = 9071
	for _, tc := range []struct{ name, label, want string }{
		{"listener", listenerLabel, "96d47f56074395083d8c41afd663d1580b8e8ee30e210fee347b548b93906b26"},
		{"relay", relayLabel, "96a90edf06296fc5918b8524d19335f449cd313792e3490f3872a53d86f378ec"},
	} {
		mac := k.proof(tc.label, port, nR, nL)
		if got := hex.EncodeToString(mac[:]); got != tc.want {
			t.Errorf("%s MAC = %s, want %s", tc.name, got, tc.want)
		}
	}

	lmac, rmac := k.proof(listenerLabel, port, nR, nL), k.proof(relayLabel, port, nR, nL)
	pl := listenerProofLine(k, port, nR, nL)
	if len(pl) != ListenerProofLen {
		t.Errorf("len(listenerProofLine) = %d, want %d", len(pl), ListenerProofLen)
	}
	if want := hex.EncodeToString(nL[:]) + " " + hex.EncodeToString(lmac[:]) + "\r\n"; string(pl) != want {
		t.Errorf("listenerProofLine = %q, want %q", pl, want)
	}
	pr := relayProofLine(k, port, nR, nL)
	if len(pr) != RelayProofLen {
		t.Errorf("len(relayProofLine) = %d, want %d", len(pr), RelayProofLen)
	}
	if want := hex.EncodeToString(rmac[:]) + "\r\n"; string(pr) != want {
		t.Errorf("relayProofLine = %q, want %q", pr, want)
	}
}

func TestProofSeparatesRolesPortsAndNonces(t *testing.T) {
	k, nR, nL := keyOf(0x11), nonceOf(0xaa), nonceOf(0xbb)
	base := k.proof(listenerLabel, 9071, nR, nL)
	if again := k.proof(listenerLabel, 9071, nR, nL); again != base {
		t.Fatal("the MAC is not deterministic")
	}
	variants := map[string][32]byte{
		"the other role's label": k.proof(relayLabel, 9071, nR, nL),
		"another port":           k.proof(listenerLabel, 9072, nR, nL),
		"the nonces swapped":     k.proof(listenerLabel, 9071, nL, nR),
		"another key":            keyOf(0x22).proof(listenerLabel, 9071, nR, nL),
		"another relay nonce":    k.proof(listenerLabel, 9071, nonceOf(0xab), nL),
		"another listener nonce": k.proof(listenerLabel, 9071, nR, nonceOf(0xbc)),
	}
	seen := map[[32]byte]string{base: "the base MAC"}
	for name, mac := range variants {
		if prev, dup := seen[mac]; dup {
			t.Errorf("%s gives the same MAC as %s", name, prev)
		}
		seen[mac] = name
	}
}

// peerResult is what a test listener saw of one relay connection.
type peerResult struct {
	hello []byte // up to HelloLen bytes: the first thing the relay sent
	rest  []byte // everything after the hello, up to EOF (only read once answered)
	err   error  // why the peer stopped early, if it did
}

var helloRE = regexp.MustCompile(`^AGENTBOX-RELAY2 [0-9a-f]{64}\r\n$`)

// startPeer plays the listener's half on a real loopback listener. It reads
// exactly HelloLen bytes and checks them against the wire format, passes the
// hello's nonce and the port to answer, and writes whatever answer returns (a nil
// return writes nothing and closes). After answering it reads until the relay
// closes its end, so a test sees every byte the relay wrote. It returns the
// relay's end of the connection, the listener's port and the channel the result
// arrives on once the peer is done.
func startPeer(t *testing.T, answer func(port uint16, nR *[32]byte) []byte) (net.Conn, uint16, <-chan peerResult) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	out := make(chan peerResult, 1)
	go func() {
		var r peerResult
		defer func() { out <- r }()
		c, err := ln.Accept()
		if err != nil {
			r.err = err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		hello := make([]byte, HelloLen)
		n, err := io.ReadFull(c, hello)
		r.hello = hello[:n]
		if err != nil {
			r.err = err
			return
		}
		if !helloRE.Match(hello) {
			r.err = errors.New("the relay's hello does not match the wire format")
			return
		}
		var nR [32]byte
		if _, err := hex.Decode(nR[:], hello[len(helloPrefix):len(helloPrefix)+64]); err != nil {
			r.err = err
			return
		}
		line := answer(port, &nR)
		if line == nil {
			return
		}
		if _, err := c.Write(line); err != nil {
			r.err = err
			return
		}
		r.rest, r.err = io.ReadAll(c)
	}()

	relay, err := net.DialTimeout("tcp4", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { relay.Close() })
	relay.SetDeadline(time.Now().Add(5 * time.Second))
	return relay, port, out
}

// finish closes the relay's end, as the relay does when it gives up, and waits
// for the peer's result.
func finish(t *testing.T, relay net.Conn, res <-chan peerResult) peerResult {
	t.Helper()
	relay.Close()
	select {
	case r := <-res:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the test listener never finished")
		return peerResult{}
	}
}

func TestRelayHandshakeAgainstAListener(t *testing.T) {
	var hellos []string
	for i := 0; i < 2; i++ {
		nL := nonceOf(byte(0x40 + i))
		var gotNR [32]byte
		relay, port, res := startPeer(t, func(port uint16, nR *[32]byte) []byte {
			gotNR = *nR
			return listenerProofLine(testKey, port, nR, nL)
		})
		if err := RelayHandshake(relay, testKey, port); err != nil {
			t.Fatalf("run %d: RelayHandshake: %v", i, err)
		}
		r := finish(t, relay, res)
		if r.err != nil {
			t.Fatalf("run %d: test listener: %v", i, r.err)
		}
		if want := relayProofLine(testKey, port, &gotNR, nL); !bytes.Equal(r.rest, want) {
			t.Errorf("run %d: the relay sent %q after the proof, want exactly %q", i, r.rest, want)
		}
		hellos = append(hellos, string(r.hello))
	}
	if hellos[0] == hellos[1] {
		t.Error("two handshakes sent the same hello: the relay nonce is not fresh")
	}
}

func TestRelayHandshakeRefusesABadListenerProof(t *testing.T) {
	nL := nonceOf(0xbb)
	hexRun := regexp.MustCompile(`[0-9a-f]{64}`)
	for _, tc := range []struct {
		name   string
		answer func(port uint16, nR *[32]byte) []byte
	}{
		{"a proof under another key", func(port uint16, nR *[32]byte) []byte {
			return listenerProofLine(otherKey, port, nR, nL)
		}},
		{"a proof for another port", func(port uint16, nR *[32]byte) []byte {
			return listenerProofLine(testKey, port+1, nR, nL)
		}},
		{"the relay's own label reflected", func(port uint16, nR *[32]byte) []byte {
			mac := testKey.proof(relayLabel, port, nR, nL)
			return []byte(hex.EncodeToString(nL[:]) + " " + hex.EncodeToString(mac[:]) + "\r\n")
		}},
		{"the right line in uppercase hex", func(port uint16, nR *[32]byte) []byte {
			return []byte(strings.ToUpper(string(listenerProofLine(testKey, port, nR, nL))))
		}},
		{"131 bytes that are not hex", func(uint16, *[32]byte) []byte {
			return bytes.Repeat([]byte{'x'}, ListenerProofLen)
		}},
		{"EOF", func(uint16, *[32]byte) []byte { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay, port, res := startPeer(t, tc.answer)
			err := RelayHandshake(relay, testKey, port)
			if err == nil {
				t.Fatal("RelayHandshake accepted the proof")
			}
			// The error may carry a wrapped I/O error and nothing the peer sent.
			if msg := err.Error(); strings.Contains(msg, "xx") || hexRun.MatchString(msg) {
				t.Errorf("the error carries received bytes: %q", msg)
			}
			r := finish(t, relay, res)
			if len(r.hello) != HelloLen {
				t.Errorf("the listener received %d bytes of hello, want %d", len(r.hello), HelloLen)
			}
			if len(r.rest) != 0 {
				t.Errorf("the relay wrote %d bytes after a bad proof, want none", len(r.rest))
			}
			if r.err != nil {
				t.Errorf("test listener: %v (want a clean EOF after the hello)", r.err)
			}
		})
	}
}

func TestRelayHandshakeRefusesAZeroKey(t *testing.T) {
	relay, port, res := startPeer(t, func(uint16, *[32]byte) []byte {
		t.Error("the listener received a hello from a relay with a zero key")
		return nil
	})
	err := RelayHandshake(relay, Key{}, port)
	if err == nil {
		t.Fatal("RelayHandshake accepted the zero key")
	}
	if r := finish(t, relay, res); len(r.hello) != 0 || len(r.rest) != 0 {
		t.Errorf("the peer received %d+%d bytes, want 0", len(r.hello), len(r.rest))
	}
}
