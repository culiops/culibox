package secretshape

import (
	"bytes"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	alnum := func(n int) string { return strings.Repeat("aB3", n)[:n] }
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"pem openssh", "-----BEGIN OPENSSH PRIVATE KEY-----", "pem-private-key"},
		{"pem rsa", "x\n-----BEGIN RSA PRIVATE KEY-----\nabc", "pem-private-key"},
		{"github classic", "ghp_" + alnum(36), "token"},
		{"github pat", "github_pat_11ABCDEFG0abcdefghij_klmnop", "token"},
		{"slack", "xoxb-1-2-abcdefghij", "token"},
		{"gitlab", "glpat-" + alnum(20), "token"},
		{"anthropic", "sk-ant-api03-" + alnum(24), "token"},
		{"google", "AIza" + alnum(35), "token"},
		{"token in text", "key is ghp_" + alnum(36) + " ok", "token"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJl", "jwt"},
		{"aws secret", "aws_secret_access_key = abc/DEF+123", "secret-assignment"},
		{"json SecretAccessKey", `"SecretAccessKey": "x"`, "secret-assignment"},
		{"json refresh_token", `"refresh_token":"x"`, "secret-assignment"},
		{"password yaml", "password: hunter2", "secret-assignment"},
		{"api_key", "api_key=1", "secret-assignment"},
		{"apikey upper", "APIKEY=abc", "secret-assignment"},
		{"session token", "aws_session_token=FwoGZXIv", "secret-assignment"},
		{"client_secret", `client_secret: "s3"`, "secret-assignment"},
		{"access_token", `"access_token" : "t"`, "secret-assignment"},
		{"SessionToken", `"SessionToken":"x"`, "secret-assignment"},

		{"akia alone", "AKIAIOSFODNN7EXAMPLE", ""},
		{"asia in json", `"accessKeyId":"ASIAXXXXXXXXXXXXXXXX"`, ""},
		{"empty password", "password:", ""},
		{"empty quoted password", `password: ""`, ""},
		{"password then newline", "password:\nnext line", ""},
		{"prose", "The quick brown fox jumps over the lazy dog.", ""},
		{"risk-free prose", "risk-free-tier-limits-apply", ""},
		{"zeros", string(make([]byte, 4096)), ""},
	}
	for _, c := range cases {
		got, ok := Find([]byte(c.in))
		if c.want == "" {
			if ok || got != "" {
				t.Errorf("%s: got (%q,%v), want no hit", c.name, got, ok)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want %q", c.name, got, ok, c.want)
		}
	}
	if _, ok := Find(bytes.Repeat([]byte{0}, 10)); ok {
		t.Error("zeros hit")
	}
}
