package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/store"
)

// revOf is GET /api/profiles/{name}'s rev; the test stops if there is none.
func revOf(t *testing.T, s *Server, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/"+name, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", name, rec.Code, rec.Body)
	}
	var doc struct {
		Rev *string `json:"rev"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("GET %s: not json: %s", name, rec.Body)
	}
	if doc.Rev == nil {
		t.Fatalf("GET %s carries no rev: %s", name, rec.Body)
	}
	return *doc.Rev
}

// putWithRevSchema PUTs body (a JSON object) with its "rev" set to rev and its
// "schema" set to schema (no schema key at all when schema is "-none-").
func putWithRevSchema(t *testing.T, s *Server, name, body, rev, schema string) *httptest.ResponseRecorder {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("putWithRev: body is not a JSON object: %v", err)
	}
	r, _ := json.Marshal(rev)
	obj["rev"] = r
	if schema != noSchema {
		sc, _ := json.Marshal(schema)
		obj["schema"] = sc
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/"+name, strings.NewReader(string(b))))
	return rec
}

// noSchema asks putWithRevSchema to leave the schema key out.
const noSchema = "-none-"

// putWithRev PUTs body with its "rev" set to rev and the page's schema (the
// server's own, as a page loaded from this build holds it).
func putWithRev(t *testing.T, s *Server, name, body, rev string) *httptest.ResponseRecorder {
	t.Helper()
	return putWithRevSchema(t, s, name, body, rev, profileSchema())
}

// putProfile is what the editor does: GET the profile's rev, then PUT body
// with it.
func putProfile(t *testing.T, s *Server, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	return putWithRev(t, s, name, body, revOf(t, s, name))
}

func postProfile(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body)))
	return rec
}

// deleteProfile is the editor's delete: DELETE with the revision the profile is at
// now (a client that opened it just before).
func deleteProfile(s *Server, name string) *httptest.ResponseRecorder {
	p, _ := s.Profiles.Get(name)
	return deleteWithRev(s, name, profileRev(p))
}

func apiCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	code, _ := apiBody(t, rec)["code"].(string)
	return code
}

// apiBody is an error body as a map: "gone" is a bool, the rest strings.
func apiBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not a json error body: %s", rec.Body)
	}
	return m
}

// revServer is a Server over a fake store and the given seeds, with profile p
// created through the API.
func revServer(t *testing.T, seeds ...config.AgentConfig) *Server {
	t.Helper()
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	if rec := postProfile(s, `{"name":"p","bin":"bash"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return s
}

func TestGetProfileCarriesTheRevision(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(rev) {
		t.Fatalf("rev %q is not 64 lowercase hex chars", rev)
	}
	p, _ := s.Profiles.Get("p")
	if want := profileRev(p); rev != want {
		t.Fatalf("GET rev = %s, want profileRev of the merged profile %s", rev, want)
	}
	if again := revOf(t, s, "p"); again != rev {
		t.Fatalf("a second GET gave another rev: %s vs %s", again, rev)
	}
	// the config fields are still there next to it
	rec := httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/p", nil))
	if !strings.Contains(rec.Body.String(), `"bin":"bash"`) {
		t.Fatalf("GET lost the profile fields: %s", rec.Body)
	}
}

func TestPutWithTheFreshRevisionSaves(t *testing.T) {
	s := revServer(t)
	old := revOf(t, s, "p")
	if rec := putWithRev(t, s, "p", `{"name":"p","bin":"bash","args":["--one"]}`, old); rec.Code != 200 {
		t.Fatalf("PUT with the fresh rev: %d %s, want 200", rec.Code, rec.Body)
	}
	if now := revOf(t, s, "p"); now == old {
		t.Fatal("the rev did not change after a save that changed the profile")
	}
	if p, _ := s.Profiles.Get("p"); len(p.Args) != 1 || p.Args[0] != "--one" {
		t.Fatalf("the save did not land: %+v", p)
	}
}

// A profile with a credential binding, so an egress policy on it is not
// answered with the "no binding" warning and a save answers with the rev alone.
const revBinding = `"aws":{"source_profile":"sandbox-admin","role_arn":"arn:aws:iam::123456789012:role/agentbox-agent","region":"ap-southeast-1","s3_read":["culishop-tfstate"]}`

func TestPutWithAStaleRevisionIs409AndMergesNothing(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	head := `{"name":"p","bin":"claude","runtime":"box",` + revBinding
	if rec := postProfile(s, head+`}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	r1 := revOf(t, s, "p") // both tabs opened the profile here

	if rec := putWithRev(t, s, "p", head+`,"egress_allow":["a.example"]}`, r1); rec.Code != 200 {
		t.Fatalf("tab 1: %d %s, want 200", rec.Code, rec.Body)
	}
	rec := putWithRev(t, s, "p", head+`,"egress_open":true}`, r1)
	if rec.Code != 409 || apiCode(t, rec) != "profile_changed" || !strings.Contains(rec.Body.String(), "nothing was saved") {
		t.Fatalf("tab 2: %d %s, want 409 profile_changed saying nothing was saved", rec.Code, rec.Body)
	}
	// The profile is there, so Load latest has a saved version: no "gone" key.
	if _, has := apiBody(t, rec)["gone"]; has {
		t.Fatalf("tab 2: %s, a changed (not deleted) profile must not carry \"gone\"", rec.Body)
	}
	p, _ := s.Profiles.Get("p")
	if len(p.EgressAllow) != 1 || p.EgressAllow[0] != "a.example" || p.EgressOpen {
		t.Fatalf("stored profile = allow %v open %v, want tab 1's allow list and egress_open false", p.EgressAllow, p.EgressOpen)
	}
}

func TestPutWithoutARevisionIs428(t *testing.T) {
	s := revServer(t)
	before := revOf(t, s, "p")
	// The page's schema is present, so it is the missing rev that is refused.
	sch := `"schema":"` + profileSchema() + `"`
	for _, body := range []string{
		`{"name":"p","bin":"bash","args":["--x"],` + sch + `}`,
		`{"name":"p","bin":"bash","args":["--x"],"rev":"",` + sch + `}`,
	} {
		rec := httptest.NewRecorder()
		s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/p", strings.NewReader(body)))
		if rec.Code != 428 || apiCode(t, rec) != "rev_required" {
			t.Fatalf("PUT %s: %d %s, want 428 rev_required", body, rec.Code, rec.Body)
		}
	}
	if got := revOf(t, s, "p"); got != before {
		t.Fatal("a refused PUT changed the profile")
	}
}

func TestPutAfterDeleteIs409AndDoesNotRecreate(t *testing.T) {
	s := revServer(t)
	rev := revOf(t, s, "p")
	if rec := deleteProfile(s, "p"); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	rec := putWithRev(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, rev)
	if rec.Code != 409 || apiCode(t, rec) != "profile_changed" || !strings.Contains(rec.Body.String(), "no longer exists") {
		t.Fatalf("PUT after delete: %d %s, want 409 profile_changed (no longer exists)", rec.Code, rec.Body)
	}
	// Same code as a changed profile, but the body says there is no saved version
	// to load, so the editor does not offer to replace the edits with one.
	if gone, _ := apiBody(t, rec)["gone"].(bool); !gone {
		t.Fatalf("PUT after delete: %s, want \"gone\":true in the body", rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/p", nil))
	if rec.Code != 404 {
		t.Fatalf("GET after the refused PUT: %d, want 404 (the profile must not be recreated)", rec.Code)
	}
}

func TestPutWithAnotherProfilesRevIs409(t *testing.T) {
	s := revServer(t)
	if rec := postProfile(s, `{"name":"q","bin":"bash"}`); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	revQ := revOf(t, s, "q")
	rec := putWithRev(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`, revQ)
	if rec.Code != 409 || apiCode(t, rec) != "profile_changed" {
		t.Fatalf("PUT p with q's rev: %d %s, want 409 profile_changed", rec.Code, rec.Body)
	}
	if p, _ := s.Profiles.Get("p"); len(p.Args) != 0 {
		t.Fatalf("p was changed: %+v", p)
	}
}

