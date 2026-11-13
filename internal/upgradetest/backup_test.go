//go:build upgrade

package upgradetest

import (
	"strings"
	"testing"
)

func file(data string, mode uint32) Entry {
	e := Entry{Type: "file", Mode: 0o600, Size: int64(len(data)), Data: []byte(data)}
	if mode != 0 {
		e.Mode = 0o755
	}
	e.SHA256 = sha(data)
	return e
}

func dir() Entry { return Entry{Type: "dir", Mode: 0o700} }

// backupFixture is a pre-upgrade root r/ and the after-state with a correct
// backup of it.
func backupFixture() (before, after Snapshot) {
	before = Snapshot{
		"r":                                      dir(),
		"r/profiles.json":                        file(`[]`, 0),
		"r/tokens.json":                          file(`[]`, 0),
		"r/store":                                dir(),
		"r/store/recipient":                      file("age1x", 0),
		"r/accounts":                             dir(),
		"r/accounts/alt":                         dir(),
		"r/accounts/alt/claude":                  dir(),
		"r/accounts/alt/claude/settings.json":    file(`{"a":1}`, 0),
		"r/accounts/alt/claude/projects":         dir(),
		"r/accounts/alt/claude/projects/x.jsonl": file("t", 0),
		"r/bin":                                  dir(),
		"r/bin/statusline-ratelimits.sh":         file("#!/bin/sh", 1),
		"r/audit.db":                             file("db", 0),
		"r/analytics.db":                         file("db", 0),
	}
	after = Snapshot{}
	for k, v := range before {
		after[k] = v
	}
	b := "r/backups/unknown-to-v2-20261113T120000Z"
	after["r/backups"] = dir()
	after[b] = dir()
	for _, k := range []string{"profiles.json", "tokens.json", "store", "store/recipient", "accounts", "accounts/alt",
		"accounts/alt/claude", "accounts/alt/claude/settings.json", "bin", "bin/statusline-ratelimits.sh", "audit.db"} {
		after[b+"/"+k] = before["r/"+k]
	}
	after["r/last-version"] = file("v2 abc1234\n", 0)
	return before, after
}

func TestBackupProblemsAcceptsAFaithfulBackup(t *testing.T) {
	before, after := backupFixture()
	if p := backupProblems(before, after, "r", "r", "v2", nil); len(p) != 0 {
		t.Fatalf("problems: %v", p)
	}
}

func TestBackupProblemsFindsEachDefect(t *testing.T) {
	b := "r/backups/unknown-to-v2-20261113T120000Z"
	for name, c := range map[string]struct {
		mutate func(after Snapshot)
		want   string
	}{
		"changed bytes":     {func(a Snapshot) { a[b+"/profiles.json"] = file(`[1]`, 0) }, "profiles.json"},
		"changed mode":      {func(a Snapshot) { e := a[b+"/tokens.json"]; e.Mode = 0o644; a[b+"/tokens.json"] = e }, "tokens.json"},
		"missing file":      {func(a Snapshot) { delete(a, b+"/store/recipient") }, "store/recipient"},
		"transcript copied": {func(a Snapshot) { a[b+"/accounts/alt/claude/projects/x.jsonl"] = file("t", 0) }, "projects/x.jsonl"},
		"no backup": {func(a Snapshot) {
			for k := range a {
				if strings.HasPrefix(k, "r/backups") {
					delete(a, k)
				}
			}
		}, "backup"},
		"two backups": {func(a Snapshot) { a["r/backups/unknown-to-v2-20261113T120001Z"] = dir() }, "backup"},
		"wrong name":  {func(a Snapshot) { a["r/backups/v1-to-v2-20261113T120000Z"] = a[b]; delete(a, b) }, "backup"},
		"stamp":       {func(a Snapshot) { a["r/last-version"] = file("v3 abc\n", 0) }, "last-version"},
		"stamp mode":  {func(a Snapshot) { e := a["r/last-version"]; e.Mode = 0o644; a["r/last-version"] = e }, "last-version"},
	} {
		before, after := backupFixture()
		c.mutate(after)
		p := backupProblems(before, after, "r", "r", "v2", nil)
		if len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), c.want) {
			t.Errorf("%s: problems %v, want one naming %q", name, p, c.want)
		}
	}
}

func TestBackupProblemsAppliesTheMoveRewrite(t *testing.T) {
	before, after := backupFixture()
	before["r/accounts/alt/claude/settings.json"] = file(`{"c":"/h/.local/share/agent-box/bin/s"}`, 0)
	after["r/accounts/alt/claude/settings.json"] = file(`{"c":"/h/.local/share/culibox/bin/s"}`, 0)
	after["r/backups/unknown-to-v2-20261113T120000Z/accounts/alt/claude/settings.json"] = file(`{"c":"/h/.local/share/culibox/bin/s"}`, 0)
	if p := backupProblems(before, after, "r", "r", "v2", moveRewrite); len(p) != 0 {
		t.Fatalf("problems: %v", p)
	}
}
