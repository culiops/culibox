package main

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
)

func TestParseStore(t *testing.T) {
	ok := []struct {
		argv []string
		want cliArgs
	}{
		{[]string{"store", "status"}, cliArgs{Cmd: "store", Sub: "status"}},
		{[]string{"store", "init"}, cliArgs{Cmd: "store", Sub: "init"}},
		{[]string{"store", "unlock"}, cliArgs{Cmd: "store", Sub: "unlock"}},
		{[]string{"store", "lock"}, cliArgs{Cmd: "store", Sub: "lock"}},
		{[]string{"store", "passwd"}, cliArgs{Cmd: "store", Sub: "passwd"}},
		{[]string{"store", "list"}, cliArgs{Cmd: "store", Sub: "list"}},
		{[]string{"store", "set", "X"}, cliArgs{Cmd: "store", Sub: "set", ID: "X"}},
		{[]string{"store", "rm", "X"}, cliArgs{Cmd: "store", Sub: "rm", ID: "X"}},
		{[]string{"store", "set", "-config", "/c.json", "X"}, cliArgs{Cmd: "store", Sub: "set", ID: "X", ConfigPath: "/c.json", ConfigSet: true}},
	}
	for _, c := range ok {
		got, err := parseArgs(c.argv)
		if err != nil || got != c.want {
			t.Errorf("parseArgs(%q) = %+v,%v want %+v", c.argv, got, err, c.want)
		}
	}
	bad := []struct {
		argv []string
		want string
	}{
		{[]string{"store", "get", "x"}, `unknown store subcommand "get"`},
		{[]string{"store"}, "store needs a subcommand: init, status, unlock, lock, passwd, set, rm or list"},
		{[]string{"store", "-config", "x"}, "store needs a subcommand: init, status, unlock, lock, passwd, set, rm or list"},
		{[]string{"store", "status", "extra"}, "unexpected arguments"},
		{[]string{"store", "list", "extra"}, "unexpected arguments"},
		{[]string{"store", "set"}, "needs exactly one <id>"},
		{[]string{"store", "set", "a", "b"}, "needs exactly one <id>"},
		{[]string{"store", "rm"}, "needs exactly one <id>"},
		{[]string{"store", "rm", "a", "b"}, "needs exactly one <id>"},
		{[]string{"store", "set", "X", "-config", "c"}, "needs exactly one <id>"},
	}
	for _, b := range bad {
		_, err := parseArgs(b.argv)
		if err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("parseArgs(%q) error = %v, want containing %q", b.argv, err, b.want)
		}
	}
}

func TestReadValueStdin(t *testing.T) {
	cases := []struct {
		in, want string
		tty      bool
	}{
		{"a\nb\n", "a\nb", false},
		{"tok\r\n", "tok", false},
		{"tok", "tok", false},
		{"a\n\n", "a\n", false}, // only one trailing newline goes
		{"-----BEGIN-----\nAAA\n-----END-----\n", "-----BEGIN-----\nAAA\n-----END-----", false},
		{"one line\nsecond\n", "one line", true}, // a tty gives one line
		{"one line\r\n", "one line", true},
	}
	for _, c := range cases {
		got, err := readValue(strings.NewReader(c.in), c.tty)
		if err != nil || string(got) != c.want {
			t.Errorf("readValue(%q, tty=%v) = %q,%v want %q", c.in, c.tty, got, err, c.want)
		}
	}
	for _, in := range []string{"", "\n", "\r\n"} {
		if _, err := readValue(strings.NewReader(in), false); err == nil {
			t.Errorf("readValue(%q): want an error for an empty value", in)
		}
	}
	if _, err := readValue(strings.NewReader(strings.Repeat("a", secretstore.MaxValueBytes+1)), false); err == nil {
		t.Error("readValue accepted a value over the size limit")
	}
}

type storeRun struct {
	t   *testing.T
	cfg config.Config
	k   secretstore.Keeper
}

