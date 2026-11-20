package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/adapter/sqlite/db"
	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) ListQueuedWakeups(ctx context.Context, agentID domain.ID) ([]app.WakeupRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, agent_id, conversation_id, kind, priority, payload, idempotency_key,
		       status, run_id, coalesced_count, attempts, cause_run_id, hops, not_before, created_at
		FROM wakeups
		WHERE agent_id = ? AND status = 'queued'
		ORDER BY priority DESC, created_at ASC, id ASC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list queued wakeups for %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []app.WakeupRecord
	for rows.Next() {
		var (
			w    app.WakeupRecord
			kind string
			prio int
		)
		if err := rows.Scan(
			&w.ID, &w.AgentID, &w.ConversationID, &kind, &prio, &w.Payload, &w.IdempotencyKey,
			&w.Status, &w.RunID, &w.CoalescedCount, &w.Attempts, &w.CauseRunID, &w.Hops, &w.NotBefore, &w.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan wakeup: %w", err)
		}
		w.Kind = domain.WakeKind(kind)
		w.Priority = domain.Priority(prio)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (t *sqliteTx) InsertConversation(ctx context.Context, c app.ConversationRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO conversations (
			id, agent_id, task_id, thread_id, sessions, last_engine, waiting_for, turns_used, closed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.AgentID, c.TaskID, c.ThreadID, defaultJSON(c.Sessions, `{}`),
		c.LastEngine, c.WaitingFor, c.TurnsUsed, c.ClosedAt,
	)
	if err != nil {
		return fmt.Errorf("insert conversation %q: %w", c.ID, err)
	}
	return nil
}

func (t *sqliteTx) SetAgentActiveRun(ctx context.Context, agentID domain.ID, runID *domain.ID) error {
	_, err := t.q.ExecContext(ctx, `UPDATE agents SET active_run_id = ? WHERE id = ?`, runID, agentID)
	if err != nil {
		return fmt.Errorf("set agent active_run_id: %w", err)
	}
	return nil
}

func (t *sqliteTx) FindWakeupByIdemKey(ctx context.Context, idemKey string) (domain.ID, bool, error) {
	id, err := t.sqlc.FindActiveWakeupByIdem(ctx, sql.NullString{String: idemKey, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find wakeup by idempotency_key: %w", err)
	}
	return id, true, nil
}

func (t *sqliteTx) CoalesceQueuedWakeup(
	ctx context.Context,
	agentID domain.ID,
	convID *domain.ID,
	kind domain.WakeKind,
	prio domain.Priority,
	payload string,
	notBefore int64,
) (domain.ID, bool, error) {
	var existingID string
	err := t.q.QueryRowContext(ctx, `
		SELECT id FROM wakeups
		WHERE agent_id = ? AND status = 'queued' AND kind = ? AND (conversation_id IS ?)
		ORDER BY created_at ASC, id ASC
		LIMIT 1`,
		agentID, string(kind), convID,
	).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find coalescible wakeup: %w", err)
	}

	_, err = t.q.ExecContext(ctx, `
		UPDATE wakeups
		SET coalesced_count = coalesced_count + 1,
		    priority = MAX(priority, ?),
		    payload = ?,
		    not_before = CASE WHEN not_before <= ? THEN not_before ELSE ? END
		WHERE agent_id = ? AND id = ?`,
		int(prio), defaultJSON(payload, `{}`), notBefore, notBefore, agentID, existingID,
	)
	if err != nil {
		return "", false, fmt.Errorf("coalesce wakeup %q: %w", existingID, err)
	}
	return existingID, true, nil
}

func (t *sqliteTx) CountQueuedWakeups(ctx context.Context, agentID domain.ID) (int, error) {
	n, err := t.sqlc.CountAgentQueuedWakeups(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("count queued wakeups: %w", err)
	}
	return int(n), nil
}

func (t *sqliteTx) RecomputeAgentReady(ctx context.Context, agentID domain.ID, nowMs int64) error {
	var (
		topPrio   int64
		topCreate int64
	)
	err := t.q.QueryRowContext(ctx, `
		SELECT priority, created_at FROM wakeups
		WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
		ORDER BY priority DESC, created_at ASC, id ASC
		LIMIT 1`,
		agentID, nowMs,
	).Scan(&topPrio, &topCreate)
	if errors.Is(err, sql.ErrNoRows) {
		return t.sqlc.SetAgentReadyState(ctx, db.SetAgentReadyStateParams{
			Ready:         0,
			ReadyPriority: 0,
			ReadySince:    sql.NullInt64{},
			ID:            agentID,
		})
	}
	if err != nil {
		return fmt.Errorf("query top ready wakeup: %w", err)
	}
	var lastRunAt int64
	_ = t.q.QueryRowContext(ctx, `SELECT COALESCE(last_run_at, 0) FROM agents WHERE id = ?`, agentID).Scan(&lastRunAt)
	fairSince := max(topCreate, lastRunAt)
	return t.sqlc.SetAgentReadyState(ctx, db.SetAgentReadyStateParams{
		Ready:         1,
		ReadyPriority: topPrio,
		ReadySince:    sql.NullInt64{Int64: fairSince, Valid: true},
		ID:            agentID,
	})
}
