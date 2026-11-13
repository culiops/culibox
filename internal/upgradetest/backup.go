//go:build upgrade

package upgradetest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// backupHolds lists what part A's pre-upgrade backup must hold, as patterns
// relative to the data root, from part A's design ("What is in the backup").
// It is kept apart from internal/upgradebackup's own list on purpose: this is
// the check of that list. audit.db is a VACUUM INTO snapshot, compared by rows
// by the caller; config.json is held when it sits in the root (it always does
// in this test).
var backupHolds = []string{
	"config.json", "profiles.json", "credentials.json", "accounts.json", "tokens.json",
	"sessions.json", "session-ids.json", "boxed-workspaces.json", "vm-shares.json",
	"inplace-locks.json", "clone-dirs", "login.token", "cookie.secret", "broker.secret",
	"egress.secret", "relay2.secret", "relay.secret", "last-version",
	"store/**", "clone-bases/**", "clone-imports/**", "clone-return/**", "bin/**",
	"accounts/*/claude/settings.json", "accounts/*/claude/.agentbox-manifest.json",
	"accounts/*/claude/agentbox-mcp.json", "accounts/*/claude/.agentbox-statusline-delegate",
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// backupName is the backup dir name part A writes: <from>-to-<to>-<UTC ts>,
// with characters outside [A-Za-z0-9._-] replaced by "_".
func backupName(from, to string) *regexp.Regexp {
	clean := func(s string) string {
		return regexp.QuoteMeta(regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(s, "_"))
	}
	return regexp.MustCompile(`^` + clean(from) + `-to-` + clean(to) + `-\d{8}T\d{6}Z$`)
}

// backupProblems checks part A's backup after an upgrade to version cur: one
// backup dir with the right name, holding exactly the backupHolds files that
// existed before the upgrade, each with its pre-upgrade bytes and mode (after
// rewrite, in S2, where the move rewrites some files before the backup runs),
// and a new stamp "<cur> <commit>\n" at 0600. before and after are snapshots
// of HOME; beforeRoot and rootRel are the data root's path in each (they
// differ when S2's move ran).
func backupProblems(before, after Snapshot, beforeRoot, rootRel, cur string, rewrite func(string) string) []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }
	from := "unknown"
	if st, ok := before[beforeRoot+"/last-version"]; ok {
		from = strings.Fields(string(st.Data) + " unknown")[0]
	}
	var dirs []string
	for k, e := range after {
		if e.Type == "dir" && path.Dir(k) == rootRel+"/backups" {
			if _, existed := before[beforeRoot+"/backups/"+path.Base(k)]; !existed {
				dirs = append(dirs, path.Base(k))
			}
		}
	}
	if len(dirs) != 1 {
		sort.Strings(dirs)
		add("want exactly one new backup in %s/backups, found %d: %v", rootRel, len(dirs), dirs)
		return probs
	}
	if !backupName(from, cur).MatchString(dirs[0]) {
		add("backup %q is not named %s-to-%s-<UTC ts>", dirs[0], from, cur)
	}
	b := rootRel + "/backups/" + dirs[0]

	// Every held file that existed before is in the backup, unchanged.
	beforeRel := map[string]Entry{}
	for k, e := range before {
		if rel, ok := strings.CutPrefix(k, beforeRoot+"/"); ok {
			beforeRel[rel] = e
		}
	}
	for rel, e := range beforeRel {
		if e.Type != "file" || rel == "audit.db" || !held(rel) {
			continue
		}
		got, ok := after[b+"/"+rel]
		switch {
		case !ok:
			add("backup is missing %s", rel)
		case got.Mode != e.Mode:
			add("backup %s mode %v, want %v", rel, got.Mode, e.Mode)
		case got.SHA256 == e.SHA256:
		case rewrite != nil && sha(rewrite(string(e.Data))) == got.SHA256:
		case rewrite != nil && ruleAccepts(rel, rewrite, e, got):
		default:
			add("backup %s differs from the pre-upgrade file", rel)
		}
	}
	// Nothing else is in it.
	for k, e := range after {
		rel, ok := strings.CutPrefix(k, b+"/")
		if !ok || e.Type == "dir" || rel == "audit.db" {
			continue
		}
		if _, existed := beforeRel[rel]; !existed || !held(rel) {
			add("backup holds %s, which is not a pre-upgrade file it should hold", rel)
		}
	}
	st, ok := after[rootRel+"/last-version"]
	switch {
	case !ok:
		add("last-version was not written")
	case st.Mode != 0o600:
		add("last-version mode %v, want 0600", st.Mode)
	default:
		f := strings.Fields(string(st.Data))
		if len(f) != 2 || f[0] != cur || !strings.HasSuffix(string(st.Data), "\n") {
			add("last-version is %q, want \"%s <commit>\\n\"", st.Data, cur)
		}
	}
	return probs
}

func held(rel string) bool {
	for _, p := range backupHolds {
		if match(p, rel) {
			return true
		}
	}
	return false
}

// ruleAccepts lets a file the S2 move rewrites (sessions.json, settings.json)
// differ from its pre-upgrade bytes the way the upgrade allowlist allows.
func ruleAccepts(rel string, rewrite func(string) string, before, got Entry) bool {
	var r Rule
	switch {
	case rel == "sessions.json":
		r = sessionsRule(rewrite)
	case match("accounts/*/claude/settings.json", rel):
		r = settingsRule(rewrite)
	default:
		return false
	}
	return r.Check(&before, &got) == nil
}

// checkBackup asserts part A's backup after the upgrade: backupProblems on the
// snapshots, then audit.db in the backup holding exactly the rows vN-1 wrote.
// root is the data root's absolute path after the upgrade.
func checkBackup(t *testing.T, before, after Snapshot, beforeRoot, rootRel, root, cur string, f *Fixture, rewrite func(string) string) {
	t.Helper()
	probs := backupProblems(before, after, beforeRoot, rootRel, cur, rewrite)
	for _, p := range probs {
		t.Errorf("part A backup: %s", p)
	}
	if len(probs) > 0 {
		return
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "backups", "*"))
	db := filepath.Join(dirs[0], "audit.db")
	if _, err := os.Stat(db); err != nil {
		t.Errorf("part A backup has no audit.db (it is skipped only above 512 MB): %v", err)
		return
	}
	rows := auditDBRows(t, db)
	if miss := missingDBRows(f.AuditDB, rows); len(miss) > 0 {
		t.Errorf("part A backup audit.db is missing rows: %v", miss)
	}
	if extra := missingDBRows(rows, f.AuditDB); len(extra) > 0 {
		t.Errorf("part A backup audit.db holds rows written after the upgrade began: %v", extra)
	}
	if !t.Failed() {
		t.Logf("PASS part A backup %s holds the pre-upgrade bytes and audit rows; last-version is %s", filepath.Base(dirs[0]), cur)
	}
}
