package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/storerefs"
)

// newKeeper builds the master-key keeper for a data dir; swapped in tests.
var newKeeper = secretstore.NewKeeper

const storeActor = "cli"

// runStore serves `agent-box store <sub>`. It writes the store's files
// directly and needs no running daemon. A value is read from stdin only (no
// echo on a tty), never from argv, and nothing here prints one. stdinTTY says
// whether stdin is a terminal; out gets every message, errors included.
func runStore(a cliArgs, cfg config.Config, home string, stdin io.Reader, stdinTTY bool, out io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(out, "agent-box store: "+format+"\n", args...)
		return 1
	}
	dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
	if err != nil {
		return fail("%v", err)
	}
	// The key description hashes the real path, so the dir has to exist first.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fail("data dir: %v", err)
	}
	k := newKeeper(dataDir)
	audit := &lazyAudit{path: filepath.Join(dataDir, "audit.db")}
	defer audit.Close()
	st := secretstore.Open(dataDir, k, audit)
	p := &prompter{in: bufio.NewReader(stdin), raw: stdin, tty: stdinTTY, out: out}

	switch a.Sub {
	case "status":
		s := st.Status()
		fmt.Fprintf(out, "platform: %s\n", s.Platform)
		if !s.Initialized {
			fmt.Fprintln(out, "store: not initialized (run: agent-box store init)")
			return 0
		}
		fmt.Fprintln(out, "store: initialized")
		if s.Locked {
			fmt.Fprintln(out, "state: locked")
		} else {
			fmt.Fprintln(out, "state: unlocked")
			if s.ExpiresAt.IsZero() {
				fmt.Fprintln(out, "expires: never (the macOS Keychain holds the master key)")
			} else {
				fmt.Fprintf(out, "expires: %s (in %s)\n", s.ExpiresAt.Format(time.RFC3339),
					time.Until(s.ExpiresAt).Round(time.Minute))
			}
		}
		fmt.Fprintf(out, "values: %d\n", s.Values)
		return 0

	case "init":
		var pass []byte
		if k.NeedsPassphrase() {
			if pass, err = p.newPassphrase("passphrase"); err != nil {
				return fail("%v", err)
			}
			defer wipe(pass)
		}
		if err := st.Init(pass, storeActor); err != nil {
			return fail("%s", storeErrText(err))
		}
		if !k.NeedsPassphrase() {
			fmt.Fprintln(out, "store initialized; the master key is in the macOS Keychain")
			return 0
		}
		// Unlock with the passphrase just entered, so there is no second prompt.
		if err := st.Unlock(pass, cfg.StoreUnlockTTL(), storeActor); err != nil {
			fmt.Fprintf(out, "store initialized, but unlocking failed: %s\nrun: agent-box store unlock\n", storeErrText(err))
			return 1
		}
		fmt.Fprintf(out, "store initialized and unlocked until %s\n", unlockExpiry(st))
		return 0

	case "unlock", "lock", "passwd":
		if !k.NeedsPassphrase() {
			fmt.Fprintln(out, "not applicable on macOS (Keychain)")
			return 0
		}
		return runStorePassphrased(a.Sub, st, cfg, p, out)

	case "set":
		if !secretstore.ValidID(a.ID) {
			return fail("%s", secretstore.ErrBadID)
		}
		if stdinTTY {
			fmt.Fprintf(out, "value for %s (input hidden): ", a.ID)
		}
		val, err := readValue(stdin, stdinTTY)
		if stdinTTY {
			fmt.Fprintln(out)
		}
		if err != nil {
			return fail("%s", storeErrText(err))
		}
		defer wipe(val)
		if err := st.Set(a.ID, val, storeActor); err != nil {
			return fail("%s", storeErrText(err))
		}
		fmt.Fprintf(out, "stored %s\n", a.ID)
		return 0

	case "rm":
		if err := st.Remove(a.ID, storeActor); err != nil {
			return fail("%s", storeErrText(err))
		}
		fmt.Fprintf(out, "removed %s\n", a.ID)
		if refs := storerefs.Refs(cfg, readAccounts(dataDir))[a.ID]; len(refs) > 0 {
			fmt.Fprintf(out, "note: still referenced by %s\n", strings.Join(refs, ", "))
		}
		return 0

	case "list":
		ents, err := st.List()
		if err != nil {
			return fail("%s", storeErrText(err))
		}
		refs := storerefs.Refs(cfg, readAccounts(dataDir))
		stored := map[string]bool{}
		for _, e := range ents {
			stored[e.ID] = true
			fmt.Fprintf(out, "%s\tupdated %s\t%s\n", e.ID, e.Updated.Format(time.RFC3339), refsText(refs[e.ID]))
		}
		// A reference whose value is not in the store would fail at use; say so.
		var missing []string
		for id := range refs {
			if !stored[id] {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		for _, id := range missing {
			fmt.Fprintf(out, "%s\tnot stored\t%s\n", id, refsText(refs[id]))
		}
		if len(ents) == 0 && len(missing) == 0 {
			fmt.Fprintln(out, "no values")
		}
		return 0
	}
	return fail("unknown subcommand %q", a.Sub)
}

// runStorePassphrased runs unlock, lock and passwd, the three Linux-only
// subcommands that deal in the passphrase.
func runStorePassphrased(sub string, st *secretstore.Store, cfg config.Config, p *prompter, out io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(out, "agent-box store: "+format+"\n", args...)
		return 1
	}
	switch sub {
	case "lock":
		if err := st.Lock(storeActor); err != nil {
			return fail("%s", storeErrText(err))
		}
		fmt.Fprintln(out, "store locked")
		return 0
	case "unlock":
		if s := st.Status(); !s.Initialized {
			return fail("%s", storeErrText(secretstore.ErrNotInitialized))
		}
		pass, err := p.passphrase("passphrase")
		if err != nil {
			return fail("%v", err)
		}
		defer wipe(pass)
		if err := st.Unlock(pass, cfg.StoreUnlockTTL(), storeActor); err != nil {
			return fail("%s", storeErrText(err))
		}
		fmt.Fprintf(out, "store unlocked until %s\n", unlockExpiry(st))
		return 0
	default: // passwd
		if s := st.Status(); !s.Initialized {
			return fail("%s", storeErrText(secretstore.ErrNotInitialized))
		}
		oldPass, err := p.passphrase("old passphrase")
		if err != nil {
			return fail("%v", err)
		}
		defer wipe(oldPass)
		newPass, err := p.newPassphrase("new passphrase")
		if err != nil {
			return fail("%v", err)
		}
		defer wipe(newPass)
		if err := st.Passwd(oldPass, newPass, storeActor); err != nil {
			return fail("%s", storeErrText(err))
		}
		fmt.Fprintln(out, "passphrase changed; values are untouched")
		return 0
	}
}

