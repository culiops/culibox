package web

import (
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

// The walker's inputs: small local structs, so the fingerprint's rules are
// tested without editing config.AgentConfig.
type schemaInner struct {
	P string `json:"p"`
}

type schemaBase struct {
	X    int           `json:"x"`
	Skip int           `json:"-"`
	hid  int           //nolint:unused // unexported: not on the wire
	In   *schemaInner  `json:"in,omitempty"`
	List []schemaInner `json:"list,omitempty"`
}

// The same wire shape with the fields in another order.
type schemaReordered struct {
	List []schemaInner `json:"list,omitempty"`
	In   *schemaInner  `json:"in,omitempty"`
	X    int           `json:"x"`
}

type schemaTopAdded struct {
	X    int           `json:"x"`
	Y    int           `json:"y"`
	In   *schemaInner  `json:"in,omitempty"`
	List []schemaInner `json:"list,omitempty"`
}

type schemaInnerAdded struct {
	P string `json:"p"`
	Q string `json:"q"`
}

type schemaNestedAdded struct {
	X    int                `json:"x"`
	In   *schemaInnerAdded  `json:"in,omitempty"`
	List []schemaInnerAdded `json:"list,omitempty"`
}

// A nested "p" and a top-level "p" are two fields, not one.
type schemaTopP struct {
	X  int           `json:"x"`
	P  string        `json:"p"`
	In *schemaInner  `json:"in,omitempty"`
	L  []schemaInner `json:"list,omitempty"`
}

type schemaEmbedded struct {
	schemaInner
	X int `json:"x"`
}

type schemaLoop struct {
	Name string      `json:"name"`
	Next *schemaLoop `json:"next,omitempty"`
}

var schemaHex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestSchemaOfIsSixteenLowercaseHexAndStable(t *testing.T) {
	a := schemaOf(reflect.TypeOf(schemaBase{}))
	if !schemaHex16.MatchString(a) {
		t.Fatalf("schemaOf = %q, want 16 lowercase hex chars", a)
	}
	if b := schemaOf(reflect.TypeOf(schemaBase{})); b != a {
		t.Fatalf("schemaOf is not stable: %s vs %s", a, b)
	}
}

func TestSchemaOfChangesWhenAFieldNameIsAdded(t *testing.T) {
	base := schemaOf(reflect.TypeOf(schemaBase{}))
	if got := schemaOf(reflect.TypeOf(schemaTopAdded{})); got == base {
		t.Fatal("a field added at the top level did not change the schema")
	}
	// The walk goes into nested struct types, through a pointer and a slice.
	if got := schemaOf(reflect.TypeOf(schemaNestedAdded{})); got == base {
		t.Fatal("a field added to a nested struct did not change the schema")
	}
	if got := schemaOf(reflect.TypeOf(schemaTopP{})); got == base {
		t.Fatal("a top-level \"p\" next to a nested \"p\" did not change the schema")
	}
}

func TestSchemaOfIgnoresWhatIsNotOnTheWire(t *testing.T) {
	base := schemaOf(reflect.TypeOf(schemaBase{}))
	// "-" and unexported fields (Skip, hid) are not wire fields, and the order
	// the fields are declared in is not part of the shape.
	if got := schemaOf(reflect.TypeOf(schemaReordered{})); got != base {
		t.Fatalf("a reordered struct with the same wire fields changed the schema: %s vs %s", got, base)
	}
}

func TestSchemaOfPromotesEmbeddedStructsAndStopsAtCycles(t *testing.T) {
	// encoding/json promotes an untagged embedded struct's fields: "p" and "x".
	type flat struct {
		P string `json:"p"`
		X int    `json:"x"`
	}
	if schemaOf(reflect.TypeOf(schemaEmbedded{})) != schemaOf(reflect.TypeOf(flat{})) {
		t.Fatal("an embedded struct's fields were not treated as the parent's own")
	}
	got := schemaOf(reflect.TypeOf(schemaLoop{})) // must return, not recurse forever
	if !schemaHex16.MatchString(got) {
		t.Fatalf("schemaOf of a recursive type = %q", got)
	}
}

func TestProfileSchemaIsTheAgentConfigFingerprint(t *testing.T) {
	got := profileSchema()
	if !schemaHex16.MatchString(got) {
		t.Fatalf("profileSchema = %q, want 16 lowercase hex chars", got)
	}
	if again := profileSchema(); again != got {
		t.Fatalf("profileSchema is not stable: %s vs %s", got, again)
	}
	if want := schemaOf(reflect.TypeOf(config.AgentConfig{})); got != want {
		t.Fatalf("profileSchema = %s, want the walker's value for AgentConfig %s", got, want)
	}
}

// /api/meta is the one value the page reads once at load; it carries the
// schema, and a second read gives the same value.
func TestMetaCarriesTheProfileSchema(t *testing.T) {
	s := revServer(t)
	s.AllowedHosts = []string{"example.com"}
	got := metaGet(t, s)
	v, _ := got["profile_schema"].(string)
	if !schemaHex16.MatchString(v) || v != profileSchema() {
		t.Fatalf("meta profile_schema = %v, want %s", got["profile_schema"], profileSchema())
	}
	if again, _ := metaGet(t, s)["profile_schema"].(string); again != v {
		t.Fatalf("meta profile_schema is not stable: %s vs %s", again, v)
	}
}

// An API client does GET, edits, PUTs back: the document it reads carries the
// schema next to the rev.
func TestGetProfileCarriesTheSchema(t *testing.T) {
	s := revServer(t)
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/p", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"schema":"`+profileSchema()+`"`) {
		t.Fatalf("GET /api/profiles/p: %d %s, want the schema in the document", rec.Code, rec.Body)
	}
}

