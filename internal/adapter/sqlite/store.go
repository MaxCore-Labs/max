package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"max/internal/adapter/sqlite/db"
	"max/internal/app"
	"max/internal/domain"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

type sqlRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store implements app.Store using a single-writer + 8-reader SQLite WAL pool (Spec 9 & S4).
type Store struct {
	wdb *sql.DB
	rdb *sql.DB
}

// Open initializes or opens a SQLite WAL database at path and applies embedded goose migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir sqlite dir: %w", err)
	}
	uriPath := escapeSQLiteURIPath(filepath.ToSlash(path))
	common := "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=mmap_size(536870912)"

	wDSN := fmt.Sprintf("file:%s?%s&_pragma=cache_size(-131072)&_pragma=wal_autocheckpoint(16000)&_txlock=immediate", uriPath, common)
	wdb, err := sql.Open("sqlite", wDSN)
	if err != nil {
		return nil, fmt.Errorf("open writer sqlite: %w", err)
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)

	if err := runMigrations(ctx, wdb); err != nil {
		_ = wdb.Close()
		return nil, err
	}

	rDSN := fmt.Sprintf("file:%s?%s&_pragma=cache_size(-4096)&_pragma=query_only(1)", uriPath, common)
	rdb, err := sql.Open("sqlite", rDSN)
	if err != nil {
		_ = wdb.Close()
		return nil, fmt.Errorf("open reader sqlite: %w", err)
	}
	rdb.SetMaxOpenConns(8)
	rdb.SetMaxIdleConns(8)

	if err := rdb.PingContext(ctx); err != nil {
		_ = rdb.Close()
		_ = wdb.Close()
		return nil, fmt.Errorf("ping reader sqlite: %w", err)
	}

	return &Store{wdb: wdb, rdb: rdb}, nil
}

