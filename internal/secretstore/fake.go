package secretstore

import (
	"sync"
	"time"
)

// FakeKeeper is an in-memory Keeper for tests, with an injectable clock.
type FakeKeeper struct {
	mu       sync.Mutex
	needs    bool
	identity string
	expires  time.Time
	now      func() time.Time
}

// NewFakeKeeper returns an empty keeper. needsPassphrase true behaves like
// Linux (TTL, ErrLocked), false like macOS (no TTL, ErrNotInitialized when
// empty).
func NewFakeKeeper(needsPassphrase bool) *FakeKeeper {
	return &FakeKeeper{needs: needsPassphrase, now: time.Now}
}

// SetNow replaces the clock used for TTL and expiry.
func (f *FakeKeeper) SetNow(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

func (f *FakeKeeper) NeedsPassphrase() bool { return f.needs }

func (f *FakeKeeper) Load() (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identity == "" {
		if f.needs {
			return "", time.Time{}, ErrLocked
		}
		return "", time.Time{}, ErrNotInitialized
	}
	if f.needs && !f.now().Before(f.expires) {
		return "", time.Time{}, ErrLocked
	}
	return f.identity, f.expires, nil
}

func (f *FakeKeeper) Save(identity string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity = identity
	f.expires = time.Time{}
	if f.needs {
		f.expires = f.now().Add(ttl)
	}
	return nil
}

func (f *FakeKeeper) Clear() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity = ""
	f.expires = time.Time{}
	return nil
}
