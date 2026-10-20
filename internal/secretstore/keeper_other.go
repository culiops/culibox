//go:build !linux && !darwin

package secretstore

import "time"

type unsupportedKeeper struct{}

// NewKeeper returns a keeper whose every method reports ErrUnsupported.
func NewKeeper(string) Keeper { return unsupportedKeeper{} }

func (unsupportedKeeper) NeedsPassphrase() bool            { return true }
func (unsupportedKeeper) Load() (string, time.Time, error) { return "", time.Time{}, ErrUnsupported }
func (unsupportedKeeper) Save(string, time.Duration) error { return ErrUnsupported }
func (unsupportedKeeper) Clear() error                     { return ErrUnsupported }
