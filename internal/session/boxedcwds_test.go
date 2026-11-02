package session

import (
	"slices"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
)

// BoxedCwds feeds the fs API's per-request containment view, so it must name
// every boxed session's cwd (exited and retained ones too, like List) and
// nothing from host sessions.
func TestBoxedCwdsReturnsBoxedOnly(t *testing.T) {
	m := NewManager(tieredLauncher{argv: []string{"true"}}, nil, 1<<16, time.Minute)
	m.mu.Lock()
	m.sessions["a"] = &Session{ID: "a", Cwd: "/w/live", Runtime: config.RuntimeBox}
	m.sessions["b"] = &Session{ID: "b", Cwd: "/w/exited", Runtime: config.RuntimeBox, exited: true}
	m.sessions["c"] = &Session{ID: "c", Cwd: "/w/host", Runtime: config.RuntimeHost}
	m.sessions["d"] = &Session{ID: "d", Cwd: "", Runtime: config.RuntimeBox}
	m.mu.Unlock()

	got := m.BoxedCwds()
	slices.Sort(got)
	want := []string{"/w/exited", "/w/live"}
	if !slices.Equal(got, want) {
		t.Fatalf("BoxedCwds = %v, want %v", got, want)
	}
}
