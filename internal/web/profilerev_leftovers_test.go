package web

import (
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/store"
)

// Leftovers of the profile revision work (#120).

// ioFailStore is a profile store whose writes fail the way a disk does: with an
// error that names a host path.
type ioFailStore struct {
	profileStore
	err error
}

func (s ioFailStore) Save(config.AgentConfig) error { return s.err }
func (s ioFailStore) Delete(string) error           { return s.err }

const hostPathErr = "/home/someone/.local/share/culibox/profiles.json.tmp"

func ioErr() error {
	return &fs.PathError{Op: "open", Path: hostPathErr, Err: syscall.EACCES}
}

// A store write that fails is the server's fault and its error names a path on
// the host: a 500 that does not say where, never a 400 with the path.
func TestSaveStoreFailureIs500AndNamesNoHostPath(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	s.Profiles.store = ioFailStore{s.Profiles.store, ioErr()}
	puts := putWithRev(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, rev)
	post := postProfile(s, `{"name":"q","bin":"bash"}`)
	for name, rec := range map[string]*httptest.ResponseRecorder{"PUT": puts, "POST": post} {
		if rec.Code != 500 {
			t.Errorf("%s with a failing store: %d %s, want 500", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "/home/") || strings.Contains(rec.Body.String(), "profiles.json") {
			t.Errorf("%s answered with the host path: %s", name, rec.Body)
		}
		if apiCode(t, rec) != "save_failed" {
			t.Errorf("%s code = %q, want save_failed", name, apiCode(t, rec))
		}
	}
	if p, _ := s.Profiles.Get("p"); len(p.Args) != 0 {
		t.Fatalf("a failed save changed the profile: %+v", p)
	}
}

// A validation refusal stays a 400 with its sentence: only a store failure moved.
func TestValidationRefusalIsStill400(t *testing.T) {
	s := revServer(t)
	rec := putProfile(t, s, "p", `{"name":"p","bin":"bash","base":"nope"}`)
	if rec.Code != 400 {
		t.Fatalf("invalid profile: %d %s, want 400", rec.Code, rec.Body)
	}
}

func TestDeleteStoreFailureIs500AndNamesNoHostPath(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	s.Profiles.store = ioFailStore{s.Profiles.store, ioErr()}
	rec := deleteWithRev(s, "p", rev)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "/home/") {
		t.Fatalf("DELETE with a failing store: %d %s, want 500 with no path", rec.Code, rec.Body)
	}
	// the page prefixes "Delete failed: ", so the text must not say it again
	if msg, _ := apiBody(t, rec)["error"].(string); strings.HasPrefix(strings.ToLower(msg), "delete failed") {
		t.Fatalf("the 500 text %q repeats the client's \"Delete failed\" prefix", msg)
	}
}

// deleteWithRev is what the editor does: DELETE with the revision it opened.
func deleteWithRev(s *Server, name, rev string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("DELETE", "/api/profiles/"+name+"?rev="+rev, nil))
	return rec
}

func TestDeleteWithTheCurrentRevisionDeletes(t *testing.T) {
	s := revServer(t)
	rec := deleteWithRev(s, "p", revOf(t, s, "p"))
	if rec.Code != 204 {
		t.Fatalf("DELETE with the current rev: %d %s, want 204", rec.Code, rec.Body)
	}
	if _, ok := s.Profiles.Get("p"); ok {
		t.Fatal("the profile is still there")
	}
}

