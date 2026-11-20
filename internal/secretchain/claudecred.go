package secretchain

import (
	"slices"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
)

// ClaudeCredSecrets are the secret names that give a boxed claude a login:
// a setup-token or an API key.
var ClaudeCredSecrets = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// PresenceFunc answers whether a name has a value. When the store key cannot be
// read it returns StoredUnavailable, normally with the Keychain error, but the
// error may be nil (Chain.Check returns (StoredUnavailable, nil) in some cases).
// The error is ignored for every other Presence.
type PresenceFunc func(name string) (Presence, error)

// ClaudeBoxCredential is the one rule for "a boxed claude has a login", shared
// by doctor and the profile list. It is Present when any of the profile's
// ClaudeCredSecrets is present; otherwise StoredUnavailable (with the first such
// error), otherwise StoredLocked, otherwise Absent. Because a PresenceFunc may
// report StoredUnavailable with a nil error, ClaudeBoxCredential may return
// (StoredUnavailable, nil); a caller that formats the error must handle nil.
// Doctor's adapters always supply the error.
//
// acctToken is asked only when no secret was present, locked or unavailable,
// and only when a.Account is neither "" nor account.Default; its argument is the
// account name. Finding the account and reading its token is the caller's.
func ClaudeBoxCredential(a config.AgentConfig, secret, acctToken PresenceFunc) (Presence, error) {
	var locked, unavailable bool
	var unavailErr error
	for _, name := range a.Secrets {
		if !slices.Contains(ClaudeCredSecrets, name) {
			continue
		}
		p, err := secret(name)
		switch p {
		case Present:
			return Present, nil
		case StoredLocked:
			locked = true
		case StoredUnavailable:
			if !unavailable {
				unavailable, unavailErr = true, err
			}
		}
	}
	if !locked && !unavailable && a.Account != "" && a.Account != account.Default {
		p, err := acctToken(a.Account)
		switch p {
		case Present, StoredLocked:
			return p, nil
		case StoredUnavailable:
			return StoredUnavailable, err
		}
	}
	switch {
	case unavailable:
		return StoredUnavailable, unavailErr
	case locked:
		return StoredLocked, nil
	}
	return Absent, nil
}

// ProfileBoxCredential feeds ClaudeBoxCredential from the secret_sources chain
// for secrets and from AccountTokenPresence (chain, then the store-only chain)
// for the account. It answers by id and never decrypts when the store can. An
// error from resolveAcct or from a check counts as Absent.
func ProfileBoxCredential(chain, store *Chain, resolveAcct func(name string) (account.Account, error)) func(config.AgentConfig) Presence {
	secret := func(name string) (Presence, error) {
		if chain == nil {
			return Absent, nil
		}
		p, err := chain.Check(name)
		if err != nil {
			return Absent, nil
		}
		return p, nil
	}
	acctToken := func(name string) (Presence, error) {
		acct, err := resolveAcct(name)
		if err != nil || chain == nil {
			return Absent, nil
		}
		p, err := AccountTokenPresence(chain, store, acct)
		if err != nil {
			return Absent, nil
		}
		return p, nil
	}
	return func(a config.AgentConfig) Presence {
		p, _ := ClaudeBoxCredential(a, secret, acctToken)
		return p
	}
}