func TestPutWithAWrongSchemaIs409PageOutdatedAndStoresNothing(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	rec := putWithRevSchema(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, rev, "0123456789abcdef")
	if rec.Code != 409 || apiCode(t, rec) != "page_outdated" {
		t.Fatalf("PUT with a wrong schema: %d %s, want 409 page_outdated", rec.Code, rec.Body)
	}
	const msg = "CuliBox was updated since this page was loaded; reload the page, then save again; nothing was saved"
	if !strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("PUT with a wrong schema: %s, want the message %q", rec.Body, msg)
	}
	if p, _ := s.Profiles.Get("p"); len(p.Args) != 0 {
		t.Fatalf("a refused PUT stored something: %+v", p)
	}
	if got := revOf(t, s, "p"); got != rev {
		t.Fatal("a refused PUT changed the profile")
	}
}

func TestPutWithoutASchemaIs428LikeAMissingRev(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	for _, schema := range []string{noSchema, ""} {
		rec := putWithRevSchema(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, rev, schema)
		if rec.Code != 428 || apiCode(t, rec) != "rev_required" || !strings.Contains(rec.Body.String(), revRequiredMsg) {
			t.Fatalf("PUT with schema %q: %d %s, want 428 rev_required with the same message as a missing rev", schema, rec.Code, rec.Body)
		}
	}
	if p, _ := s.Profiles.Get("p"); len(p.Args) != 0 {
		t.Fatalf("a refused PUT stored something: %+v", p)
	}
}

func TestPutWithTheRightSchemaAndRevIs204(t *testing.T) {
	s := revServer(t)
	rec := putWithRevSchema(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, revOf(t, s, "p"), profileSchema())
	if rec.Code != 204 {
		t.Fatalf("PUT with the right schema and rev: %d %s, want 204", rec.Code, rec.Body)
	}
	if p, _ := s.Profiles.Get("p"); len(p.Args) != 1 {
		t.Fatalf("the save did not land: %+v", p)
	}
}

// The schema is checked before the rev: a page from before an update is told so,
// not told its profile changed (which Load latest would not cure).
func TestPutChecksTheSchemaBeforeTheRev(t *testing.T) {
	s := revServer(t)
	stale := strings.Repeat("0", 64)
	rec := putWithRevSchema(t, s, "p", `{"name":"p","bin":"bash"}`, stale, "0123456789abcdef")
	if rec.Code != 409 || apiCode(t, rec) != "page_outdated" {
		t.Fatalf("wrong schema and stale rev: %d %s, want 409 page_outdated", rec.Code, rec.Body)
	}
}

func TestPostWithAWrongSchemaIs409AndCreatesNothing(t *testing.T) {
	s := revServer(t)
	rec := postProfile(s, `{"name":"q","bin":"bash","schema":"0123456789abcdef"}`)
	if rec.Code != 409 || apiCode(t, rec) != "page_outdated" {
		t.Fatalf("POST with a wrong schema: %d %s, want 409 page_outdated", rec.Code, rec.Body)
	}
	if _, ok := s.Profiles.Get("q"); ok {
		t.Fatal("a refused POST created the profile")
	}
}

// API clients create with a bare POST; a page sends the schema it holds.
func TestPostWithoutASchemaOrWithTheRightOneCreates(t *testing.T) {
	s := revServer(t)
	if rec := postProfile(s, `{"name":"q","bin":"bash"}`); rec.Code != 201 {
		t.Fatalf("POST without a schema: %d %s, want 201", rec.Code, rec.Body)
	}
	if rec := postProfile(s, `{"name":"r","bin":"bash","schema":"`+profileSchema()+`"}`); rec.Code != 201 {
		t.Fatalf("POST with the right schema: %d %s, want 201", rec.Code, rec.Body)
	}
}

// The schema is wire-only, like the rev: no config field may share its name, or
// the wire field would shadow it and a save would write the page's value into
// profiles.json.
func TestNoConfigFieldIsCalledSchemaOrRev(t *testing.T) {
	for _, n := range schemaNames(reflect.TypeOf(config.AgentConfig{})) {
		if n == "schema" || n == "rev" {
			t.Fatalf("config.AgentConfig has a json field %q, which the profile wire format uses", n)
		}
	}
}