func TestDeleteWithoutARevisionIs428AndKeepsTheProfile(t *testing.T) {
	s := revServer(t)
	for _, url := range []string{"/api/profiles/p", "/api/profiles/p?rev="} {
		rec := httptest.NewRecorder()
		s.handleProfile(rec, httptest.NewRequest("DELETE", url, nil))
		if rec.Code != 428 || apiCode(t, rec) != "rev_required" {
			t.Fatalf("DELETE %s: %d %s, want 428 rev_required", url, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "delete") {
			t.Fatalf("DELETE %s: %s, the text must say it is the delete that carries no revision", url, rec.Body)
		}
	}
	if _, ok := s.Profiles.Get("p"); !ok {
		t.Fatal("a refused DELETE removed the profile")
	}
}

// A tab opened before another tab edited the profile cannot delete the edit.
func TestDeleteWithAStaleRevisionIs409AndKeepsTheProfile(t *testing.T) {
	s := revServer(t)
	old := revOf(t, s, "p")
	if rec := putWithRev(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, old); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	rec := deleteWithRev(s, "p", old)
	if rec.Code != 409 || apiCode(t, rec) != "profile_changed" {
		t.Fatalf("DELETE with a stale rev: %d %s, want 409 profile_changed", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "nothing was saved") {
		t.Fatalf("a refused delete must not say \"saved\": %s", rec.Body)
	}
	if p, ok := s.Profiles.Get("p"); !ok || len(p.Args) != 1 {
		t.Fatalf("the edited profile did not survive: %+v %v", p, ok)
	}
}

// Deleting a stored override reverts it to its config seed; a stale tab must not.
func TestDeleteOfAStaleOverrideKeepsTheOverride(t *testing.T) {
	s := revServer(t, config.AgentConfig{Name: "seed", Bin: "bash"})
	old := revOf(t, s, "seed")
	if rec := putWithRev(t, s, "seed", `{"name":"seed","bin":"bash","args":["--a"]}`, old); rec.Code != 200 {
		t.Fatalf("override: %d %s", rec.Code, rec.Body)
	}
	if rec := deleteWithRev(s, "seed", old); rec.Code != 409 {
		t.Fatalf("DELETE with the seed's old rev: %d %s, want 409", rec.Code, rec.Body)
	}
	if p, _ := s.Profiles.Get("seed"); len(p.Args) != 1 {
		t.Fatalf("the override was reverted: %+v", p)
	}
	if rec := deleteWithRev(s, "seed", revOf(t, s, "seed")); rec.Code != 204 {
		t.Fatalf("DELETE with the override's rev: %d %s, want 204", rec.Code, rec.Body)
	}
	if p, _ := s.Profiles.Get("seed"); len(p.Args) != 0 {
		t.Fatalf("the override was not removed: %+v", p)
	}
}

func TestDeleteOfAMissingProfileIsStill404(t *testing.T) {
	s := revServer(t)
	if rec := deleteWithRev(s, "ghost", strings.Repeat("0", 64)); rec.Code != 404 {
		t.Fatalf("DELETE of a profile that is not there: %d %s, want 404", rec.Code, rec.Body)
	}
}

// PUT and POST answer with the revision of what they stored, so a client whose
// re-read fails still holds the revision to save against next.
func TestPutAnswersWithTheNewRevision(t *testing.T) {
	s := revServer(t)
	rec := putProfile(t, s, "p", `{"name":"p","bin":"bash","args":["--one"]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s, want 200", rec.Code, rec.Body)
	}
	var got struct {
		Rev string `json:"rev"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Rev == "" {
		t.Fatalf("PUT body %s carries no rev", rec.Body)
	}
	if want := revOf(t, s, "p"); got.Rev != want {
		t.Fatalf("PUT rev = %s, want the rev GET shows now, %s", got.Rev, want)
	}
	// and saving again with it works: no 409 against the user's own save
	if rec := putWithRev(t, s, "p", `{"name":"p","bin":"bash","args":["--two"]}`, got.Rev); rec.Code != 200 {
		t.Fatalf("second PUT with the returned rev: %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestPutWithWarningsAnswersWithRevAndWarnings(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	// no credential binding: the egress policy is answered with a warning
	head := `{"name":"p","bin":"claude","runtime":"box"`
	if rec := postProfile(s, head+`}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := putProfile(t, s, "p", head+`,"egress_open":true}`)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["rev"] != revOf(t, s, "p") || got["warnings"] == nil {
		t.Fatalf("PUT with a warning: %d %s, want 200 with rev and warnings", rec.Code, rec.Body)
	}
}

func TestPostAnswersWithTheRevisionOfWhatItCreated(t *testing.T) {
	s := revServer(t)
	rec := postProfile(s, `{"name":"q","bin":"bash"}`)
	var got struct {
		Name string `json:"name"`
		Rev  string `json:"rev"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 201 {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	if got.Name != "q" || got.Rev != revOf(t, s, "q") {
		t.Fatalf("POST body %s, want name q and the rev GET shows", rec.Body)
	}
}

// The rev a save returns is taken under the manager's lock, of the profile it
// just stored, not read back later.
func TestUpdateReturnsTheRevOfTheStoredProfile(t *testing.T) {
	pm, _ := revManager(t, nil, nil, newFakeStore())
	rev0, err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"})
	if err != nil || rev0 != profileRev(mustGet(t, pm, "p")) {
		t.Fatalf("Create rev = %q err %v, want the rev of the stored profile", rev0, err)
	}
	rev1, err := pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--x"}}, rev0)
	if err != nil || rev1 != profileRev(mustGet(t, pm, "p")) || rev1 == rev0 {
		t.Fatalf("Update rev = %q err %v, want the new profile's rev", rev1, err)
	}
	if rev, err := pm.Update(config.AgentConfig{Name: "p", Bin: "bash"}, rev0); err == nil || rev != "" {
		t.Fatalf("a refused Update returned rev %q err %v", rev, err)
	}
}

// 428 text: the message names what is missing.
func TestMissingSchemaAnd428TextNamesWhatIsMissing(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	noSch := putWithRevSchema(t, s, "p", `{"name":"p","bin":"bash"}`, rev, noSchema)
	if noSch.Code != 428 || !strings.Contains(noSch.Body.String(), "schema") || strings.Contains(noSch.Body.String(), "carries no revision") {
		t.Fatalf("PUT with no schema: %d %s, want 428 that names the schema, not the revision", noSch.Code, noSch.Body)
	}
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/p", strings.NewReader(`{"name":"p","bin":"bash","schema":"`+profileSchema()+`"}`)))
	if rec.Code != 428 || !strings.Contains(rec.Body.String(), "revision") || strings.Contains(rec.Body.String(), "schema") {
		t.Fatalf("PUT with no rev: %d %s, want 428 that names the revision, not the schema", rec.Code, rec.Body)
	}
	if apiCode(t, noSch) != "rev_required" || apiCode(t, rec) != "rev_required" {
		t.Fatal("the 428 code is part of the client contract and stays rev_required")
	}
}

// The fingerprint covers a field's kind as well as its name.
type schemaKindA struct {
	X int `json:"x"`
}
type schemaKindB struct {
	X string `json:"x"`
}
type schemaKindC struct {
	X []string `json:"x"`
}
type schemaKindD struct {
	X map[string]string `json:"x"`
}
type schemaKindE struct {
	X *int `json:"x"`
}

func TestSchemaOfChangesWhenAFieldKindChanges(t *testing.T) {
	seen := map[string]string{}
	for name, v := range map[string]any{"int": schemaKindA{}, "string": schemaKindB{}, "[]string": schemaKindC{}, "map": schemaKindD{}} {
		got := schemaOf(reflect.TypeOf(v))
		if other, dup := seen[got]; dup {
			t.Fatalf("a field of kind %s and one of kind %s have the same schema %s", name, other, got)
		}
		seen[got] = name
	}
	// a pointer is the same wire value as what it points to
	if schemaOf(reflect.TypeOf(schemaKindE{})) != schemaOf(reflect.TypeOf(schemaKindA{})) {
		t.Fatal("*int and int are one wire shape and must have one schema")
	}
}

// The page's schema is baked into the script that holds its form logic, so the
// pin cannot come from a later request that a restart may have moved on.
func TestProfileFormScriptCarriesTheSchemaItWasServedWith(t *testing.T) {
	s := revServer(t)
	s.AllowedHosts = []string{"example.com"}
	ck := issueCookie(t, s)
	h := s.Handler()
	req := httptest.NewRequest("GET", "/profileform.js", nil)
	req.AddCookie(ck)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /profileform.js: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `var PROFILE_SCHEMA_BAKED = "`+profileSchema()+`";`) {
		t.Fatal("the served profileform.js does not carry the build's profile schema")
	}
	if !strings.Contains(rec.Body.String(), "function schemaPin()") {
		t.Fatal("the served profileform.js lost its own code")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("Content-Type = %q, want a javascript type", ct)
	}
}

// convert-aws: every refusal after the connection was created takes it back out
// and leaves the profile as it was.
func TestConvertOverAHandEditedFileIs409AndTakesTheConnectionBack(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	path := filepath.Join(t.TempDir(), "profiles.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c.fx.pm.store = st
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	hand := []byte(`[{"name":"dev","bin":"bash","env":{"EDITED":"by hand"}}]`)
	if err := os.WriteFile(path, hand, 0o600); err != nil {
		t.Fatal(err)
	}
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 409 || convertBody(t, w)["code"] != "profiles_file_changed" {
		t.Fatalf("convert over a hand-edited file: %d %s, want 409 profiles_file_changed", w.Code, w.Body)
	}
	if got := c.fx.file(t).AWSConnections; len(got) != 0 {
		t.Fatalf("the connection was left behind: %+v", got)
	}
	if p, _ := c.fx.pm.Get("dev"); p.AWS == nil || len(p.AWSConnections) != 0 {
		t.Fatalf("the profile changed: %+v", p)
	}
	if b, _ := os.ReadFile(path); string(b) != string(hand) {
		t.Fatalf("the hand edit was overwritten: %s", b)
	}
}

func TestConvertWhileTheProfileChangesIs409AndTakesTheConnectionBack(t *testing.T) {
	for name, mutate := range map[string]func(c *convertRoutes){
		"another binding": func(c *convertRoutes) {
			p := inlineAWSProfile("dev")
			p.AWS.Region = "eu-west-1"
			_ = c.fx.pm.Save(p)
		},
		"deleted": func(c *convertRoutes) { _ = c.fx.pm.Delete("dev") },
		"converted elsewhere": func(c *convertRoutes) {
			_ = c.fx.pm.Save(config.AgentConfig{Name: "dev", Bin: "bash", Runtime: config.RuntimeBox})
		},
	} {
		c := newConvertRoutes(t, config.Config{}, nil)
		if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
			t.Fatal(err)
		}
		c.svc.check = awsprobe.Result{State: awsprobe.NoneFound}
		c.svc.onCheck = func() { mutate(c) }
		w := c.convert("dev", "", credCookie, credOrigin)
		if w.Code != 409 || convertBody(t, w)["code"] != "profile_changed" {
			t.Errorf("%s: %d %s, want 409 profile_changed", name, w.Code, w.Body)
		}
		if got := c.fx.file(t).AWSConnections; len(got) != 0 {
			t.Errorf("%s: the connection was left behind: %+v", name, got)
		}
	}
}

// A store that cannot write fails the convert as a 500 without the path, and
// the connection goes back out.
func TestConvertStoreFailureIs500AndNamesNoHostPath(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	c.fx.pm.store = ioFailStore{c.fx.pm.store, ioErr()}
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 500 || strings.Contains(w.Body.String(), "/home/") {
		t.Fatalf("%d %s, want 500 with no path", w.Code, w.Body)
	}
	if got := c.fx.file(t).AWSConnections; len(got) != 0 {
		t.Fatalf("the connection was left behind: %+v", got)
	}
}
