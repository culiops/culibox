package session

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Session ids are the key audit rows are filed under, so an id must never be
// issued twice: a new session that reused p-2 would inherit the history of the
// old p-2. Counters seeded from live sessions alone forget every id that has
// exited, so the highest number issued per agent is also kept on disk
// (idMark), and a floor read from the audit databases covers a lost file.

// SetIDStatePath loads the persisted per-agent high-water marks from path
// (JSON {"<agent>": <highest n issued>}) and keeps the file current from then
// on. A missing file is a first start; an unreadable or corrupt one is logged
// and ignored, so a damaged file never stops the daemon from spawning.
func (m *Manager) SetIDStatePath(path string) {
	marks := map[string]int{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &marks); err != nil {
			log.Printf("session id state %s unreadable (%v); ignoring it", path, err)
			marks = map[string]int{}
		}
	} else if !os.IsNotExist(err) {
		log.Printf("session id state %s unreadable (%v); ignoring it", path, err)
	}
	m.mu.Lock()
	m.idStatePath = path
	for agent, n := range marks {
		if n > m.idMark[agent] {
			m.idMark[agent] = n
		}
	}
	m.mu.Unlock()
}

// SeedIDFloor raises the per-agent high-water marks to at least floor. It never
// lowers one, so a floor older than the persisted mark changes nothing.
func (m *Manager) SeedIDFloor(floor map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for agent, n := range floor {
		if n > m.idMark[agent] {
			m.idMark[agent] = n
		}
	}
}

// persistIDMarksLocked writes the marks through a tmp file and a rename, so a
// crash leaves the old file or the new one, never half of one. A failure is
// logged and swallowed: a spawn is never blocked on bookkeeping. The caller
// holds m.mu.
func (m *Manager) persistIDMarksLocked() {
	if m.idStatePath == "" {
		return
	}
	b, err := json.Marshal(m.idMark)
	if err != nil {
		log.Printf("session id state: %v", err)
		return
	}
	tmp := m.idStatePath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("session id state: %v", err)
		return
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, m.idStatePath)
	}
	if err != nil {
		_ = os.Remove(tmp)
		log.Printf("session id state: %v", err)
		return
	}
	if d, err := os.Open(filepath.Dir(m.idStatePath)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// FloorFromIDs turns session ids seen elsewhere (the audit databases) into the
// per-agent floor SeedIDFloor takes: the highest number seen per agent. Ids
// that are not "<agent>-<n>" are ignored.
func FloorFromIDs(ids []string) map[string]int {
	floor := map[string]int{}
	for _, id := range ids {
		if agent, n, ok := parseID(id); ok && n > floor[agent] {
			floor[agent] = n
		}
	}
	return floor
}
