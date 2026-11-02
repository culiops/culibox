package session

import (
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

func TestApplyStoreNeed(t *testing.T) {
	cases := []struct {
		name, need string
		exited     bool
		want       string
	}{
		{"quiet session", "", false, "store_locked"},
		{"turn", "turn", false, "store_locked"},
		{"ask wins", "ask", false, "ask"},
		{"sso wins", "sso", false, "sso"},
		{"exited session keeps its need", "turn", true, "turn"},
	}
	for _, c := range cases {
		in := Info{Need: c.need, Exited: c.exited, Activity: "working"}
		applyStoreNeed(&in)
		if in.Need != c.want || in.Activity != "working" {
			t.Errorf("%s: need %q activity %q, want need %q", c.name, in.Need, in.Activity, c.want)
		}
	}
}

// List raises store_locked only when the broker says this session was refused
// AND the store is locked right now; sso still wins.
func TestListStoreLockedNeed(t *testing.T) {
	m, _, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	need := func() string {
		for _, i := range m.List() {
			if i.ID == s.ID {
				return i.Need
			}
		}
		t.Fatal("session not listed")
		return ""
	}
	locked := true
	m.SetStoreLocked(func() bool { return locked })

	if got := need(); got == "store_locked" {
		t.Fatalf("broker never refused this session: %q", got)
	}
	hooks.setStoreLocked(s.ID, true)
	if got := need(); got != "store_locked" {
		t.Fatalf("locked + refused: need %q", got)
	}
	locked = false
	if got := need(); got == "store_locked" {
		t.Fatalf("store unlocked: need %q", got)
	}
	locked = true
	hooks.setState(s.ID, time.Time{}, "example-sso")
	if got := need(); got != "sso" {
		t.Fatalf("sso must win over store_locked: %q", got)
	}
}
