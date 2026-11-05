package proxyline

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// The relay handshake replaces the static key line (Key.Preamble) with a mutual
// challenge-response, so the key never crosses the wire and the relay forwards
// nothing until the listener has proven it holds the key. Every line has a
// fixed length and uses lowercase hex only, so each side reads an exact byte
// count and never has to decide where a line ends:
//
//	relay    -> listener  "AGENTBOX-RELAY2 " || hex(nR) || "\r\n"      HelloLen         = 82
//	listener -> relay     hex(nL) || " " || hex(MAC_L) || "\r\n"        ListenerProofLen = 131
//	relay    -> listener  hex(MAC_R) || "\r\n"                          RelayProofLen    = 66
//
//	MAC_X = HMAC-SHA256(key, label_X || 0x00 || uint16be(port) || nR || nL)
//
// nR and nL are 32 random bytes each. The input is unambiguous: the label is
// one of two NUL-terminated constants and every later field is fixed-width. The
// labels separate the directions, so a listener can never be made to produce a
// relay proof. The port is the one the relay dialed, which is the listener's own
// local port, so a process holding one port cannot pass the relay's challenge
// through CuliBox's listener on another. nR makes every listener proof
// single-use and nL makes every relay proof single-use.
const (
	// HelloLen is the length of the relay's hello: the prefix, 64 hex characters
	// and CRLF.
	HelloLen = 82
	// ListenerProofLen is the length of the listener's proof line: its nonce, a
	// space, its MAC and CRLF.
	ListenerProofLen = 131
	// RelayProofLen is the length of the relay's proof line: its MAC and CRLF.
	RelayProofLen = 66

	helloPrefix   = "AGENTBOX-RELAY2 "
	listenerLabel = "culibox relay2 listener"
	relayLabel    = "culibox relay2 relay"
)

// proof returns the MAC for one direction of the handshake. label is
// listenerLabel or relayLabel, port the port the relay dialed, nR the relay's
// nonce and nL the listener's.
func (k Key) proof(label string, port uint16, nR, nL *[32]byte) [32]byte {
	m := hmac.New(sha256.New, k[:])
	m.Write([]byte(label))
	m.Write([]byte{0, byte(port >> 8), byte(port)})
	m.Write(nR[:])
	m.Write(nL[:])
	var mac [32]byte
	copy(mac[:], m.Sum(nil))
	return mac
}

// listenerProofLine returns the 131 bytes the listener answers a hello with:
// hex(nL), a space, hex(MAC_L) and CRLF. The relay's check and the listener's
// answer share it, so the two halves cannot drift apart.
func listenerProofLine(k Key, port uint16, nR, nL *[32]byte) []byte {
	mac := k.proof(listenerLabel, port, nR, nL)
	return []byte(hex.EncodeToString(nL[:]) + " " + hex.EncodeToString(mac[:]) + "\r\n")
}

// relayProofLine returns the 66 bytes the relay writes once the listener's proof
// has matched: hex(MAC_R) and CRLF.
func relayProofLine(k Key, port uint16, nR, nL *[32]byte) []byte {
	mac := k.proof(relayLabel, port, nR, nL)
	return []byte(hex.EncodeToString(mac[:]) + "\r\n")
}

// listenerProofRE is the only form a listener's proof line may take: lowercase
// hex only, so one MAC has exactly one encoding.
var listenerProofRE = regexp.MustCompile(`^[0-9a-f]{64} [0-9a-f]{64}\r\n$`)

// errListenerProof is the one error for a listener proof that is malformed or
// does not match. It never carries what the peer sent: until the proof matches,
// the peer is not known to hold the key, so its bytes do not go into an error
// or a log line.
var errListenerProof = errors.New("proxyline: listener proof missing or wrong")

// RelayHandshake is the relay's half in Go, the reference for
// boxvm.relayScript; the daemon never dials; tests in other packages dial a
// wrapped listener with it.
//
// It writes the hello with a fresh random nonce, reads exactly ListenerProofLen
// bytes and checks them against the MAC it computes under k for port (the port
// it dialed). Only when they match does it write the relay's proof, so a peer
// that does not hold the key receives nothing but the hello. It does not write
// the PROXY line: the caller does, after RelayHandshake returns nil, and the
// caller closes rw when it returns an error.
//
// A zero key is refused before anything is written. Errors are fixed strings
// plus wrapped I/O errors; none carries a byte the peer sent.
func RelayHandshake(rw io.ReadWriter, k Key, port uint16) error {
	if k.IsZero() {
		return errors.New("proxyline: relay key must not be all zero")
	}
	var nR [32]byte
	if _, err := rand.Read(nR[:]); err != nil {
		return fmt.Errorf("proxyline: relay nonce: %w", err)
	}
	if _, err := io.WriteString(rw, helloPrefix+hex.EncodeToString(nR[:])+"\r\n"); err != nil {
		return fmt.Errorf("proxyline: writing hello: %w", err)
	}

	var line [ListenerProofLen]byte
	if _, err := io.ReadFull(rw, line[:]); err != nil {
		return fmt.Errorf("%w: %w", errListenerProof, err)
	}
	if !listenerProofRE.Match(line[:]) {
		return errListenerProof
	}
	// The pattern guarantees both fields are 64 lowercase hex characters, so
	// neither decode can fail.
	var nL, got [32]byte
	hex.Decode(nL[:], line[:64])
	hex.Decode(got[:], line[65:129])
	want := k.proof(listenerLabel, port, &nR, &nL)
	if !hmac.Equal(got[:], want[:]) {
		return errListenerProof
	}

	if _, err := rw.Write(relayProofLine(k, port, &nR, &nL)); err != nil {
		return fmt.Errorf("proxyline: writing relay proof: %w", err)
	}
	return nil
}
