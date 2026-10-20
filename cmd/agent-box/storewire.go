package main

import (
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/storerefs"
)

// lazyAudit is the store's audit Recorder. It opens audit.db on the first
// event, so a daemon or command that never changes the store creates nothing,
// and a store action whose row cannot be written is refused (the store records
// before it acts). The daemon already holds other handles on the same file;
// SQLite's WAL and busy timeout cover concurrent writers.
type lazyAudit struct {
	path string
	mu   sync.Mutex
	a    *broker.Audit
}

func (l *lazyAudit) RecordStore(e secretstore.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.a == nil {
		a, err := broker.OpenAudit(l.path)
		if err != nil {
			return fmt.Errorf("audit store: %w", err)
		}
		l.a = a
	}
	return l.a.RecordStore(e)
}

func (l *lazyAudit) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.a == nil {
		return nil
	}
	err := l.a.Close()
	l.a = nil
	return err
}

// storeIDs lists the ids in the store. Satisfied by *secretstore.Store.
type storeIDs interface {
	List() ([]secretstore.Entry, error)
}

// plaintextCopies returns, sorted, the secret names that a store secret_source
// serves (an agent's secrets entry or an account's token_secret whose value is
// in the store) and that are also set non-empty in the daemon's own
// environment: a copy the store's encryption does not protect. Names only.
func plaintextCopies(cfg config.Config, accts []account.Account, st storeIDs, lookupEnv func(string) (string, bool)) []string {
	if !storerefs.HasSource(cfg) {
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
	for name := range storerefs.SecretNameRefs(cfg, accts) {
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

// logPlaintextCopies prints one start-up line per plaintext copy, names only.
func logPlaintextCopies(names []string) {
	for _, n := range names {
		log.Printf("store: plaintext copy of %s is still in the daemon environment", n)
	}
}
