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

// The listener's half. A hand-rolled relay below bends one step at a time; the
// ordinary client is relayWrite.

// goodHello is the relay's hello for nonce nR.
func goodHello(nR *[32]byte) string { return helloPrefix + hex.EncodeToString(nR[:]) + "\r\n" }

// portOf is the port l listens on.
func portOf(l net.Listener) uint16 { return uint16(l.Addr().(*net.TCPAddr).Port) }

// nLOf is the nonce in a listener proof line.
func nLOf(line []byte) *[32]byte {
	var n [32]byte
	hex.Decode(n[:], line[:64])
	return &n
}

// readProofL reads exactly one listener proof line from c.
func readProofL(t *testing.T, c net.Conn) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line := make([]byte, ListenerProofLen)
	if _, err := io.ReadFull(c, line); err != nil {
		t.Fatalf("reading the listener's proof: %v", err)
	}
	if !listenerProofRE.Match(line) {
		t.Fatal("the listener's proof does not match the wire format")
	}
	return line
}

// challenge dials l, sends a good hello for nR and starts the listener's side
// of the handshake in the background. It returns both ends, the listener's
// proof line and a channel closed when that handshake is over, however it ended.
func challenge(t *testing.T, l net.Listener, nR *[32]byte) (cl, sc net.Conn, proofL []byte, over <-chan struct{}) {
	t.Helper()
	cl = dial(t, l)
	if _, err := io.WriteString(cl, goodHello(nR)); err != nil {
		t.Fatal(err)
	}
	sc = accept(t, l)
	done := make(chan struct{})
	go func() {
		sc.RemoteAddr() // runs the handshake
		close(done)
	}()
	return cl, sc, readProofL(t, cl), done
}

// waitOver fails the test unless the handshake ends in time.
func waitOver(t *testing.T, over <-chan struct{}) {
	t.Helper()
	select {
	case <-over:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener's handshake never ended")
	}
}

// recordRelayProof completes a whole handshake on l with nonce nR and returns
// the relay proof line that matched, as a process that watched it could record.
func recordRelayProof(t *testing.T, l net.Listener, nR *[32]byte) []byte {
	t.Helper()
	cl, sc, line, over := challenge(t, l, nR)
	proof := relayProofLine(testKey, portOf(l), nR, nLOf(line))
	if _, err := io.WriteString(cl, string(proof)+goodHeader); err != nil {
		t.Fatal(err)
	}
	waitOver(t, over)
	if got := sc.RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("the recorded handshake was refused (RemoteAddr %s)", got)
	}
	return proof
}

func TestWrapCompletesTheRelayHandshake(t *testing.T) {
	var r refusals
	l := listenOpts(t, r.opts(testKey, 5*time.Second))
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader+"hello")
	sc := accept(t, l)
	if got := sc.RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("RemoteAddr = %s", got)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(sc, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("payload = %q, %v", buf, err)
	}
	if got := r.n.Load(); got != 0 {
		t.Fatalf("KeyRefused called %d times for a good handshake", got)
	}
}

// TCP may split the hello anywhere.
func TestWrapAcceptsAHelloSplitAcrossWrites(t *testing.T) {
	var r refusals
	l := listenOpts(t, r.opts(testKey, 5*time.Second))
	cl := dial(t, l)
	nR, port := nonceOf(0x61), portOf(l)
	hello := goodHello(nR)
	go func() {
		io.WriteString(cl, hello[:30])
		time.Sleep(50 * time.Millisecond)
		io.WriteString(cl, hello[30:60])
		time.Sleep(50 * time.Millisecond)
		io.WriteString(cl, hello[60:])
		line := make([]byte, ListenerProofLen)
		if _, err := io.ReadFull(cl, line); err != nil {
			return
		}
		cl.Write(relayProofLine(testKey, port, nR, nLOf(line)))
		io.WriteString(cl, goodHeader+"payload")
	}()
	sc := accept(t, l)
	if got := sc.RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("RemoteAddr = %s", got)
	}
	buf := make([]byte, len("payload"))
	if _, err := io.ReadFull(sc, buf); err != nil || string(buf) != "payload" {
		t.Fatalf("payload = %q, %v", buf, err)
	}
	if got := r.n.Load(); got != 0 {
		t.Fatalf("KeyRefused called %d times for a good handshake", got)
	}
}

