package ghapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// SignJWT builds the RS256 App JWT the REST API wants: issued 60 s in the past
// (clock-skew allowance) and valid for 9 minutes from now (the maximum is 10).
func SignJWT(key *rsa.PrivateKey, clientID string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	hdr, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{now.Unix() - 60, now.Unix() + 9*60, clientID})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
