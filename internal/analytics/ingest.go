package analytics

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// IngestStats summarises one ingest pass for logging / the API response.
type IngestStats struct {
	Files     int   `json:"files"`     // transcripts with new bytes ingested
	Scanned   int   `json:"scanned"`   // transcripts examined
	BytesRead int64 `json:"bytesRead"` // new bytes parsed this pass
	Errors    int   `json:"errors"`
}

// projectFromRel derives the project slug from a transcript path relative to its
// root: the FIRST path segment. This is the top-level `<slug>` dir even for a
// nested subagent file (`<slug>/<uuid>/subagents/agent-*.jsonl`), so subagent
// usage is attributed to the same project as its parent session.
func projectFromRel(rel string) string {
	rel = filepath.ToSlash(rel)
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return "" // a file directly in the root (no slug) — skipped by the caller
}

// stemSessionID is the fallback session id for very old transcripts that predate
// the `sessionId` record field: the filename without its .jsonl extension.
func stemSessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// ingestFile reads the new bytes of one transcript (from the recorded offset to
// EOF) and applies the resulting rollup deltas. Incremental and idempotent:
//   - size == offset  → nothing new, no-op.
//   - size  > offset  → parse [offset, last-newline], advance the offset.
//   - size  < offset  → the file shrank (rotation/rewrite — should not happen for
//     append-only transcripts). Rollups are additive with no path dimension to
//     reverse, so re-reading from 0 would DOUBLE-count. Instead clamp the offset
//     down to the new size and do not re-add; log it so a real rotation is visible.
func ingestFile(s *Store, path, project string, st *IngestStats) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	size, mtime := fi.Size(), fi.ModTime().Unix()

	_, offset, _, err := s.State(path)
	if err != nil {
		return err
	}
	if size == offset {
		return nil
	}
	if size < offset {
		log.Printf("analytics: %s shrank (%d < %d); clamping offset, not re-ingesting", path, size, offset)
		return s.Apply("", project, path, newParsed(), size, size, mtime)
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	parsed, sessionID, consumed, err := ParseTranscript(f)
	if err != nil {
		return err
	}
	if sessionID == "" {
		sessionID = stemSessionID(path)
	}
	st.BytesRead += consumed
	st.Files++
	return s.Apply(sessionID, project, path, parsed, size, offset+consumed, mtime)
}

// ingestRoot walks one root recursively and ingests every *.jsonl under a slug
// dir. Errors on individual files are counted and logged, never fatal — one
// unreadable transcript must not stop the pass.
func ingestRoot(s *Store, root string, st *IngestStats) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			st.Errors++
			return nil // skip unreadable dir/file, keep walking
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		project := projectFromRel(rel)
		if project == "" {
			return nil // a jsonl directly in the root, not under a project slug
		}
		st.Scanned++
		if err := ingestFile(s, path, project, st); err != nil {
			st.Errors++
			log.Printf("analytics: ingest %s: %v", path, err)
		}
		return nil
	})
}
