package sqlite

import (
	"context"
	"fmt"

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
