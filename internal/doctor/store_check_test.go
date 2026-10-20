package doctor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
)

// storeFixture opens a store on a macOS-like fake keeper (no passphrase, so no
// scrypt in the suite). It returns the store and its keeper.
func storeFixture(t *testing.T, init bool) (*secretstore.Store, *secretstore.FakeKeeper) {
	t.Helper()
	k := secretstore.NewFakeKeeper(false)
	st := secretstore.Open(t.TempDir(), k, nil)
	if init {
		if err := st.Init(nil, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return st, k
}

func storeConfig() config.Config {
	return config.Config{
		GitHubApps:    []config.GitHubApp{{Name: "ci", PrivateKey: config.KeyRef{Type: "store", ID: "gh-key"}}},
		StaticTokens:  []config.StaticToken{{Name: "grafana", Value: config.KeyRef{Type: "store", ID: "graf"}}},
		SecretSources: []config.SecretSource{{Type: "store"}, {Type: "env"}},
		Agents:        []config.AgentConfig{{Name: "claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}},
	}
}

func noEnv(string) (string, bool) { return "", false }

func seccomp(on bool, err error) func() (bool, error) {
	return func() (bool, error) { return on, err }
}

func find(rs []Result, status, sub string) bool {
	for _, r := range rs {
		if r.Check == "store" && r.Status == status && strings.Contains(r.Detail, sub) {
			return true
		}
	}
	return false
}

func dump(rs []Result) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Status + " " + r.Check + " " + r.Detail + "\n")
	}
	return b.String()
}

func TestCheckStoreNotInUseIsSilent(t *testing.T) {
	st, _ := storeFixture(t, false)
	if got := checkStore(config.Config{}, st, nil, noEnv, "linux", seccomp(false, nil)); got != nil {
		t.Fatalf("not in use: %s", dump(got))
	}
}

func TestCheckStoreNotInitializedFails(t *testing.T) {
	st, _ := storeFixture(t, false)
	got := checkStore(storeConfig(), st, nil, noEnv, "darwin", nil)
	if !find(got, Fail, "not initialized") {
		t.Fatalf("want FAIL not initialized:\n%s", dump(got))
	}
}

func TestCheckStoreLockedWarns(t *testing.T) {
	st, k := storeFixture(t, true)
	if err := k.Clear(); err != nil {
		t.Fatal(err)
	}
	got := checkStore(storeConfig(), st, nil, noEnv, "darwin", nil)
	if !find(got, Warn, `store locked (unlock with "agent-box store unlock")`) {
		t.Fatalf("want WARN locked:\n%s", dump(got))
	}
}

func TestCheckStoreUnlockedPassesWithExpiry(t *testing.T) {
	// A Linux-like keeper has a TTL; reuse the identity a macOS-like init made.
	dir := t.TempDir()
	k0 := secretstore.NewFakeKeeper(false)
	if err := secretstore.Open(dir, k0, nil).Init(nil, "test"); err != nil {
		t.Fatal(err)
	}
	id, _, err := k0.Load()
	if err != nil {
		t.Fatal(err)
	}
	k := secretstore.NewFakeKeeper(true)
	exp := time.Now().Add(48 * time.Hour)
	if err := k.Save(id, 48*time.Hour); err != nil {
		t.Fatal(err)
	}
	st := secretstore.Open(dir, k, nil)
	got := checkStore(storeConfig(), st, nil, noEnv, "darwin", nil)
	if !find(got, Pass, "unlocked") || !find(got, Pass, exp.UTC().Format("2006-01-02")) {
		t.Fatalf("want PASS unlocked with the expiry date:\n%s", dump(got))
	}
}

func TestCheckStoreMissingValueFailsNamingIDAndRef(t *testing.T) {
	st, _ := storeFixture(t, true)
	if err := st.Set("gh-key", []byte("pem"), "t"); err != nil {
		t.Fatal(err)
	}
	got := checkStore(storeConfig(), st, nil, noEnv, "darwin", nil)
	if !find(got, Fail, `"graf"`) || !find(got, Fail, "static_tokens/grafana") {
		t.Fatalf("want FAIL naming graf and static_tokens/grafana:\n%s", dump(got))
	}
	if find(got, Fail, "gh-key") {
		t.Fatalf("gh-key is present:\n%s", dump(got))
	}
	// A declared secret NAME absent from the store may be served by a later
	// source (env), so it is not a failure.
	if find(got, Fail, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("a secret name falling through to env must not FAIL:\n%s", dump(got))
	}
}

func TestCheckStorePlaintextCopyWarns(t *testing.T) {
	st, _ := storeFixture(t, true)
	for _, id := range []string{"gh-key", "graf", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if err := st.Set(id, []byte("v"), "t"); err != nil {
			t.Fatal(err)
		}
	}
	accts := []account.Account{}
	look := func(k string) (string, bool) {
		if k == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "sk-SECRET", true
		}
		return "", false
	}
	got := checkStore(storeConfig(), st, accts, look, "darwin", nil)
	if !find(got, Warn, "plaintext copy of CLAUDE_CODE_OAUTH_TOKEN is still in this environment") {
		t.Fatalf("want plaintext WARN:\n%s", dump(got))
	}
	if strings.Contains(dump(got), "sk-SECRET") {
		t.Fatalf("a value leaked into the report:\n%s", dump(got))
	}
}

func TestCheckStoreSeccompOffFailsOnLinuxOnly(t *testing.T) {
	st, _ := storeFixture(t, true)
	want := box.SeccompOffText
	if got := checkStore(storeConfig(), st, nil, noEnv, "linux", seccomp(false, nil)); !find(got, Fail, want) {
		t.Fatalf("linux + seccomp off:\n%s", dump(got))
	}
	if got := checkStore(storeConfig(), st, nil, noEnv, "linux", seccomp(true, nil)); find(got, Fail, "seccomp") {
		t.Fatalf("seccomp on:\n%s", dump(got))
	}
	if got := checkStore(storeConfig(), st, nil, noEnv, "darwin", seccomp(false, nil)); find(got, Fail, "seccomp") {
		t.Fatalf("darwin boxes run in a VM:\n%s", dump(got))
	}
	// Docker unreachable: unknown is not a failure doctor can name, but is not silent either.
	if got := checkStore(storeConfig(), st, nil, noEnv, "linux", seccomp(false, errors.New("no docker"))); !find(got, Warn, "seccomp") {
		t.Fatalf("seccomp unknown:\n%s", dump(got))
	}
}

func TestCheckStoreSeccompNotCheckedWithoutInitializedStore(t *testing.T) {
	st, _ := storeFixture(t, false)
	got := checkStore(storeConfig(), st, nil, noEnv, "linux", seccomp(false, nil))
	if find(got, Fail, "seccomp") {
		t.Fatalf("no store yet, no gate:\n%s", dump(got))
	}
}