func newStoreRun(t *testing.T, passphrase bool) *storeRun {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	k := secretstore.NewFakeKeeper(passphrase)
	old := newKeeper
	newKeeper = func(string) secretstore.Keeper { return k }
	t.Cleanup(func() { newKeeper = old })
	return &storeRun{t: t, k: k, cfg: config.Config{
		DataDir:       dir,
		SecretSources: []config.SecretSource{{Type: "store"}, {Type: "env"}},
		Agents:        []config.AgentConfig{{Name: "claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}},
	}}
}

func (r *storeRun) run(sub, id, stdin string) (int, string) {
	r.t.Helper()
	var out bytes.Buffer
	code := runStore(cliArgs{Cmd: "store", Sub: sub, ID: id}, r.cfg, r.t.TempDir(), strings.NewReader(stdin), false, &out)
	return code, out.String()
}

const testPass = "correct horse battery"
const testVal = "sk-ant-oat01-SUPERSECRETVALUE"

func TestRunStoreLinuxFlow(t *testing.T) {
	r := newStoreRun(t, true)

	if code, out := r.run("status", "", ""); code != 0 || !strings.Contains(out, "not initialized") {
		t.Fatalf("status before init = %d %q", code, out)
	}
	if code, out := r.run("init", "", "short\n"); code == 0 || !strings.Contains(out, "12 characters") {
		t.Fatalf("init with a short passphrase = %d %q", code, out)
	}
	code, out := r.run("init", "", testPass+"\n")
	if code != 0 || !strings.Contains(out, "unlocked until") {
		t.Fatalf("init = %d %q (Linux init unlocks right after, with the TTL)", code, out)
	}
	if strings.Contains(out, testPass) {
		t.Fatal("init printed the passphrase")
	}
	if _, _, err := r.k.Load(); err != nil {
		t.Fatalf("keeper not unlocked after init: %v", err)
	}
	if code, out := r.run("init", "", testPass+"\n"); code == 0 || !strings.Contains(out, "already exists") {
		t.Fatalf("second init = %d %q, want a refusal", code, out)
	}

	if code, out := r.run("set", "CLAUDE_CODE_OAUTH_TOKEN", testVal+"\n"); code != 0 || strings.Contains(out, testVal) {
		t.Fatalf("set = %d %q", code, out)
	}
	if code, out := r.run("set", "bad id", testVal); code == 0 || strings.Contains(out, testVal) {
		t.Fatalf("set with a bad id = %d %q", code, out)
	}
	if code, _ := r.run("set", "EMPTY", ""); code == 0 {
		t.Fatal("set accepted an empty value")
	}

	code, out = r.run("list", "", "")
	if code != 0 || !strings.Contains(out, "CLAUDE_CODE_OAUTH_TOKEN") || !strings.Contains(out, "agents/claude.secrets") {
		t.Fatalf("list = %d %q", code, out)
	}
	if strings.Contains(out, testVal) {
		t.Fatal("list printed a value")
	}

	code, out = r.run("status", "", "")
	if code != 0 || !strings.Contains(out, "state: unlocked") || !strings.Contains(out, "values: 1") {
		t.Fatalf("status = %d %q", code, out)
	}
	if !strings.Contains(out, "expires") {
		t.Fatalf("status shows no expiry: %q", out)
	}

	if code, out := r.run("lock", "", ""); code != 0 || !strings.Contains(out, "locked") {
		t.Fatalf("lock = %d %q", code, out)
	}
	if code, out := r.run("status", "", ""); code != 0 || !strings.Contains(out, "state: locked") {
		t.Fatalf("status after lock = %d %q", code, out)
	}
	// Setting works while locked: it needs only the public recipient.
	if code, out := r.run("set", "OTHER", "v2\n"); code != 0 {
		t.Fatalf("set while locked = %d %q", code, out)
	}
	if code, out := r.run("unlock", "", "wrong passphrase here\n"); code == 0 || !strings.Contains(out, "wrong passphrase") {
		t.Fatalf("unlock with a wrong passphrase = %d %q", code, out)
	}
	if code, out := r.run("unlock", "", testPass+"\n"); code != 0 || !strings.Contains(out, "unlocked until") {
		t.Fatalf("unlock = %d %q", code, out)
	}

	newPass := "another long passphrase"
	if code, out := r.run("passwd", "", testPass+"\n"+newPass+"\n"); code != 0 {
		t.Fatalf("passwd = %d %q", code, out)
	}
	r.run("lock", "", "")
	if code, _ := r.run("unlock", "", testPass+"\n"); code == 0 {
		t.Fatal("old passphrase still unlocks after passwd")
	}
	if code, _ := r.run("unlock", "", newPass+"\n"); code != 0 {
		t.Fatal("new passphrase does not unlock after passwd")
	}

	if code, out := r.run("rm", "OTHER", ""); code != 0 {
		t.Fatalf("rm = %d %q", code, out)
	}
	if code, out := r.run("rm", "OTHER", ""); code == 0 || !strings.Contains(out, "no such value") {
		t.Fatalf("rm of a missing id = %d %q", code, out)
	}
	if code, out := r.run("list", "", ""); strings.Contains(out, "OTHER") || code != 0 {
		t.Fatalf("list after rm = %d %q", code, out)
	}
}

func TestRunStoreAuditsAndNeverLogsValues(t *testing.T) {
	r := newStoreRun(t, true)
	r.run("init", "", testPass+"\n")
	r.run("set", "CLAUDE_CODE_OAUTH_TOKEN", testVal+"\n")
	r.run("rm", "CLAUDE_CODE_OAUTH_TOKEN", "")

	db, err := sql.Open("sqlite", "file:"+filepath.Join(r.cfg.DataDir, "audit.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT action, actor, detail FROM store_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var a, actor, d string
		if err := rows.Scan(&a, &actor, &d); err != nil {
			t.Fatal(err)
		}
		got = append(got, a+"|"+actor+"|"+d)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"init|cli|", "unlocked|cli|ttl=", "value_set|cli|CLAUDE_CODE_OAUTH_TOKEN", "value_removed|cli|CLAUDE_CODE_OAUTH_TOKEN"} {
		if !strings.Contains(all, want) {
			t.Errorf("audit rows missing %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, testVal) || strings.Contains(all, testPass) {
		t.Fatal("audit rows carry a value or passphrase")
	}
}

func TestRunStoreReadOnlyCommandsDoNotCreateAudit(t *testing.T) {
	r := newStoreRun(t, true)
	r.run("status", "", "")
	r.run("list", "", "")
	if matches, _ := filepath.Glob(filepath.Join(r.cfg.DataDir, "audit.db*")); len(matches) != 0 {
		t.Fatalf("read-only commands created %v", matches)
	}
}

func TestRunStoreDarwinFake(t *testing.T) {
	r := newStoreRun(t, false)
	if code, out := r.run("init", "", ""); code != 0 || strings.Contains(out, "passphrase") {
		t.Fatalf("init = %d %q", code, out)
	}
	for _, sub := range []string{"lock", "unlock", "passwd"} {
		code, out := r.run(sub, "", "")
		if code != 0 || !strings.Contains(out, "not applicable on macOS (Keychain)") {
			t.Fatalf("%s = %d %q", sub, code, out)
		}
	}
	if code, out := r.run("set", "X", testVal); code != 0 {
		t.Fatalf("set = %d %q", code, out)
	}
	code, out := r.run("status", "", "")
	if code != 0 || !strings.Contains(out, "state: unlocked") || !strings.Contains(out, "values: 1") {
		t.Fatalf("status = %d %q", code, out)
	}
}

func TestRunStoreUninitialized(t *testing.T) {
	r := newStoreRun(t, true)
	for _, sub := range []string{"set", "rm", "list", "unlock"} {
		id := ""
		if sub == "set" || sub == "rm" {
			id = "X"
		}
		code, out := r.run(sub, id, testPass+"\n")
		if code == 0 || !strings.Contains(out, "not initialized") {
			t.Errorf("%s on an uninitialized store = %d %q", sub, code, out)
		}
	}
}

func TestPlaintextCopies(t *testing.T) {
	r := newStoreRun(t, true)
	r.run("init", "", testPass+"\n")
	r.run("set", "CLAUDE_CODE_OAUTH_TOKEN", "v\n")
	r.run("set", "UNUSED", "v\n")
	r.run("set", "WORK_TOKEN", "v\n")
	st := secretstore.Open(r.cfg.DataDir, r.k, nil)
	accts := []account.Account{{Name: "work", Provider: "claude", TokenSecret: "WORK_TOKEN"}}
	env := map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "plain", "UNUSED": "plain", "WORK_TOKEN": ""}
	look := func(n string) (string, bool) { v, ok := env[n]; return v, ok }

	got := plaintextCopies(r.cfg, accts, st, look)
	if len(got) != 1 || got[0] != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("plaintextCopies = %v, want only CLAUDE_CODE_OAUTH_TOKEN (UNUSED is no declared secret, WORK_TOKEN is empty in env)", got)
	}
	cfg := r.cfg
	cfg.SecretSources = []config.SecretSource{{Type: "env"}}
	if got := plaintextCopies(cfg, accts, st, look); got != nil {
		t.Fatalf("plaintextCopies without a store source = %v", got)
	}
	// Names only: no value in what the log would print.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	logPlaintextCopies(plaintextCopies(r.cfg, accts, st, look))
	if !strings.Contains(buf.String(), "store: plaintext copy of CLAUDE_CODE_OAUTH_TOKEN is still in the daemon environment") ||
		strings.Contains(buf.String(), "plain\n") {
		t.Fatalf("log = %q", buf.String())
	}
}
