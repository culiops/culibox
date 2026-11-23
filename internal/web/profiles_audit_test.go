package web

import (
	"errors"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
)

func auditedPM(t *testing.T) (*ProfileManager, *fakeStore, *fakeCredRec) {
	t.Helper()
	st := newFakeStore()
	pm := NewProfileManager([]config.AgentConfig{{Name: "seed", Bin: "claude"}}, nil, config.RuntimeHost, nil, st, func([]config.AgentConfig) {})
	rec := &fakeCredRec{}
	pm.SetAudit(rec)
	return pm, st, rec
}

// #330: every profile change leaves a row saying who changed which fields of
// which profile; never a value.
func TestProfileChangesAreAudited(t *testing.T) {
	pm, _, rec := auditedPM(t)
	p := config.AgentConfig{Name: "work", Bin: "claude", Env: map[string]string{"TOKEN": "s3cret-value"}}
	rev, err := pm.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	p.EgressAllow = []string{"go.dev", "dl.google.com"}
	p.Runtime = config.RuntimeBox
	p.Env = map[string]string{"TOKEN": "an0ther-value"}
	p.Args = []string{"--model", "x"}
	if rev, err = pm.Update(p, rev); err != nil {
		t.Fatal(err)
	}
	// A save that changes nothing still says so.
	if rev, err = pm.Update(p, rev); err != nil {
		t.Fatal(err)
	}
	if err := pm.Remove("work", rev); err != nil {
		t.Fatal(err)
	}
	// A seed's first save is an update of the seed.
	seed, _ := pm.Get("seed")
	seedRev := profileRev(seed)
	seed.Args = []string{"--x"}
	if _, err := pm.Update(seed, seedRev); err != nil {
		t.Fatal(err)
	}
	want := []broker.CredEvent{
		{Action: "created", Kind: "profile", Name: "work", Actor: "web"},
		{Action: "updated", Kind: "profile", Name: "work", Actor: "web", Detail: "args, egress_allow, env, runtime"},
		{Action: "updated", Kind: "profile", Name: "work", Actor: "web", Detail: "no field changed"},
		{Action: "deleted", Kind: "profile", Name: "work", Actor: "web"},
		{Action: "updated", Kind: "profile", Name: "seed", Actor: "web", Detail: "args"},
	}
	if len(rec.events) != len(want) {
		t.Fatalf("events %+v", rec.events)
	}
	for i := range want {
		if rec.events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, rec.events[i], want[i])
		}
		if strings.Contains(rec.events[i].Detail, "value") || strings.Contains(rec.events[i].Detail, "go.dev") {
			t.Errorf("a value in the detail: %q", rec.events[i].Detail)
		}
	}
}

// Fail closed: a row that cannot be written stops the change.
func TestProfileChangeRefusedWhenTheAuditFails(t *testing.T) {
	pm, st, rec := auditedPM(t)
	rev, err := pm.Create(config.AgentConfig{Name: "work", Bin: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	rec.err = errors.New("disk full")
	if _, err := pm.Create(config.AgentConfig{Name: "new", Bin: "claude"}); err == nil {
		t.Fatal("create went ahead")
	}
	if _, ok := st.m["new"]; ok {
		t.Fatal("stored without a row")
	}
	if _, err := pm.Update(config.AgentConfig{Name: "work", Bin: "claude", Args: []string{"x"}}, rev); err == nil {
		t.Fatal("update went ahead")
	}
	if len(st.m["work"].Args) != 0 {
		t.Fatal("updated without a row")
	}
	if err := pm.Remove("work", rev); err == nil {
		t.Fatal("remove went ahead")
	}
	if _, ok := st.m["work"]; !ok {
		t.Fatal("removed without a row")
	}
}
