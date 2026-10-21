package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/secretstore"
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

// RecordCred appends one credential-definition event, opening audit.db on the
// first one the same way RecordStore does. The error is returned so the caller
// can fail closed.
func (l *lazyAudit) RecordCred(e broker.CredEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.a == nil {
		a, err := broker.OpenAudit(l.path)
		if err != nil {
			return fmt.Errorf("audit store: %w", err)
		}
		l.a = a
	}
	return l.a.RecordCred(e)
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

// logPlaintextCopies prints one start-up line per plaintext copy, names only.
func logPlaintextCopies(names []string) {
	for _, n := range names {
		log.Printf("store: plaintext copy of %s is still in the daemon environment", n)
	}
}
