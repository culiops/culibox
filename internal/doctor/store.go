package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/storerefs"
)

// StoreFor opens the encrypted store under dataDir for a config that reads from
// it, read-only and without an audit recorder (doctor changes nothing). It
// returns an untyped nil for a config that does not use the store, so callers
// can pass the result on without a typed-nil interface. doctor runs in its own
// process and reads the same kernel keyring entry the daemon does.
func StoreFor(c config.Config, dataDir string) credref.StoreGetter {
	if st := openStore(c, dataDir); st != nil {
		return st
	}
	return nil
}

// openStore is StoreFor with the concrete type, nil when the config does not
// use the store.
func openStore(c config.Config, dataDir string) *secretstore.Store {
	if !storerefs.InUse(c) {
		return nil
	}
	return secretstore.Open(dataDir, secretstore.NewKeeper(dataDir), nil)
}

// openStoreForCheck is openStore, but also opens an initialized store the
// config does not reference yet: the daemon's seccomp gate fires on an
// initialized store, so doctor must look at it too. nil when there is no store.
func openStoreForCheck(c config.Config, dataDir string) *secretstore.Store {
	if st := openStore(c, dataDir); st != nil {
		return st
	}
	st := secretstore.Open(dataDir, secretstore.NewKeeper(dataDir), nil)
	if st.Status().Initialized {
		return st
	}
	return nil
}

// defaultResolver resolves file, env and store references, the way the daemon
// does. A nil store refuses store references.
func defaultResolver(st credref.StoreGetter) credref.Default {
	d := credref.Default{}
	if st != nil {
		d.Store = st
	}
	return d
}

// checkStore reports the encrypted store's state. It returns nothing when
// nothing uses the store and none is initialized. Never prints a value: ids, reference
// names and secret NAMES only.
//
// A missing value FAILs for a direct reference (a GitHub App key or a static
// token) because nothing else can serve it. A declared secret name missing
// from the store is not reported: a later secret_source (env, file) may serve
// it, and a name that resolves nowhere is the spawn's concern, not the store's.
// seccomp is only consulted on linux (macOS boxes run in a VM) and only once a
// store is in use or initialized; nil skips it.
func checkStore(c config.Config, st *secretstore.Store, accts []account.Account,
	lookupEnv func(string) (string, bool), goos string, seccomp func() (bool, error)) []Result {
	const ck = "store"
	inUse := storerefs.InUse(c)
	s := st.Status()
	if !inUse {
		// Nothing references the store, so it has nothing to report beyond the
		// daemon's seccomp gate, which fires as soon as a store is initialized.
		if !s.Initialized {
			return nil
		}
		return seccompResults(ck, goos, seccomp)
	}
	if !s.Initialized {
		return []Result{{Fail, ck, `store not initialized: the configuration reads it (run "agent-box store init")`}}
	}
	var out []Result
	if s.Locked {
		out = append(out, Result{Warn, ck, `store locked (unlock with "agent-box store unlock")`})
	} else if s.ExpiresAt.IsZero() {
		out = append(out, Result{Pass, ck, fmt.Sprintf("store unlocked, %d values", s.Values)})
	} else {
		out = append(out, Result{Pass, ck, fmt.Sprintf("store unlocked, %d values, key expires %s",
			s.Values, s.ExpiresAt.UTC().Format(time.RFC3339))})
	}
	ents, err := st.List()
	if err != nil {
		return append(out, Result{Fail, ck, "cannot list stored values: " + err.Error()})
	}
	have := map[string]bool{}
	for _, e := range ents {
		have[e.ID] = true
	}
	refs := storerefs.Refs(c, accts)
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if have[id] {
			continue
		}
		var direct []string
		for _, r := range refs[id] {
			if strings.HasPrefix(r, "github_apps/") || strings.HasPrefix(r, "static_tokens/") {
				direct = append(direct, r)
			}
		}
		if len(direct) > 0 {
			out = append(out, Result{Fail, ck, fmt.Sprintf("value %q is missing from the store (used by %s)", id, strings.Join(direct, ", "))})
		}
	}
	for _, name := range storerefs.PlaintextCopies(c, accts, st, lookupEnv) {
		out = append(out, Result{Warn, ck, "plaintext copy of " + name + " is still in this environment"})
	}
	return append(out, seccompResults(ck, goos, seccomp)...)
}

// seccompResults is the Docker seccomp check the daemon gate enforces once a
// store is initialized. Linux only (macOS boxes run in a VM); nil skips it.
func seccompResults(ck, goos string, seccomp func() (bool, error)) []Result {
	if goos != "linux" || seccomp == nil {
		return nil
	}
	switch on, err := seccomp(); {
	case errors.Is(err, exec.ErrNotFound):
		// No docker CLI: no boxes either, so there is nothing to gate (the
		// boxed tier row already reports the missing docker).
	case err != nil:
		return []Result{{Warn, ck, "could not tell whether Docker seccomp is on (" + err.Error() + ")"}}
	case !on:
		return []Result{{Fail, ck, box.SeccompOffText}}
	}
	return nil
}

// dockerSeccomp asks the local docker daemon whether its default seccomp
// profile is on, only when checkStore needs the answer.
func dockerSeccomp(c config.Config) func() (bool, error) {
	return func() (bool, error) {
		bin, err := box.LookDocker(c.DockerBin)
		if err != nil {
			return false, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return box.CLIClient{Bin: bin}.SeccompActive(ctx)
	}
}