// refusal is one refused connection: both ends, what the listener's Read
// reported, and every byte the client sent.
type refusal struct {
	r    refusals
	cl   net.Conn
	sc   net.Conn
	err  error
	sent []byte
}

// malformedHellos are first bytes the listener must refuse without a word.
var malformedHellos = []struct{ name, payload string }{
	{"the v1 key line", "AGENTBOX-RELAY " + testKey.Hex() + "\r\n" + goodHeader},
	{"another prefix", "AGENTBOX-RELAY3 " + hex.EncodeToString(nonceOf(0x61)[:]) + "\r\n"},
	{"an uppercase nonce", helloPrefix + strings.ToUpper(hex.EncodeToString(nonceOf(0xab)[:])) + "\r\n"},
	{"a 65-hex nonce", helloPrefix + hex.EncodeToString(nonceOf(0x61)[:]) + "a\r\n"},
	{"LF only", helloPrefix + hex.EncodeToString(nonceOf(0x61)[:]) + "\n" + goodHeader},
	{"a PROXY line only", goodHeader},
	{"82 zero bytes", strings.Repeat("\x00", HelloLen)},
}

// refuseHello sends payload as the first bytes of a connection to a listener
// that wants testKey and returns the refusal.
func refuseHello(t *testing.T, key Key, payload string) *refusal {
	t.Helper()
	rf := &refusal{sent: []byte(payload)}
	l := listenOpts(t, rf.r.opts(key, 300*time.Millisecond))
	rf.cl = dial(t, l)
	if _, err := io.WriteString(rf.cl, payload); err != nil {
		t.Fatal(err)
	}
	rf.sc = accept(t, l)
	rf.err = readRefused(t, rf.sc)
	return rf
}

func TestWrapRefusesAMalformedHello(t *testing.T) {
	for _, tc := range malformedHellos {
		t.Run(tc.name, func(t *testing.T) {
			rf := refuseHello(t, testKey, tc.payload)
			seesNothing(t, rf.cl) // no proof to a bad hello
			if got := rf.r.n.Load(); got != 1 {
				t.Fatalf("KeyRefused called %d times, want 1", got)
			}
			// Sticky: the same refusal on every later call, and no second count.
			if _, err := rf.sc.Write([]byte("x")); !errors.Is(err, ErrHeader) {
				t.Fatalf("Write err = %v, want ErrHeader", err)
			}
			if got := rf.r.n.Load(); got != 1 {
				t.Fatalf("KeyRefused called %d times after a second call, want 1", got)
			}
		})
	}
}

// badProofs are what a client sends instead of the relay's proof after a good
// hello and the listener's answer. proof gets the port, both nonces and the
// proof_R an earlier complete handshake with the same nR recorded; the client
// then half-closes when eof is set.
var badProofs = []struct {
	name  string
	proof func(port uint16, nR, nL *[32]byte, recorded []byte) []byte
	eof   bool
}{
	{"the relay MAC under another key", func(port uint16, nR, nL *[32]byte, _ []byte) []byte {
		return relayProofLine(otherKey, port, nR, nL)
	}, false},
	{"the listener's own MAC reflected", func(port uint16, nR, nL *[32]byte, _ []byte) []byte {
		mac := testKey.proof(listenerLabel, port, nR, nL)
		return []byte(hex.EncodeToString(mac[:]) + "\r\n")
	}, false},
	{"the right MAC for another port", func(port uint16, nR, nL *[32]byte, _ []byte) []byte {
		return relayProofLine(testKey, port+1, nR, nL)
	}, false},
	{"a replay from an earlier handshake", func(_ uint16, _, _ *[32]byte, recorded []byte) []byte {
		return recorded
	}, false},
	{"the right line in uppercase", func(port uint16, nR, nL *[32]byte, _ []byte) []byte {
		return []byte(strings.ToUpper(string(relayProofLine(testKey, port, nR, nL))))
	}, false},
	{"the right line with the last byte changed", func(port uint16, nR, nL *[32]byte, _ []byte) []byte {
		line := relayProofLine(testKey, port, nR, nL)
		line[len(line)-1] = ' '
		return line
	}, false},
	{"30 bytes then EOF", func(port uint16, nR, nL *[32]byte, _ []byte) []byte {
		return relayProofLine(testKey, port, nR, nL)[:30]
	}, true},
}

