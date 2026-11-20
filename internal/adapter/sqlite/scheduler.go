package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) CountFutureQuotaLedgerRows(ctx context.Context, afterMs int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE window_start > ?`, afterMs).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count future quota_ledger rows: %w", err)
	}
	return n, nil
}

func (t *sqliteTx) PromoteDueWakeups(ctx context.Context, nowMs int64) (int, error) {
	// Must include not_before > 0 so SQLite uses the wakeups_due partial index (S4).
	rows, err := t.q.QueryContext(ctx, `
		SELECT DISTINCT agent_id FROM wakeups
		WHERE status = 'queued' AND not_before > 0 AND not_before <= ?`, nowMs)
	if err != nil {
		return 0, fmt.Errorf("select due wakeup agents: %w", err)
	}
	var agentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		agentIDs = append(agentIDs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(agentIDs) == 0 {
		return 0, nil
	}

	res, err := t.q.ExecContext(ctx, `
		UPDATE wakeups SET not_before = 0
		WHERE status = 'queued' AND not_before > 0 AND not_before <= ?`, nowMs)
	if err != nil {
		return 0, fmt.Errorf("promote due wakeups: %w", err)
	}
	promoted, _ := res.RowsAffected()

	for _, id := range agentIDs {
		if err := t.RecomputeAgentReady(ctx, id, nowMs); err != nil {
			return 0, err
		}
	}
	return int(promoted), nil
}

func (t *sqliteTx) ReconcileTeamRunning(ctx context.Context) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE teams
		SET running = (
			SELECT COUNT(*)
			FROM runs r
			JOIN agents a ON a.id = r.agent_id
			WHERE a.team_id = teams.id AND r.status = 'running' AND r.ended_at IS NULL
		)`)
	if err != nil {
		return fmt.Errorf("reconcile teams.running: %w", err)
	}
	return nil
}

func (t *sqliteTx) PruneTerminalWakeupsBefore(ctx context.Context, cutoffMs int64, limit int) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	res, err := t.q.ExecContext(ctx, `
		DELETE FROM wakeups
		WHERE (agent_id, id) IN (
			SELECT agent_id, id FROM wakeups
			WHERE status IN ('done', 'dropped') AND created_at < ?
			LIMIT ?
		)`, cutoffMs, limit)
	if err != nil {
		return 0, fmt.Errorf("prune terminal wakeups: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (t *sqliteTx) SetTeamRunningRaw(ctx context.Context, teamID domain.ID, running int) error {
	_, err := t.q.ExecContext(ctx, `UPDATE teams SET running = ? WHERE id = ?`, running, teamID)
	if err != nil {
		return fmt.Errorf("set team running raw: %w", err)
	}
	return nil
}

func (r *sqliteReader) GetRoutineState(ctx context.Context, slug string) (app.RoutineStateRecord, bool, error) {
	var (
		st                      app.RoutineStateRecord
		autoDisabled, badReport int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT slug, last_scheduled_min, last_fired_at, active_task_id,
		       consecutive_failures, auto_disabled, bad_file_reported, updated_at
		FROM routine_state WHERE slug = ?`, slug,
	).Scan(
		&st.Slug, &st.LastScheduledMin, &st.LastFiredAt, &st.ActiveTaskID,
		&st.ConsecutiveFailures, &autoDisabled, &badReport, &st.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.RoutineStateRecord{}, false, nil
	}
	if err != nil {
		return app.RoutineStateRecord{}, false, fmt.Errorf("get routine_state %q: %w", slug, err)
	}
	st.AutoDisabled = autoDisabled != 0
	st.BadFileReported = badReport != 0
	return st, true, nil
}

func (r *sqliteReader) ListDueHeartbeatAgents(ctx context.Context, nowMs int64, limit int) ([]app.DueHeartbeatAgent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT a.id, a.slug, COALESCE(k.value, ''), a.last_run_at
		FROM agents a
		LEFT JOIN kv k ON k.key = 'hb_cron:' || a.id
		WHERE a.next_heartbeat_at IS NOT NULL
		  AND a.next_heartbeat_at <= ?
		  AND a.status = 'active'
		ORDER BY a.next_heartbeat_at ASC
		LIMIT ?`, nowMs, limit)
	if err != nil {
		return nil, fmt.Errorf("list due heartbeat agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.DueHeartbeatAgent
	for rows.Next() {
		var d app.DueHeartbeatAgent
		if err := rows.Scan(&d.ID, &d.Slug, &d.Heartbeat, &d.LastRunAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *sqliteReader) HasHeartbeatWork(ctx context.Context, agentID domain.ID, lastRunAt *int64) (bool, error) {
	if lastRunAt == nil {
		return true, nil
	}
	var count int
	if err := r.q.QueryRowContext(ctx, `
		SELECT (
			(SELECT COUNT(*) FROM tasks WHERE (assignee_agent_id = ? OR created_by_agent = ?) AND status NOT IN ('done', 'cancelled')) +
			(SELECT COUNT(*) FROM inbox_items WHERE resolved_at IS NULL) +
			(SELECT COUNT(*) FROM tasks WHERE updated_at >= ?)
		)`, agentID, agentID, *lastRunAt,
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (t *sqliteTx) UpsertRoutineState(ctx context.Context, st app.RoutineStateRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO routine_state (
			slug, last_scheduled_min, last_fired_at, active_task_id,
			consecutive_failures, auto_disabled, bad_file_reported, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			last_scheduled_min = excluded.last_scheduled_min,
			last_fired_at = excluded.last_fired_at,
			active_task_id = excluded.active_task_id,
			consecutive_failures = excluded.consecutive_failures,
			auto_disabled = excluded.auto_disabled,
			bad_file_reported = excluded.bad_file_reported,
			updated_at = excluded.updated_at`,
		st.Slug, st.LastScheduledMin, st.LastFiredAt, st.ActiveTaskID,
		st.ConsecutiveFailures, boolInt(st.AutoDisabled), boolInt(st.BadFileReported), st.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert routine_state %q: %w", st.Slug, err)
	}
	return nil
}

func (t *sqliteTx) SetAgentHeartbeatSchedule(ctx context.Context, agentID domain.ID, cronExpr string, nextAtMs *int64) error {
	if _, err := t.q.ExecContext(ctx, `UPDATE agents SET next_heartbeat_at = ? WHERE id = ?`, nextAtMs, agentID); err != nil {
		return fmt.Errorf("set agent next_heartbeat_at: %w", err)
	}
	if cronExpr != "" {
		return t.UpsertKV(ctx, "hb_cron:"+agentID, cronExpr, 0)
	}
	return nil
}