func runMigrations(ctx context.Context, wdb *sql.DB) error {
	subFS, err := fs.Sub(embedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("sub migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		wdb,
		subFS,
		goose.WithVerbose(false),
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	if _, err := wdb.ExecContext(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("analyze sqlite: %w", err)
	}
	return nil
}

func escapeSQLiteURIPath(p string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return r.Replace(p)
}

// Close closes reader and writer pools cleanly.
func (s *Store) Close() error {
	rErr := s.rdb.Close()
	wErr := s.wdb.Close()
	return errors.Join(rErr, wErr)
}

// Pragmas returns active journal_mode and foreign_keys on the writer connection.
func (s *Store) Pragmas(ctx context.Context) (journalMode string, foreignKeys int, err error) {
	if err := s.wdb.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return "", 0, fmt.Errorf("pragma journal_mode: %w", err)
	}
	if err := s.wdb.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return "", 0, fmt.Errorf("pragma foreign_keys: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(journalMode)), foreignKeys, nil
}

// ListTables returns all user table names present in sqlite_master.
func (s *Store) ListTables(ctx context.Context) ([]string, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Read returns a non-transactional Reader over the reader pool.
func (s *Store) Read() app.Reader {
	return &sqliteReader{q: s.rdb, sqlc: db.New(s.rdb)}
}

// WithTx executes fn inside a single BEGIN IMMEDIATE transaction on the single writer connection.
func (s *Store) WithTx(ctx context.Context, fn func(tx app.Tx) error) error {
	sqlTx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	wrapped := &sqliteTx{
		sqliteReader: sqliteReader{q: sqlTx, sqlc: db.New(sqlTx)},
		tx:           sqlTx,
	}
	if err := fn(wrapped); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

type sqliteReader struct {
	q    sqlRunner
	sqlc *db.Queries
}

func (r *sqliteReader) GetKV(ctx context.Context, key string) (string, bool, error) {
	v, err := r.sqlc.GetKV(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get kv %q: %w", key, err)
	}
	return v, true, nil
}

func (r *sqliteReader) GetAgent(ctx context.Context, id domain.ID) (app.AgentRecord, bool, error) {
	var (
		a                        app.AgentRecord
		autonomy, browser, state string
		allowUngated, web, shell int
		ready                    int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, slug, name, template_slug, team_id, manager_agent_id,
		       engines, engine_mask, allow_ungated_engine, models, scope_violations,
		       autonomy, web, browser, shell, skills, params, quota_tokens_day,
		       config_rev, status, ready, ready_priority, ready_since,
		       active_run_id, cooldown_until, last_run_at, next_heartbeat_at
		FROM agents WHERE id = ?`, id).Scan(
		&a.ID, &a.Slug, &a.Name, &a.TemplateSlug, &a.TeamID, &a.ManagerAgentID,
		&a.EnginesJSON, &a.EngineMask, &allowUngated, &a.ModelsJSON, &a.ScopeViolations,
		&autonomy, &web, &browser, &shell, &a.SkillsJSON, &a.ParamsJSON, &a.QuotaTokensDay,
		&a.ConfigRev, &state, &ready, &a.ReadyPriority, &a.ReadySince,
		&a.ActiveRunID, &a.CooldownUntil, &a.LastRunAt, &a.NextHeartbeatAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.AgentRecord{}, false, nil
	}
	if err != nil {
		return app.AgentRecord{}, false, fmt.Errorf("get agent %q: %w", id, err)
	}
	a.AllowUngatedEngine = allowUngated != 0
	a.Autonomy = domain.Autonomy(autonomy)
	a.Web = web != 0
	a.Browser = domain.BrowserProfile(browser)
	a.Shell = shell != 0
	a.Status = domain.AgentStatus(state)
	a.Ready = ready != 0
	return a, true, nil
}

type sqliteTx struct {
	sqliteReader
	tx *sql.Tx
}

var _ io.Closer = (*Store)(nil)

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defaultJSON(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func (t *sqliteTx) UpsertKV(ctx context.Context, key, val string, updatedAt int64) error {
	return t.sqlc.UpsertKV(ctx, db.UpsertKVParams{
		Key:       key,
		Value:     val,
		UpdatedAt: updatedAt,
	})
}

func (t *sqliteTx) InsertTeam(ctx context.Context, tm app.TeamRecord) error {
	status := defaultJSON(tm.Status, "active")
	maxRun := tm.MaxRunning
	if maxRun <= 0 {
		maxRun = 2
	}
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO teams (id, slug, name, mandate, max_running, running, quota_tokens_day, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tm.ID, tm.Slug, tm.Name, defaultJSON(tm.MandateJSON, "{}"), maxRun, tm.Running, tm.QuotaTokensDay, status,
	)
	if err != nil {
		return fmt.Errorf("insert team %q: %w", tm.Slug, err)
	}
	return nil
}

func (t *sqliteTx) InsertAgent(ctx context.Context, a app.AgentRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO agents (
			id, slug, name, template_slug, team_id, manager_agent_id,
			engines, engine_mask, allow_ungated_engine, models, scope_violations,
			autonomy, web, browser, shell, skills, params, quota_tokens_day,
			config_rev, status, ready, ready_priority, ready_since,
			active_run_id, cooldown_until, last_run_at, next_heartbeat_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Slug, a.Name, a.TemplateSlug, a.TeamID, a.ManagerAgentID,
		defaultJSON(a.EnginesJSON, `["agy"]`), a.EngineMask, boolInt(a.AllowUngatedEngine),
		defaultJSON(a.ModelsJSON, `{}`), a.ScopeViolations,
		string(a.Autonomy), boolInt(a.Web), string(a.Browser), boolInt(a.Shell),
		defaultJSON(a.SkillsJSON, `[]`), defaultJSON(a.ParamsJSON, `{}`), a.QuotaTokensDay,
		a.ConfigRev, string(a.Status), boolInt(a.Ready), a.ReadyPriority, a.ReadySince,
		a.ActiveRunID, a.CooldownUntil, a.LastRunAt, a.NextHeartbeatAt,
	)
	if err != nil {
		return fmt.Errorf("insert agent %q: %w", a.Slug, err)
	}
	return nil
}

func (t *sqliteTx) InsertWakeupRaw(ctx context.Context, w app.WakeupRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO wakeups (
			id, agent_id, conversation_id, kind, priority, payload, idempotency_key,
			status, run_id, coalesced_count, attempts, cause_run_id, hops, not_before, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.AgentID, w.ConversationID, string(w.Kind), int(w.Priority),
		defaultJSON(w.Payload, `{}`), w.IdempotencyKey, defaultJSON(w.Status, "queued"),
		w.RunID, w.CoalescedCount, w.Attempts, w.CauseRunID, w.Hops, w.NotBefore, w.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert wakeup %q: %w", w.ID, err)
	}
	return nil
}
