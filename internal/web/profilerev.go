package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/culiops/culibox/internal/config"
)

// profileRev is a profile's revision: the hex sha256 of its JSON, 64 lowercase
// characters. It is derived from the content on every read and never stored, so
// it survives a restart and needs no new field in profiles.json. encoding/json
// writes map keys sorted, so two equal profiles hash alike whatever order their
// maps were filled in. It is taken of the merged (option-scoped) profile, the
// value Get returns, so what an editor was shown and what a save is checked
// against are the same bytes. It returns "" if the profile cannot be encoded,
// which matches no revision a client holds.
func profileRev(p config.AgentConfig) string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// What a refused save reports. The API maps them to a 409 (see writeSaveErr).
var (
	// ErrProfileChanged: the profile is not what the saver opened.
	ErrProfileChanged = errors.New("this profile was changed after you opened it (another tab, another device or a restart); nothing was saved")
	// ErrProfileGone: the profile the saver opened no longer exists.
	ErrProfileGone = errors.New("this profile no longer exists (it was deleted after you opened it); nothing was saved")
	// ErrProfileExists: a create named a profile that is already there.
	ErrProfileExists = errors.New("a profile with this name already exists; nothing was saved")
)
