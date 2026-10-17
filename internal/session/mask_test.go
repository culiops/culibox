package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

const exampleKey = "AKIAIOSFODNN7EXAMPLE"

func TestMaskNeverTouchesAskFull(t *testing.T) {
	id := "msg_mask"
	cmd := "aws configure set aws_access_key_id " + exampleKey
	p, ok := parsePeekTail(txBytes(txUser("go"), txRecID("assistant", id, txBash(cmd))))
	if !ok || !p.Pending {
		t.Fatalf("peek = %+v", p)
	}
	if strings.Contains(p.Ask, exampleKey) || !strings.Contains(p.Ask, "[masked:aws-key-id]") {
		t.Fatalf("Ask not masked: %q", p.Ask)
	}
	if p.AskFull != "Bash("+cmd+")" {
		t.Fatalf("AskFull changed: %q", p.AskFull)
	}
	sum := sha256.Sum256([]byte("Bash(" + cmd + ")"))
	if !askHashMatches(p.AskFull, hex.EncodeToString(sum[:])) {
		t.Fatal("the approval hash no longer matches the command that runs")
	}
}

func TestPeekTextAndSummaryMasked(t *testing.T) {
	text := "Found it: " + exampleKey + " in .env"
	p, ok := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txText(text))))
	if !ok || strings.Contains(p.Text, exampleKey) {
		t.Fatalf("Text = %q", p.Text)
	}
	if s := summaryLine(p.Text); strings.Contains(s, exampleKey) || !strings.Contains(s, "[masked:aws-key-id]") {
		t.Fatalf("summary = %q", s)
	}
}

// A key past askMax must not leak a maskable prefix after truncation.
func TestAskMaskedBeforeTruncation(t *testing.T) {
	// "Bash(" + 64 x + space puts the key at rune 70, so the 80-rune cut would
	// fall inside the key if truncation ran first.
	cmd := strings.Repeat("x", 64) + " " + exampleKey + " tail"
	p, ok := parsePeekTail(txBytes(txUser("go"), txRec("assistant", txBash(cmd))))
	if !ok || !p.Pending {
		t.Fatalf("peek = %+v", p)
	}
	if strings.Contains(p.Ask, "AKIA") || !strings.Contains(p.Ask, "[masked:") {
		t.Fatalf("Ask leaks a key prefix or is not masked: %q", p.Ask)
	}
	if r := []rune(p.Ask); len(r) > askMax {
		t.Fatalf("Ask is %d runes, want <= %d", len(r), askMax)
	}
	if !strings.Contains(p.AskFull, exampleKey) {
		t.Fatalf("AskFull must stay raw: %q", p.AskFull)
	}
}

// List ships Info.AskFull raw for the reply card and Info.Ask/Summary masked.
func TestListMasksAskButNotAskFull(t *testing.T) {
	m, home := newLocatorManager(t)
	cmd := "aws configure set aws_access_key_id " + exampleKey
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(),
		txUser("go"),
		txRec("assistant", txText("Found "+exampleKey+" in .env")),
		txRec("assistant", txBash(cmd)))
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	s.status = statusLive

	got := m.List()[0]
	if got.Need != "ask" {
		t.Fatalf("Need = %q, want ask (fixture drifted): %+v", got.Need, got)
	}
	if strings.Contains(got.Ask, exampleKey) || !strings.Contains(got.Ask, "[masked:aws-key-id]") {
		t.Errorf("Info.Ask not masked: %q", got.Ask)
	}
	if strings.Contains(got.Summary, exampleKey) || !strings.Contains(got.Summary, "[masked:aws-key-id]") {
		t.Errorf("Info.Summary not masked: %q", got.Summary)
	}
	if got.AskFull != "Bash("+cmd+")" {
		t.Errorf("Info.AskFull = %q, want the raw command", got.AskFull)
	}
}
