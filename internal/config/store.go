package config

import (
	"fmt"
	"time"
)

// Bounds and default for how long an unlock of the encrypted store lasts.
const (
	defaultStoreUnlockTTL = 168 * time.Hour
	minStoreUnlockTTL     = time.Hour
	maxStoreUnlockTTL     = 720 * time.Hour
)

// StoreConfig configures the encrypted value store. The zero value is the
// default: a 168h unlock.
type StoreConfig struct {
	UnlockTTL string `json:"unlock_ttl,omitempty"` // Go duration, 1h..720h; "" → 168h
}

// StoreUnlockTTL is how long an unlock lasts. An empty or unparsable value
// (Validate refuses the latter) means the default.
func (c Config) StoreUnlockTTL() time.Duration {
	if c.Store.UnlockTTL == "" {
		return defaultStoreUnlockTTL
	}
	d, err := time.ParseDuration(c.Store.UnlockTTL)
	if err != nil {
		return defaultStoreUnlockTTL
	}
	return d
}

func validStore(s StoreConfig) error {
	if s.UnlockTTL == "" {
		return nil
	}
	d, err := time.ParseDuration(s.UnlockTTL)
	if err != nil {
		return fmt.Errorf("store.unlock_ttl %q is not a duration (use e.g. \"168h\")", s.UnlockTTL)
	}
	if d < minStoreUnlockTTL || d > maxStoreUnlockTTL {
		return fmt.Errorf("store.unlock_ttl %q must be between %s and %s", s.UnlockTTL, minStoreUnlockTTL, maxStoreUnlockTTL)
	}
	return nil
}