func TestPostOfATakenNameIs409(t *testing.T) {
	s := revServer(t, config.AgentConfig{Name: "seed", Bin: "bash"})
	before := revOf(t, s, "p")
	for _, name := range []string{"p", "seed"} {
		rec := postProfile(s, `{"name":"`+name+`","bin":"bash","args":["--x"]}`)
		if rec.Code != 409 || apiCode(t, rec) != "profile_exists" {
			t.Fatalf("POST %q: %d %s, want 409 profile_exists", name, rec.Code, rec.Body)
		}
	}
	if got := revOf(t, s, "p"); got != before {
		t.Fatal("a refused POST changed the stored profile")
	}
	if p, _ := s.Profiles.Get("seed"); len(p.Args) != 0 {
		t.Fatalf("a refused POST overrode the seed: %+v", p)
	}
}

// fileServer is a Server over a real store file, with profile p created over
// the API and then the file rewritten by hand.
func fileServer(t *testing.T) (s *Server, path string, hand []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "profiles.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = &Server{Profiles: NewProfileManager(nil, nil, "", nil, st, func([]config.AgentConfig) {})}
	if rec := postProfile(s, `{"name":"p","bin":"bash"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	hand = []byte(`[{"name":"p","bin":"bash","env":{"EDITED":"by hand"}}]`)
	if err := os.WriteFile(path, hand, 0o600); err != nil {
		t.Fatal(err)
	}
	return s, path, hand
}

func TestPutOverAHandEditedFileIs409WithTheRestartMessage(t *testing.T) {
	s, path, hand := fileServer(t)
	rev := revOf(t, s, "p")
	rec := putProfile(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`)
	if rec.Code != 409 || apiCode(t, rec) != "profiles_file_changed" || !strings.Contains(rec.Body.String(), "restart") {
		t.Fatalf("PUT over a hand-edited file: %d %s, want 409 profiles_file_changed telling to restart", rec.Code, rec.Body)
	}
	if got := revOf(t, s, "p"); got != rev {
		t.Fatal("a refused PUT changed what GET shows")
	}
	if b, _ := os.ReadFile(path); string(b) != string(hand) {
		t.Fatalf("the hand edit was overwritten: %s", b)
	}
}

