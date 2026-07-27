package session

import (
	"strings"
	"testing"
)

func TestPreambleTrimmer_StripsLeadingPreamble(t *testing.T) {
	var tr preambleTrimmer
	got := string(tr.filter([]byte(attachPreamble + "\x1b[Hhello")))
	if want := "\x1b[Hhello"; got != want {
		t.Fatalf("filter=%q, want %q", got, want)
	}
}

// A PTY read can split the 8-byte sequence anywhere; feeding it one byte at a
// time is the worst case and must produce the same stream.
func TestPreambleTrimmer_StripsAcrossSplitReads(t *testing.T) {
	var tr preambleTrimmer
	var b strings.Builder
	for _, c := range []byte(attachPreamble + "\x1b[Hhello") {
		b.Write(tr.filter([]byte{c}))
	}
	b.Write(tr.flush())
	if want := "\x1b[Hhello"; b.String() != want {
		t.Fatalf("filter=%q, want %q", b.String(), want)
	}
}

func TestPreambleTrimmer_LeavesOtherStreamsAlone(t *testing.T) {
	var tr preambleTrimmer
	got := string(tr.filter([]byte("hello world")))
	if got != "hello world" {
		t.Fatalf("filter=%q, want %q", got, "hello world")
	}
}

// An inner TUI (vim, less) switches to the alternate screen later in the
// stream. That switch must survive — only abduco's attach preamble goes.
func TestPreambleTrimmer_KeepsLaterAltScreenSwitch(t *testing.T) {
	var tr preambleTrimmer
	var b strings.Builder
	b.Write(tr.filter([]byte("$ vim\r\n")))
	b.Write(tr.filter([]byte(attachPreamble + "~")))
	if want := "$ vim\r\n" + attachPreamble + "~"; b.String() != want {
		t.Fatalf("filter=%q, want %q", b.String(), want)
	}
}

// A session that dies mid-preamble must not swallow the bytes it held back.
func TestPreambleTrimmer_FlushesPartialPreamble(t *testing.T) {
	var tr preambleTrimmer
	if got := tr.filter([]byte("\x1b[?10")); len(got) != 0 {
		t.Fatalf("filter=%q, want held (empty)", got)
	}
	if got := string(tr.flush()); got != "\x1b[?10" {
		t.Fatalf("flush=%q, want %q", got, "\x1b[?10")
	}
}

func TestPreambleTrimmer_FlushIsEmptyOnceDone(t *testing.T) {
	var tr preambleTrimmer
	tr.filter([]byte(attachPreamble))
	if got := tr.flush(); len(got) != 0 {
		t.Fatalf("flush=%q, want empty", got)
	}
}
