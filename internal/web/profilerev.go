package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/store"
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

// profileDoc is a profile on the wire for the editor: the config fields plus
// the revision. GET sends it; PUT must send back the rev it was given. It is
// not a config field and never reaches profiles.json: Rev is a pointer so a
// missing rev (nil) and an empty one ("") can both be told from a real value.
type profileDoc struct {
	config.AgentConfig
	Rev *string `json:"rev,omitempty"`
}

// profilesFileChangedMsg is what a save refused by store.ErrChangedOnDisk says:
// the store never reloads, so the way back is a restart.
const profilesFileChangedMsg = "the profiles file was changed on disk after CuliBox loaded it, so nothing was saved; restart CuliBox to load it, then save again"

// revRequiredMsg is the 428 body of a PUT that carries no rev.
const revRequiredMsg = "this page is out of date (its save carries no revision): reload it and save again; an API client sends the rev from GET /api/profiles/{name}"

// writeSaveErr answers a failed profile save. A refusal that means "what you
// saw is not what is there" is a 409 with a code the editor acts on; anything
// else (a validation error) stays a 400 with its message, as it always was.
func writeSaveErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrProfileChanged), errors.Is(err, ErrProfileGone):
		writeErrCode(w, http.StatusConflict, err.Error(), "profile_changed")
	case errors.Is(err, ErrProfileExists):
		writeErrCode(w, http.StatusConflict, err.Error(), "profile_exists")
	case errors.Is(err, store.ErrChangedOnDisk):
		writeErrCode(w, http.StatusConflict, profilesFileChangedMsg, "profiles_file_changed")
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}