// refuseProof runs the handshake by hand on a listener that wants testKey up
// to the relay's proof, sends bad instead of it (then, unless the client
// half-closes, a PROXY line and a byte), and returns the refusal. An earlier
// complete handshake with the same nonce has run on the listener first.
func refuseProof(t *testing.T, bad int) *refusal {
	t.Helper()
	b := badProofs[bad]
	rf := &refusal{}
	l := listenOpts(t, rf.r.opts(testKey, 5*time.Second))
	nR := nonceOf(0x71)
	recorded := recordRelayProof(t, l, nR)

	var line []byte
	var over <-chan struct{}
	rf.cl, rf.sc, line, over = challenge(t, l, nR)
	send := b.proof(portOf(l), nR, nLOf(line), recorded)
	if !b.eof {
		send = append(append([]byte(nil), send...), (goodHeader + "x")...)
	}
	if _, err := rf.cl.Write(send); err != nil {
		t.Fatal(err)
	}
	if b.eof {
		rf.cl.(*net.TCPConn).CloseWrite()
	}
	waitOver(t, over)
	rf.err = readRefused(t, rf.sc)
	rf.sent = append([]byte(goodHello(nR)), send...)
	return rf
}

func TestWrapRefusesABadRelayProof(t *testing.T) {
	for i, tc := range badProofs {
		t.Run(tc.name, func(t *testing.T) {
			rf := refuseProof(t, i)
			// The client got exactly one proof line (readProofL took it) and nothing
			// after it.
			seesNothing(t, rf.cl)
			if got := rf.r.n.Load(); got != 1 {
				t.Fatalf("KeyRefused called %d times, want 1", got)
			}
			// The PROXY line that followed the bad proof was never believed.
			if got := rf.sc.RemoteAddr().String(); strings.HasPrefix(got, "172.29.72.5") {
				t.Fatalf("RemoteAddr = %s: the header after a bad proof was trusted", got)
			}
		})
	}
}

// A listener's proof answers one hello and one port: the same nR gets a
// different nL and MAC each time, and the MAC verifies only for the listener's
// own port.
func TestWrapListenerProofIsFreshAndBoundToItsPort(t *testing.T) {
	l := listenOpts(t, Options{Key: testKey, Allow: allowAll, Timeout: 5 * time.Second})
	nR, port := nonceOf(0x61), portOf(l)
	_, _, first, _ := challenge(t, l, nR)
	_, _, second, _ := challenge(t, l, nR)
	if bytes.Equal(first, second) || bytes.Equal(nLOf(first)[:], nLOf(second)[:]) {
		t.Fatal("two handshakes with the same relay nonce got the same listener proof: it is not fresh")
	}
	for i, line := range [][]byte{first, second} {
		nL := nLOf(line)
		want := testKey.proof(listenerLabel, port, nR, nL)
		if got := line[65:129]; string(got) != hex.EncodeToString(want[:]) {
			t.Errorf("proof %d does not verify for the listener's own port", i)
		}
		other := testKey.proof(listenerLabel, port+1, nR, nL)
		if string(line[65:129]) == hex.EncodeToString(other[:]) {
			t.Errorf("proof %d also verifies for port %d: the port is not in the MAC", i, port+1)
		}
	}
}

// Before the relay has proven the key the listener writes its proof line and
// nothing else, not even a caller's bytes.
func TestWrapWritesOnlyItsProofBeforeTheRelayProof(t *testing.T) {
	l := listenOpts(t, Options{Key: testKey, Allow: allowAll, Timeout: 200 * time.Millisecond})
	cl := dial(t, l)
	if _, err := io.WriteString(cl, goodHello(nonceOf(0x61))); err != nil {
		t.Fatal(err)
	}
	sc := accept(t, l)
	n, err := sc.Write([]byte("secret"))
	if !errors.Is(err, ErrHeader) || n != 0 {
		t.Fatalf("Write = %d, %v; want 0, ErrHeader", n, err)
	}
	got := readUntilClosed(t, cl)
	if len(got) != ListenerProofLen || !listenerProofRE.Match(got) {
		t.Fatalf("the client received %d bytes, want exactly one %d-byte proof line", len(got), ListenerProofLen)
	}
	if bytes.Contains(got, []byte("secret")) {
		t.Fatal("the caller's bytes reached a relay that had not proven the key")
	}
}

