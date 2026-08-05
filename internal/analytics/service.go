package analytics

import (
	"context"
	"log"
	"time"
)

// Service ties the store, the transcript roots, and the ingest schedule together.
// It is the only type the web/main layers touch.
type Service struct {
	store    *Store
	roots    []string
	interval time.Duration
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

// Ingest runs one full pass over all roots. Safe to call concurrently with reads
// (SQLite WAL); callers should avoid overlapping Ingest calls (Start does not).
func (s *Service) Ingest() (IngestStats, error) {
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
			stats, _ := s.Ingest()
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
func (s *Service) Sessions(from, to string, limit int) ([]SessionRow, error) {
	return s.store.Sessions(from, to, limit)
}
func (s *Service) Tools(from, to string, limit int) ([]ToolRow, error) {
	return s.store.Tools(from, to, limit)
}
