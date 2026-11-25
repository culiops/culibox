package web

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
)

const inPlaceRepo = "/home/u/repo"

// inPlacePM is a manager on box whose record has a folder history for
// inPlaceRepo (an entry recorded before the in-place flag existed) and audits.
func inPlacePM(t *testing.T) (*ProfileManager, *fakeStore, *fakeCredRec, *[]string) {
	t.Helper()
	st := newFakeStore()
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, st, func([]config.AgentConfig) {})
	pm.home = t.TempDir()
	rec := &fakeCredRec{}
	pm.SetAudit(rec)
	var asked []string
	pm.SetInPlaceHistoryCheck(func(of string) bool { asked = append(asked, of); return of == inPlaceRepo })
	return pm, st, rec, &asked
}

func ackRows(rec *fakeCredRec) []broker.CredEvent {
	var out []broker.CredEvent
	for _, e := range rec.events {
		if e.Action == "clone_ack_inplace_history" {
			out = append(out, e)
		}
	}
	return out
}

func TestSaveNewCloneOfOnUnknownHistoryNeedsAck(t *testing.T) {
	pm, st, rec, _ := inPlacePM(t)
	p := cloneProfile("")
	p.CloneOf = inPlaceRepo
	_, err := pm.Create(p)
	var ih *InPlaceHistoryError
	if !errors.As(err, &ih) {
		t.Fatalf("Create without ack = %v, want *InPlaceHistoryError", err)
	}
	if !strings.Contains(err.Error(), inPlaceRepo) || !strings.Contains(err.Error(), "box") {
		t.Errorf("the message names neither the folder nor the box: %q", err)
	}
	if _, ok := st.Get("cl"); ok {
		t.Fatal("a refused save stored the profile")
	}
	if len(rec.events) != 0 {
		t.Fatalf("a refused save left audit rows: %+v", rec.events)
	}

	// With the ack it saves, and the ack is audited once, before the write.
	rev, err := pm.CreateWith(p, SaveOpts{AckInPlaceHistory: true})
	if err != nil || rev == "" {
		t.Fatalf("Create with ack: rev=%q err=%v", rev, err)
	}
	if got, ok := st.Get("cl"); !ok || got.CloneOf != inPlaceRepo {
		t.Fatalf("not stored: %+v", got)
	}
	rows := ackRows(rec)
	want := broker.CredEvent{Action: "clone_ack_inplace_history", Kind: "profile", Name: "cl", Actor: "web", Detail: "clone_of " + inPlaceRepo}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("ack rows = %+v, want exactly [%+v]", rows, want)
	}
}

func TestSaveUnchangedCloneOfOnUnknownHistoryAsksNothing(t *testing.T) {
	pm, st, rec, asked := inPlacePM(t)
	p := cloneProfile("")
	p.CloneOf = inPlaceRepo
	// Accepted under the old rules: stored while the record knew nothing.
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	cur, _ := pm.Get("cl")
	cur.Args = []string{"--x"}
	if _, err := pm.Update(cur, profileRev(profileOf(t, pm, "cl"))); err != nil {
		t.Fatalf("re-saving an existing clone profile: %v", err)
	}
	if len(ackRows(rec)) != 0 {
		t.Fatalf("an ack row for a save that needed none: %+v", rec.events)
	}
	if len(*asked) != 0 {
		t.Fatalf("the history was looked up for an unchanged clone_of: %v", *asked)
	}
}

func profileOf(t *testing.T, pm *ProfileManager, name string) config.AgentConfig {
	t.Helper()
	p, ok := pm.Get(name)
	if !ok {
		t.Fatalf("no profile %q", name)
	}
	return p
}

func TestSaveChangedCloneOfOnUnknownHistoryNeedsAck(t *testing.T) {
	pm, st, rec, _ := inPlacePM(t)
	old := cloneProfile("")
	old.CloneOf = "/home/u/other"
	if err := st.Save(old); err != nil {
		t.Fatal(err)
	}
	p := old
	p.CloneOf = inPlaceRepo
	rev := profileRev(profileOf(t, pm, "cl"))
	var ih *InPlaceHistoryError
	if _, err := pm.Update(p, rev); !errors.As(err, &ih) {
		t.Fatalf("a changed clone_of onto an unknown history: %v", err)
	}
	if got := profileOf(t, pm, "cl"); got.CloneOf != "/home/u/other" {
		t.Fatalf("stored despite the refusal: %+v", got)
	}
	if _, err := pm.UpdateWith(p, rev, SaveOpts{AckInPlaceHistory: true}); err != nil {
		t.Fatal(err)
	}
	if len(ackRows(rec)) != 1 {
		t.Fatalf("ack rows: %+v", ackRows(rec))
	}
}

