// Package secretchain resolves a secret name through the configured
// secret_sources in order, one of which may be the encrypted store.
package secretchain

import (
	"errors"
	"fmt"

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
}

type step struct {
	store credref.StoreGetter // non-nil for the store source
	res   resolver            // non-nil for every other source
}

// New builds the chain. Each non-store source becomes its own secrets
// resolver (so the store type never reaches secrets.New). A store source needs
// st: without one the chain refuses to build rather than silently skipping it.
func New(sources []config.SecretSource, st credref.StoreGetter) (*Chain, error) {
	c := &Chain{}
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
