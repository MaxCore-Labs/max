package sqlite

import (
	"context"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) ListQueuedBacklogStats(ctx context.Context) ([]app.QueuedBacklogRow, error) {
	rows, err := r.q.QueryContext(ctx, `
		WITH prio_stats AS (
			SELECT priority, COUNT(*) AS cnt, MIN(created_at) AS min_ts
			FROM wakeups
			WHERE status = 'queued'
			GROUP BY priority
		),
		oldest_wake AS (
			SELECT w.priority, w.agent_id,
			       ROW_NUMBER() OVER (PARTITION BY w.priority ORDER BY w.created_at ASC, w.id ASC) AS rn
			FROM wakeups w
			WHERE w.status = 'queued'
		)
		SELECT p.priority, p.cnt, p.min_ts,
		       a.status, a.engine_mask, a.cooldown_until, a.active_run_id,
		       tm.running, tm.max_running
		FROM prio_stats p
		JOIN oldest_wake ow ON ow.priority = p.priority AND ow.rn = 1
		JOIN agents a ON a.id = ow.agent_id
		JOIN teams tm ON tm.id = a.team_id
		ORDER BY p.priority DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.QueuedBacklogRow
	for rows.Next() {
		var rec app.QueuedBacklogRow
		var agStatus string
		if err := rows.Scan(
			&rec.Priority, &rec.Count, &rec.OldestCreatedAt,
			&agStatus, &rec.EngineMask, &rec.CooldownUntil, &rec.ActiveRunID,
			&rec.TeamRunning, &rec.TeamMaxRunning,
		); err != nil {
			return nil, err
		}
		rec.AgentStatus = domain.AgentStatus(agStatus)
		out = append(out, rec)
	}
	return out, rows.Err()
}
