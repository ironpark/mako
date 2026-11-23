package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// Sandboxed per-scope SQLite databases (permission "db") and row-level CRDT
// synced tables (permission "db:sync") replicated between mutually trusted
// peers over Mako's libp2p mesh.

const (
	dbOpsPerMinute         = 300
	defaultDBQuota         = 32 << 20 // 32 MiB total across all scopes of one plugin
	maxDBFilePages         = 4096     // 16 MiB per single SQLite file (4 KiB pages)
	maxDBQueryRows         = 500
	maxDBResultBytes       = 1 << 20 // 1 MiB per query response
	maxDBQueryTimeout      = 500 * time.Millisecond
	maxDBBatchStmts        = 64
	maxDBSQLBytes          = 64 << 10
	maxDBParams            = 64
	maxDBParamBytes        = 256 << 10
	maxDBDocBytes          = 256 << 10
	maxDBRowIDBytes        = 256
	maxDBSyncTables        = 16
	maxDBSyncRowsPerBucket = 2000
	maxDBOpenHandles       = 16
)

var syncTableNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// dbStore manages a single plugin's sandboxed SQLite files, one per scopeKey,
// under plugin-data/<plugin-id>/db/.
type dbStore struct {
	dir     string
	quota   int
	clock   *syncClock
	onDirty func()

	mu      sync.Mutex
	handles map[string]*dbHandle // scopeKey -> open handle
}

type dbHandle struct {
	db       *sql.DB
	path     string
	scopeKey string
	lastUsed time.Time
	syncInit bool
}

// syncClock guards a Manager-wide crdt.Clock used to stamp plugin DB CRDT rows.
type syncClock struct {
	mu    sync.Mutex
	clock *crdt.Clock
}

func newSyncClock(nodeID string) *syncClock {
	if nodeID == "" {
		nodeID = "local"
	}
	return &syncClock{clock: crdt.NewClock(nodeID, true)}
}

func (c *syncClock) tick() crdt.HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clock.Now(time.Now().UnixMilli())
}

func (c *syncClock) observe(h crdt.HLC, now int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clock.Observe(h, now)
}

func newDBStore(dir string, quota int, clock *syncClock, onDirty func()) *dbStore {
	if quota <= 0 {
		quota = defaultDBQuota
	}
	return &dbStore{
		dir:     dir,
		quota:   quota,
		clock:   clock,
		onDirty: onDirty,
		handles: map[string]*dbHandle{},
	}
}

func dbFileName(scopeKey string) string {
	if scopeKey == "" {
		return "global.sqlite"
	}
	sum := sha256.Sum256([]byte(scopeKey))
	return "s-" + hex.EncodeToString(sum[:8]) + ".sqlite"
}

func (s *dbStore) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, h := range s.handles {
		_ = h.db.Close()
		delete(s.handles, k)
	}
}

func (s *dbStore) used() int64 {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func (s *dbStore) openLocked(scopeKey string) (*dbHandle, error) {
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil, err
	}
	if h, ok := s.handles[scopeKey]; ok {
		h.lastUsed = time.Now()
		return h, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	// Evict least-recently-used handle if too many scope DBs are open at once.
	if len(s.handles) >= maxDBOpenHandles {
		var victimKey string
		var victimTime time.Time
		for k, h := range s.handles {
			if victimKey == "" || h.lastUsed.Before(victimTime) {
				victimKey, victimTime = k, h.lastUsed
			}
		}
		if victimKey != "" {
			_ = s.handles[victimKey].db.Close()
			delete(s.handles, victimKey)
		}
	}
	path := filepath.Join(s.dir, dbFileName(scopeKey))
	db, err := openHardenedSQLite(path)
	if err != nil {
		return nil, err
	}
	h := &dbHandle{db: db, path: path, scopeKey: scopeKey, lastUsed: time.Now()}
	s.handles[scopeKey] = h
	return h, nil
}

func openHardenedSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 2000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA trusted_schema = OFF",
		"PRAGMA cell_size_check = ON",
		fmt.Sprintf("PRAGMA max_page_count = %d", maxDBFilePages),
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite init (%s): %w", p, err)
		}
	}
	return db, nil
}

