package mask

import (
	"strings"
	"testing"
)

const (
	key    = "AKIAIOSFODNN7EXAMPLE"
	secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	ghp    = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
)

var displayCases = []struct {
	name, in, want string
}{
	{"plain", "plain text", "plain text"},
	{"empty", "", ""},
	{"access key", key, "[masked:aws-key-id]"},
	{"sts key", "ASIAIOSFODNN7EXAMPLE", "[masked:aws-key-id]"},
	{"key in sentence", "Found it: " + key + " in .env", "Found it: [masked:aws-key-id] in .env"},
	{"two keys", key + " and ASIAIOSFODNN7EXAMPLE", "[masked:aws-key-id] and [masked:aws-key-id]"},
	{"session token iqo", "tok=IQoJb3JpZ2luX2VjEBkaCXVzLWVhc3QtMQ== end", "tok=[masked:aws-session-token] end"},
	{"session token fwo", "FwoGZXIvYXdzEBYaDHqa0AP1abcdefghijkl", "[masked:aws-session-token]"},
	{"secret keyed", "aws_secret_access_key = " + secret, "aws_secret_access_key = [masked:aws-secret]"},
	{"secret json", `"SecretAccessKey": "` + secret + `"`, `"SecretAccessKey": "[masked:aws-secret]"`},
	{"secret unkeyed stays", "value " + secret, "value " + secret},
	{"lowercase akia stays", "akiaiosfodnn7example", "akiaiosfodnn7example"},
	{"short akia stays", "AKIA" + "IOSFODNN7EXAMPL", "AKIA" + "IOSFODNN7EXAMPL"},
	{"github ghp", "t " + ghp, "t [masked:github-token]"},
	{"github pat", "github_pat_" + strings.Repeat("a1_", 10), "[masked:github-token]"},
	{"anthropic", "key sk-ant-api03-abcdefghijklmnopqrstuv", "key [masked:anthropic-key]"},
	{"short sk-ant stays", "sk-ant-short", "sk-ant-short"},
	{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIB\nabc\n-----END RSA PRIVATE KEY-----", "[masked:private-key]"},
	{"pem middle", "before\n-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\nafter", "before\n[masked:private-key]\nafter"},
	{"pem unterminated", "x -----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\nmore text", "x [masked:private-key]"},
	{"key inside pem masked once", "-----BEGIN PRIVATE KEY-----\n" + key + "\n-----END PRIVATE KEY-----", "[masked:private-key]"},
}

func TestDisplay(t *testing.T) {
	for _, c := range displayCases {
		t.Run(c.name, func(t *testing.T) {
			if got := Display(c.in); got != c.want {
				t.Fatalf("Display(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestDisplayIsIdempotent(t *testing.T) {
	for _, c := range displayCases {
		once := Display(c.in)
		if twice := Display(once); twice != once {
			t.Errorf("%s: Display not idempotent: %q then %q", c.name, once, twice)
		}
	}
}

func TestDisplayNoMatchDoesNotAllocate(t *testing.T) {
	s := strings.Repeat("an ordinary line of agent output, nothing secret here. ", 50)
	if n := testing.AllocsPerRun(100, func() { _ = Display(s) }); n != 0 {
		t.Fatalf("allocs = %v, want 0", n)
	}
}

func TestDisplayInvalidUTF8(t *testing.T) {
	in := "bad \xff\xfe bytes " + key
	want := "bad \xff\xfe bytes [masked:aws-key-id]"
	if got := Display(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
