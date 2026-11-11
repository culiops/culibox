package session

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// lockingBoxes is a box layer that locks files on the host (macOS #96).
type lockingBoxes struct {
	fakeBoxes
	lmu    sync.Mutex
	locked []string
	ended  []string // "box/session" per SessionEnded
}

func (f *lockingBoxes) LockedFiles(string) []string {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	return slices.Clone(f.locked)
}

func (f *lockingBoxes) SessionEnded(box, id string) {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	f.ended = append(f.ended, box+"/"+id)
}

func (f *lockingBoxes) endedList() []string {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	return slices.Clone(f.ended)
}

func TestBoxedSpawnInfoCarriesLockedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	l := tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	b := &lockingBoxes{locked: []string{".mcp.json", "CLAUDE.md"}}
	m.SetBoxes(b)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Info().LockedFiles; !slices.Equal(got, []string{".mcp.json", "CLAUDE.md"}) {
		t.Fatalf("Info.LockedFiles = %v", got)
	}
	raw, _ := json.Marshal(s.Info())
	if !strings.Contains(string(raw), `"locked_files":[".mcp.json","CLAUDE.md"]`) {
		t.Fatalf("wire shape = %s", raw)
	}
	if md := sidecarEntry(t, path, s.ID); !slices.Equal(md.LockedFiles, []string{".mcp.json", "CLAUDE.md"}) {
		t.Fatalf("sidecar LockedFiles = %v", md.LockedFiles)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(b.endedList()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.endedList(); !slices.Equal(got, []string{"ab-box-test/" + s.ID}) {
		t.Fatalf("SessionEnded calls = %v, want exactly one for the session", got)
	}
}

// A box layer without locks (Linux) leaves the wire shape unchanged.
func TestBoxedSpawnWithoutLocksHasNoLockedFiles(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	raw, _ := json.Marshal(s.Info())
	if strings.Contains(string(raw), "locked_files") {
		t.Fatalf("wire shape = %s", raw)
	}
}
