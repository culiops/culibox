// Package auth: tokens.go — named, revocable bearer tokens stored as hashes.
package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/datacompat"
)

// ErrTokenNotFound is returned by Revoke for an unknown id.
var ErrTokenNotFound = errors.New("auth: token not found")

const base62 = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// TokenInfo is the non-secret view of a token (safe to serve).
type TokenInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at,omitempty"`
}

type tokenRec struct {
	TokenInfo
	Hash string `json:"hash"` // sha256 hex of the plaintext
}

// TokenStore is a JSON-file-backed set of access tokens, safe for concurrent use.
type TokenStore struct {
	path string
	mu   sync.Mutex
	m    map[string]*tokenRec // keyed by id
	now  func() time.Time
}

// OpenTokenStore loads the store from path (a missing file is an empty store).
// The file is decoded strictly: a field this build does not know could be a
// future scope or expiry, and ignoring it would widen that token to full
// access, so any parse failure is reported as a file a newer CuliBox may have
// written (tokens.json has no version field to tell the cases apart).
func OpenTokenStore(path string) (*TokenStore, error) {
	s := &TokenStore{path: path, m: map[string]*tokenRec{}, now: time.Now}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []*tokenRec
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	err = dec.Decode(&list)
	if err == nil {
		if _, e := dec.Token(); !errors.Is(e, io.EOF) {
			err = errors.New("unexpected data after the JSON value")
		}
	}
	if err != nil {
		return nil, datacompat.Newer(path, "it does not parse as this build's token list: "+err.Error())
	}
	for _, r := range list {
		if r == nil {
			return nil, datacompat.Newer(path, "it does not parse as this build's token list: a record is null")
		}
		s.m[r.ID] = r
	}
	return s, nil
}

func randString(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(base62)))
	for i := range out {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = base62[k.Int64()]
	}
	return string(out), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// Create mints a new token, persists its hash + metadata, and returns the
// plaintext exactly once. 28 base62 chars ≈ 166 bits of entropy.
func (s *TokenStore) Create(name string) (string, TokenInfo, error) {
	body, err := randString(28)
	if err != nil {
		return "", TokenInfo{}, err
	}
	id, err := randString(12)
	if err != nil {
		return "", TokenInfo{}, err
	}
	plain := "abx_" + body
	rec := &tokenRec{
		TokenInfo: TokenInfo{ID: id, Name: name, CreatedAt: s.now().Unix()},
		Hash:      hashToken(plain),
	}
	s.mu.Lock()
	s.m[id] = rec
	err = s.flushLocked()
	if err != nil {
		delete(s.m, id) // don't leave a live token the caller was told failed
	}
	s.mu.Unlock()
	if err != nil {
		return "", TokenInfo{}, err
	}
	return plain, rec.TokenInfo, nil
}

// List returns token metadata (never the hash), sorted by creation time.
func (s *TokenStore) List() []TokenInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TokenInfo, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r.TokenInfo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// Revoke deletes a token by id.
func (s *TokenStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.m[id]
	if !ok {
		return ErrTokenNotFound
	}
	delete(s.m, id)
	if err := s.flushLocked(); err != nil {
		s.m[id] = rec // roll back: still live until the delete can be persisted
		return err
	}
	return nil
}

// Verify hashes the presented token and returns its info if live. It bumps
// LastUsedAt best-effort (a persist failure does not fail the auth check).
func (s *TokenStore) Verify(presented string) (TokenInfo, bool) {
	want := hashToken(presented)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.m {
		if subtle.ConstantTimeCompare([]byte(r.Hash), []byte(want)) == 1 {
			// Bump LastUsedAt at most once per second. Bearer tokens are the
			// high-frequency path and flushLocked rewrites+fsyncs the whole file
			// under the lock, so persisting on every request would be a
			// throughput footgun for exactly the clients bearer tokens serve.
			if now := s.now().Unix(); r.LastUsedAt != now {
				r.LastUsedAt = now
				_ = s.flushLocked() // best-effort
			}
			return r.TokenInfo, true
		}
	}
	return TokenInfo{}, false
}

func (s *TokenStore) flushLocked() error {
	list := make([]*tokenRec, 0, len(s.m))
	for _, r := range s.m {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
