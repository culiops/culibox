package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/upgradebackup"
)

var upgradeNow = time.Date(2026, 11, 13, 10, 30, 0, 0, time.UTC)

type upLog struct{ lines []string }

func (l *upLog) logf(f string, a ...any) { l.lines = append(l.lines, fmt.Sprintf(f, a...)) }
func (l *upLog) joined() string          { return strings.Join(l.lines, "\n") }

func upRoot(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	if err := os.WriteFile(filepath.Join(r, "profiles.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r, "last-version"), []byte("v0 old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUpgradeBackupLogsRestoreLine(t *testing.T) {
	r := upRoot(t)
	var l upLog
	if err := upgradeBackup(r, nil, "v1", "abc", upgradeNow, "/x/culibox", l.logf); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(r, "backups"))
	if len(ents) != 1 {
		t.Fatalf("backups: %v", ents)
	}
	dir := filepath.Join(r, "backups", ents[0].Name())
	out := l.joined()
	for _, want := range []string{upgradebackup.RestoreCommand(r, dir), "docs/UPGRADING.md", dir, "v0"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestUpgradeBackupNoChangeLogsNothing(t *testing.T) {
	r := upRoot(t)
	if err := os.WriteFile(filepath.Join(r, "last-version"), []byte("v1 abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var l upLog
	if err := upgradeBackup(r, nil, "v1", "abc", upgradeNow, "/x/culibox", l.logf); err != nil {
		t.Fatal(err)
	}
	if len(l.lines) != 0 {
		t.Fatalf("logged: %v", l.lines)
	}
}

func TestUpgradeBackupFailClosedMessage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a chmod-000 file")
	}
	r := upRoot(t)
	tok := filepath.Join(r, "tokens.json")
	if err := os.WriteFile(tok, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tok, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(tok, 0o600) })

	exe := "/opt/bin/culibox"
	err := upgradeBackup(r, nil, "v1", "abc", upgradeNow, exe, (&upLog{}).logf)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"upgrade backup failed", "tokens.json", exe + ".prev", "docs/UPGRADING.md", "Nothing in the data root was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestUpgradeBackupConfigInRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(r string) []string
		want bool
	}{
		{"in root", func(r string) []string { return []string{filepath.Join(r, "config.json")} }, true},
		{"in root, unclean", func(r string) []string { return []string{filepath.Join(r, "sub", "..", "config.json")} }, true},
		{"elsewhere", func(r string) []string { return []string{"/home/u/.config/culibox/config.json"} }, false},
		{"none", func(r string) []string { return nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := upRoot(t)
			if err := os.WriteFile(filepath.Join(r, "config.json"), []byte(`{"a":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := upgradeBackup(r, tc.cfg(r), "v1", "abc", upgradeNow, "/x/culibox", (&upLog{}).logf); err != nil {
				t.Fatal(err)
			}
			ents, _ := os.ReadDir(filepath.Join(r, "backups"))
			if len(ents) != 1 {
				t.Fatalf("backups: %v", ents)
			}
			_, err := os.Stat(filepath.Join(r, "backups", ents[0].Name(), "config.json"))
			if got := err == nil; got != tc.want {
				t.Fatalf("config.json in backup = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpgradeBackupDevBuildNoop(t *testing.T) {
	r := upRoot(t)
	if err := upgradeBackup(r, nil, "", "", upgradeNow, "/x/culibox", (&upLog{}).logf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r, "backups")); err == nil {
		t.Fatal("dev build made a backup")
	}
}