func TestDeleteOverAHandEditedFileIs409(t *testing.T) {
	s, path, hand := fileServer(t)
	rec := deleteProfile(s, "p")
	if rec.Code != 409 || apiCode(t, rec) != "profiles_file_changed" || !strings.Contains(rec.Body.String(), "restart") {
		t.Fatalf("DELETE over a hand-edited file: %d %s, want 409 profiles_file_changed", rec.Code, rec.Body)
	}
	if _, ok := s.Profiles.Get("p"); !ok {
		t.Fatal("a refused DELETE removed the profile from memory")
	}
	if b, _ := os.ReadFile(path); string(b) != string(hand) {
		t.Fatalf("the hand edit was overwritten: %s", b)
	}
}

// readOnlyServer is a Server over a real store file that does not parse: the
// store is read-only, as serve starts it.
func readOnlyServer(t *testing.T) (s *Server, path string, raw []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "profiles.json")
	raw = []byte(`{"version":2}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, parseErr, err := store.OpenOrReadOnly(path)
	if err != nil || parseErr == nil {
		t.Fatalf("OpenOrReadOnly: parseErr=%v err=%v", parseErr, err)
	}
	return &Server{Profiles: NewProfileManager(nil, nil, "", nil, st, func([]config.AgentConfig) {})}, path, raw
}

// unreadableStore holds profile p but refuses every write as a read-only store
// does; the real one is empty, so only this reaches a PUT and a DELETE of p.
type unreadableStore struct{ *fakeStore }

func (unreadableStore) Save(config.AgentConfig) error { return store.ErrUnreadable }
func (unreadableStore) Delete(string) error           { return store.ErrUnreadable }

func wantUnreadable(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	b := apiBody(t, rec)
	if rec.Code != 409 || b["code"] != "profiles_file_unreadable" || b["error"] != profilesFileUnreadableMsg {
		t.Fatalf("%s: %d %s, want 409 profiles_file_unreadable with the approved text", what, rec.Code, rec.Body)
	}
}

func TestUnreadableProfilesFileRefusesAPostWithA409(t *testing.T) {
	s, path, raw := readOnlyServer(t)
	wantUnreadable(t, "POST", postProfile(s, `{"name":"p","bin":"bash"}`))
	if _, ok := s.Profiles.Get("p"); ok {
		t.Fatal("a refused POST left the profile in memory")
	}
	if b, _ := os.ReadFile(path); string(b) != string(raw) {
		t.Fatalf("the file was changed: %s", b)
	}
	if _, err := os.Stat(path + ".corrupt"); err == nil {
		t.Fatal("the file was quarantined")
	}
}

func TestUnreadableProfilesFileRefusesAPutAndADeleteWith409(t *testing.T) {
	fs := newFakeStore()
	fs.m["p"] = config.AgentConfig{Name: "p", Bin: "bash"}
	s := &Server{Profiles: NewProfileManager(nil, nil, "", nil, unreadableStore{fs}, func([]config.AgentConfig) {})}
	wantUnreadable(t, "PUT", putProfile(t, s, "p", `{"name":"p","bin":"bash","args":["--x"]}`))
	wantUnreadable(t, "DELETE", deleteProfile(s, "p"))
	if _, ok := s.Profiles.Get("p"); !ok {
		t.Fatal("a refused DELETE removed the profile from memory")
	}
}

func TestUnreadableProfilesFileDeleteOfAnAbsentProfileIs404(t *testing.T) {
	// The read-only store is empty, so there is nothing to delete; the screen
	// shows no runtime profiles and a stale page gets the plain "no such profile".
	s, _, _ := readOnlyServer(t)
	if rec := deleteWithRev(s, "p", "x"); rec.Code != 404 {
		t.Fatalf("DELETE of a profile the empty store lacks: %d %s", rec.Code, rec.Body)
	}
}
