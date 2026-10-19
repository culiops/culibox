package ghapp

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignJWTClaims(t *testing.T) {
	k := genKey(t)
	now := time.Unix(1_800_000_000, 0)
	tok, err := SignJWT(k, "Iv1.abc", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var hdr map[string]string
	if err := json.Unmarshal(dec(parts[0]), &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		t.Fatalf("header = %v", hdr)
	}
	var c struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(dec(parts[1]), &c); err != nil {
		t.Fatal(err)
	}
	if c.Iss != "Iv1.abc" || c.Iat != now.Unix()-60 || c.Exp != now.Unix()+540 {
		t.Fatalf("claims = %+v", c)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
		t.Fatalf("signature: %v", err)
	}
}
