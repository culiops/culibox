package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

const (
	tokTest   = "sk-ant-oat01-TESTTOKEN"
	tokPass   = "correct horse battery"
	tokAcctID = "ACCT_CLAUDE_BOX_TOKEN"
)

// tokFix is the seams step 5 runs through: a fake keeper, a no-echo reader that
// hands out scripted answers (and fails once they run out, like a closed
// terminal), and a recorder for run.
type tokFix struct {
	t       *testing.T
	dataDir string
	k       *secretstore.FakeKeeper
	reads   []string
	nReads  int
	runs    [][]string
	runErr  error
}

func newTokFix(t *testing.T, linux bool, reads ...string) *tokFix {
	t.Helper()
	home := initHome(t)
	dataDir := filepath.Join(home, ".local", "share", "culibox")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &tokFix{t: t, dataDir: dataDir, k: secretstore.NewFakeKeeper(linux), reads: reads}
}

func (f *tokFix) deps() initDeps {
	goos := "darwin"
	if f.k.NeedsPassphrase() {
		goos = "linux"
	}
	return initDeps{
		goos: goos,
		run: func(name string, args ...string) error {
			f.runs = append(f.runs, append([]string{name}, args...))
			return f.runErr
		},
		readSecret: func() ([]byte, error) {
			f.nReads++
			if len(f.reads) == 0 {
				return nil, errors.New("closed")
			}
			r := f.reads[0]
			f.reads = f.reads[1:]
			return []byte(r), nil
		},
		newKeeper: func(string) secretstore.Keeper { return f.k },
	}
}

func (f *tokFix) store() *secretstore.Store { return secretstore.Open(f.dataDir, f.k, nil) }

func (f *tokFix) accountsPath() string { return filepath.Join(f.dataDir, "accounts.json") }

// run answers the step's yes/no questions with in and returns what it printed.
func (f *tokFix) run(in string, cfg config.Config) (name string, ok bool, out string) {
	f.t.Helper()
	var b bytes.Buffer
	name, ok = initTokenStep(newTestUI(in, &b), cfg, f.dataDir, f.deps())
	noToken(f.t, b.String())
	return name, ok, b.String()
}

// seed makes a store holding the token and an init-shaped claude-box account.
func (f *tokFix) seed(pass string) {
	f.t.Helper()
	var p []byte
	if f.k.NeedsPassphrase() {
		p = []byte(pass)
	}
	st := f.store()
	if err := st.Init(p, "t"); err != nil {
		f.t.Fatal(err)
	}
	if err := st.Set(tokAcctID, []byte("sk-ant-oat01-OLDOLD"), "t"); err != nil {
		f.t.Fatal(err)
	}
	f.createAccount(account.Account{Name: "claude-box", Provider: account.ProviderClaude, TokenSecret: tokAcctID})
}

