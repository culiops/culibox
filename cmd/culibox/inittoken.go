package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

// initAccountName is the account init links the box Claude token to.
const initAccountName = "claude-box"

// How many times a token, or a new passphrase, is asked for before the step
// gives up.
const (
	initTokenAttempts      = 2
	initPassphraseAttempts = 3
)

// initTokenStep is step 5: the box's Claude token, kept in the encrypted store
// as account.TokenID("claude-box") with an account of that name pointing at it.
// It returns the account name when the account is ready to link ("claude-box"),
// "" when the step was skipped (ok stays true), and ok=false when it stopped.
//
// The token is read with no echo, never printed, logged, put in a message or
// written to config.json, and its bytes are wiped once the store has it. Every
// message below is built from fixed text, ids, paths and store errors (which
// carry no value).
func initTokenStep(u *initUI, cfg config.Config, dataDir string, d initDeps) (string, bool) {
	u.begin("Claude token")
	tokenID := account.TokenID(initAccountName)

	// accounts.json is read now to decide, and read again right before it is
	// written: the login and the paste in between take minutes, and Create
	// rewrites the whole file from what it loaded.
	accts, haveAccount, foreign, err := initOpenAccount(dataDir, tokenID)
	if err != nil {
		u.stopped(fmt.Sprintf("the accounts file cannot be read (%v); fix that, then run culibox init again", err))
		return "", false
	}
	accts.Close()
	if foreign {
		u.skipped(initForeignAccountWhy)
		return "", true
	}

	audit := &lazyAudit{path: filepath.Join(dataDir, "audit.db")}
	defer audit.Close()
	st := secretstore.Open(dataDir, d.newKeeper(dataDir), audit)

	status := st.Status()
	if status.Unavailable != nil {
		u.stopped(status.Unavailable.Fix())
		return "", false
	}
	if status.Initialized {
		// List never decrypts, so a locked store answers without a passphrase.
		stored, err := storeHasID(st, tokenID)
		if err != nil {
			u.stopped(storeErrText(err) + "; fix that, then run culibox init again")
			return "", false
		}
		if haveAccount && stored {
			if !u.ask("Replace the token?", false) {
				u.already(fmt.Sprintf("account %s with its token in the store", initAccountName))
				return initAccountName, true
			}
		}
	} else if !initStoreInit(u, st, d) {
		return "", false
	}

	token := initReadToken(u, cfg, d)
	if token == nil {
		u.skipped("no token was given; run culibox init again when you have one")
		return "", true
	}
	err = st.Set(tokenID, token, storeActor)
	wipe(token)
	if err != nil {
		initStoreFailed(u, err)
		return "", false
	}
	// The token is in the store first, so the account never points at nothing.
	accts, haveAccount, foreign, err = initOpenAccount(dataDir, tokenID)
	if err != nil {
		u.stopped(fmt.Sprintf("the token is stored as %s but the accounts file cannot be read (%v); run culibox init again", tokenID, err))
		return "", false
	}
	defer accts.Close()
	if foreign {
		u.skipped(fmt.Sprintf("the token is stored as %s, but an account named %s appeared meanwhile that is not the one init makes; %s", tokenID, initAccountName, initForeignAccountWhy))
		return "", true
	}
	created := false
	if !haveAccount {
		if _, err := accts.Create(account.Account{Name: initAccountName, Provider: account.ProviderClaude, TokenSecret: tokenID}); err != nil {
			u.stopped(fmt.Sprintf("the token is stored as %s but account %s could not be created (%v); run culibox init again", tokenID, initAccountName, err))
			return "", false
		}
		created = true
	}
	u.say("the token is in the encrypted store as %s; account %s uses it", tokenID, initAccountName)
	if created {
		u.say("if culibox serve is already running, restart it so it picks up the %s account", initAccountName)
	}
	u.done()
	return initAccountName, true
}

// storeHasID says whether the store lists id. List never decrypts, so a
// locked store answers without a passphrase.
func storeHasID(st *secretstore.Store, id string) (bool, error) {
	ents, err := st.List()
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.ID == id {
			return true, nil
		}
	}
	return false, nil
}

const initForeignAccountWhy = "it inherits your personal login, or uses another token; in Settings, Agent accounts, give it its own token, or rename it"

