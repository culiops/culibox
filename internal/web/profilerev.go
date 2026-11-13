package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

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
// config.AgentConfig and of the structs nested in it, each with the shape of
// its value (see schemaShape). A page reads it once, from the script it loaded
// with, and sends it back with each save; a save from a page that was loaded
// before an update that added or retyped a field would otherwise be accepted and
// silently drop or garble that field, because the old form does not know it. It
// is derived from the type, never stored, and computed once.
//
// What it cannot see: a field that keeps its name and its shape but changes
// meaning (a string that held seconds and now holds a duration). Such a change
// has to come with a new field name, or with a bump of schemaSalt.
func profileSchema() string {
	profileSchemaOnce.Do(func() { profileSchemaValue = schemaOf(reflect.TypeOf(config.AgentConfig{})) })
	return profileSchemaValue
}

// schemaSalt is part of every fingerprint. Change it when a field's meaning
// changes without its name or shape changing, so pages loaded before refuse
// their saves (page_outdated) instead of writing the old meaning.
const schemaSalt = "1"

var (
	profileSchemaOnce  sync.Once
	profileSchemaValue string
)

// schemaOf is the fingerprint of t's JSON field names and value shapes (see
// profileSchema).
func schemaOf(t reflect.Type) string {
	var parts []string
	for _, f := range schemaFields(t) {
		parts = append(parts, f.name+":"+f.shape)
	}
	sum := sha256.Sum256([]byte(schemaSalt + "\n" + strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// schemaField is one JSON field reachable from a type: its dotted name and the
// shape of its value.
type schemaField struct{ name, shape string }

// schemaNames lists the JSON field names reachable from t, sorted. A nested
// struct's fields carry their path ("aws.region"), so a nested "p" and a
// top-level "p" are two names. Fields json skips ("-", unexported) are left
// out; an untagged embedded struct's fields count as the parent's, as
// encoding/json promotes them. Pointers, slices, arrays and maps are looked
// through to their element type; a type met again on its own path is not
// entered twice, so a recursive type ends.
func schemaNames(t reflect.Type) []string {
	var names []string
	for _, f := range schemaFields(t) {
		names = append(names, f.name)
	}
	return names
}

// schemaFields is schemaNames with each field's shape, sorted by name.
func schemaFields(t reflect.Type) []schemaField {
	var out []schemaField
	walkSchema(t, "", map[reflect.Type]bool{}, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// schemaShape is what a field's value looks like on the wire: a pointer is the
// value it points to, a slice or array is a list of its element's shape, a map
// is keyed by its key kind, a struct is an object (its own fields are listed on
// their own), anything else is its kind.
func schemaShape(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return schemaShape(t.Elem())
	case reflect.Slice, reflect.Array:
		return "[]" + schemaShape(t.Elem())
	case reflect.Map:
		return "map[" + t.Key().Kind().String() + "]" + schemaShape(t.Elem())
	case reflect.Struct:
		return "object"
	}
	return t.Kind().String()
}

func walkSchema(t reflect.Type, prefix string, onPath map[reflect.Type]bool, out *[]schemaField) {
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
		*out = append(*out, schemaField{prefix + name, schemaShape(f.Type)})
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

// errDeleteRevRequired: a delete named no revision (Remove).
var errDeleteRevRequired = errors.New("a delete needs the revision the profile was opened at")

// storeFailure is a profile store write that failed (disk full, no permission,
// a file that cannot be read). The cause names a path on the host, so it is kept
// for the log and its text is never what Error says or the API answers with.
type storeFailure struct{ cause error }

func (e *storeFailure) Error() string { return "profiles store write failed" }
func (e *storeFailure) Unwrap() error { return e.cause }

// profileStoreErr marks err, a store write failure, as a *storeFailure; the one refusal
// the store makes on purpose (a profiles file changed by hand) stays as it is.
func profileStoreErr(err error) error {
	if err == nil || errors.Is(err, store.ErrChangedOnDisk) {
		return err
	}
	return &storeFailure{cause: err}
}

// saveFailedMsg is the 500 body of a save the store could not write.
const saveFailedMsg = "the profile could not be written; nothing was saved (details are in the CuliBox log)"

// deleteFailedMsg is the 500 body of a delete the store could not write.
const deleteFailedMsg = "delete failed"

// deleteChangedMsg is the 409 body of a delete whose revision is not the
// profile's: the profile was edited after the page opened it.
const deleteChangedMsg = "this profile was changed after you opened it (another tab, another device or a restart); nothing was deleted; reload it and look again"

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

// revRequiredMsg is the 428 body of a PUT that carries no rev. The page that
// sent it knows the schema (that is checked first), so what is missing is the
// revision of the profile it opened.
const revRequiredMsg = "this save carries no revision: open the profile again and save; an API client sends the rev from GET /api/profiles/{name}"

// schemaRequiredMsg is the 428 body of a PUT that carries no schema: a page from
// before the schema existed, or a client that did not send it.
const schemaRequiredMsg = "this page is out of date (its save carries no schema version): reload it and save again; an API client sends the schema from GET /api/profiles/{name}"

// deleteRevRequiredMsg is the 428 body of a DELETE that carries no rev.
const deleteRevRequiredMsg = "this delete carries no revision: reload the page and delete again; an API client sends the rev from GET /api/profiles/{name} as ?rev="

// isStoreFailure reports whether err is a store write failure (a *storeFailure).
func isStoreFailure(err error) bool {
	var sf *storeFailure
	return errors.As(err, &sf)
}

// writeSaveErr answers a failed profile save. A refusal that means "what you
// saw is not what is there" is a 409 with a code the editor acts on; a store
// that could not write is a 500 without the path; anything else (a validation
// error) stays a 400 with its message, as it always was.
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
	case isStoreFailure(err):
		// Not the caller's fault and not a validation sentence: the cause (it names
		// a host path) goes to the log, the answer does not carry it.
		var sf *storeFailure
		errors.As(err, &sf)
		log.Printf("profile save: %v", sf.cause)
		writeErrCode(w, http.StatusInternalServerError, saveFailedMsg, "save_failed")
	default:
		// A validation refusal: the sentence an older client matches on, plus the
		// field it is about when the refusal names one. One that names no field
		// has no "field" key; the editor shows it at the top of the open block.
		var fe *config.FieldError
		if errors.As(err, &fe) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": fe.Path})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// profileFormJS is profileform.js with this build's profile schema appended as
// PROFILE_SCHEMA_BAKED, which app.js pins. The schema is part of the script the
// page runs, not a later /api/meta read: a page loaded while the daemon was
// restarting into a new build would otherwise pin the new build's schema next to
// the old build's form, and its saves would drop the fields the form does not
// know. The file and the schema come out of one binary, in one response.
func profileFormJS() ([]byte, error) {
	src, err := staticFS.ReadFile("static/profileform.js")
	if err != nil {
		return nil, err
	}
	return append(src, []byte("\nvar PROFILE_SCHEMA_BAKED = \""+profileSchema()+"\";\n")...), nil
}

// serveProfileForm answers GET /profileform.js (see profileFormJS).
func (s *Server) serveProfileForm(w http.ResponseWriter, r *http.Request) {
	b, err := profileFormJS()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "profileform.js unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "profileform.js", time.Time{}, bytes.NewReader(b))
}
