package storerefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
)

// StoreIDs lists the ids in the store. Satisfied by *secretstore.Store.
type StoreIDs interface {
	List() ([]secretstore.Entry, error)
}

// PlaintextCopies returns, sorted, the secret names that a store secret_source
// serves (an agent's secrets entry or an account's token_secret whose value is
// in the store) and that are also set non-empty in the environment lookupEnv
// reads: a copy the store's encryption does not protect. Names only. The
// daemon passes os.LookupEnv once at start, so it reports its own environment
// as of then; doctor passes the environment it runs in, which can differ.
func PlaintextCopies(cfg config.Config, accts []account.Account, st StoreIDs, lookupEnv func(string) (string, bool)) []string {
	if !HasSource(cfg) {
		return nil
	}
	ents, err := st.List()
	if err != nil {
		return nil
	}
	stored := map[string]bool{}
	for _, e := range ents {
		stored[e.ID] = true
	}
	var out []string
	for name := range SecretNameRefs(cfg, accts) {
		if !stored[name] {
			continue
		}
		if v, ok := lookupEnv(name); ok && v != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ReadAccounts reads accounts.json from the data dir without opening the
// account store (which would re-provision every account). A missing or
// unreadable file means no accounts: list only uses it to show references.
func ReadAccounts(dataDir string) []account.Account {
	b, err := os.ReadFile(filepath.Join(dataDir, "accounts.json"))
	if err != nil {
		return nil
	}
	var list []account.Account
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}
