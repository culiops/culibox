package analytics

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

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
  mtime INTEGER NOT NULL,
  seen_ids TEXT NOT NULL DEFAULT ''
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
//
// Pragmas go in the DSN, not a post-open Exec: database/sql pools connections and
// an Exec'd PRAGMA binds only the one connection that served it, so a lazily-opened
// connection serving a concurrent HTTP read would miss busy_timeout and fail with
// SQLITE_BUSY instead of waiting. In the DSN, modernc applies them to every
// connection the pool opens. (journal_mode=WAL persists in the file header, but the
// others do not — hence the DSN for all three.)
func Open(path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// State returns the recorded size and read offset for a transcript path, and the
// message ids already counted near that offset (oldest first), which let a
// message split across two ingest batches be counted once.
func (s *Store) State(path string) (size, offset int64, ok bool, seen []string, err error) {
	var ids string
	row := s.db.QueryRow(`SELECT size, read_offset, seen_ids FROM ingest_state WHERE path=?`, path)
	switch err = row.Scan(&size, &offset, &ids); err {
	case nil:
		return size, offset, true, strings.Fields(ids), nil
	case sql.ErrNoRows:
		return 0, 0, false, nil, nil
	default:
		return 0, 0, false, nil, err
	}
}

// Apply writes one transcript batch's rollup deltas AND the new ingest_state row
// in a single transaction, so a crash can never advance the offset without the
// matching rows (or vice versa). session_id and project tag every delta row.
func (s *Store) Apply(sessionID, project, path string, p *Parsed, size, offset, mtime int64, seen []string) error {
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
INSERT INTO ingest_state(path,size,read_offset,mtime,seen_ids) VALUES(?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET size=excluded.size, read_offset=excluded.read_offset, mtime=excluded.mtime, seen_ids=excluded.seen_ids`
	if _, err := tx.Exec(upsertState, path, size, offset, mtime, strings.Join(seen, " ")); err != nil {
		return fmt.Errorf("state upsert: %w", err)
	}

	return tx.Commit()
}

// --- query types --------------------------------------------------------------

// DayModelRow is one (day, model) aggregate across all sessions/projects.
type DayModelRow struct {
	Day           string  `json:"day"`
	Model         string  `json:"model"`
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cacheRead"`
	CacheCreation int64   `json:"cacheCreation"`
	Prompts       int64   `json:"prompts"`
	Cost          float64 `json:"costUsd"`
}

// ModelRow aggregates one model across the whole window (all days/projects),
// with a distinct-session count and a dollar cost.
type ModelRow struct {
	Model         string  `json:"model"`
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cacheRead"`
	CacheCreation int64   `json:"cacheCreation"`
	Prompts       int64   `json:"prompts"`
	Sessions      int64   `json:"sessions"`
	Cost          float64 `json:"costUsd"`
}

// ProjectRow is one (project, model) aggregate over the window. Returning the
// model breakdown lets the frontend build both a project total and the
// per-project model-mix from a single query.
type ProjectRow struct {
	Project       string  `json:"project"`
	Model         string  `json:"model"`
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cacheRead"`
	CacheCreation int64   `json:"cacheCreation"`
	Prompts       int64   `json:"prompts"`
	Cost          float64 `json:"costUsd"`
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
	Cost            float64 `json:"costUsd"`
}

// SessionAggRow is one session (session_id, project) folded across all its
// day/model rows, with a dollar cost. Model is the single model used, or "mixed".
type SessionAggRow struct {
	SessionID     string  `json:"sessionId"`
	Project       string  `json:"project"`
	Day           string  `json:"day"` // latest active day
	Model         string  `json:"model"`
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cacheRead"`
	CacheCreation int64   `json:"cacheCreation"`
	Prompts       int64   `json:"prompts"`
	Cost          float64 `json:"costUsd"`
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
		r.Cost = CostUSD(r.Model, r.Input, r.Output, r.CacheRead, r.CacheCreation)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ByModel returns one row per model across the whole window, with a
// distinct-session count (session identity is (session_id, project), matching
// the rollup PK) and a dollar cost. Ordered by cost descending.
func (s *Store) ByModel(from, to string) ([]ModelRow, error) {
	rows, err := s.db.Query(`
SELECT model,
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts),
  COUNT(DISTINCT session_id || char(31) || project)
FROM session_rollup WHERE day BETWEEN ? AND ?
GROUP BY model`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelRow
	for rows.Next() {
		var r ModelRow
		if err := rows.Scan(&r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts, &r.Sessions); err != nil {
			return nil, err
		}
		r.Cost = CostUSD(r.Model, r.Input, r.Output, r.CacheRead, r.CacheCreation)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out, nil
}

// ByProject returns per-(project, model) aggregates over the window, each with a
// dollar cost. The frontend sums rows for a project total and uses the per-model
// split for the mix. Ordered by cost descending.
func (s *Store) ByProject(from, to string) ([]ProjectRow, error) {
	rows, err := s.db.Query(`
SELECT project, model,
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts)
FROM session_rollup WHERE day BETWEEN ? AND ?
GROUP BY project, model`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		var r ProjectRow
		if err := rows.Scan(&r.Project, &r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts); err != nil {
			return nil, err
		}
		r.Cost = CostUSD(r.Model, r.Input, r.Output, r.CacheRead, r.CacheCreation)
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
		r.Cost = CostUSD(r.Model, r.Input, r.Output, r.CacheRead, r.CacheCreation)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionCosts folds every (day,model) row of a session (session identity is
// (session_id, project), matching the rollup PK) into one per-session aggregate
// with a dollar cost, cost-descending. Unlike Sessions, this is the COMPLETE set
// (no day-ordered LIMIT), so the Sessions view's distinct count, cost histogram,
// and top-by-cost list stay correct on wide windows. Bounded by fleet size
// (one grouped row per session/model), so it does not need a cap.
func (s *Store) SessionCosts(from, to string) ([]SessionAggRow, error) {
	rows, err := s.db.Query(`
SELECT session_id, project, model, MAX(day),
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts)
FROM session_rollup WHERE day BETWEEN ? AND ?
GROUP BY session_id, project, model`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type acc struct {
		row    SessionAggRow
		models map[string]struct{}
	}
	byKey := make(map[string]*acc)
	order := []string{}
	for rows.Next() {
		var sid, project, model, day string
		var in, out, cr, cc, prompts int64
		if err := rows.Scan(&sid, &project, &model, &day, &in, &out, &cr, &cc, &prompts); err != nil {
			return nil, err
		}
		k := sid + "\x1f" + project
		a := byKey[k]
		if a == nil {
			a = &acc{row: SessionAggRow{SessionID: sid, Project: project}, models: map[string]struct{}{}}
			byKey[k] = a
			order = append(order, k)
		}
		a.row.Input += in
		a.row.Output += out
		a.row.CacheRead += cr
		a.row.CacheCreation += cc
		a.row.Prompts += prompts
		a.row.Cost += CostUSD(model, in, out, cr, cc)
		if day > a.row.Day {
			a.row.Day = day
		}
		a.models[model] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SessionAggRow, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		if len(a.models) == 1 {
			for m := range a.models {
				a.row.Model = m
			}
		} else {
			a.row.Model = "mixed"
		}
		out = append(out, a.row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out, nil
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