func ensureSyncSchemaTx(tx *sql.Tx, scopeKey string) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS _mako_sync_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS _mako_sync_tables (name TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS _mako_sync (
			table_name TEXT NOT NULL,
			row_id TEXT NOT NULL,
			doc TEXT NOT NULL,
			hlc_wall INTEGER NOT NULL,
			hlc_logical INTEGER NOT NULL,
			hlc_node TEXT NOT NULL,
			deleted INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (table_name, row_id)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO _mako_sync_meta (k, v) VALUES ('scope_key', ?)`, scopeKey)
	return err
}

func ensureSyncTableTx(tx *sql.Tx, table string) error {
	if err := checkSyncTableName(table); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO _mako_sync_tables (name) VALUES (?)`, table); err != nil {
		return err
	}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (
		id TEXT PRIMARY KEY,
		doc TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		deleted INTEGER NOT NULL DEFAULT 0
	)`, table)
	_, err := tx.Exec(ddl)
	return err
}

func checkSyncTableName(table string) error {
	if !syncTableNameRe.MatchString(table) {
		return fmt.Errorf("sync table %q must be lowercase letters, digits and underscores, starting with a letter", table)
	}
	if strings.HasPrefix(table, "_mako_") || strings.HasPrefix(table, "sqlite_") {
		return fmt.Errorf("sync table %q uses a reserved prefix", table)
	}
	return nil
}

func checkSyncRowID(id string) error {
	if id == "" || len(id) > maxDBRowIDBytes || !utf8.ValidString(id) || strings.ContainsFunc(id, protocol.Spoofing) {
		return fmt.Errorf("row id must be valid UTF-8 of 1 to %d bytes without control characters", maxDBRowIDBytes)
	}
	return nil
}

func checkSyncDocJSON(doc string) (string, error) {
	if len(doc) == 0 || len(doc) > maxDBDocBytes || !utf8.ValidString(doc) {
		return "", fmt.Errorf("doc_json must be valid UTF-8 JSON of 1 to %d KiB", maxDBDocBytes>>10)
	}
	if !json.Valid([]byte(doc)) {
		return "", errors.New("doc_json is not valid JSON")
	}
	return doc, nil
}

// checkSQL validates that sqlStr is a single safe SQLite statement and enforces
// read-only queries, blocks dangerous statements (ATTACH/PRAGMA/etc.), and
// prevents plugins from mutating internal _mako_sync tables directly.
func checkSQL(sqlStr string, readOnly bool) error {
	if len(sqlStr) == 0 || len(sqlStr) > maxDBSQLBytes || !utf8.ValidString(sqlStr) {
		return fmt.Errorf("sql must be valid UTF-8 of 1 to %d KiB", maxDBSQLBytes>>10)
	}
	tokens, multi, err := scanSQLTokens(sqlStr)
	if err != nil {
		return err
	}
	if multi {
		return errors.New("multiple SQL statements are not allowed; use batch for transactions")
	}
	if len(tokens) == 0 {
		return errors.New("sql statement is empty")
	}
	first := strings.ToUpper(tokens[0])
	if readOnly {
		switch first {
		case "SELECT", "WITH", "EXPLAIN":
		default:
			return fmt.Errorf("query requires SELECT, WITH, or EXPLAIN (got %s)", first)
		}
	} else {
		switch first {
		case "ATTACH", "DETACH", "PRAGMA", "VACUUM", "REINDEX", "BEGIN", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
			return fmt.Errorf("SQL statement %s is not allowed", first)
		}
	}
	for _, tok := range tokens {
		upper := strings.ToUpper(tok)
		switch upper {
		case "ATTACH", "DETACH", "PRAGMA", "LOAD_EXTENSION":
			return fmt.Errorf("SQL keyword %s is not allowed", upper)
		}
		if readOnly {
			switch upper {
			case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER", "VACUUM", "REINDEX":
				return fmt.Errorf("query may not contain write keyword %s", upper)
			}
		} else {
			lower := strings.ToLower(tok)
			if lower == "_mako_sync" || lower == "_mako_sync_meta" || lower == "_mako_sync_tables" {
				return fmt.Errorf("table %s is managed by mako and cannot be modified directly", lower)
			}
		}
	}
	return nil
}

// scanSQLTokens extracts bare identifiers/keywords and detects multiple
// statements separated by ';' outside quotes and comments.
func scanSQLTokens(src string) (tokens []string, multi bool, err error) {
	n := len(src)
	i := 0
	sawSemi := false
	for i < n {
		c := src[i]
		// Whitespace
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
			i++
			continue
		}
		// Line comment -- ...
		if c == '-' && i+1 < n && src[i+1] == '-' {
			i += 2
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment /* ... */
		if c == '/' && i+1 < n && src[i+1] == '*' {
			i += 2
			closed := false
			for i+1 < n {
				if src[i] == '*' && src[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false, errors.New("unterminated SQL block comment")
			}
			continue
		}
		if sawSemi {
			return nil, true, nil
		}
		// Semicolon
		if c == ';' {
			sawSemi = true
			i++
			continue
		}
		// Single-quoted string literal
		if c == '\'' {
			i++
			closed := false
			for i < n {
				if src[i] == '\'' {
					if i+1 < n && src[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false, errors.New("unterminated SQL string literal")
			}
			continue
		}
		// Quoted identifier: "...", `...`, [...]
		if c == '"' || c == '`' || c == '[' {
			endChar := c
			if c == '[' {
				endChar = ']'
			}
			i++
			start := i
			closed := false
			for i < n {
				if src[i] == endChar {
					if endChar != ']' && i+1 < n && src[i+1] == endChar {
						i += 2
						continue
					}
					ident := src[start:i]
					if ident != "" {
						tokens = append(tokens, ident)
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false, errors.New("unterminated SQL identifier")
			}
			continue
		}
		// Bare word (keyword or identifier)
		if isSQLIdentStart(c) {
			start := i
			i++
			for i < n && isSQLIdentPart(src[i]) {
				i++
			}
			tokens = append(tokens, src[start:i])
			continue
		}
		i++
	}
	return tokens, false, nil
}

func isSQLIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isSQLIdentPart(c byte) bool {
	return isSQLIdentStart(c) || (c >= '0' && c <= '9') || c == '$'
}

func decodeSQLParams(raw []string) ([]any, error) {
	if len(raw) > maxDBParams {
		return nil, fmt.Errorf("at most %d SQL parameters are allowed", maxDBParams)
	}
	out := make([]any, len(raw))
	for i, s := range raw {
		if len(s) > maxDBParamBytes {
			return nil, fmt.Errorf("SQL parameter %d exceeds %d KiB", i, maxDBParamBytes>>10)
		}
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("SQL parameter %d is not valid JSON: %w", i, err)
		}
		switch x := v.(type) {
		case nil:
			out[i] = nil
		case bool:
			if x {
				out[i] = int64(1)
			} else {
				out[i] = int64(0)
			}
		case string:
			out[i] = x
		case json.Number:
			if iv, err := x.Int64(); err == nil {
				out[i] = iv
			} else if fv, err := x.Float64(); err == nil {
				out[i] = fv
			} else {
				return nil, fmt.Errorf("SQL parameter %d has invalid number %q", i, x)
			}
		default:
			return nil, fmt.Errorf("SQL parameter %d must be a scalar (null, boolean, number, or string)", i)
		}
	}
	return out, nil
}

