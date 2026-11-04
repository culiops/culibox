// Package storerefs finds which configuration entries point at the encrypted
// store. It lives outside secretstore because config already imports
// secretstore.
package storerefs

import (
	"sort"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

// HasSource reports whether secret_sources names the store.
func HasSource(cfg config.Config) bool {
	for _, s := range cfg.SecretSources {
		if s.Type == "store" {
			return true
		}
	}
	return false
}

// InUse reports whether anything in cfg reads from the store: a store-backed
// GitHub App key or static token, or a store secret_source.
func InUse(cfg config.Config) bool {
	if HasSource(cfg) {
		return true
	}
	for _, a := range cfg.GitHubApps {
		if a.PrivateKey.Type == "store" {
			return true
		}
	}
	for _, t := range cfg.StaticTokens {
		if t.Value.Type == "store" {
			return true
		}
	}
	return false
}

// Refs maps a store id to the entries that reference it, each sorted:
// "github_apps/<name>", "static_tokens/<name>", "agents/<agent>.secrets",
// "accounts/<account>.token_secret". Secret NAMES (agent secrets and account
// token_secret) count as store ids only when a store secret_source exists,
// because only then does a name resolve to the value with that id. Names that
// are not valid store ids are skipped. Values are never read.
func Refs(cfg config.Config, accts []account.Account) map[string][]string {
	return refs(cfg, SecretNameRefs(cfg, accts))
}

// Claims is Refs with secret names counted whether or not a store source
// exists: the ids a definition reads now or will read once one is added. An
// owner check uses it, so a value written today cannot become an account's or
// agent's value when the founder adds a store source later.
func Claims(cfg config.Config, accts []account.Account) map[string][]string {
	return refs(cfg, secretNames(cfg, accts))
}

func refs(cfg config.Config, names map[string][]string) map[string][]string {
	out := map[string][]string{}
	add := func(id, ref string) { out[id] = append(out[id], ref) }
	for _, a := range cfg.GitHubApps {
		if a.PrivateKey.Type == "store" {
			add(a.PrivateKey.ID, "github_apps/"+a.Name)
		}
	}
	for _, t := range cfg.StaticTokens {
		if t.Value.Type == "store" {
			add(t.Value.ID, "static_tokens/"+t.Name)
		}
	}
	for id, r := range names {
		out[id] = append(out[id], r...)
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out
}

// SecretNameRefs maps each secret name that a store secret_source could serve
// (an agent's secrets entry, an account's token_secret) to the entries that
// declare it. Without a store source only an account's own pasted token
// (account.OwnsStoreToken) is mapped: the launcher reads that one straight
// from the store. Names that are not valid store ids are skipped.
func SecretNameRefs(cfg config.Config, accts []account.Account) map[string][]string {
	if !HasSource(cfg) {
		out := map[string][]string{}
		for _, a := range accts {
			if a.OwnsStoreToken() {
				out[a.TokenSecret] = append(out[a.TokenSecret], "accounts/"+a.Name+".token_secret")
			}
		}
		return out
	}
	return secretNames(cfg, accts)
}

// AccountTokensStored reports whether the store holds any account's own pasted
// token: such a store is in use even when the config never names it.
func AccountTokensStored(accts []account.Account, st StoreIDs) bool {
	own := map[string]bool{}
	for _, a := range accts {
		if a.OwnsStoreToken() {
			own[a.TokenSecret] = true
		}
	}
	if len(own) == 0 || st == nil {
		return false
	}
	ents, err := st.List()
	if err != nil {
		return false
	}
	for _, e := range ents {
		if own[e.ID] {
			return true
		}
	}
	return false
}

func secretNames(cfg config.Config, accts []account.Account) map[string][]string {
	out := map[string][]string{}
	for _, a := range cfg.Agents {
		for _, s := range a.Secrets {
			if secretstore.ValidID(s) {
				out[s] = append(out[s], "agents/"+a.Name+".secrets")
			}
		}
	}
	for _, a := range accts {
		if a.TokenSecret != "" && secretstore.ValidID(a.TokenSecret) {
			out[a.TokenSecret] = append(out[a.TokenSecret], "accounts/"+a.Name+".token_secret")
		}
	}
	return out
}
