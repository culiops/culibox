package handback

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizePath(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"plain", "dir/a.pdf", "dir/a.pdf"},
		{"bidi override", "a‮txt.exe", "a?txt.exe"},
		{"escape", "a\x1b[31m", "a?[31m"},
		{"C0 and DEL", "a\x00b\nc\x7fd", "a?b?c?d"},
		{"C1", "a\u0085b\u009fc", "a?b?c"},
		{"line separators", "a b c", "a?b?c"},
		{"bidi isolates", "⁦a⁩", "?a?"},
		{"non-ASCII kept", "café/日本.txt", "café/日本.txt"},
		{"invalid UTF-8", "a\xffb", "a�b"},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizePath(tt.in); got != tt.want {
				t.Errorf("SanitizePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizePathKeepsTheTail(t *testing.T) {
	long := strings.Repeat("a", 2000-4) + ".pdf"
	got := SanitizePath(long)
	if n := utf8.RuneCountInString(got); n > 1024 {
		t.Fatalf("%d runes, want <= 1024", n)
	}
	if !strings.HasSuffix(got, ".pdf") || !strings.HasPrefix(got, "…") {
		t.Fatalf("tail lost: %q...%q", got[:8], got[len(got)-8:])
	}
	// Exactly at the cap is untouched; one over is cut.
	at := strings.Repeat("b", 1024)
	if SanitizePath(at) != at {
		t.Error("a 1,024-rune string must pass through")
	}
	if n := utf8.RuneCountInString(SanitizePath(at + "c")); n != 1024 {
		t.Errorf("1,025 runes cut to %d, want 1024", n)
	}
	// Multi-byte runes count as one each, and control runes are cleaned before the cut.
	wide := strings.Repeat("日", 1500) + "‮.pdf"
	got = SanitizePath(wide)
	if utf8.RuneCountInString(got) != 1024 || !strings.HasSuffix(got, "?.pdf") {
		t.Errorf("wide: %d runes, suffix %q", utf8.RuneCountInString(got), got[len(got)-8:])
	}
}

func TestMismatchSurvivesALongName(t *testing.T) {
	name := SanitizePath(strings.Repeat("x", 2000) + ".pdf")
	classes, _ := Classify(name, 0o644, elfHead(binary.LittleEndian, 2))
	if len(classes) == 0 || classes[0] != ClassMismatch {
		t.Fatalf("classes = %v, want extension-mismatch first", classes)
	}
}
