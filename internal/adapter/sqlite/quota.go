package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetQuotaWindowTokens(ctx context.Context, engine domain.EngineName, window string, windowStartMs int64) (int64, int, error) {
	var tokens int64
	var runs int
	err := r.q.QueryRowContext(ctx, `
		SELECT tokens, runs FROM quota_ledger
		WHERE engine = ? AND window = ? AND window_start = ?`,
		string(engine), window, windowStartMs,
	).Scan(&tokens, &runs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("get quota_ledger (%s, %s): %w", engine, window, err)
	}
	return tokens, runs, nil
}

func (r *sqliteReader) CountQuotaLedgerRows(ctx context.Context, engine domain.EngineName, window string) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM quota_ledger WHERE engine = ? AND window = ?`,
		string(engine), window,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count quota_ledger rows: %w", err)
	}
	return n, nil
}

func (t *sqliteTx) AddQuotaLedgerUsage(ctx context.Context, engine domain.EngineName, window string, windowStartMs, tokens int64) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO quota_ledger (engine, window, window_start, tokens, runs)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(engine, window, window_start) DO UPDATE SET
			tokens = quota_ledger.tokens + excluded.tokens,
			runs = quota_ledger.runs + 1`,
		string(engine), window, windowStartMs, tokens,
	)
	if err != nil {
		return fmt.Errorf("add quota_ledger usage: %w", err)
	}
	return nil
}

func (t *sqliteTx) AddDailyUsage(ctx context.Context, day string, agentID, teamID domain.ID, engine domain.EngineName, tokens int64) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO usage_daily (day, agent_id, team_id, engine, runs, tokens)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(day, agent_id, engine) DO UPDATE SET
			runs = usage_daily.runs + 1,
			tokens = usage_daily.tokens + excluded.tokens`,
		day, agentID, teamID, string(engine), tokens,
	)
	if err != nil {
		return fmt.Errorf("add usage_daily: %w", err)
	}
	return nil
}

func (t *sqliteTx) SaveLearnedQuotaState(ctx context.Context, qs app.QuotaStateRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO quota_state (engine, est_short_tokens, est_week_tokens, limited_until, last_limit_at, probe_attempts)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(engine) DO UPDATE SET
			est_short_tokens = COALESCE(excluded.est_short_tokens, quota_state.est_short_tokens),
			est_week_tokens = COALESCE(excluded.est_week_tokens, quota_state.est_week_tokens),
			limited_until = excluded.limited_until,
			last_limit_at = COALESCE(excluded.last_limit_at, quota_state.last_limit_at),
			probe_attempts = excluded.probe_attempts`,
		string(qs.Engine), qs.EstShortTokens, qs.EstWeekTokens, qs.LimitedUntil, qs.LastLimitAt, qs.ProbeAttempts,
	)
	if err != nil {
		return fmt.Errorf("save learned quota_state %q: %w", qs.Engine, err)
	}
	return nil
}