func (s *dbStore) query(ctx context.Context, scopeKey, sqlStr string, rawParams []string) (*hostapi.QueryResponse, error) {
	if err := checkSQL(sqlStr, true); err != nil {
		return nil, err
	}
	params, err := decodeSQLParams(rawParams)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.openLocked(scopeKey)
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout)
	defer cancel()
	rows, err := h.db.QueryContext(qctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &hostapi.QueryResponse{Columns: cols}
	totalBytes := 0
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(out.RowsJson) >= maxDBQueryRows {
			out.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(cols))
		for i, v := range vals {
			switch b := v.(type) {
			case []byte:
				if utf8.Valid(b) {
					row[i] = string(b)
				} else {
					row[i] = base64.StdEncoding.EncodeToString(b)
				}
			default:
				row[i] = v
			}
		}
		enc, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if totalBytes+len(enc) > maxDBResultBytes {
			out.Truncated = true
			break
		}
		totalBytes += len(enc)
		out.RowsJson = append(out.RowsJson, string(enc))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *dbStore) checkQuotaLocked() error {
	if s.used() >= int64(s.quota) {
		return fmt.Errorf("plugin database quota of %d MiB reached", s.quota>>20)
	}
	return nil
}

func (s *dbStore) exec(ctx context.Context, scopeKey, sqlStr string, rawParams []string) (*hostapi.ExecResponse, error) {
	if err := checkSQL(sqlStr, false); err != nil {
		return nil, err
	}
	params, err := decodeSQLParams(rawParams)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkQuotaLocked(); err != nil {
		return nil, err
	}
	h, err := s.openLocked(scopeKey)
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout)
	defer cancel()
	res, err := h.db.ExecContext(qctx, sqlStr, params...)
	if err != nil {
		return nil, err
	}
	aff, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()
	return &hostapi.ExecResponse{RowsAffected: int32(aff), LastInsertId: float64(lastID)}, nil
}

