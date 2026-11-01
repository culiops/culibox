package secretstore

import (
	"strings"
	"testing"
)

func TestKeychainReason(t *testing.T) {
	long := strings.Repeat("x", 500) + "\n"
	cases := []struct {
		name   string
		stderr string
		code   int
		want   string
	}{
		{"prefix stripped", "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.\n", 44,
			"SecKeychainSearchCopyNext: The specified item could not be found in the keychain."},
		{"empty stderr", "", 51, "security exited 51"},
		{"blank first line only", "\n\n", 7, "security exited 7"},
		{"first line only", "first\nsecond\n", 1, "first"},
		{"truncated", long, 1, strings.Repeat("x", 200)},
	}
	for _, c := range cases {
		if got := keychainReason([]byte(c.stderr), c.code); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	got := keychainReason([]byte("\x1b[31mbad\n"), 1)
	if strings.ContainsRune(got, 0x1b) || got == "" {
		t.Fatalf("control byte kept or empty: %q", got)
	}
	for _, r := range keychainReason([]byte("a\x00b\x07c\x7fd\xffe\n"), 1) {
		if r < 0x20 || r > 0x7e {
			t.Fatalf("non-printable rune %q kept", r)
		}
	}
}
