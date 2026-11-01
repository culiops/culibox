// Package secretchain resolves a secret name through the configured
// secret_sources in order, one of which may be the encrypted store.
package secretchain

import (
	"errors"
	"fmt"
	"slices"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
	"github.com/culiops/agent-box/internal/secrets"
	"github.com/culiops/agent-box/internal/secretstore"
)

// resolver is what Chain resolves non-store sources through; secrets.New's result satisfies it.
type resolver interface {
	Resolve(name string) (string, bool, error)
}

// Chain walks the sources in the order given. The first source that has the
// name wins. A locked store is an error, never "not found": a plaintext copy
// further down the chain must not mask a lock.
type Chain struct {
	steps []step
	// probe is the store's id list and lock state when it offers them, so Check
	// can say a value exists without decrypting it. nil = Check reads through Get.
	probe StoreProbe
}

// StoreProbe is the part of the encrypted store that answers "is this id there,
// and is the store locked" without decrypting a value. *secretstore.Store
// satisfies it; List works while the store is locked.
type StoreProbe interface {
	List() ([]secretstore.Entry, error)
	Status() secretstore.Status
}

// Presence is what Check says about a secret name.
type Presence int

const (
	Absent            Presence = iota // no source has it
	Present                           // a source has it
	StoredLocked                      // the store has it (or is locked) and cannot be read now
	StoredUnavailable                 // the store has it but its key could not be read from the Keychain
)

type step struct {
	store credref.StoreGetter // non-nil for the store source
	res   resolver            // non-nil for every other source
}

// New builds the chain. Each non-store source becomes its own secrets
// resolver (so the store type never reaches secrets.New). A store source needs
// st: without one the chain refuses to build rather than silently skipping it.
func New(sources []config.SecretSource, st credref.StoreGetter) (*Chain, error) {
	c := &Chain{}
	c.probe, _ = st.(StoreProbe)
	for _, s := range sources {
		if s.Type == "store" {
			if st == nil {
				return nil, errors.New("secretchain: a store secret_source needs the agent-box store")
			}
			c.steps = append(c.steps, step{store: st})
			continue
		}
		r, err := secrets.New([]config.SecretSource{s})
		if err != nil {
			return nil, err
		}
		c.steps = append(c.steps, step{res: r})
	}
	return c, nil
}

// Resolve returns the first source's value for name. The store only answers
// for a valid store id; any other name is skipped, not an error.
func (c *Chain) Resolve(name string) (string, bool, error) {
	for _, s := range c.steps {
		if s.res != nil {
			v, ok, err := s.res.Resolve(name)
			if err != nil || ok {
				return v, ok, err
			}
			continue
		}
		if !secretstore.ValidID(name) {
			continue
		}
		raw, err := credref.GetStore(s.store, name)
		if errors.Is(err, secretstore.ErrNoSuchValue) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		v, err := credref.Value(raw)
		if err != nil {
			return "", false, fmt.Errorf("store %s: %w", name, err)
		}
		return string(v), true, nil
	}
	return "", false, nil
}

// Check walks the sources like Resolve but reports only whether name resolves.
// For the store it asks by id (List) and never decrypts when the store offers
// that; otherwise it reads through Get into a buffer it zeroes at once. The env
// and file sources can only answer by resolving, so their value passes through
// a local string that is dropped unread (a Go string cannot be zeroed). The
// order and the lock rule are Resolve's: the first source that has the name
// wins, and a locked store that holds it is StoredLocked, never Absent.
func (c *Chain) Check(name string) (Presence, error) {
	for _, s := range c.steps {
		if s.res != nil {
			_, ok, err := s.res.Resolve(name)
			if err != nil {
				return Absent, err
			}
			if ok {
				return Present, nil
			}
			continue
		}
		if !secretstore.ValidID(name) {
			continue
		}
		if c.probe != nil {
			ents, err := c.probe.List()
			if errors.Is(err, secretstore.ErrNotInitialized) {
				continue
			}
			if err != nil {
				return Absent, err
			}
			if !slices.ContainsFunc(ents, func(e secretstore.Entry) bool { return e.ID == name }) {
				continue
			}
			st := c.probe.Status()
			if st.Unavailable != nil {
				return StoredUnavailable, nil
			}
			if st.Locked {
				return StoredLocked, nil
			}
			return Present, nil
		}
		raw, err := credref.GetStore(s.store, name)
		for i := range raw {
			raw[i] = 0
		}
		switch {
		case errors.Is(err, secretstore.ErrNoSuchValue):
			continue
		case errors.Is(err, credref.ErrLocked):
			return StoredLocked, nil
		case errors.Is(err, secretstore.ErrKeeperUnavailable):
			return StoredUnavailable, nil
		case err != nil:
			return Absent, err
		}
		return Present, nil
	}
	return Absent, nil
}
