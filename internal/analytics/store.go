package analytics

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite" (no CGO)
)

// Store is the durable SQLite home for analytics rollups. Rollups are ADDITIVE
// (upserts add deltas), so ingestion must never re-feed the same bytes — the
// per-path read offset in ingest_state is what guarantees that.
//
// Primary keys include `project` (the top-level slug) as well as session_id: a
// session UUID can appear under two slugs (e.g. after the agent-console→agent-box
// rename), and without project in the key those would clobber each other's rows.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS ingest_state (
  path TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  read_offset INTEGER NOT NULL,
  mtime INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS session_rollup (
  day TEXT NOT NULL,
  session_id TEXT NOT NULL,
  project TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read INTEGER NOT NULL DEFAULT 0,
  cache_creation INTEGER NOT NULL DEFAULT 0,
  prompts INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, session_id, project, model)
);
CREATE TABLE IF NOT EXISTS attribution_rollup (
  day TEXT NOT NULL,
  session_id TEXT NOT NULL,
  project TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  records INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, session_id, project, kind, name)
);
CREATE INDEX IF NOT EXISTS idx_session_day ON session_rollup(day);
CREATE INDEX IF NOT EXISTS idx_attr_day ON attribution_rollup(day);
`

// Open opens (creating if needed) the analytics DB at path and ensures the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One writer at a time (the ingester); WAL + a busy timeout keep concurrent
	// reads (the API) from tripping "database is locked".
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// State returns the recorded size and read offset for a transcript path.
func (s *Store) State(path string) (size, offset int64, ok bool, err error) {
	row := s.db.QueryRow(`SELECT size, read_offset FROM ingest_state WHERE path=?`, path)
	switch err = row.Scan(&size, &offset); err {
	case nil:
		return size, offset, true, nil
	case sql.ErrNoRows:
		return 0, 0, false, nil
	default:
		return 0, 0, false, err
	}
}

// Apply writes one transcript batch's rollup deltas AND the new ingest_state row
// in a single transaction, so a crash can never advance the offset without the
// matching rows (or vice versa). session_id and project tag every delta row.
func (s *Store) Apply(sessionID, project, path string, p *Parsed, size, offset, mtime int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	const upsertSession = `
INSERT INTO session_rollup(day,session_id,project,model,input_tokens,output_tokens,cache_read,cache_creation,prompts)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(day,session_id,project,model) DO UPDATE SET
  input_tokens=input_tokens+excluded.input_tokens,
  output_tokens=output_tokens+excluded.output_tokens,
  cache_read=cache_read+excluded.cache_read,
  cache_creation=cache_creation+excluded.cache_creation,
  prompts=prompts+excluded.prompts`
	for k, t := range p.Tokens {
		if _, err := tx.Exec(upsertSession, k.Day, sessionID, project, k.Model,
			t.Input, t.Output, t.CacheRead, t.CacheCreation, t.Prompts); err != nil {
			return fmt.Errorf("session upsert: %w", err)
		}
	}

	const upsertAttr = `
INSERT INTO attribution_rollup(day,session_id,project,kind,name,records)
VALUES(?,?,?,?,?,?)
ON CONFLICT(day,session_id,project,kind,name) DO UPDATE SET
  records=records+excluded.records`
	for k, n := range p.Attrs {
		if _, err := tx.Exec(upsertAttr, k.Day, sessionID, project, k.Kind, k.Name, n); err != nil {
			return fmt.Errorf("attr upsert: %w", err)
		}
	}

	const upsertState = `
INSERT INTO ingest_state(path,size,read_offset,mtime) VALUES(?,?,?,?)
ON CONFLICT(path) DO UPDATE SET size=excluded.size, read_offset=excluded.read_offset, mtime=excluded.mtime`
	if _, err := tx.Exec(upsertState, path, size, offset, mtime); err != nil {
		return fmt.Errorf("state upsert: %w", err)
	}

	return tx.Commit()
}

// --- query types --------------------------------------------------------------

// DayModelRow is one (day, model) aggregate across all sessions/projects.
type DayModelRow struct {
	Day           string `json:"day"`
	Model         string `json:"model"`
	Input         int64  `json:"input"`
	Output        int64  `json:"output"`
	CacheRead     int64  `json:"cacheRead"`
	CacheCreation int64  `json:"cacheCreation"`
	Prompts       int64  `json:"prompts"`
}

// SessionRow is one (day, session, project, model) row with a derived tokens/prompt.
type SessionRow struct {
	Day             string  `json:"day"`
	SessionID       string  `json:"sessionId"`
	Project         string  `json:"project"`
	Model           string  `json:"model"`
	Input           int64   `json:"input"`
	Output          int64   `json:"output"`
	CacheRead       int64   `json:"cacheRead"`
	CacheCreation   int64   `json:"cacheCreation"`
	Prompts         int64   `json:"prompts"`
	TokensPerPrompt float64 `json:"tokensPerPrompt"`
}

// ToolRow is one attribution aggregate across the window.
type ToolRow struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Records int64  `json:"records"`
}

// ByDayModel returns per-(day,model) token/prompt aggregates in [from,to] (inclusive).
func (s *Store) ByDayModel(from, to string) ([]DayModelRow, error) {
	rows, err := s.db.Query(`
SELECT day, model,
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts)
FROM session_rollup WHERE day BETWEEN ? AND ?
GROUP BY day, model ORDER BY day, model`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayModelRow
	for rows.Next() {
		var r DayModelRow
		if err := rows.Scan(&r.Day, &r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Sessions returns per-(day,session,project,model) rows in [from,to], newest day
// first, with tokens/prompt derived (0 when a row has no prompts).
func (s *Store) Sessions(from, to string, limit int) ([]SessionRow, error) {
	rows, err := s.db.Query(`
SELECT day, session_id, project, model, input_tokens, output_tokens, cache_read, cache_creation, prompts
FROM session_rollup WHERE day BETWEEN ? AND ?
ORDER BY day DESC, (input_tokens+output_tokens+cache_read+cache_creation) DESC
LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.Day, &r.SessionID, &r.Project, &r.Model,
			&r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts); err != nil {
			return nil, err
		}
		if r.Prompts > 0 {
			total := r.Input + r.Output + r.CacheRead + r.CacheCreation
			r.TokensPerPrompt = float64(total) / float64(r.Prompts)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Tools returns attribution aggregates (skill/plugin/mcp) in [from,to], busiest first.
func (s *Store) Tools(from, to string, limit int) ([]ToolRow, error) {
	rows, err := s.db.Query(`
SELECT kind, name, SUM(records) AS r
FROM attribution_rollup WHERE day BETWEEN ? AND ?
GROUP BY kind, name ORDER BY r DESC LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolRow
	for rows.Next() {
		var r ToolRow
		if err := rows.Scan(&r.Kind, &r.Name, &r.Records); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
