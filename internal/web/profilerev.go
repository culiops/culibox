package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"

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

// profileSchema is the fingerprint of the profile wire format this build knows:
// the first 16 hex chars of the sha256 of every JSON field name of
// config.AgentConfig and of the structs nested in it. A page reads it once at
// load and sends it back with each save; a save from a page that was loaded
// before an update that added a field would otherwise be accepted and silently
// drop that field, because the old form does not know it. It is derived from
// the type, never stored, and computed once.
func profileSchema() string {
	profileSchemaOnce.Do(func() { profileSchemaValue = schemaOf(reflect.TypeOf(config.AgentConfig{})) })
	return profileSchemaValue
}

var (
	profileSchemaOnce  sync.Once
	profileSchemaValue string
)

// schemaOf is the fingerprint of t's JSON field names (see profileSchema).
func schemaOf(t reflect.Type) string {
	sum := sha256.Sum256([]byte(strings.Join(schemaNames(t), "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// schemaNames lists the JSON field names reachable from t, sorted. A nested
// struct's fields carry their path ("aws.region"), so a nested "p" and a
// top-level "p" are two names. Fields json skips ("-", unexported) are left
// out; an untagged embedded struct's fields count as the parent's, as
// encoding/json promotes them. Pointers, slices, arrays and maps are looked
// through to their element type; a type met again on its own path is not
// entered twice, so a recursive type ends.
func schemaNames(t reflect.Type) []string {
	var names []string
	walkSchema(t, "", map[reflect.Type]bool{}, &names)
	sort.Strings(names)
	return names
}

func walkSchema(t reflect.Type, prefix string, onPath map[reflect.Type]bool, out *[]string) {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
			continue
		}
		break
	}
	if t.Kind() != reflect.Struct || onPath[t] {
		return
	}
	onPath[t] = true
	defer delete(onPath, t)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			walkSchema(f.Type, prefix, onPath, out)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		*out = append(*out, prefix+name)
		walkSchema(f.Type, prefix+name+".", onPath, out)
	}
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
// the revision and the schema. GET sends both; PUT must send back the rev it was
// given and the schema its page loaded with. Neither is a config field and
// neither reaches profiles.json. Both are pointers so a missing value (nil) and
// an empty one ("") can be told from a real one.
type profileDoc struct {
	config.AgentConfig
	Rev    *string `json:"rev,omitempty"`
	Schema *string `json:"schema,omitempty"`
}

// pageOutdatedMsg is the 409 body of a save whose schema is not this build's.
const pageOutdatedMsg = "CuliBox was updated since this page was loaded; reload the page, then save again; nothing was saved"

// schemaStale reports whether schema, the one a save carried, is one this build
// does not hold. A nil schema is "not sent" and is the caller's to judge.
func schemaStale(schema *string) bool {
	return schema != nil && *schema != profileSchema()
}

// writePageOutdated answers a save from a page loaded before an update: its form
// does not know every field the profile now has, so saving it would drop them.
func writePageOutdated(w http.ResponseWriter) {
	writeErrCode(w, http.StatusConflict, pageOutdatedMsg, "page_outdated")
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
	case errors.Is(err, ErrProfileChanged):
		writeErrCode(w, http.StatusConflict, err.Error(), "profile_changed")
	case errors.Is(err, ErrProfileGone):
		// The same code as a changed profile, plus "gone": there is no saved
		// version for the editor to load, so it must not offer to replace the
		// user's edits with one.
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "profile_changed", "gone": true})
	case errors.Is(err, ErrProfileExists):
		writeErrCode(w, http.StatusConflict, err.Error(), "profile_exists")
	case errors.Is(err, store.ErrChangedOnDisk):
		writeErrCode(w, http.StatusConflict, profilesFileChangedMsg, "profiles_file_changed")
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}
