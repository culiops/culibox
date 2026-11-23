package web

import (
	"errors"
	"fmt"
	"slices"
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

// M4: the detail fits the audit's 200-byte detail whole: it stops at a whole
// field name and says how many more changed.
func TestChangedFieldsFitTheAuditDetail(t *testing.T) {
	var names []string
	for i := range 40 {
		names = append(names, fmt.Sprintf("field_name_%02d", i))
	}
	got := fieldList(names)
	if len(got) > 200 {
		t.Fatalf("%d bytes: %q", len(got), got)
	}
	if !strings.HasSuffix(got, " more") || !strings.Contains(got, ", and ") {
		t.Fatalf("no count of the rest: %q", got)
	}
	shown := strings.Split(strings.TrimSuffix(got[:strings.Index(got, ", and ")], ","), ", ")
	for _, n := range shown {
		if !slices.Contains(names, n) {
			t.Fatalf("cut name %q in %q", n, got)
		}
	}
	if want := fmt.Sprintf(", and %d more", 40-len(shown)); !strings.HasSuffix(got, want) {
		t.Fatalf("%q does not end with %q", got, want)
	}
	if got := fieldList([]string{"args", "env"}); got != "args, env" {
		t.Fatalf("short list = %q", got)
	}
	// Through a real update with many fields changed.
	a := config.AgentConfig{Name: "w", Bin: "claude"}
	b := config.AgentConfig{Name: "w", Bin: "codex", Args: []string{"x"}, Env: map[string]string{"A": "1"}, Secrets: []string{"S"},
		EgressAllow: []string{"h"}, EgressOpen: true, Runtime: config.RuntimeBox, DefaultCwd: "/x", Account: "acc"}
	if d := changedFields(a, b); len(d) > 200 || strings.Contains(d, "1") {
		t.Fatalf("detail %q", d)
	}
}
