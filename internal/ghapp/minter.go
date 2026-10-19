package ghapp

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

// MintInput names what a token should cover. Repos are bare repository names
// under Owner.
type MintInput struct {
	App, Owner  string
	Repos       []string
	Permissions map[string]string
}

// Minted is a fresh token plus the installation it came from.
type Minted struct {
	Token
	InstallationID int64
}

type installKey struct{ app, owner string }

// Minter turns a MintInput into an installation token. It caches each App's
// loaded key and each (app, owner) installation id; the JWT is signed per call.
type Minter struct {
	apps   map[string]config.GitHubApp
	client Client
	now    func() time.Time

	mu    sync.Mutex
	keys  map[string]*rsa.PrivateKey
	insts map[installKey]int64
}

func NewMinter(apps []config.GitHubApp, c Client) *Minter {
	m := &Minter{
		apps:   make(map[string]config.GitHubApp, len(apps)),
		client: c,
		now:    time.Now,
		keys:   map[string]*rsa.PrivateKey{},
		insts:  map[installKey]int64{},
	}
	for _, a := range apps {
		m.apps[a.Name] = a
	}
	return m
}

// key returns the cached key for app, loading it on first use.
func (m *Minter) key(app string) (config.GitHubApp, *rsa.PrivateKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.apps[app]
	if !ok {
		return a, nil, fmt.Errorf("unknown GitHub App %q", app)
	}
	if k, ok := m.keys[app]; ok {
		return a, k, nil
	}
	k, err := LoadKey(a.PrivateKey)
	if err != nil {
		return a, nil, fmt.Errorf("GitHub App %q: %w", app, err)
	}
	m.keys[app] = k
	return a, k, nil
}

// Check reports whether app is configured and its key loads (and caches it).
func (m *Minter) Check(app string) error {
	_, _, err := m.key(app)
	return err
}

// Mint returns an installation token limited to in.Repos and in.Permissions.
func (m *Minter) Mint(ctx context.Context, in MintInput) (Minted, error) {
	if in.Owner == "" || len(in.Repos) == 0 {
		return Minted{}, errors.New("mint needs an owner and at least one repository")
	}
	a, key, err := m.key(in.App)
	if err != nil {
		return Minted{}, err
	}
	jwt, err := SignJWT(key, a.ClientID, m.now())
	if err != nil {
		return Minted{}, err
	}
	ik := installKey{in.App, strings.ToLower(in.Owner)}
	m.mu.Lock()
	id, cached := m.insts[ik]
	m.mu.Unlock()
	if !cached {
		id, err = m.client.InstallationID(ctx, jwt, in.Owner, in.Repos[0])
		if err != nil {
			return Minted{}, err
		}
		m.mu.Lock()
		m.insts[ik] = id
		m.mu.Unlock()
	}
	tok, err := m.client.CreateToken(ctx, jwt, id, in.Repos, in.Permissions)
	if err != nil {
		// A stale cached id (App uninstalled, then reinstalled) must not stick.
		var ae *APIError
		if cached && errors.As(err, &ae) && ae.Status == 404 {
			m.mu.Lock()
			delete(m.insts, ik)
			m.mu.Unlock()
		}
		return Minted{}, err
	}
	return Minted{Token: tok, InstallationID: id}, nil
}

// Revoke invalidates an installation token.
func (m *Minter) Revoke(ctx context.Context, token string) error {
	return m.client.Revoke(ctx, token)
}