func unlockExpiry(st *secretstore.Store) string {
	if exp := st.Status().ExpiresAt; !exp.IsZero() {
		return exp.Format(time.RFC3339)
	}
	return "it is locked again"
}

func refsText(refs []string) string {
	if len(refs) == 0 {
		return "not referenced"
	}
	return strings.Join(refs, ", ")
}

// storeErrText is the message for a store error. The store's errors carry no
// value, passphrase or path; the two states with a next step say what it is.
func storeErrText(err error) string {
	switch {
	case errors.Is(err, secretstore.ErrNotInitialized):
		return "the store is not initialized; run: agent-box store init"
	case errors.Is(err, secretstore.ErrExists):
		return "the store already exists (to change the passphrase run: agent-box store passwd)"
	case errors.Is(err, secretstore.ErrBadPassphrase):
		return "wrong passphrase"
	}
	return err.Error()
}

// readAccounts reads accounts.json from the data dir without opening the
// account store (which would re-provision every account). A missing or
// unreadable file means no accounts: list only uses it to show references.
func readAccounts(dataDir string) []account.Account {
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

// readValue reads one value. On a terminal it reads one line with no echo.
// Otherwise it reads all of r and trims exactly one trailing newline (\n or
// \r\n), so a piped PEM keeps its inner newlines and a piped token loses only
// the newline echo added.
func readValue(r io.Reader, tty bool) ([]byte, error) {
	var val []byte
	if tty {
		if f, ok := r.(*os.File); ok {
			b, err := term.ReadPassword(int(f.Fd()))
			if err != nil {
				return nil, errors.New("could not read the value")
			}
			val = b
		} else {
			line, err := readLine(bufio.NewReader(r))
			if err != nil {
				return nil, err
			}
			val = line
		}
	} else {
		b, err := io.ReadAll(io.LimitReader(r, secretstore.MaxValueBytes+2))
		if err != nil {
			return nil, errors.New("could not read the value")
		}
		val = bytes.TrimSuffix(bytes.TrimSuffix(b, []byte("\n")), []byte("\r"))
	}
	if len(val) > secretstore.MaxValueBytes {
		return nil, secretstore.ErrTooLarge
	}
	if len(val) == 0 {
		return nil, errors.New("value is empty")
	}
	return val, nil
}

// readLine reads up to the first newline and trims a trailing \r\n or \n. EOF
// after some bytes is a line; EOF with none is an error.
func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return nil, errors.New("no input")
	}
	return bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")), nil
}

// prompter reads passphrases: on a tty with no echo (twice for a new one),
// otherwise one line each from stdin.
type prompter struct {
	in  *bufio.Reader
	raw io.Reader // the original stdin, for the terminal fd
	tty bool
	out io.Writer
}

func (p *prompter) passphrase(label string) ([]byte, error) {
	if !p.tty {
		return readLine(p.in)
	}
	f, ok := p.raw.(*os.File)
	if !ok {
		return readLine(p.in)
	}
	fmt.Fprintf(p.out, "%s: ", label)
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(p.out)
	if err != nil {
		return nil, errors.New("could not read the passphrase")
	}
	return b, nil
}

// newPassphrase reads a passphrase to be set. On a terminal it asks twice and
// the two must match; piped, one line is the passphrase.
func (p *prompter) newPassphrase(label string) ([]byte, error) {
	first, err := p.passphrase(label)
	if err != nil {
		return nil, err
	}
	if !p.tty {
		return first, nil
	}
	defer wipe(first)
	again, err := p.passphrase("repeat " + label)
	if err != nil {
		return nil, err
	}
	defer wipe(again)
	if !bytes.Equal(first, again) {
		return nil, errors.New("the two passphrases differ")
	}
	return bytes.Clone(first), nil
}

// wipe zeroes b. Best effort: Go may have copied it.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
