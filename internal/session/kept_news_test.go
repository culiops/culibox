package session

import (
	"slices"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// A boxed spawn asks the box layer, by the key of the store its box mounts,
// which kept folders are new, and carries them as kept_new. A host spawn asks
// nothing.
func TestBoxedSpawnCarriesKeptNew(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.conv = testConvFolder
	boxes.keptNews = func(key string) []string {
		if key != testConvFolder {
			return nil
		}
		return []string{".culiops", ".local/share/culiops"}
	}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := s.Info().KeptNew; !slices.Equal(got, []string{".culiops", ".local/share/culiops"}) {
		t.Fatalf("Info().KeptNew = %v", got)
	}

	// No conversations key: no store to ask about.
	boxes.conv = ""
	boxes.mu.Lock()
	boxes.keptAsked = nil
	boxes.mu.Unlock()
	s2, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s2.ID)
	boxes.mu.Lock()
	asked := slices.Clone(boxes.keptAsked)
	boxes.mu.Unlock()
	if len(asked) != 0 || len(s2.Info().KeptNew) != 0 {
		t.Fatalf("asked %v, kept_new %v: want neither without a store key", asked, s2.Info().KeptNew)
	}
}

// The box layer is asked outside the manager's lock: a store lock wait or disk
// read there must never freeze Get/List.
func TestBoxedSpawnTakesKeptNewsOutsideManagerLock(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.conv = testConvFolder
	var held, asked bool
	boxes.keptNews = func(string) []string {
		asked = true
		if !m.mu.TryLock() {
			held = true
			return nil
		}
		m.mu.Unlock()
		return []string{".culiops"}
	}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if !asked || held {
		t.Fatalf("asked=%v, manager lock held during TakeKeptNews=%v", asked, held)
	}
	if got := s.Info().KeptNew; !slices.Equal(got, []string{".culiops"}) {
		t.Fatalf("KeptNew = %v", got)
	}
}
