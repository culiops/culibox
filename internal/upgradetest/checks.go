//go:build upgrade

package upgradetest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// AllowRules is the allowlist for one data root, as a slash path relative to
// the snapshot base (HOME). rewrite, when set, maps a before-path string onto
// the after root (S2's move); S1 passes nil.
//
// TODO(229A): when part A (version stamp + pre-upgrade backup) is on main, add
// rules for last-version and backups/** and assert the backup's bytes (spec,
// "Part A backup"). Until then a new last-version or backups/ is not on this
// list, so it fails the diff on purpose.
func AllowRules(rootRel string, rewrite func(string) string) []Rule {
	p := func(s string) string { return rootRel + "/" + s }
	db := func(name, why string) []Rule {
		var r []Rule
		for _, s := range []string{"", "-wal", "-shm"} {
			r = append(r, Rule{Pattern: p(name + s), Why: why, AllowNew: true, AllowVanish: s != ""})
		}
		return r
	}
	rules := []Rule{
		{Pattern: p("abduco"), Why: "runtime: abduco sockets", AllowNew: true},
		{Pattern: p("abduco/**"), Why: "runtime: abduco sockets", AllowNew: true, AllowVanish: true},
		{Pattern: p("sessions/pids/**"), Why: "runtime: session pid files", AllowNew: true, AllowVanish: true},
		{Pattern: p("serve.lock"), Why: "instance lock, empty in both versions", AllowNew: true, Check: emptyBoth},
		withName(sessionsRule(rewrite), p("sessions.json")),
		withName(tokensRule(), p("tokens.json")),
	}
	rules = append(rules, db("audit.db", "SQLite: rows are compared (auditDBRows)")...)
	rules = append(rules, db("analytics.db", "SQLite: compared through the analytics API")...)
	if rewrite != nil {
		rules = append(rules, withName(settingsRule(rewrite), p("accounts/*/claude/settings.json")))
	}
	return rules
}

func withName(r Rule, pattern string) Rule {
	r.Pattern = pattern
	return r
}

func emptyBoth(b, a *Entry) error {
	if b.Size != 0 || a.Size != 0 {
		return fmt.Errorf("serve.lock is not empty (%d -> %d bytes)", b.Size, a.Size)
	}
	return nil
}

// sessionsRule allows sessions.json to lose entries (reconcile drops sessions
// that are not alive) and, with rewrite, to have its paths moved to the new
// root. An entry that changed in any other way, or a new entry, fails.
func sessionsRule(rewrite func(string) string) Rule {
	return Rule{
		Why: "reconcile drops dead sessions; the #255 move rewrites config_dir",
		Check: func(b, a *Entry) error {
			before, after := map[string]any{}, map[string]any{}
			bs := string(b.Data)
			if rewrite != nil {
				bs = rewrite(bs)
			}
			if err := json.Unmarshal([]byte(bs), &before); err != nil {
				return fmt.Errorf("sessions.json before: %v", err)
			}
			if err := json.Unmarshal(a.Data, &after); err != nil {
				return fmt.Errorf("sessions.json after: %v", err)
			}
			var bad []string
			for id, v := range after {
				w, ok := before[id]
				switch {
				case !ok:
					bad = append(bad, fmt.Sprintf("%q is new", id))
				case !reflect.DeepEqual(v, w):
					bad = append(bad, fmt.Sprintf("%q changed", id))
				}
			}
			if len(bad) > 0 {
				sort.Strings(bad)
				return fmt.Errorf("sessions.json: %s", strings.Join(bad, ", "))
			}
			if b.Mode != a.Mode {
				return fmt.Errorf("sessions.json mode %v -> %v", b.Mode, a.Mode)
			}
			return nil
		},
	}
}

