package analytics

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrIngestBusy is returned by Ingest when a pass is already running. Ingestion
// MUST be serialized: the read offset is loaded outside the write transaction, so
// two overlapping passes would both start from the same offset and additively
// upsert the same bytes twice — silent, irreversible double-counting.
var ErrIngestBusy = errors.New("analytics: ingest already running")

// Service ties the store, the transcript roots, and the ingest schedule together.
// It is the only type the web/main layers touch.
type Service struct {
	store    *Store
	roots    []string
	interval time.Duration
	boxState string     // <dataDir>/boxstate; roots inside it are walked through os.Root
	mu       sync.Mutex // held for the duration of one Ingest pass

	// costs is the all-days cost per conversation id, rebuilt after every
	// ingest pass (see refreshCosts) so a session list reads a map, not SQL.
	// costMu guards only the map pointer; it is never held across a query and
	// never while taking another lock.
	costMu sync.RWMutex
	costs  map[string]float64
}

// New opens the analytics DB at dbPath and returns a Service over the given
// transcript root glob patterns (e.g. ~/.claude/projects and
// <dataDir>/boxstate/*/.claude/projects), re-expanded on every Ingest.
func New(dbPath string, roots []string, interval time.Duration) (*Service, error) {
	st, err := Open(dbPath)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	svc := &Service{store: st, roots: roots, interval: interval}
	// Seed the map from what is already stored, so a restart does not show
	// $0.00 for a whole interval while the first ingest is still scanning.
	svc.refreshCosts()
	return svc, nil
}

// refreshCosts rebuilds the cost map from the store. On a query error it keeps
// the previous map: a stale price beats wiping every session to $0.00.
func (s *Service) refreshCosts() {
	m, err := s.store.ConversationCosts()
	if err != nil {
		log.Printf("analytics: cost map refresh failed, keeping the previous one: %v", err)
		return
	}
	s.costMu.Lock()
	s.costs = m
	s.costMu.Unlock()
}

// CostOf sums the cached cost of the given conversation ids (an id with no
// rows counts 0). It reads the map built by the last ingest pass; it never
// touches SQLite, so it is cheap enough for every session-list poll.
func (s *Service) CostOf(sids []string) float64 {
	s.costMu.RLock()
	defer s.costMu.RUnlock()
	var sum float64
	for _, id := range sids {
		sum += s.costs[id]
	}
	return sum
}

// SetBoxState names the directory whose subtrees a contained agent writes
// (<dataDir>/boxstate). Any root that resolves inside it is walked through an
// os.Root that refuses symlinks (see ingestBoxRoot); all other roots keep the
// plain walk. Call it before Start.
func (s *Service) SetBoxState(dir string) { s.boxState = filepath.Clean(dir) }

// inBoxState reports whether root lies strictly inside the box-controlled dir.
func (s *Service) inBoxState(root string) bool {
	if s.boxState == "" || s.boxState == "." {
		return false
	}
	rel, err := filepath.Rel(s.boxState, filepath.Clean(root))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Ingest runs one full pass over all roots. It is serialized: a call made while
// another pass is running returns ErrIngestBusy immediately rather than racing it
// (which would double-count — see ErrIngestBusy). Concurrent READS are unaffected
// (SQLite WAL). Both the daily ticker and the POST /ingest handler funnel here, so
// a "Re-scan" click during the multi-minute startup scan is refused, not raced.
func (s *Service) Ingest() (IngestStats, error) {
	if !s.mu.TryLock() {
		return IngestStats{}, ErrIngestBusy
	}
	defer s.mu.Unlock()
	s.store.beginPass()
	defer s.store.endPass()
	var st IngestStats
	// Roots are glob patterns, expanded on every pass so an agent account or box
	// created after startup is ingested without a restart. A literal path is a
	// pattern that matches itself.
	for _, pat := range s.roots {
		matches, err := filepath.Glob(pat)
		if err != nil { // malformed pattern: treat as a literal path
			matches = []string{pat}
		}
		for _, root := range matches {
			if s.inBoxState(root) {
				ingestBoxRoot(s.store, s.boxState, root, &st)
				continue
			}
			ingestRoot(s.store, root, &st)
		}
	}
	s.refreshCosts()
	return st, nil
}

// Start runs an initial ingest, then re-ingests every interval until ctx is done.
// It blocks; run it in its own goroutine.
func (s *Service) Start(ctx context.Context) {
	stats, _ := s.Ingest()
	log.Printf("analytics: initial ingest — %d files, %d scanned, %d bytes, %d errors",
		stats.Files, stats.Scanned, stats.BytesRead, stats.Errors)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			stats, err := s.Ingest()
			if err != nil {
				log.Printf("analytics: scheduled ingest skipped: %v", err)
				continue
			}
			// Every couple of minutes: say nothing about a pass that found nothing.
			if stats.Files > 0 || stats.Errors > 0 {
				log.Printf("analytics: ingest — %d files, %d bytes, %d errors",
					stats.Files, stats.BytesRead, stats.Errors)
			}
		}
	}
}

func (s *Service) Close() error { return s.store.Close() }

// Query pass-throughs (the web layer never touches the store directly).

func (s *Service) ByDayModel(from, to string) ([]DayModelRow, error) {
	return s.store.ByDayModel(from, to)
}
func (s *Service) ByModel(from, to string) ([]ModelRow, error) {
	return s.store.ByModel(from, to)
}
func (s *Service) ByProject(from, to string) ([]ProjectRow, error) {
	return s.store.ByProject(from, to)
}
func (s *Service) Sessions(from, to string, limit int) ([]SessionRow, error) {
	return s.store.Sessions(from, to, limit)
}
func (s *Service) SessionCosts(from, to string) ([]SessionAggRow, error) {
	return s.store.SessionCosts(from, to)
}
func (s *Service) Tools(from, to string, limit int) ([]ToolRow, error) {
	return s.store.Tools(from, to, limit)
}