// One deadline covers hello, proof_L, proof_R and the PROXY line, so a relay
// cannot take Timeout per phase.
func TestWrapOneDeadlineCoversTheWholeHandshake(t *testing.T) {
	const timeout = 300 * time.Millisecond
	t.Run("a trickled relay proof", func(t *testing.T) {
		var r refusals
		l := listenOpts(t, r.opts(testKey, timeout))
		nR, port := nonceOf(0x61), portOf(l)
		cl := dial(t, l)
		go func() {
			io.WriteString(cl, goodHello(nR))
			line := make([]byte, ListenerProofLen)
			if _, err := io.ReadFull(cl, line); err != nil {
				return
			}
			for _, b := range relayProofLine(testKey, port, nR, nLOf(line)) {
				if _, err := cl.Write([]byte{b}); err != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		sc := accept(t, l)
		start := time.Now()
		readRefused(t, sc)
		if el := time.Since(start); el > time.Second {
			t.Fatalf("refused after %v: a trickled proof is not bounded by the timeout", el)
		}
		if got := r.n.Load(); got != 1 {
			t.Fatalf("KeyRefused called %d times, want 1", got)
		}
	})
	t.Run("each phase under the timeout and the sum over it", func(t *testing.T) {
		var r refusals
		l := listenOpts(t, r.opts(testKey, timeout))
		nR, port := nonceOf(0x61), portOf(l)
		cl := dial(t, l)
		go func() {
			time.Sleep(200 * time.Millisecond)
			io.WriteString(cl, goodHello(nR))
			line := make([]byte, ListenerProofLen)
			if _, err := io.ReadFull(cl, line); err != nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
			cl.Write(relayProofLine(testKey, port, nR, nLOf(line)))
			io.WriteString(cl, goodHeader)
		}()
		sc := accept(t, l)
		start := time.Now()
		err := readRefused(t, sc)
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("err = %v, want a timeout", err)
		}
		if el := time.Since(start); el < 250*time.Millisecond || el > 450*time.Millisecond {
			t.Fatalf("refused after %v, want about %v (the phases share one deadline)", el, timeout)
		}
		if got := r.n.Load(); got != 1 {
			t.Fatalf("KeyRefused called %d times, want 1", got)
		}
	})
}

// The relay proves the key and then says nothing: the PROXY line is missing,
// which is a header failure and not a key refusal. The PROXY line shares the
// handshake's one deadline: the relay's hello comes 200 ms in and the handshake
// is over a moment later, so a connection that is closed at about Timeout (300 ms
// from accept) was held to the deadline set at the start, and one closed at
// 500 ms got a fresh deadline for the PROXY line.
func TestWrapHandshakeThenSilenceIsNotAKeyRefusal(t *testing.T) {
	const timeout = 300 * time.Millisecond
	var r refusals
	l := listenOpts(t, r.opts(testKey, timeout))
	port := portOf(l)
	cl := dial(t, l)
	cl.SetDeadline(time.Now().Add(10 * time.Second))
	go func() {
		time.Sleep(200 * time.Millisecond)
		RelayHandshake(cl, testKey, port)
	}()
	sc := accept(t, l)
	start := time.Now()
	err := readRefused(t, sc)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if el := time.Since(start); el < 250*time.Millisecond || el > 450*time.Millisecond {
		t.Fatalf("closed after %v, want about %v (the PROXY line shares the handshake's deadline)", el, timeout)
	}
	if got := r.n.Load(); got != 0 {
		t.Fatalf("KeyRefused called %d times for a relay that proved the key", got)
	}
}

// The handshake owns both deadlines; afterwards neither it nor its leftovers
// limit the connection.
func TestWrapClearsItsHandshakeDeadlines(t *testing.T) {
	l := listenOpts(t, Options{Key: testKey, Allow: allowAll, Timeout: 200 * time.Millisecond})
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader)
	sc := accept(t, l)
	if got := sc.RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("RemoteAddr = %s", got)
	}
	time.Sleep(400 * time.Millisecond) // past the handshake's deadline

	if _, err := sc.Write([]byte("late")); err != nil {
		t.Fatalf("Write after the handshake deadline: %v", err)
	}
	reply := make([]byte, 4)
	cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(cl, reply); err != nil || string(reply) != "late" {
		t.Fatalf("client received %q, %v", reply, err)
	}

	got := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(sc, make([]byte, 4))
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("Read ended after the handshake deadline instead of waiting for data: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := io.WriteString(cl, "data"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read never returned the data")
	}
}