// tokensRule allows tokens.json to change only by last_used_at moving
// forward: the test itself uses the API token on vN (internal/auth/tokens.go
// bumps it on every bearer request), which is not an upgrade change.
func tokensRule() Rule {
	return Rule{
		Why: "the test's own bearer request bumps last_used_at",
		Check: func(b, a *Entry) error {
			var before, after []map[string]any
			if err := json.Unmarshal(b.Data, &before); err != nil {
				return fmt.Errorf("tokens.json before: %v", err)
			}
			if err := json.Unmarshal(a.Data, &after); err != nil {
				return fmt.Errorf("tokens.json after: %v", err)
			}
			if len(before) != len(after) {
				return fmt.Errorf("tokens.json: %d tokens -> %d", len(before), len(after))
			}
			for i := range before {
				bu, _ := before[i]["last_used_at"].(float64)
				au, _ := after[i]["last_used_at"].(float64)
				if au < bu {
					return fmt.Errorf("tokens.json: last_used_at of %v went back", before[i]["id"])
				}
				delete(before[i], "last_used_at")
				delete(after[i], "last_used_at")
			}
			if !reflect.DeepEqual(before, after) || b.Mode != a.Mode {
				return fmt.Errorf("tokens.json changed beyond last_used_at: %s -> %s", b.Data, a.Data)
			}
			return nil
		},
	}
}

