package broker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestNewNonceShapeAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	a, b := NewNonce(), NewNonce()
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("nonce shape: %q %q", a, b)
	}
	if a == b {
		t.Fatalf("two nonces equal: %q", a)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	nonce := NewNonce()
	tok := Token(testSecret, "claude-3", nonce)
	sid, n, ok := ParseToken(testSecret, tok)
	if !ok || sid != "claude-3" || n != nonce {
		t.Fatalf("round trip: %q %q %v", sid, n, ok)
	}
}

func TestTokenFormat(t *testing.T) {
	nonce := NewNonce()
	tok := Token(testSecret, "claude-3", nonce)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != "claude-3" || parts[1] != nonce || len(parts[2]) != 64 {
		t.Fatalf("format: %q", tok)
	}
}

func TestTokenDiffersPerNonce(t *testing.T) {
	// Session ids are reused after a restart; a fresh nonce must change the token.
	if Token(testSecret, "claude-3", NewNonce()) == Token(testSecret, "claude-3", NewNonce()) {
		t.Fatal("tokens for the same id but different nonces are equal")
	}
}

func TestTokenRejectsForgeries(t *testing.T) {
	other := []byte("ffffffffffffffffffffffffffffffff")
	nonce := NewNonce()
	nonce2 := NewNonce()
	tok := Token(testSecret, "claude-3", nonce)
	mac := tok[strings.LastIndexByte(tok, '.')+1:]
	for name, bad := range map[string]string{
		"empty":           "",
		"no mac":          "claude-3",
		"no mac with dot": "claude-3." + nonce,
		"other secret":    Token(other, "claude-3", nonce),
		"swapped id":      "claude-4" + tok[len("claude-3"):],
		"swapped nonce":   "claude-3." + nonce2 + "." + mac,
		"truncated mac":   tok[:len(tok)-1],
		"extended mac":    tok + "0",
		"short nonce":     Token(testSecret, "claude-3", nonce[:31]),
		"long nonce":      Token(testSecret, "claude-3", nonce+"0"),
		"uppercase nonce": Token(testSecret, "claude-3", strings.ToUpper(nonce)),
		"non-hex nonce":   Token(testSecret, "claude-3", strings.Repeat("z", 32)),
		"empty id":        Token(testSecret, "", nonce),
		"dots only":       "..",
		"dot only":        ".",
		"uppercase mac":   "claude-3." + nonce + "." + strings.ToUpper(mac),
	} {
		if sid, n, ok := ParseToken(testSecret, bad); ok || sid != "" || n != "" {
			t.Errorf("%s accepted: %q %q %v", name, sid, n, ok)
		}
	}
}

func TestParseTokenParsesFromTheRight(t *testing.T) {
	// A session id never contains '.', but parsing from the right must still
	// recover the whole id rather than being fooled by an embedded dot.
	nonce := NewNonce()
	tok := Token(testSecret, "a.b", nonce)
	sid, n, ok := ParseToken(testSecret, tok)
	if !ok || sid != "a.b" || n != nonce {
		t.Fatalf("got %q %q %v", sid, n, ok)
	}
}

// The refactor onto sesstoken must not change a single byte: live PROD
// sessions hold tokens minted by the old code.
func TestBrokerTokenBytesUnchanged(t *testing.T) {
	sec := []byte("0123456789abcdef0123456789abcdef")
	n := "00112233445566778899aabbccddeeff"
	h := hmac.New(sha256.New, sec)
	h.Write([]byte("broker\x00claude-3\x00" + n))
	want := "claude-3." + n + "." + hex.EncodeToString(h.Sum(nil))
	if got := Token(sec, "claude-3", n); got != want {
		t.Fatalf("Token = %q, want %q", got, want)
	}
}
