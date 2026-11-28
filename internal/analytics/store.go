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
	backfilled   bool   // driver and dim data cover every byte before offset
}

const schema = `
CREATE TABLE IF NOT EXISTS ingest_state (
  path TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  read_offset INTEGER NOT NULL,
  mtime INTEGER NOT NULL,
  seen_ids TEXT NOT NULL DEFAULT '',
  backfilled INTEGER NOT NULL DEFAULT 0
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
` + schemaV3Tables

// schemaV3Tables are the tables schema version 3 adds. They are a separate
// constant because the v2 -> v3 migration runs them alone, on top of a DB whose
// other tables must stay exactly as they are.
const schemaV3Tables = `
CREATE TABLE IF NOT EXISTS session_dim (
  session_id TEXT NOT NULL,
  project    TEXT NOT NULL,
  cwd        TEXT NOT NULL DEFAULT '',
  branch     TEXT NOT NULL DEFAULT '',
  runtime    TEXT NOT NULL DEFAULT '',
  account    TEXT NOT NULL DEFAULT '',
  root_key   TEXT NOT NULL DEFAULT '',
  workspace  TEXT NOT NULL DEFAULT '',
  unmatched  INTEGER NOT NULL DEFAULT 0,
  profile    TEXT NOT NULL DEFAULT '',
  via        TEXT NOT NULL DEFAULT '',
  via_path   TEXT NOT NULL DEFAULT '',
  parsed     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, project)
);
CREATE TABLE IF NOT EXISTS driver_rollup (
  day TEXT NOT NULL,
  session_id TEXT NOT NULL,
  project TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  parent TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0,
  tokens INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, session_id, project, kind, name, parent)
);
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
// Version 3 adds session_dim, driver_rollup and ingest_state.backfilled and keeps
// every existing row.
const schemaVersion = 3

// migrate brings a DB to schemaVersion.
//
// A DB from before v2 is not converted: its numbers are wrong, so the analytics
// tables are dropped and the next ingest pass rebuilds them from the transcripts
// on disk (ingest_state is empty, so every transcript is read from offset 0). The
// old DB is first copied to <path>.pre-dedupe.bak through SQLite (VACUUM INTO — a
// consistent snapshot that includes the WAL, safe while the DB is live). The
// backup is made once and never overwritten. If it cannot be made, nothing is
// dropped and the error is returned. That path creates the current (v3) schema.
//
// A v2 DB is only added to: its session_rollup history may outlive the
// transcripts it came from, so nothing is dropped or rewritten (migrateV2ToV3).
func migrate(db *sql.DB, path string) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	switch {
	case version >= schemaVersion:
		return nil
	case version < 2:
		return migrateRebuild(db, path)
	default:
		return migrateV2ToV3(db, path)
	}
}

// migrateRebuild is the pre-v2 path: back up, drop the analytics tables, create
// the current schema. A brand-new DB takes it too, with nothing to back up or drop.
func migrateRebuild(db *sql.DB, path string) error {
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

// migrateV2ToV3 adds session_dim, driver_rollup and ingest_state.backfilled and
// sets user_version = 3, in one transaction, after a one-time backup to
// <path>.pre-v3.bak. Nothing is dropped; existing rows get backfilled = 0.
func migrateV2ToV3(db *sql.DB, path string) error {
	bak := path + ".pre-v3.bak"
	if err := backupOnce(db, bak); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(schemaV3Tables); err != nil {
		return fmt.Errorf("schema v3 tables: %w", err)
	}
	var has int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('ingest_state') WHERE name='backfilled'`).Scan(&has); err != nil {
		return fmt.Errorf("inspect ingest_state: %w", err)
	}
	if has == 0 {
		if _, err := tx.Exec(`ALTER TABLE ingest_state ADD COLUMN backfilled INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add ingest_state.backfilled: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("analytics: schema v3 — added session and cost-driver tables, kept all rows (backup %s)", bak)
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
	rows, err := tx.Query(rewritePrefixQuery, underRoot(oldRoot)...)
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
		if p == oldRoot || strings.HasPrefix(p, oldRoot+"/") { // what the query selects, kept as a guard
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
	carried := make(map[string]fileState, len(from))
	for _, p := range from {
		to := newRoot + p[len(oldRoot):]
		res, err := tx.Exec(`INSERT OR IGNORE INTO ingest_state(path,size,read_offset,mtime,seen_ids,backfilled)
SELECT ?,size,read_offset,mtime,seen_ids,backfilled FROM ingest_state WHERE path=?`, to, p)
		if err != nil {
			return 0, fmt.Errorf("analytics: copy %q: %w", p, err)
		}
		k, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += k
		var fs fileState
		var bf int
		if err := tx.QueryRow(`SELECT size, read_offset, seen_ids, backfilled FROM ingest_state WHERE path=?`, to).
			Scan(&fs.size, &fs.offset, &fs.seen, &bf); err != nil {
			return 0, fmt.Errorf("analytics: read copy %q: %w", to, err)
		}
		fs.backfilled = bf != 0
		carried[to] = fs
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// A pass may be running (adoption does not wait for it, see
	// Service.CarryOffsets). Its cache was loaded before these rows existed, so
	// without them it would read each moved transcript from 0 when it reaches
	// the new path and count it again. A key the cache already has is newer
	// than this copy (Apply wrote it) and is kept. Accepted: a pass whose
	// beginPass read the table before this commit but installed its cache after
	// it misses the copy (the window is the cache load itself).
	s.stMu.Lock()
	if s.states != nil {
		for to, fs := range carried {
			if _, ok := s.states[to]; !ok {
				s.states[to] = fs
			}
		}
	}
	s.stMu.Unlock()
	return int(n), nil
}

// rewritePrefixQuery selects the ingest_state paths under a root (the root
// itself, or below it as a whole path component), with the arguments underRoot
// gives, through the path index: adoption calls RewritePathPrefix once per moved
// entry under the box manager's lock (#330), so it must not read the whole
// table each time. path is TEXT with the default BINARY collation, so the
// comparisons are bytewise: every path starting with "<root>/" is at least
// "<root>/" and below "<root>0" ('/' is 0x2F, '0' is 0x30), and a sibling such
// as "<root>-prod" or "<root>.bak" sorts before "<root>/".
const rewritePrefixQuery = `SELECT path FROM ingest_state WHERE path = ? OR (path >= ? AND path < ?)`

func underRoot(root string) []any { return []any{root, root + "/", root + "0"} }

// State returns the recorded size and read offset for a transcript path, the
// message ids already counted near that offset (oldest first), which let a
// message split across two ingest batches be counted once, and whether the
// session and cost-driver data already cover every byte before the offset
// (backfilled; false for a file counted before schema v3).
func (s *Store) State(path string) (size, offset int64, ok bool, seen []string, backfilled bool, err error) {
	s.stMu.Lock()
	if s.states != nil {
		st, found := s.states[path]
		s.stMu.Unlock()
		if !found {
			return 0, 0, false, nil, false, nil
		}
		return st.size, st.offset, true, strings.Fields(st.seen), st.backfilled, nil
	}
	s.stMu.Unlock()
	var ids string
	var bf int
	row := s.db.QueryRow(`SELECT size, read_offset, seen_ids, backfilled FROM ingest_state WHERE path=?`, path)
	switch err = row.Scan(&size, &offset, &ids, &bf); err {
	case nil:
		return size, offset, true, strings.Fields(ids), bf != 0, nil
	case sql.ErrNoRows:
		return 0, 0, false, nil, false, nil
	default:
		return 0, 0, false, nil, false, err
	}
}

// beginPass loads every ingest_state row into memory so State is a map lookup
// until endPass. Passes are serialized by Service.mu, so nothing else writes
// ingest_state meanwhile. On error State keeps querying the DB.
func (s *Store) beginPass() {
	rows, err := s.db.Query(`SELECT path, size, read_offset, seen_ids, backfilled FROM ingest_state`)
	if err != nil {
		return
	}
	defer rows.Close()
	m := map[string]fileState{}
	for rows.Next() {
		var p string
		var fs fileState
		var bf int
		if rows.Scan(&p, &fs.size, &fs.offset, &fs.seen, &bf) != nil {
			return
		}
		fs.backfilled = bf != 0
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
//
// It also records the session's dims (folder, branch, where it ran: info comes
// from the ingest root) and adds the cost-driver deltas. fromZero says this read
// started at offset 0: it parsed the whole file, drivers included, so the row is
// marked backfilled; any other row keeps the mark it has.
func (s *Store) Apply(sessionID, project, path string, info RootInfo, p *Parsed, fromZero bool, size, offset, mtime int64, seen []string) error {
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

	if err := applyDimAndDrivers(tx, sessionID, project, path, info, p); err != nil {
		return err
	}

	// Where this read started decides the mark: from 0 the parse saw every
	// byte, so drivers are complete; else keep what the row has. The row's
	// stored offset cannot tell: a row carried to this path while the pass ran
	// holds the old path's offset although this read began at 0.
	backfilled := fromZero
	var oldBF int
	switch err := tx.QueryRow(`SELECT backfilled FROM ingest_state WHERE path=?`, path).Scan(&oldBF); err {
	case nil:
		backfilled = fromZero || oldBF != 0
	case sql.ErrNoRows:
	default:
		return fmt.Errorf("state read: %w", err)
	}

	const upsertState = `
INSERT INTO ingest_state(path,size,read_offset,mtime,seen_ids,backfilled) VALUES(?,?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET size=excluded.size, read_offset=excluded.read_offset, mtime=excluded.mtime, seen_ids=excluded.seen_ids, backfilled=excluded.backfilled`
	// seen ids are whitespace-free by construction (safeMsgID), so Join/Fields round-trips.
	if _, err := tx.Exec(upsertState, path, size, offset, mtime, strings.Join(seen, " "), boolInt(backfilled)); err != nil {
		return fmt.Errorf("state upsert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.stMu.Lock()
	if s.states != nil {
		s.states[path] = fileState{size: size, offset: offset, seen: strings.Join(seen, " "), backfilled: backfilled}
	}
	s.stMu.Unlock()
	return nil
}

// ApplyBackfill adds the session dims and cost-driver deltas of a file's bytes
// that were counted before schema v3 (parsed with a fresh dedupe by the caller)
// and marks the file backfilled, in one transaction. It never writes
// session_rollup or attribution_rollup, and never moves the read offset, so it
// cannot change a total.
func (s *Store) ApplyBackfill(sessionID, project, path string, info RootInfo, p *Parsed) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := applyDimAndDrivers(tx, sessionID, project, path, info, p); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ingest_state SET backfilled=1 WHERE path=?`, path); err != nil {
		return fmt.Errorf("backfill mark: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.stMu.Lock()
	if s.states != nil {
		if st, ok := s.states[path]; ok {
			st.backfilled = true
			s.states[path] = st
		}
	}
	s.stMu.Unlock()
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// applyDimAndDrivers upserts a session's dims and adds its driver deltas. An
// empty session id (a clamp of a shrunken file, which parsed nothing) writes
// nothing. cwd keeps the first value, branch is replaced only by a non-empty
// one, and runtime, account and root_key follow the ingest root. A legacy
// parsed = 0 row becomes parsed = 1. A subagent transcript never sets cwd: its
// folder can differ from the launch folder, and the walk reads <sid>/subagents/
// before <sid>.jsonl, so only the session's main file may set it. workspace, unmatched, profile, via and
// via_path are resolved elsewhere and never touched here.
func applyDimAndDrivers(tx *sql.Tx, sessionID, project, path string, info RootInfo, p *Parsed) error {
	if sessionID == "" {
		return nil
	}
	const upsertDim = `
INSERT INTO session_dim(session_id,project,cwd,branch,runtime,account,root_key,parsed)
VALUES(?,?,?,?,?,?,?,1)
ON CONFLICT(session_id,project) DO UPDATE SET
  parsed=1,
  cwd=CASE WHEN session_dim.cwd='' THEN excluded.cwd ELSE session_dim.cwd END,
  branch=CASE WHEN excluded.branch<>'' THEN excluded.branch ELSE session_dim.branch END,
  runtime=excluded.runtime, account=excluded.account, root_key=excluded.root_key`
	cwd := p.Cwd
	if isSubagentPath(path) {
		cwd = ""
	}
	if _, err := tx.Exec(upsertDim, sessionID, project, cwd, p.Branch, info.Runtime, info.Account, info.Key); err != nil {
		return fmt.Errorf("dim upsert: %w", err)
	}
	const upsertDriver = `
INSERT INTO driver_rollup(day,session_id,project,kind,name,parent,cost_usd,tokens)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(day,session_id,project,kind,name,parent) DO UPDATE SET
  cost_usd=cost_usd+excluded.cost_usd,
  tokens=tokens+excluded.tokens`
	for k, v := range p.Drivers {
		if _, err := tx.Exec(upsertDriver, k.Day, sessionID, project, k.Kind, k.Name, k.Parent, v.Cost, v.Tokens); err != nil {
			return fmt.Errorf("driver upsert: %w", err)
		}
	}
	return nil
}

// SeedLegacyDims gives every session counted before schema v3 a session_dim row
// with parsed = 0, derived from its ingest_state path alone: the project and
// session id from the path's tail, runtime/account/key from the root classify
// finds. A path under no known root still gets a row, with runtime ”. A row
// that exists is left as it is, except that a parsed = 0 row with no runtime
// takes the runtime, account and key of a later path of the same session under
// a known root. It returns how many rows it inserted or so completed; a second
// call changes none.
func (s *Store) SeedLegacyDims(classify func(path string) (RootInfo, string, bool)) (int, error) {
	rows, err := s.db.Query(`SELECT path FROM ingest_state ORDER BY path`)
	if err != nil {
		return 0, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	n := 0
	for _, path := range paths {
		var info RootInfo
		rel := ""
		if classify != nil {
			var ok bool
			if info, rel, ok = classify(path); !ok {
				info, rel = RootInfo{}, ""
			}
		}
		if rel == "" {
			rel = tailRel(path)
		}
		sid, project, ok := legacyKeyFromRel(rel)
		if !ok {
			continue
		}
		// A row seeded with no runtime (a path under no known root, e.g. the
		// old root of a data-root move, which sorts first) takes the runtime of
		// a later path of the same session that has one. Nothing else changes.
		res, err := tx.Exec(`INSERT INTO session_dim(session_id,project,runtime,account,root_key,parsed) VALUES(?,?,?,?,?,0)
ON CONFLICT(session_id,project) DO UPDATE SET runtime=excluded.runtime, account=excluded.account, root_key=excluded.root_key
WHERE session_dim.parsed=0 AND session_dim.runtime='' AND excluded.runtime<>''`,
			sid, project, info.Runtime, info.Account, info.Key)
		if err != nil {
			return 0, fmt.Errorf("seed dim: %w", err)
		}
		k, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += int(k)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// DimRow is a session_dim row waiting for its workspace: the key and what
// Resolve reads.
type DimRow struct {
	SessionID, Project string
	DimIn
}

// PendingDims is every session with no workspace yet (an empty one), one no
// workspace explained (unmatched = 1), and one filed by its slug (via legacy)
// whose transcript has since given it a cwd: a session first read before any
// line with a cwd gets one more resolution, from the cwd, which never says
// legacy. Any other resolved row is a snapshot and is never listed again.
func (s *Store) PendingDims() ([]DimRow, error) {
	rows, err := s.db.Query(`SELECT session_id,project,cwd,runtime,root_key FROM session_dim
WHERE workspace='' OR unmatched=1 OR (via='legacy' AND parsed=1 AND cwd<>'') ORDER BY session_id,project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DimRow
	for rows.Next() {
		var r DimRow
		if err := rows.Scan(&r.SessionID, &r.Project, &r.Cwd, &r.Runtime, &r.RootKey); err != nil {
			return nil, err
		}
		r.DimIn.Project = r.Project
		out = append(out, r)
	}
	return out, rows.Err()
}

// KnownPath is a folder an earlier session points at and where that session
// ran ("host", "box" or "").
type KnownPath struct{ Path, Runtime string }

// KnownPaths is every folder earlier sessions point at, for the slug match of a
// session that carries no cwd: the workspace of each matched row and the cwd of
// each parsed one, each with the runtime of the row it came from (a folder that
// rows of two runtimes point at is listed once per runtime). An unmatched row's
// workspace is only its slug, so it is left out.
func (s *Store) KnownPaths() ([]KnownPath, error) {
	rows, err := s.db.Query(`SELECT workspace, runtime FROM session_dim WHERE workspace<>'' AND unmatched=0
UNION
SELECT cwd, runtime FROM session_dim WHERE cwd<>'' AND parsed=1
ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnownPath
	for rows.Next() {
		var k KnownPath
		if err := rows.Scan(&k.Path, &k.Runtime); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SetResolved writes the resolution of one session: workspace, unmatched,
// profile, via and via_path, and nothing else. The WHERE clause skips a row that
// already holds exactly these values, so an unmatched session retried every pass
// costs a read, not a write.
func (s *Store) SetResolved(sessionID, project string, r Resolved) error {
	_, err := s.db.Exec(`UPDATE session_dim SET workspace=?,unmatched=?,profile=?,via=?,via_path=?
WHERE session_id=? AND project=? AND (workspace<>? OR unmatched<>? OR profile<>? OR via<>? OR via_path<>?)`,
		r.Workspace, boolInt(r.Unmatched), r.Profile, r.Via, r.ViaPath, sessionID, project,
		r.Workspace, boolInt(r.Unmatched), r.Profile, r.Via, r.ViaPath)
	return err
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
	// From session_dim; empty when the session has no dim row. Workspace is the
	// project slug for a session no workspace explains, as Workspaces files it.
	Workspace string `json:"workspace"`
	Runtime   string `json:"runtime"`
	Profile   string `json:"profile"`
	Branch    string `json:"branch"`
}

// SessionModelCost is one model's cost within one session over the window: the
// grouped row SessionCosts sees before it folds a multi-model session to "mixed".
type SessionModelCost struct {
	SessionID string  `json:"sessionId"`
	Project   string  `json:"project"`
	Model     string  `json:"model"`
	Cost      float64 `json:"costUsd"`
}

// ToolRow is one attribution aggregate across the window.
type ToolRow struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Records int64  `json:"records"`
}

// ByDayModel returns per-(day,model) token/prompt aggregates in [from,to] (inclusive).
// Every query below takes a Where; the zero Where is every session.
func (s *Store) ByDayModel(from, to string, w Where) ([]DayModelRow, error) {
	src, and, wargs := w.source("session_rollup")
	rows, err := s.db.Query(`
SELECT r.day, r.model,
  SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.cache_read), SUM(r.cache_creation), SUM(r.prompts), SUM(r.cost_usd)
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.day, r.model ORDER BY r.day, r.model`, append([]any{from, to}, wargs...)...)
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
func (s *Store) ByModel(from, to string, w Where) ([]ModelRow, error) {
	src, and, wargs := w.source("session_rollup")
	rows, err := s.db.Query(`
SELECT r.model,
  SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.cache_read), SUM(r.cache_creation), SUM(r.prompts), SUM(r.cost_usd),
  COUNT(DISTINCT r.session_id || char(31) || r.project)
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.model`, append([]any{from, to}, wargs...)...)
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
func (s *Store) ByProject(from, to string, w Where) ([]ProjectRow, error) {
	src, and, wargs := w.source("session_rollup")
	rows, err := s.db.Query(`
SELECT r.project, r.model,
  SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.cache_read), SUM(r.cache_creation), SUM(r.prompts), SUM(r.cost_usd)
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.project, r.model`, append([]any{from, to}, wargs...)...)
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
func (s *Store) Sessions(from, to string, limit int, w Where) ([]SessionRow, error) {
	src, and, wargs := w.source("session_rollup")
	rows, err := s.db.Query(`
SELECT r.day, r.session_id, r.project, r.model, r.input_tokens, r.output_tokens, r.cache_read, r.cache_creation, r.prompts, r.cost_usd
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
ORDER BY r.day DESC, (r.input_tokens+r.output_tokens+r.cache_read+r.cache_creation) DESC
LIMIT ?`, append(append([]any{from, to}, wargs...), limit)...)
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
func (s *Store) SessionCosts(from, to string, w Where) ([]SessionAggRow, error) {
	src, and, wargs := w.joined("session_rollup")
	rows, err := s.db.Query(`
SELECT r.session_id, r.project, r.model, MAX(r.day),
  SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.cache_read), SUM(r.cache_creation), SUM(r.prompts), SUM(r.cost_usd),
  COALESCE(d.workspace,''), COALESCE(d.runtime,''), COALESCE(d.profile,''), COALESCE(d.branch,'')
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.session_id, r.project, r.model, d.workspace, d.runtime, d.profile, d.branch`, append([]any{from, to}, wargs...)...)
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
		var sid, project, model, day, ws, runtime, profile, branch string
		var in, out, cr, cc, prompts int64
		var cost float64
		if err := rows.Scan(&sid, &project, &model, &day, &in, &out, &cr, &cc, &prompts, &cost,
			&ws, &runtime, &profile, &branch); err != nil {
			return nil, err
		}
		ws, _ = workspaceOf(project, ws, false)
		k := sid + "\x1f" + project
		a := byKey[k]
		if a == nil {
			a = &acc{row: SessionAggRow{SessionID: sid, Project: project,
				Workspace: ws, Runtime: runtime, Profile: profile, Branch: branch}, models: map[string]struct{}{}}
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

// SessionModelCosts returns one row per (session, model) over the window, the
// grouped rows SessionCosts folds. Bounded by sessions x models. Costliest first.
func (s *Store) SessionModelCosts(from, to string, w Where) ([]SessionModelCost, error) {
	src, and, wargs := w.source("session_rollup")
	rows, err := s.db.Query(`
SELECT r.session_id, r.project, r.model, SUM(r.cost_usd)
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.session_id, r.project, r.model`, append([]any{from, to}, wargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionModelCost{}
	for rows.Next() {
		var r SessionModelCost
		if err := rows.Scan(&r.SessionID, &r.Project, &r.Model, &r.Cost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Cost != b.Cost {
			return a.Cost > b.Cost
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Model < b.Model
	})
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
func (s *Store) Tools(from, to string, limit int, w Where) ([]ToolRow, error) {
	src, and, wargs := w.source("attribution_rollup")
	rows, err := s.db.Query(`
SELECT r.kind, r.name, SUM(r.records) AS n
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.kind, r.name ORDER BY n DESC LIMIT ?`, append(append([]any{from, to}, wargs...), limit)...)
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
