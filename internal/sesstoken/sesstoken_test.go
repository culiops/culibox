package sesstoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

const nonce = "00112233445566778899aabbccddeeff"

func TestMintFormatIsIndependentlyComputable(t *testing.T) {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("egress\x00claude-3\x00" + nonce))
	want := "claude-3." + nonce + "." + hex.EncodeToString(h.Sum(nil))
	if got := Mint(DomainEgress, secret, "claude-3", nonce); got != want {
		t.Fatalf("Mint = %q, want %q", got, want)
	}
}

func TestDomainsDoNotCrossVerify(t *testing.T) {
	b := Mint(DomainBroker, secret, "claude-3", nonce)
	if _, _, ok := Parse(DomainEgress, secret, b); ok {
		t.Fatal("a broker token verified as an egress token")
	}
	e := Mint(DomainEgress, secret, "claude-3", nonce)
	if _, _, ok := Parse(DomainBroker, secret, e); ok {
		t.Fatal("an egress token verified as a broker token")
	}
	if sid, n, ok := Parse(DomainEgress, secret, e); !ok || sid != "claude-3" || n != nonce {
		t.Fatalf("own domain: %q %q %v", sid, n, ok)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	good := Mint(DomainEgress, secret, "a.b-1", nonce) // a dotted session id still parses (last two dots)
	if sid, _, ok := Parse(DomainEgress, secret, good); !ok || sid != "a.b-1" {
		t.Fatalf("dotted id: %q %v", sid, ok)
	}
	// Flip the last MAC char to something else (a fixed "0" is a no-op when the MAC already ends in 0).
	flipped := good[:len(good)-1] + "0"
	if good[len(good)-1] == '0' {
		flipped = good[:len(good)-1] + "1"
	}
	for _, tok := range []string{"", ".", "..", "x.y.z", "claude-3." + nonce + ".", "claude-3.NOTHEX" + nonce[6:] + ".ab",
		flipped, "." + nonce + "." + good[len(good)-64:]} {
		if _, _, ok := Parse(DomainEgress, secret, tok); ok {
			t.Fatalf("accepted %q", tok)
		}
	}
}