// A write deadline the caller set before the connection was first used is held
// across the handshake and applied afterwards, like the read deadline.
func TestWrapKeepsTheCallersWriteDeadline(t *testing.T) {
	l := listenOpts(t, Options{Key: testKey, Allow: allowAll, Timeout: 5 * time.Second})
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader)
	sc := accept(t, l)
	if err := sc.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := sc.RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("the handshake did not complete under an expired caller deadline: RemoteAddr = %s", got)
	}
	_, err := sc.Write([]byte("x"))
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("Write err = %v, want the caller's deadline to apply", err)
	}
}

// With no key there is nothing to prove: a well-formed hello gets no answer.
func TestWrapZeroKeyAnswersNothing(t *testing.T) {
	rf := refuseHello(t, Key{}, goodHello(nonceOf(0x61)))
	seesNothing(t, rf.cl)
	if got := rf.r.n.Load(); got != 1 {
		t.Fatalf("KeyRefused called %d times, want 1", got)
	}
}

// A conn that cannot name its own port cannot prove it to the relay: refuse,
// without a panic (a nil address is what a fake or an exotic conn reports).
func TestWrapRefusesAnUnusableLocalAddress(t *testing.T) {
	for name, local := range map[string]net.Addr{
		"none":         nil,
		"not a socket": &net.UnixAddr{Name: "x", Net: "unix"},
	} {
		t.Run(name, func(t *testing.T) {
			cs, ss := net.Pipe()
			defer cs.Close()
			go cs.Write([]byte(goodHello(nonceOf(0x61))))
			var r refusals
			l := Wrap(&fakeListener{conn: fakeConn{Conn: ss, remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, local: local}}, r.opts(testKey, time.Second))
			c, err := l.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			readRefused(t, c)
			if got := r.n.Load(); got != 1 {
				t.Fatalf("KeyRefused called %d times, want 1", got)
			}
			cs.SetReadDeadline(time.Now().Add(2 * time.Second))
			if n, err := cs.Read(make([]byte, 16)); n != 0 || err != io.EOF {
				t.Fatalf("the peer received %d bytes (err %v), want none and EOF", n, err)
			}
		})
	}
}

// Until the relay's proof matched, nothing the peer sent is in an error: not
// the key, not its nonce, not its proof.
func TestWrapRefusalErrorsHoldNoReceivedBytes(t *testing.T) {
	hexRun := regexp.MustCompile(`[0-9a-fA-F]{32,}`)
	check := func(t *testing.T, rf *refusal) {
		t.Helper()
		msg := rf.err.Error()
		if hexRun.MatchString(msg) {
			t.Errorf("the error holds a long hex run: %q", msg)
		}
		for _, secret := range []string{testKey.Hex(), otherKey.Hex()} {
			if strings.Contains(msg, secret) {
				t.Errorf("the error holds a key: %q", msg)
			}
		}
		for _, run := range hexRun.FindAll(rf.sent, -1) {
			if strings.Contains(msg, string(run)) || strings.Contains(strings.ToLower(msg), strings.ToLower(string(run))) {
				t.Errorf("the error holds hex the peer sent: %q", msg)
			}
		}
	}
	for _, tc := range malformedHellos {
		t.Run(tc.name, func(t *testing.T) { check(t, refuseHello(t, testKey, tc.payload)) })
	}
	for i, tc := range badProofs {
		t.Run(tc.name, func(t *testing.T) { check(t, refuseProof(t, i)) })
	}
}
