package analytics

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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

	// states, when non-nil, is an in-memory copy of ingest_state for the
	// duration of one ingest pass (beginPass/endPass). A no-change pass would
	// otherwise run one prepared SELECT per transcript, which is most of its
	// cost. Apply keeps it current, so a path seen twice in one pass reads its
	// own latest offset exactly as the SELECT would.
	stMu   sync.Mutex
	states map[string]fileState
}

type fileState struct {
	size, offset int64
	seen         string // space-joined message ids, as stored
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
  cache_creation_1h INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
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
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// schemaVersion is the PRAGMA user_version this code writes. Version 2 stores the
// per-message cost and the 1h cache-write split, and counts each message once;
// rollups written before that (version 0) are ~2x inflated and are rebuilt.
const schemaVersion = 2

// migrate brings a DB to schemaVersion. A DB from before v2 is not converted: its
// numbers are wrong, so the analytics tables are dropped and the next ingest pass
// rebuilds them from the transcripts on disk (ingest_state is empty, so every
// transcript is read from offset 0). The old DB is first copied to
// <path>.pre-dedupe.bak through SQLite (VACUUM INTO — a consistent snapshot that
// includes the WAL, safe while the DB is live). The backup is made once and never
// overwritten. If it cannot be made, nothing is dropped and the error is returned.
func migrate(db *sql.DB, path string) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if version >= schemaVersion {
		return nil
	}
	var existing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('ingest_state','session_rollup','attribution_rollup')`).Scan(&existing); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	rebuilt := existing > 0
	if rebuilt {
		if err := backupOnce(db, path+".pre-dedupe.bak"); err != nil {
			return err
		}
	}
	// One transaction: the wipe, the new schema and the version bump land together
	// or not at all (DDL and PRAGMA user_version are transactional in SQLite).
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if rebuilt {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS attribution_rollup; DROP TABLE IF EXISTS session_rollup; DROP TABLE IF EXISTS ingest_state;`); err != nil {
			return fmt.Errorf("drop old rollups: %w", err)
		}
	}
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if rebuilt {
		log.Printf("analytics: schema v2 — rebuilt rollups from transcripts (backup %s)", path+".pre-dedupe.bak")
	}
	return nil
}