// initOpenAccount loads accounts.json fresh and says whether claude-box exists
// and whether it is NOT the account init makes (a claude account with its own
// store token and no inherited login). The caller closes the store.
func initOpenAccount(dataDir, tokenID string) (accts *account.Store, have, foreign bool, err error) {
	accts, err = account.Open(filepath.Join(dataDir, "accounts.json"), dataDir, bundle.DefaultSource())
	if err != nil {
		return nil, false, false, err
	}
	r, err := accts.Resolve(initAccountName)
	if errors.Is(err, account.ErrUnknown) {
		return accts, false, false, nil
	}
	if err != nil {
		accts.Close()
		return nil, false, false, err
	}
	shaped := r.Provider == account.ProviderClaude && !r.InheritPersonal && r.TokenSecret == tokenID
	return accts, true, !shaped, nil
}

// initStoreFailed ends the step on a store error: a Keychain problem says its
// fix, anything else its message.
func initStoreFailed(u *initUI, err error) {
	var ue *secretstore.UnavailableError
	if errors.As(err, &ue) {
		u.stopped(ue.Fix())
		return
	}
	u.stopped(storeErrText(err) + "; fix that, then run culibox init again")
}

// initStoreInit creates the store: on macOS the master key goes to the Keychain
// with no passphrase; on Linux it asks for a new one (hidden, twice, at least
// secretstore.MinPassphraseChars) like `culibox store init`.
func initStoreInit(u *initUI, st *secretstore.Store, d initDeps) bool {
	var pass []byte
	if st.Status().PassphraseMode {
		u.say("the store is new: choose a passphrase for it (at least %d characters; you type it again to unlock the store)", secretstore.MinPassphraseChars)
		if pass = initNewPassphrase(u, d); pass == nil {
			u.stopped("no passphrase was set; run culibox store init, then culibox init again")
			return false
		}
		defer wipe(pass)
	}
	if err := st.Init(pass, storeActor); err != nil {
		initStoreFailed(u, err)
		return false
	}
	u.say("created the encrypted store")
	return true
}

// initNewPassphrase reads a new passphrase with no echo and its confirmation,
// asking again when it is short or the two differ. nil = none was given.
func initNewPassphrase(u *initUI, d initDeps) []byte {
	read := func(label string) []byte {
		fmt.Fprintf(u.out, "  %s (input hidden): ", label)
		b, err := d.readSecret()
		fmt.Fprintln(u.out)
		if err != nil {
			return nil
		}
		return b
	}
	for range initPassphraseAttempts {
		first := read("Passphrase")
		if first == nil {
			return nil
		}
		if utf8.RuneCount(first) < secretstore.MinPassphraseChars {
			wipe(first)
			u.say("the passphrase must be at least %d characters", secretstore.MinPassphraseChars)
			continue
		}
		again := read("Repeat the passphrase")
		if again == nil {
			wipe(first)
			return nil
		}
		same := bytes.Equal(first, again)
		wipe(again)
		if same {
			return first
		}
		wipe(first)
		u.say("the two passphrases do not match")
	}
	return nil
}

// initReadToken offers `claude setup-token`, then reads the pasted token. nil
// means none was given. The caller wipes the result.
func initReadToken(u *initUI, cfg config.Config, d initDeps) []byte {
	if u.ask("Run `claude setup-token` now? It opens a browser to log in.", true) {
		bin := initClaudeBin(cfg)
		if err := d.run(bin, "setup-token"); err != nil {
			u.say("`claude setup-token` did not finish (%v); you can still paste a token you made another way", err)
		}
	}
	for range initTokenAttempts {
		fmt.Fprint(u.out, "  Paste the token (input hidden): ")
		raw, err := d.readSecret()
		fmt.Fprintln(u.out)
		if err != nil {
			wipe(raw)
			return nil
		}
		tok := bytes.TrimSpace(raw) // a subslice of raw: wiping raw wipes it
		switch {
		case len(tok) == 0:
			u.say("nothing was pasted")
		case bytes.IndexFunc(tok, unicode.IsSpace) >= 0:
			u.say("a token has no spaces or line breaks in it; paste only the token")
		case !bytes.HasPrefix(tok, []byte("sk-ant-")) && !u.ask("doesn't look like a Claude token; use it anyway?", false):
			// declined: ask for another
		default:
			out := bytes.Clone(tok)
			wipe(raw)
			return out
		}
		wipe(raw)
	}
	return nil
}