// settingsRule allows an account's settings.json to change only by the root
// rewrite of the move (statusLine.command); the JSON must otherwise be equal.
func settingsRule(rewrite func(string) string) Rule {
	return Rule{
		Why: "the #255 move rewrites statusLine.command to the new root",
		Check: func(b, a *Entry) error {
			var before, after any
			if err := json.Unmarshal([]byte(rewrite(string(b.Data))), &before); err != nil {
				return fmt.Errorf("settings.json before: %v", err)
			}
			if err := json.Unmarshal(a.Data, &after); err != nil {
				return fmt.Errorf("settings.json after: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				return fmt.Errorf("settings.json changed beyond the root rewrite: %s -> %s", b.Data, a.Data)
			}
			if b.Mode != a.Mode {
				return fmt.Errorf("settings.json mode %v -> %v", b.Mode, a.Mode)
			}
			return nil
		},
	}
}

// sameProjected compares two JSON documents after dropping, from after, every
// object key before does not have (a newer build may add fields to a view).
// rewrite, when set, is applied to before's text first.
func sameProjected(before, after string, rewrite func(string) string) error {
	if rewrite != nil {
		before = rewrite(before)
	}
	var b, a any
	if err := json.Unmarshal([]byte(before), &b); err != nil {
		return fmt.Errorf("before: %v", err)
	}
	if err := json.Unmarshal([]byte(after), &a); err != nil {
		return fmt.Errorf("after: %v", err)
	}
	if p := project(b, a); !reflect.DeepEqual(b, p) {
		bb, _ := json.Marshal(b)
		pp, _ := json.Marshal(p)
		return fmt.Errorf("differs:\n  before %s\n  after  %s", bb, pp)
	}
	return nil
}

func project(shape, v any) any {
	switch s := shape.(type) {
	case map[string]any:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := map[string]any{}
		for k := range s {
			if x, ok := m[k]; ok {
				out[k] = project(s[k], x)
			}
		}
		return out
	case []any:
		l, ok := v.([]any)
		if !ok || len(l) != len(s) {
			return v
		}
		out := make([]any, len(l))
		for i := range l {
			out[i] = project(s[i], l[i])
		}
		return out
	}
	return v
}

// missingRows returns the before rows absent from after, matched on
// (ts, kind, outcome) as a multiset.
func missingRows(before, after []AuditRow) []AuditRow {
	type key struct {
		ts            int64
		kind, outcome string
	}
	have := map[key]int{}
	for _, r := range after {
		have[key{r.TS, r.Kind, r.Outcome}]++
	}
	var miss []AuditRow
	for _, r := range before {
		k := key{r.TS, r.Kind, r.Outcome}
		if have[k] == 0 {
			miss = append(miss, r)
			continue
		}
		have[k]--
	}
	return miss
}

// auditDBRows reads every row of every table of the audit.db at path, through
// a copy (with its -wal) so the data root is never opened for writing.
func auditDBRows(t *testing.T, path string) map[string][]map[string]any {
	t.Helper()
	dir := t.TempDir()
	for _, s := range []string{"", "-wal"} {
		if err := copyFile(path+s, filepath.Join(dir, "audit.db"+s)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]any{}
	for _, tab := range tables {
		rows, err := db.Query(`SELECT * FROM "` + tab + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, c := range cols {
				if b, ok := vals[i].([]byte); ok {
					vals[i] = string(b)
				}
				row[c] = vals[i]
			}
			out[tab] = append(out[tab], row)
		}
		rows.Close()
	}
	return out
}

func tableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// missingDBRows lists every before row with no equal row in after, compared
// on before's columns only (a later build may add columns).
func missingDBRows(before, after map[string][]map[string]any) []string {
	var miss []string
	for tab, rows := range before {
		used := make([]bool, len(after[tab]))
	next:
		for _, r := range rows {
			for i, a := range after[tab] {
				if used[i] {
					continue
				}
				eq := true
				for c, v := range r {
					if !reflect.DeepEqual(a[c], v) {
						eq = false
						break
					}
				}
				if eq {
					used[i] = true
					continue next
				}
			}
			miss = append(miss, fmt.Sprintf("%s %v", tab, r))
		}
	}
	sort.Strings(miss)
	return miss
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CheckUpgraded asserts, against the running build behind c, that everything
// f recorded from vN-1 loads and is intact. rewrite maps vN-1's root paths onto
// the root vN uses (S2's move); nil when the root did not move.
func CheckUpgraded(t *testing.T, c *Client, f *Fixture, rewrite func(string) string) {
	t.Helper()
	if code, err := c.Do("GET", "/api/accounts", nil, nil); err != nil || code != 200 {
		t.Errorf("the vN-1 session cookie does not work on vN (cookie.secret): %d %v", code, err)
	} else {
		t.Logf("PASS the vN-1 session cookie works on vN")
	}
	fresh := NewClient(strings.TrimPrefix(c.Base, "http://127.0.0.1:"))
	if err := fresh.Login(f.Token); err != nil {
		t.Errorf("login.token does not log in on vN: %v", err)
	}
	if code, err := c.WithBearer(f.Bearer).Do("GET", "/api/sessions", nil, nil); err != nil || code != 200 {
		t.Errorf("the vN-1 API token does not work on vN (tokens.json): %d %v", code, err)
	} else {
		t.Logf("PASS the vN-1 API token works on vN")
	}
	compare(t, "accounts", f.Accounts, getRaw(t, c, "/api/accounts"), rewrite)
	compare(t, "profiles", f.Profiles, getRaw(t, c, "/api/profiles"), rewrite)

	var st struct {
		Initialized bool `json:"initialized"`
		Values      int  `json:"values"`
	}
	must(t, c, "GET", "/api/store", nil, &st)
	if !st.Initialized || st.Values != len(storeValues) {
		t.Errorf("store on vN: initialized=%v values=%d, want true %d", st.Initialized, st.Values, len(storeValues))
	} else {
		t.Logf("PASS the store is initialized with %d values on vN", st.Values)
	}
	if miss := missingRows(f.Audit, auditRows(t, c)); len(miss) > 0 {
		t.Errorf("audit rows vN-1 listed are missing on vN: %+v", miss)
	} else {
		t.Logf("PASS every vN-1 audit row (%d) is listed by vN", len(f.Audit))
	}
	must(t, c, "POST", "/api/analytics/ingest", nil, nil)
	compare(t, "analytics summary", f.Analytics, getRaw(t, c, analyticsPath()), nil)
	unlockThroughBinary(t, c, f)
}

func compare(t *testing.T, what string, before, after json.RawMessage, rewrite func(string) string) {
	t.Helper()
	if err := sameProjected(string(before), string(after), rewrite); err != nil {
		t.Errorf("%s: %v", what, err)
	} else {
		t.Logf("PASS %s: the same on vN", what)
	}
}

// unlockThroughBinary reads UPG_ONE back through the running binary: unlock
// (Linux), spawn the upg-secret agent, read what it was given. Without a kernel
// keyring (a box) this is a SKIP, and only when vN-1 could not unlock either.
func unlockThroughBinary(t *testing.T, c *Client, f *Fixture) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		_, err := c.Do("POST", "/api/store/unlock", map[string]string{"passphrase": passphrase}, nil)
		if err != nil {
			if !f.StoreUnlockable && strings.Contains(LogTail(f.Log, 1000), "kernel keyring add: operation not permitted") {
				t.Logf("SKIP store unlock through the binary: no kernel keyring (host row H229-B1)")
				return
			}
			t.Errorf("store unlock on vN: %v", err)
			return
		}
	}
	out := filepath.Join(f.Fake, "secret.out")
	_ = os.Remove(out)
	var sess struct {
		ID string `json:"id"`
	}
	if _, err := c.Do("POST", "/api/sessions", map[string]string{"agent": "upg-secret", "runtime": "host", "cwd": f.Home}, &sess); err != nil {
		t.Errorf("spawn upg-secret on vN: %v", err)
		return
	}
	defer c.Do("DELETE", "/api/sessions/"+sess.ID, nil, nil)
	want := "UPG_ONE=" + storeValues["UPG_ONE"] + "\n"
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(out); err == nil && string(b) == want {
			t.Logf("PASS vN unlocked the store and handed UPG_ONE to a session")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	b, _ := os.ReadFile(out)
	t.Errorf("upg-secret got %q, want %q", b, want)
}

// Relative paths (under HOME) of the data root before and after the #255
// move, and the move's journal.
const (
	legacyRel   = ".local/share/agent-box"
	newRel      = ".local/share/culibox"
	moveJournal = ".culibox-move"
)

// MoveMapper maps a before-path under from onto the same path under to. from
// itself is left alone: it becomes a symlink, which CheckMoved asserts.
func MoveMapper(from, to string) func(string) string {
	return func(rel string) string {
		if strings.HasPrefix(rel, from+"/") {
			return to + strings.TrimPrefix(rel, from)
		}
		return rel
	}
}

// moveRewrite maps the legacy root onto the new one inside file contents and
// API answers, in absolute (/…/.local/share/agent-box) and ~-relative forms.
func moveRewrite(s string) string {
	return strings.ReplaceAll(s, "/"+legacyRel, "/"+newRel)
}

// CheckMoved reports whether vN moved the legacy root: the legacy path is a
// symlink to the absolute new root, the new root is a real directory, and no
// journal is left in it. A half-done move is a test failure.
func CheckMoved(t *testing.T, home string) bool {
	t.Helper()
	legacy, newRoot := filepath.Join(home, legacyRel), filepath.Join(home, newRel)
	fi, err := os.Lstat(legacy)
	if err != nil {
		t.Errorf("the legacy root is gone after the upgrade: %v", err)
		return false
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	if target, err := os.Readlink(legacy); err != nil || target != newRoot {
		t.Errorf("the legacy root links to %q (%v), want %q", target, err, newRoot)
	}
	if ni, err := os.Lstat(newRoot); err != nil || !ni.IsDir() {
		t.Errorf("the new root is not a directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(newRoot, moveJournal)); err == nil {
		t.Errorf("the move journal %s is still in the new root", moveJournal)
	}
	return true
}

// rootmoveLines returns the serve log lines about the root move, for a
// failure message.
func rootmoveLines(log string) string {
	var out []string
	for _, l := range strings.Split(LogTail(log, 5000), "\n") {
		if strings.Contains(l, "move") || strings.Contains(l, "culibox") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
