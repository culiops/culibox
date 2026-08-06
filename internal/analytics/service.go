package analytics

import (
	"context"
	"errors"
	"log"
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
	mu       sync.Mutex // held for the duration of one Ingest pass
}

// New opens the analytics DB at dbPath and returns a Service over the given
// transcript roots (e.g. ~/.claude/projects and <dataDir>/boxstate/*/.claude/projects).
func New(dbPath string, roots []string, interval time.Duration) (*Service, error) {
	st, err := Open(dbPath)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Service{store: st, roots: roots, interval: interval}, nil
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
	var st IngestStats
	for _, root := range s.roots {
		ingestRoot(s.store, root, &st)
	}
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
			log.Printf("analytics: ingest — %d files, %d bytes, %d errors",
				stats.Files, stats.BytesRead, stats.Errors)
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