func (f *tokFix) createAccount(a account.Account) {
	f.t.Helper()
	as, err := account.Open(f.accountsPath(), f.dataDir, filepath.Join(f.dataDir, "..", "claude-default"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer as.Close()
	if _, err := as.Create(a); err != nil {
		f.t.Fatal(err)
	}
}

func (f *tokFix) account() (account.Account, bool) {
	f.t.Helper()
	as, err := account.Open(f.accountsPath(), f.dataDir, filepath.Join(f.dataDir, "..", "claude-default"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer as.Close()
	r, err := as.Resolve("claude-box")
	if err != nil {
		return account.Account{}, false
	}
	return r.Account, true
}

// noToken fails when the token (or any part of it) was printed.
func noToken(t *testing.T, s string) {
	t.Helper()
	for _, bad := range []string{tokTest, "TESTTOKEN"} {
		if strings.Contains(s, bad) {
			t.Fatalf("the token was printed (%q) in:\n%s", bad, s)
		}
	}
}

func (f *tokFix) wantToken(want string) {
	f.t.Helper()
	got, err := f.store().Get(tokAcctID)
	if err != nil || string(got) != want {
		f.t.Fatalf("stored token = %q, %v; want %q", got, err, want)
	}
}

func (f *tokFix) wantLinked() {
	f.t.Helper()
	a, ok := f.account()
	if !ok || a.TokenSecret != account.TokenID("claude-box") || a.Provider != account.ProviderClaude || a.InheritPersonal {
		f.t.Fatalf("account = %+v, %v", a, ok)
	}
	b, _ := os.ReadFile(f.accountsPath())
	noToken(f.t, string(b))
}

func TestTokenStepFreshDarwin(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	f.wantToken(tokTest)
	f.wantLinked()
	if len(f.runs) != 0 {
		t.Fatalf("setup-token ran after n: %v", f.runs)
	}
	if !strings.Contains(out, "== step 1/8: Claude token") || !strings.HasSuffix(strings.TrimSpace(out), "done") {
		t.Fatalf("out=%s", out)
	}
	if f.store().Status().PassphraseMode || f.nReads != 1 {
		t.Fatalf("darwin must not ask a passphrase (reads=%d)", f.nReads)
	}
}

func TestTokenStepRunsSetupToken(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	name, ok, out := f.run("\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if len(f.runs) != 1 || strings.Join(f.runs[0], " ") != "claude setup-token" {
		t.Fatalf("runs = %v", f.runs)
	}
	f.wantToken(tokTest)
}

func TestTokenStepSetupTokenUsesTheClaudeAgentBin(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	cfg := config.Config{Agents: []config.AgentConfig{{Name: "codex", Bin: "/x/codex"}, {Name: "claude", Bin: "/opt/bin/claude"}}}
	if _, ok, out := f.run("\n", cfg); !ok {
		t.Fatalf("out=%s", out)
	}
	if len(f.runs) != 1 || strings.Join(f.runs[0], " ") != "/opt/bin/claude setup-token" {
		t.Fatalf("runs = %v", f.runs)
	}
}

func TestTokenStepSetupTokenFailureStillOffersThePaste(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	f.runErr = errors.New("exit status 1")
	name, ok, out := f.run("\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if !strings.Contains(out, "setup-token") || !strings.Contains(out, "exit status 1") {
		t.Fatalf("the failure must be said: %s", out)
	}
	f.wantToken(tokTest)
}

func TestTokenStepAlreadyDone(t *testing.T) {
	f := newTokFix(t, false)
	f.seed("")
	before, _ := f.store().List()
	time.Sleep(10 * time.Millisecond)
	name, ok, out := f.run("\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if !strings.Contains(out, "Replace the token? [y/N]") || !strings.Contains(out, "already done") {
		t.Fatalf("out=%s", out)
	}
	after, _ := f.store().List()
	if len(after) != 1 || !after[0].Updated.Equal(before[0].Updated) || f.nReads != 0 || len(f.runs) != 0 {
		t.Fatalf("a kept token must not be touched: reads=%d runs=%v", f.nReads, f.runs)
	}
	f.wantToken("sk-ant-oat01-OLDOLD")
}

func TestTokenStepAlreadyDoneEOFKeeps(t *testing.T) {
	f := newTokFix(t, false)
	f.seed("")
	name, ok, out := f.run("", config.Config{})
	if !ok || name != "claude-box" || !strings.Contains(out, "already done") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
}

func TestTokenStepReplace(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	f.seed("")
	name, ok, out := f.run("y\nn\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	f.wantToken(tokTest)
	f.wantLinked()
	if strings.Contains(out, "already done") {
		t.Fatalf("a replaced token is done, not already done: %s", out)
	}
}

func TestTokenStepAlreadyDoneWhileLockedNeverUnlocks(t *testing.T) {
	f := newTokFix(t, true)
	f.seed(tokPass) // the keeper is locked after Init
	name, ok, out := f.run("\n", config.Config{})
	if !ok || name != "claude-box" || !strings.Contains(out, "already done") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if f.nReads != 0 {
		t.Fatalf("a locked store must not be asked to unlock (reads=%d)", f.nReads)
	}
}

func TestTokenStepAccountWithoutTokenInStoreIsNotDone(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	f.seed("")
	if err := f.store().Remove(tokAcctID, "t"); err != nil {
		t.Fatal(err)
	}
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "claude-box" || strings.Contains(out, "already done") || strings.Contains(out, "Replace") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	f.wantToken(tokTest)
}

func TestTokenStepForeignAccountUntouched(t *testing.T) {
	for name, a := range map[string]account.Account{
		"inherit":      {Name: "claude-box", Provider: account.ProviderClaude, InheritPersonal: true},
		"other secret": {Name: "claude-box", Provider: account.ProviderClaude, TokenSecret: "SOME_OTHER_TOKEN"},
		"no secret":    {Name: "claude-box", Provider: account.ProviderClaude},
		"codex":        {Name: "claude-box", Provider: account.ProviderCodex, TokenSecret: tokAcctID},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTokFix(t, false, tokTest)
			f.createAccount(a)
			before, _ := os.ReadFile(f.accountsPath())
			got, ok, out := f.run("\n", config.Config{})
			if !ok || got != "" {
				t.Fatalf("got %q,%v: %s", got, ok, out)
			}
			if !strings.Contains(out, "skipped:") || !strings.Contains(out, "Settings") {
				t.Fatalf("out=%s", out)
			}
			after, _ := os.ReadFile(f.accountsPath())
			if !bytes.Equal(before, after) {
				t.Fatalf("accounts.json changed:\n%s\n%s", before, after)
			}
			if f.nReads != 0 || len(f.runs) != 0 || f.store().Status().Initialized {
				t.Fatal("a foreign account must stop the step before anything else happens")
			}
		})
	}
}

func TestTokenStepFreshLinuxAsksPassphrase(t *testing.T) {
	f := newTokFix(t, true, "short", tokPass, tokPass, tokTest)
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if !strings.Contains(out, "at least 12") {
		t.Fatalf("a short passphrase must be refused with the reason: %s", out)
	}
	for _, secret := range []string{tokPass, "short"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the passphrase %q was echoed: %s", secret, out)
		}
	}
	st := f.store()
	if !st.Status().Initialized {
		t.Fatal("store not initialized")
	}
	if st.Status().Locked {
		t.Fatal("the new store must be left unlocked, like culibox store init")
	}
	if err := st.Lock("t"); err != nil {
		t.Fatal(err)
	}
	if err := st.Unlock([]byte(tokPass), time.Minute, "t"); err != nil {
		t.Fatalf("the passphrase typed must unlock the store: %v", err)
	}
	f.wantToken(tokTest)
	f.wantLinked()
}

func TestTokenStepLinuxPassphraseMismatchReasks(t *testing.T) {
	f := newTokFix(t, true, tokPass, "a different passphrase", tokPass, tokPass, tokTest)
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "claude-box" || !strings.Contains(out, "do not match") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if err := f.store().Unlock([]byte(tokPass), time.Minute, "t"); err != nil {
		t.Fatal(err)
	}
	f.wantToken(tokTest)
}

func TestTokenStepLinuxPassphraseGivesUp(t *testing.T) {
	f := newTokFix(t, true, "a", "b", "c")
	name, ok, out := f.run("n\n", config.Config{})
	if ok || name != "" || !strings.Contains(out, "stopped:") || !strings.Contains(out, "culibox store init") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if f.store().Status().Initialized {
		t.Fatal("no store without a passphrase")
	}
}

func TestTokenStepLockedStoreStillSets(t *testing.T) {
	f := newTokFix(t, true, tokTest)
	if err := f.store().Init([]byte(tokPass), "t"); err != nil {
		t.Fatal(err)
	}
	if !f.store().Status().Locked {
		t.Fatal("setup: store should be locked")
	}
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if f.nReads != 1 {
		t.Fatalf("only the token may be read, got %d reads", f.nReads)
	}
	if err := f.store().Unlock([]byte(tokPass), time.Minute, "t"); err != nil {
		t.Fatal(err)
	}
	f.wantToken(tokTest)
}

func TestTokenStepEmptyToken(t *testing.T) {
	f := newTokFix(t, false, "", "  \n")
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if f.nReads != 2 || !strings.Contains(out, "skipped:") {
		t.Fatalf("re-ask once then skip: reads=%d out=%s", f.nReads, out)
	}
	if _, found := f.account(); found {
		t.Fatal("no account without a token")
	}
	if l, _ := f.store().List(); len(l) != 0 {
		t.Fatalf("stored %v", l)
	}
}

func TestTokenStepReadErrorIsSkipped(t *testing.T) {
	f := newTokFix(t, false)
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "" || !strings.Contains(out, "skipped:") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
}

func TestTokenStepWhitespaceTrimmedAndInnerWhitespaceReasks(t *testing.T) {
	f := newTokFix(t, false, tokTest+" extra", "  "+tokTest+"\r\n")
	name, ok, out := f.run("n\n", config.Config{})
	if !ok || name != "claude-box" || f.nReads != 2 {
		t.Fatalf("got %q,%v reads=%d: %s", name, ok, f.nReads, out)
	}
	if !strings.Contains(out, "spaces") {
		t.Fatalf("the reason must be said: %s", out)
	}
	f.wantToken(tokTest)
}

func TestTokenStepOddPrefixConfirms(t *testing.T) {
	const odd = "TESTTOKEN-no-prefix"
	f := newTokFix(t, false, odd)
	name, ok, out := f.run("n\ny\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if !strings.Contains(out, "doesn't look like a Claude token; use it anyway? [y/N]") {
		t.Fatalf("out=%s", out)
	}
	f.wantToken(odd)
}

func TestTokenStepOddPrefixDeclinedReasksOnceThenSkips(t *testing.T) {
	f := newTokFix(t, false, "nope", "still-nope")
	name, ok, out := f.run("n\nn\nn\n", config.Config{})
	if !ok || name != "" || !strings.Contains(out, "skipped:") || f.nReads != 2 {
		t.Fatalf("got %q,%v reads=%d: %s", name, ok, f.nReads, out)
	}
	if l, _ := f.store().List(); len(l) != 0 {
		t.Fatalf("stored %v", l)
	}
}

func TestTokenStepOddPrefixEnterIsNo(t *testing.T) {
	f := newTokFix(t, false, "nope", tokTest)
	name, ok, out := f.run("n\n\n", config.Config{})
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	f.wantToken(tokTest)
}

func TestTokenStepKeychainUnavailableStops(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	f.k.FailSave(&secretstore.UnavailableError{Kind: secretstore.KindAccess, Reason: "keychain locked"})
	name, ok, out := f.run("n\n", config.Config{})
	if ok || name != "" || !strings.Contains(out, "stopped:") || !strings.Contains(out, "Unlock the login keychain") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if _, found := f.account(); found {
		t.Fatal("no account when the store is not usable")
	}
}

func TestTokenStepKeychainUnavailableOnExistingStoreStopsBeforeAnyPrompt(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	if err := f.store().Init(nil, "t"); err != nil {
		t.Fatal(err)
	}
	f.k.FailLoad(&secretstore.UnavailableError{Kind: secretstore.KindKeyLost, Reason: "no key", StoreDir: filepath.Join(f.dataDir, "store")})
	name, ok, out := f.run("n\n", config.Config{})
	if ok || name != "" || !strings.Contains(out, "stopped:") || f.nReads != 0 || len(f.runs) != 0 {
		t.Fatalf("got %q,%v reads=%d: %s", name, ok, f.nReads, out)
	}
}

func TestTokenStepSetFailureNeverShowsTheToken(t *testing.T) {
	huge := "sk-ant-" + strings.Repeat("A", secretstore.MaxValueBytes+1)
	f := newTokFix(t, false, huge)
	name, ok, out := f.run("n\n", config.Config{})
	if ok || name != "" || !strings.Contains(out, "stopped:") {
		t.Fatalf("got %q,%v: %s", name, ok, out)
	}
	if strings.Contains(out, "AAAAAAAA") {
		t.Fatalf("token material in output: %.200s", out)
	}
	if _, found := f.account(); found {
		t.Fatal("the account must not exist when the token was not stored")
	}
}

func TestTokenStepWipesTheTokenBuffer(t *testing.T) {
	f := newTokFix(t, false)
	var held []byte
	d := f.deps()
	d.readSecret = func() ([]byte, error) {
		held = []byte("  " + tokTest + "\n")
		return held, nil
	}
	var b bytes.Buffer
	if _, ok := initTokenStep(newTestUI("n\n", &b), config.Config{}, f.dataDir, d); !ok {
		t.Fatal(b.String())
	}
	if strings.Contains(string(held), "TESTTOKEN") {
		t.Fatalf("token bytes survive in the read buffer: %q", held)
	}
}

// addOtherAccount writes another account to accounts.json through its own
// store, as a Settings save or a second process would.
func (f *tokFix) addOtherAccount(name string) {
	f.t.Helper()
	f.createAccount(account.Account{Name: name, Provider: account.ProviderClaude, InheritPersonal: true})
}

func (f *tokFix) hasAccount(name string) bool {
	f.t.Helper()
	as, err := account.Open(f.accountsPath(), f.dataDir, filepath.Join(f.dataDir, "..", "claude-default"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer as.Close()
	_, err = as.Resolve(name)
	return err == nil
}

func TestTokenStepKeepsAnAccountAddedWhileItWaited(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	d := f.deps()
	d.run = func(name string, args ...string) error { // the browser login takes minutes
		f.addOtherAccount("work")
		return nil
	}
	var b bytes.Buffer
	name, ok := initTokenStep(newTestUI("\n", &b), config.Config{}, f.dataDir, d)
	noToken(t, b.String())
	if !ok || name != "claude-box" {
		t.Fatalf("got %q,%v: %s", name, ok, b.String())
	}
	if !f.hasAccount("work") {
		t.Fatalf("the account added meanwhile was dropped:\n%s", b.String())
	}
	f.wantLinked()
	if !strings.Contains(b.String(), "restart it") {
		t.Fatalf("a created account must tell the user to restart a running serve: %s", b.String())
	}
}

func TestTokenStepForeignAccountAppearingWhileItWaitedIsLeftAlone(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	d := f.deps()
	d.run = func(name string, args ...string) error {
		f.createAccount(account.Account{Name: "claude-box", Provider: account.ProviderClaude, InheritPersonal: true})
		return nil
	}
	var b bytes.Buffer
	name, ok := initTokenStep(newTestUI("\n", &b), config.Config{}, f.dataDir, d)
	noToken(t, b.String())
	if !ok || name != "" || !strings.Contains(b.String(), "skipped:") {
		t.Fatalf("got %q,%v: %s", name, ok, b.String())
	}
	a, _ := f.account()
	if !a.InheritPersonal || a.TokenSecret != "" {
		t.Fatalf("the foreign account was modified: %+v", a)
	}
}

func TestTokenStepNoRestartLineWhenNothingWasCreated(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	f.seed("")
	_, ok, out := f.run("y\nn\n", config.Config{})
	if !ok || strings.Contains(out, "restart") {
		t.Fatalf("out=%s", out)
	}
}

func TestTokenStepNeverWritesConfig(t *testing.T) {
	f := newTokFix(t, false, tokTest)
	f.run("n\n", config.Config{})
	var found []string
	filepath.WalkDir(filepath.Dir(f.dataDir), func(p string, _ os.DirEntry, _ error) error {
		if filepath.Base(p) == "config.json" {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("step 5 must not write config.json: %v", found)
	}
}