func (s *dbStore) batch(ctx context.Context, scopeKey string, stmts []*hostapi.DbStatement) (*hostapi.BatchResponse, error) {
	if len(stmts) == 0 {
		return &hostapi.BatchResponse{}, nil
	}
	if len(stmts) > maxDBBatchStmts {
		return nil, fmt.Errorf("at most %d statements per batch are allowed", maxDBBatchStmts)
	}
	decoded := make([][]any, len(stmts))
	for i, st := range stmts {
		if err := checkSQL(st.GetSql(), false); err != nil {
			return nil, fmt.Errorf("statement %d: %w", i, err)
		}
		p, err := decodeSQLParams(st.GetParamsJson())
		if err != nil {
			return nil, fmt.Errorf("statement %d: %w", i, err)
		}
		decoded[i] = p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkQuotaLocked(); err != nil {
		return nil, err
	}
	h, err := s.openLocked(scopeKey)
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout*2)
	defer cancel()
	tx, err := h.db.BeginTx(qctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	results := make([]*hostapi.ExecResponse, len(stmts))
	for i, st := range stmts {
		res, err := tx.ExecContext(qctx, st.GetSql(), decoded[i]...)
		if err != nil {
			return nil, fmt.Errorf("statement %d: %w", i, err)
		}
		aff, _ := res.RowsAffected()
		lastID, _ := res.LastInsertId()
		results[i] = &hostapi.ExecResponse{RowsAffected: int32(aff), LastInsertId: float64(lastID)}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &hostapi.BatchResponse{Results: results}, nil
}

func (s *dbStore) stats(ctx context.Context, scopeKey string) (*hostapi.StatsResponse, error) {
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var syncTables []string
	path := filepath.Join(s.dir, dbFileName(scopeKey))
	if _, err := os.Stat(path); err == nil {
		h, err := s.openLocked(scopeKey)
		if err == nil {
			qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout)
			rows, qerr := h.db.QueryContext(qctx, `SELECT name FROM _mako_sync_tables ORDER BY name`)
			if qerr == nil {
				for rows.Next() {
					var name string
					if rows.Scan(&name) == nil {
						syncTables = append(syncTables, name)
					}
				}
				_ = rows.Close()
			}
			cancel()
		}
	}
	return &hostapi.StatsResponse{
		UsedBytes:  uint32(s.used()),
		QuotaBytes: uint32(s.quota),
		SyncTables: syncTables,
	}, nil
}

func (s *dbStore) syncEnable(ctx context.Context, scopeKey, table string) error {
	if err := checkSyncTableName(table); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkQuotaLocked(); err != nil {
		return err
	}
	h, err := s.openLocked(scopeKey)
	if err != nil {
		return err
	}
	qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout)
	defer cancel()
	tx, err := h.db.BeginTx(qctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSyncSchemaTx(tx, scopeKey); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(qctx, `SELECT COUNT(*) FROM _mako_sync_tables WHERE name != ?`, table).Scan(&count); err != nil {
		return err
	}
	if count >= maxDBSyncTables {
		return fmt.Errorf("at most %d synced tables per scope database are allowed", maxDBSyncTables)
	}
	if err := ensureSyncTableTx(tx, table); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	h.syncInit = true
	return nil
}

func (s *dbStore) syncPut(ctx context.Context, scopeKey, table, id, docJSON string) (int64, error) {
	if err := checkSyncTableName(table); err != nil {
		return 0, err
	}
	if err := checkSyncRowID(id); err != nil {
		return 0, err
	}
	doc, err := checkSyncDocJSON(docJSON)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if err := s.checkQuotaLocked(); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	h, err := s.openLocked(scopeKey)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout)
	defer cancel()
	tx, err := h.db.BeginTx(qctx, nil)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSyncSchemaTx(tx, scopeKey); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	var exists int
	if err := tx.QueryRowContext(qctx, `SELECT COUNT(*) FROM _mako_sync_tables WHERE name = ?`, table).Scan(&exists); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	if exists == 0 {
		s.mu.Unlock()
		return 0, fmt.Errorf("table %q is not enabled for sync (call syncEnable first)", table)
	}
	var rowCount int
	if err := tx.QueryRowContext(qctx, `SELECT COUNT(*) FROM _mako_sync WHERE NOT (table_name = ? AND row_id = ?)`, table, id).Scan(&rowCount); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	if rowCount >= maxDBSyncRowsPerBucket {
		s.mu.Unlock()
		return 0, fmt.Errorf("synced bucket reached limit of %d rows", maxDBSyncRowsPerBucket)
	}
	hlc := s.clock.tick()
	if _, err := tx.ExecContext(qctx,
		`INSERT OR REPLACE INTO _mako_sync (table_name, row_id, doc, hlc_wall, hlc_logical, hlc_node, deleted) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		table, id, doc, hlc.WallTime, hlc.Logical, hlc.NodeID,
	); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	projSQL := fmt.Sprintf(`INSERT OR REPLACE INTO "%s" (id, doc, updated_at, deleted) VALUES (?, ?, ?, 0)`, table)
	if _, err := tx.ExecContext(qctx, projSQL, id, doc, hlc.WallTime); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()
	if s.onDirty != nil {
		s.onDirty()
	}
	return hlc.WallTime, nil
}

func (s *dbStore) syncDelete(ctx context.Context, scopeKey, table, id string) (bool, error) {
	if err := checkSyncTableName(table); err != nil {
		return false, err
	}
	if err := checkSyncRowID(id); err != nil {
		return false, err
	}
	s.mu.Lock()
	h, err := s.openLocked(scopeKey)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	qctx, cancel := context.WithTimeout(ctx, maxDBQueryTimeout)
	defer cancel()
	tx, err := h.db.BeginTx(qctx, nil)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSyncSchemaTx(tx, scopeKey); err != nil {
		s.mu.Unlock()
		return false, err
	}
	var exists int
	if err := tx.QueryRowContext(qctx, `SELECT COUNT(*) FROM _mako_sync_tables WHERE name = ?`, table).Scan(&exists); err != nil {
		s.mu.Unlock()
		return false, err
	}
	if exists == 0 {
		s.mu.Unlock()
		return false, fmt.Errorf("table %q is not enabled for sync (call syncEnable first)", table)
	}
	var prevDeleted int
	hadLive := false
	err = tx.QueryRowContext(qctx, `SELECT deleted FROM _mako_sync WHERE table_name = ? AND row_id = ?`, table, id).Scan(&prevDeleted)
	if err == nil && prevDeleted == 0 {
		hadLive = true
	}
	hlc := s.clock.tick()
	if _, err := tx.ExecContext(qctx,
		`INSERT OR REPLACE INTO _mako_sync (table_name, row_id, doc, hlc_wall, hlc_logical, hlc_node, deleted) VALUES (?, ?, '{}', ?, ?, ?, 1)`,
		table, id, hlc.WallTime, hlc.Logical, hlc.NodeID,
	); err != nil {
		s.mu.Unlock()
		return false, err
	}
	projSQL := fmt.Sprintf(`INSERT OR REPLACE INTO "%s" (id, doc, updated_at, deleted) VALUES (?, '{}', ?, 1)`, table)
	if _, err := tx.ExecContext(qctx, projSQL, id, hlc.WallTime); err != nil {
		s.mu.Unlock()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.mu.Unlock()
	if s.onDirty != nil {
		s.onDirty()
	}
	return hadLive, nil
}

// exportBuckets reads all synced CRDT rows across every scope DB of this plugin.
func (s *dbStore) exportBuckets(pluginID string) []*protocol.PluginSyncBucket {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*protocol.PluginSyncBucket
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sqlite") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		// Reuse an already-open handle if present, else open briefly.
		var db *sql.DB
		var closeAfter bool
		for _, h := range s.handles {
			if h.path == path {
				db = h.db
				break
			}
		}
		if db == nil {
			var err error
			db, err = openHardenedSQLite(path)
			if err != nil {
				continue
			}
			closeAfter = true
		}
		bucket := exportBucketFromDB(db, pluginID)
		if closeAfter {
			_ = db.Close()
		}
		if bucket != nil && len(bucket.Rows) > 0 {
			out = append(out, bucket)
		}
	}
	slices.SortFunc(out, func(a, b *protocol.PluginSyncBucket) int {
		return strings.Compare(a.Scope, b.Scope)
	})
	return out
}

func exportBucketFromDB(db *sql.DB, pluginID string) *protocol.PluginSyncBucket {
	var scopeKey string
	if err := db.QueryRow(`SELECT v FROM _mako_sync_meta WHERE k = 'scope_key'`).Scan(&scopeKey); err != nil {
		return nil
	}
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil
	}
	rows, err := db.Query(`SELECT table_name, row_id, doc, hlc_wall, hlc_logical, hlc_node, deleted FROM _mako_sync ORDER BY table_name, row_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	bucket := &protocol.PluginSyncBucket{PluginId: pluginID, Scope: scopeKey}
	for rows.Next() {
		var r protocol.PluginSyncRow
		var wall int64
		var logical uint32
		var node string
		var del int
		if err := rows.Scan(&r.Table, &r.Id, &r.Doc, &wall, &logical, &node, &del); err != nil {
			continue
		}
		r.Clock = &protocol.HLC{WallTime: wall, Logical: logical, NodeId: node}
		r.Deleted = del != 0
		bucket.Rows = append(bucket.Rows, &r)
	}
	if len(bucket.Rows) == 0 {
		return nil
	}
	bucket.Digest = syncBucketDigest(bucket)
	return bucket
}

// mergeBucket applies incoming CRDT rows for one (pluginID, scopeKey) database,
// returning which tables had winning remote edits and how many rows changed.
func (s *dbStore) mergeBucket(pluginID string, bucket *protocol.PluginSyncBucket) (tables []string, changed int, err error) {
	if bucket == nil || len(bucket.GetRows()) == 0 {
		return nil, 0, nil
	}
	if len(bucket.GetRows()) > maxDBSyncRowsPerBucket {
		return nil, 0, fmt.Errorf("sync bucket exceeds %d rows", maxDBSyncRowsPerBucket)
	}
	scopeKey := bucket.GetScope()
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil, 0, err
	}
	now := time.Now().UnixMilli()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkQuotaLocked(); err != nil {
		return nil, 0, err
	}
	h, err := s.openLocked(scopeKey)
	if err != nil {
		return nil, 0, err
	}
	tx, err := h.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSyncSchemaTx(tx, scopeKey); err != nil {
		return nil, 0, err
	}

	ensured := map[string]bool{}
	touchedTables := map[string]bool{}
	for _, r := range bucket.GetRows() {
		table := r.GetTable()
		id := r.GetId()
		if err := checkSyncTableName(table); err != nil {
			continue
		}
		if err := checkSyncRowID(id); err != nil {
			continue
		}
		doc := r.GetDoc()
		if r.GetDeleted() && doc == "" {
			doc = "{}"
		}
		if _, err := checkSyncDocJSON(doc); err != nil {
			continue
		}
		inHLC := protoToHLC(r.GetClock())
		if inHLC.IsZero() || crdt.Quarantined(inHLC, now) {
			continue
		}
		s.clock.observe(inHLC, now)

		var curWall int64
		var curLogical uint32
		var curNode string
		var curHLC crdt.HLC
		if err := tx.QueryRow(`SELECT hlc_wall, hlc_logical, hlc_node FROM _mako_sync WHERE table_name = ? AND row_id = ?`, table, id).Scan(&curWall, &curLogical, &curNode); err == nil {
			curHLC = crdt.HLC{WallTime: curWall, Logical: curLogical, NodeID: curNode}
		}
		tieKey := pluginID + "\x00" + scopeKey + "\x00" + table + "\x00" + id
		if !inHLC.After(curHLC, tieKey) {
			continue
		}
		if !ensured[table] {
			if err := ensureSyncTableTx(tx, table); err != nil {
				return nil, 0, err
			}
			ensured[table] = true
		}
		delInt := 0
		if r.GetDeleted() {
			delInt = 1
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO _mako_sync (table_name, row_id, doc, hlc_wall, hlc_logical, hlc_node, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			table, id, doc, inHLC.WallTime, inHLC.Logical, inHLC.NodeID, delInt,
		); err != nil {
			return nil, 0, err
		}
		projSQL := fmt.Sprintf(`INSERT OR REPLACE INTO "%s" (id, doc, updated_at, deleted) VALUES (?, ?, ?, ?)`, table)
		if _, err := tx.Exec(projSQL, id, doc, inHLC.WallTime, delInt); err != nil {
			return nil, 0, err
		}
		touchedTables[table] = true
		changed++
	}
	if changed == 0 {
		return nil, 0, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	for t := range touchedTables {
		tables = append(tables, t)
	}
	slices.Sort(tables)
	return tables, changed, nil
}

func protoToHLC(h *protocol.HLC) crdt.HLC {
	if h == nil {
		return crdt.HLC{}
	}
	return crdt.HLC{
		WallTime: h.GetWallTime(),
		Logical:  h.GetLogical(),
		NodeID:   h.GetNodeId(),
	}
}

func syncBucketDigest(b *protocol.PluginSyncBucket) []byte {
	h := sha256.New()
	writeLenStr(h, b.GetPluginId())
	writeLenStr(h, b.GetScope())
	for _, r := range b.GetRows() {
		writeLenStr(h, r.GetTable())
		writeLenStr(h, r.GetId())
		writeLenStr(h, r.GetDoc())
		var buf [13]byte
		binary.LittleEndian.PutUint64(buf[0:8], uint64(r.GetClock().GetWallTime()))
		binary.LittleEndian.PutUint32(buf[8:12], r.GetClock().GetLogical())
		if r.GetDeleted() {
			buf[12] = 1
		}
		h.Write(buf[:])
		writeLenStr(h, r.GetClock().GetNodeId())
	}
	return h.Sum(nil)
}

func syncBundleDigest(buckets []*protocol.PluginSyncBucket) []byte {
	if len(buckets) == 0 {
		return nil
	}
	h := sha256.New()
	for _, b := range buckets {
		writeLenStr(h, b.GetPluginId())
		writeLenStr(h, b.GetScope())
		h.Write(b.GetDigest())
	}
	return h.Sum(nil)
}

func writeLenStr(h interface{ Write([]byte) (int, error) }, s string) {
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(s)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(s))
}

// SyncDigest returns the cached 32-byte SHA-256 digest of all P2P-synced plugin
// SQLite buckets on this node (nil when no plugin has synced rows).
func (m *Manager) SyncDigest() []byte {
	m.syncDigestMu.Lock()
	if m.syncDigestValid {
		out := slices.Clone(m.syncDigest)
		m.syncDigestMu.Unlock()
		return out
	}
	m.syncDigestMu.Unlock()

	bundle := m.ExportSyncBundle()
	return bundle.GetDigest()
}

func (m *Manager) invalidateSyncDigest(notify bool) {
	m.syncDigestMu.Lock()
	m.syncDigestValid = false
	m.syncDigest = nil
	m.syncDigestMu.Unlock()
	if notify && m.cfg.SyncChanged != nil {
		m.cfg.SyncChanged()
	}
}

// ExportSyncBundle gathers all CRDT-synced SQLite buckets from every plugin
// currently granted db:sync on this node.
func (m *Manager) ExportSyncBundle() *protocol.PluginSyncBundle {
	m.mu.Lock()
	ps := make([]*plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		ps = append(ps, p)
	}
	m.mu.Unlock()
	slices.SortFunc(ps, func(a, b *plugin) int { return strings.Compare(a.id, b.id) })

	var buckets []*protocol.PluginSyncBucket
	for _, p := range ps {
		if !p.hasGrant(hostapi.PermDbSync) {
			continue
		}
		buckets = append(buckets, p.db.exportBuckets(p.id)...)
	}
	digest := syncBundleDigest(buckets)
	m.syncDigestMu.Lock()
	m.syncDigest = slices.Clone(digest)
	m.syncDigestValid = true
	m.syncDigestMu.Unlock()
	return &protocol.PluginSyncBundle{Digest: digest, Buckets: buckets}
}

// MergeSyncBundle merges remote CRDT-synced plugin SQLite buckets received from
// a mutually trusted peer. Only plugins installed and granted db:sync on this
// node accept incoming rows; affected plugins receive an on.dbSync event and
// reload any watched blocks in the updated scope.
func (m *Manager) MergeSyncBundle(bundle *protocol.PluginSyncBundle) bool {
	if bundle == nil || len(bundle.GetBuckets()) == 0 {
		return false
	}
	anyChanged := false
	for _, bucket := range bundle.GetBuckets() {
		pid := bucket.GetPluginId()
		if pid == "" || strings.ContainsFunc(pid, unicode.IsSpace) {
			continue
		}
		p, ok := m.plugin(pid)
		if !ok || !p.hasGrant(hostapi.PermDbSync) {
			continue
		}
		tables, count, err := p.db.mergeBucket(pid, bucket)
		if err != nil {
			m.cfg.Log.Warn("plugin db sync merge failed", "plugin", pid, "scope", bucket.GetScope(), "err", err)
			continue
		}
		if count > 0 {
			anyChanged = true
			p.deliverDBSync(bucket.GetScope(), tables, count)
		}
	}
	if anyChanged {
		m.invalidateSyncDigest(true)
	}
	return anyChanged
}

type dbSyncPending struct {
	tables map[string]struct{}
	rows   int
}

// deliverDBSync coalesces merged remote rows per scopeKey and signals the
// plugin's actor via a non-droppable cap-1 channel.
func (p *plugin) deliverDBSync(scopeKey string, tables []string, count int) {
	if count <= 0 {
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	p.dbSyncMu.Lock()
	if p.dbSyncPend == nil {
		p.dbSyncPend = map[string]*dbSyncPending{}
	}
	pend, ok := p.dbSyncPend[scopeKey]
	if !ok {
		pend = &dbSyncPending{tables: map[string]struct{}{}}
		p.dbSyncPend[scopeKey] = pend
	}
	for _, t := range tables {
		if t != "" {
			pend.tables[t] = struct{}{}
		}
	}
	pend.rows += count
	p.dbSyncMu.Unlock()

	select {
	case p.dbSyncCh <- struct{}{}:
	default:
	}
}

func (p *plugin) flushDBSync() {
	p.dbSyncMu.Lock()
	pend := p.dbSyncPend
	p.dbSyncPend = nil
	p.dbSyncMu.Unlock()
	if len(pend) == 0 {
		return
	}
	keys := make([]string, 0, len(pend))
	for k := range pend {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, scopeKey := range keys {
		item := pend[scopeKey]
		if item == nil || item.rows <= 0 {
			continue
		}
		if !p.watched() && p.rt == nil {
			continue
		}
		if err := p.ensure(); err != nil {
			return
		}
		tables := make([]string, 0, len(item.tables))
		for t := range item.tables {
			tables = append(tables, t)
		}
		slices.Sort(tables)
		scopeKind := protocol.ScopeGlobal
		if k, _, err := parseScopeKey(scopeKey); err == nil {
			scopeKind = k
		}
		payload, err := json.Marshal(struct {
			Scope    string   `json:"scope"`
			ScopeKey string   `json:"scopeKey"`
			Tables   []string `json:"tables"`
			Rows     int      `json:"rows"`
		}{
			Scope:    scopeKind,
			ScopeKey: scopeKey,
			Tables:   tables,
			Rows:     item.rows,
		})
		if err != nil {
			continue
		}
		_, _ = p.invoke("dbSyncEvent", false, string(payload))
		p.reloadWatchedScope(scopeKey)
	}
}

// reloadWatchedScope runs load+render for watched block instances matching
// scopeKey (plus global blocks) after remote CRDT rows are merged.
func (p *plugin) reloadWatchedScope(scopeKey string) {
	p.mu.Lock()
	var targets []*instance
	for _, in := range p.inst {
		if in.subs > 0 && (scopeKey == "" || in.scopeKey == scopeKey || in.scopeKey == "") {
			targets = append(targets, in)
		}
	}
	p.mu.Unlock()
	slices.SortFunc(targets, func(a, b *instance) int { return strings.Compare(a.key, b.key) })
	for _, in := range targets {
		if p.rt == nil {
			return
		}
		p.load(in)
		p.render(in.key)
	}
}