// backupOnce writes a SQLite snapshot of db to dst unless a backup is already
// there. It writes to dst+".tmp" and renames, so a crash mid-copy can never leave
// a truncated file that a later boot would mistake for a finished backup.
func backupOnce(db *sql.DB, dst string) error {
	if fi, err := os.Stat(dst); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("backup %s exists and is not a regular file", dst)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("backup %s: %w", dst, err)
	}
	tmp := dst + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear stale backup temp: %w", err)
	}
	// The target is a bound parameter, never spliced into the SQL text.
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("backup old analytics DB: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	// SQLite does not fsync VACUUM INTO output; make the bytes durable before the
	// rename publishes them, so a power loss can't leave a renamed empty backup.
	f, err := os.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("sync backup: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync backup: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("sync backup: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalize backup: %w", err)
	}
	// Make the rename durable. Best effort, like the other atomic writers here
	// (store, auth, credfile): not every platform supports fsync on a directory.
	if d, err := os.Open(filepath.Dir(dst)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// RewritePathPrefix copies every ingest_state row under oldRoot (the path
// equal to it, or below it as a whole path component) to the same place under
// newRoot, in one transaction, and returns how many rows it inserted. A data
// root move (#255) calls it: without it each transcript would read as new and
// be counted again from offset 0. The rows under oldRoot stay (ruling R12): an
// older binary rolled back onto the link at the old path then resumes from the
// offsets of the move instead of re-reading every transcript from 0. A row
// already under newRoot
// is kept as it is (INSERT OR IGNORE). A sibling such as <oldRoot>-prod is not
// touched. Idempotent: a second run inserts nothing.
func (s *Store) RewritePathPrefix(oldRoot, newRoot string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT path FROM ingest_state`)
	if err != nil {
		return 0, err
	}
	var from []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		if p == oldRoot || strings.HasPrefix(p, oldRoot+"/") {
			from = append(from, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var n int64
	for _, p := range from {
		res, err := tx.Exec(`INSERT OR IGNORE INTO ingest_state(path,size,read_offset,mtime,seen_ids)
SELECT ?,size,read_offset,mtime,seen_ids FROM ingest_state WHERE path=?`, newRoot+p[len(oldRoot):], p)
		if err != nil {
			return 0, fmt.Errorf("analytics: copy %s: %w", p, err)
		}
		k, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += k
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// State returns the recorded size and read offset for a transcript path, and the
// message ids already counted near that offset (oldest first), which let a
// message split across two ingest batches be counted once.
func (s *Store) State(path string) (size, offset int64, ok bool, seen []string, err error) {
	s.stMu.Lock()
	if s.states != nil {
		st, found := s.states[path]
		s.stMu.Unlock()
		if !found {
			return 0, 0, false, nil, nil
		}
		return st.size, st.offset, true, strings.Fields(st.seen), nil
	}
	s.stMu.Unlock()
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

// beginPass loads every ingest_state row into memory so State is a map lookup
// until endPass. Passes are serialized by Service.mu, so nothing else writes
// ingest_state meanwhile. On error State keeps querying the DB.
func (s *Store) beginPass() {
	rows, err := s.db.Query(`SELECT path, size, read_offset, seen_ids FROM ingest_state`)
	if err != nil {
		return
	}
	defer rows.Close()
	m := map[string]fileState{}
	for rows.Next() {
		var p string
		var fs fileState
		if rows.Scan(&p, &fs.size, &fs.offset, &fs.seen) != nil {
			return
		}
		m[p] = fs
	}
	if rows.Err() != nil {
		return
	}
	s.stMu.Lock()
	s.states = m
	s.stMu.Unlock()
}

func (s *Store) endPass() {
	s.stMu.Lock()
	s.states = nil
	s.stMu.Unlock()
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
INSERT INTO session_rollup(day,session_id,project,model,input_tokens,output_tokens,cache_read,cache_creation,cache_creation_1h,cost_usd,prompts)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(day,session_id,project,model) DO UPDATE SET
  input_tokens=input_tokens+excluded.input_tokens,
  output_tokens=output_tokens+excluded.output_tokens,
  cache_read=cache_read+excluded.cache_read,
  cache_creation=cache_creation+excluded.cache_creation,
  cache_creation_1h=cache_creation_1h+excluded.cache_creation_1h,
  cost_usd=cost_usd+excluded.cost_usd,
  prompts=prompts+excluded.prompts`
	for k, t := range p.Tokens {
		if _, err := tx.Exec(upsertSession, k.Day, sessionID, project, k.Model,
			t.Input, t.Output, t.CacheRead, t.CacheCreation, t.CacheWrite1h, t.Cost, t.Prompts); err != nil {
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
	// seen ids are whitespace-free by construction (safeMsgID), so Join/Fields round-trips.
	if _, err := tx.Exec(upsertState, path, size, offset, mtime, strings.Join(seen, " ")); err != nil {
		return fmt.Errorf("state upsert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.stMu.Lock()
	if s.states != nil {
		s.states[path] = fileState{size: size, offset: offset, seen: strings.Join(seen, " ")}
	}
	s.stMu.Unlock()
	return nil
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
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts), SUM(cost_usd)
FROM session_rollup WHERE day BETWEEN ? AND ?
GROUP BY day, model ORDER BY day, model`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayModelRow
	for rows.Next() {
		var r DayModelRow
		if err := rows.Scan(&r.Day, &r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts, &r.Cost); err != nil {
			return nil, err
		}
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
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts), SUM(cost_usd),
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
		if err := rows.Scan(&r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts, &r.Cost, &r.Sessions); err != nil {
			return nil, err
		}
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
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts), SUM(cost_usd)
FROM session_rollup WHERE day BETWEEN ? AND ?
GROUP BY project, model`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		var r ProjectRow
		if err := rows.Scan(&r.Project, &r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts, &r.Cost); err != nil {
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
SELECT day, session_id, project, model, input_tokens, output_tokens, cache_read, cache_creation, prompts, cost_usd
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
			&r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Prompts, &r.Cost); err != nil {
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

// SessionCosts folds every (day,model) row of a session (session identity is
// (session_id, project), matching the rollup PK) into one per-session aggregate
// with a dollar cost, cost-descending. Unlike Sessions, this is the COMPLETE set
// (no day-ordered LIMIT), so the Sessions view's distinct count, cost histogram,
// and top-by-cost list stay correct on wide windows. Bounded by fleet size
// (one grouped row per session/model), so it does not need a cap.
func (s *Store) SessionCosts(from, to string) ([]SessionAggRow, error) {
	rows, err := s.db.Query(`
SELECT session_id, project, model, MAX(day),
  SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cache_creation), SUM(prompts), SUM(cost_usd)
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
		var cost float64
		if err := rows.Scan(&sid, &project, &model, &day, &in, &out, &cr, &cc, &prompts, &cost); err != nil {
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
		a.row.Cost += cost
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

// ConversationCosts returns the total cost per conversation id (session_id)
// over every day it ran. It is the whole history, not a window: a session's
// price is what its conversations cost since they began.
func (s *Store) ConversationCosts() (map[string]float64, error) {
	rows, err := s.db.Query(`SELECT session_id, SUM(cost_usd) FROM session_rollup GROUP BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var sid string
		var c float64
		if err := rows.Scan(&sid, &c); err != nil {
			return nil, err
		}
		out[sid] = c
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