// An ack nobody needed leaves no row, and a folder with no history never asks.
func TestSaveAckOnlyAuditedWhenNeeded(t *testing.T) {
	pm, _, rec, _ := inPlacePM(t)
	p := cloneProfile("") // clone_of /home/u/repo = inPlaceRepo, so use a clean one
	p.CloneOf = "/home/u/clean"
	if _, err := pm.CreateWith(p, SaveOpts{AckInPlaceHistory: true}); err != nil {
		t.Fatal(err)
	}
	if len(ackRows(rec)) != 0 {
		t.Fatalf("an ack row without a history: %+v", rec.events)
	}
	// And without the hook installed nothing is asked.
	pm.SetInPlaceHistoryCheck(nil)
	q := p
	q.Name, q.CloneOf, q.DefaultCwd = "q", inPlaceRepo, "/home/u/clones/q"
	if _, err := pm.Create(q); err != nil {
		t.Fatalf("no hook: %v", err)
	}
}

// Fail closed like every profile row: an ack that cannot be recorded stops the save.
func TestSaveAckRefusedWhenTheAuditFails(t *testing.T) {
	pm, st, rec, _ := inPlacePM(t)
	rec.err = errors.New("disk full")
	p := cloneProfile("")
	p.CloneOf = inPlaceRepo
	if _, err := pm.CreateWith(p, SaveOpts{AckInPlaceHistory: true}); err == nil {
		t.Fatal("saved without its ack row")
	}
	if _, ok := st.Get("cl"); ok {
		t.Fatal("stored without the ack row")
	}
}

// The API: 400 {code: inplace_history} without the flag; saved with
// ack_inplace_history; the flag is never stored on the profile.
func TestProfileAPIInPlaceHistory(t *testing.T) {
	pm, st, rec, _ := inPlacePM(t)
	s := &Server{Profiles: pm}
	body := `{"name":"cl","bin":"claude","runtime":"box","clone_of":"` + inPlaceRepo + `","default_cwd":"/home/u/clones/repo"`
	r := postProfile(s, body+`}`)
	if r.Code != 400 || apiCode(t, r) != "inplace_history" {
		t.Fatalf("POST without ack: %d %s", r.Code, r.Body)
	}
	got := apiBody(t, r)
	if msg, _ := got["error"].(string); !strings.Contains(msg, inPlaceRepo) || got["field"] != "clone_of" {
		t.Fatalf("body = %v", got)
	}
	if _, ok := st.Get("cl"); ok {
		t.Fatal("stored")
	}
	r = postProfile(s, body+`,"ack_inplace_history":true}`)
	if r.Code != 201 {
		t.Fatalf("POST with ack: %d %s", r.Code, r.Body)
	}
	if len(ackRows(rec)) != 1 {
		t.Fatalf("ack rows: %+v", rec.events)
	}
	if b, _ := json.Marshal(profileOf(t, pm, "cl")); strings.Contains(string(b), "ack_inplace") {
		t.Fatalf("the ack is part of the stored profile: %s", b)
	}

	// PUT: a different clone_of onto the history asks, the ack saves.
	other := `{"name":"cl","bin":"claude","runtime":"box","clone_of":"/home/u/other","default_cwd":"/home/u/clones/repo"}`
	if r := putProfile(t, s, "cl", other); r.Code != 200 {
		t.Fatalf("PUT to a clean folder: %d %s", r.Code, r.Body)
	}
	back := strings.Replace(other, "/home/u/other", inPlaceRepo, 1)
	if r := putProfile(t, s, "cl", back); r.Code != 400 || apiCode(t, r) != "inplace_history" {
		t.Fatalf("PUT onto the history: %d %s", r.Code, r.Body)
	}
	if r := putProfile(t, s, "cl", strings.Replace(back, `}`, `,"ack_inplace_history":true}`, 1)); r.Code != 200 {
		t.Fatalf("PUT with ack: %d %s", r.Code, r.Body)
	}
	if len(ackRows(rec)) != 2 {
		t.Fatalf("ack rows: %+v", rec.events)
	}
}
