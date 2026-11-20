package sqlite

import (
	"context"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (t *sqliteTx) DrainDueWakeupsForConv(
	ctx context.Context,
	agentID domain.ID,
	origConvID *domain.ID,
	targetConvID, runID domain.ID,
	nowMs int64,
	limit int,
) ([]app.WakeupRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := t.q.QueryContext(ctx, `
		SELECT id, agent_id, conversation_id, kind, priority, payload, idempotency_key,
		       status, run_id, coalesced_count, attempts, cause_run_id, hops, not_before, created_at
		FROM wakeups
		WHERE agent_id = ? AND status = 'queued' AND not_before <= ? AND (conversation_id IS ?)
		ORDER BY priority DESC, created_at ASC, id ASC
		LIMIT ?`, agentID, nowMs, origConvID, limit)
	if err != nil {
		return nil, fmt.Errorf("select wakeups to drain: %w", err)
	}
	var drained []app.WakeupRecord
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
			_ = rows.Close()
			return nil, fmt.Errorf("scan drained wakeup: %w", err)
		}
		w.Kind = domain.WakeKind(kind)
		w.Priority = domain.Priority(prio)
		drained = append(drained, w)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range drained {
		w := &drained[i]
		_, err := t.q.ExecContext(ctx, `
			UPDATE wakeups
			SET status = 'claimed',
			    run_id = ?,
			    attempts = attempts + 1,
			    conversation_id = ?
			WHERE agent_id = ? AND id = ?`,
			runID, targetConvID, agentID, w.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("mark wakeup %q claimed: %w", w.ID, err)
		}
		w.Status = "claimed"
		w.RunID = &runID
		w.Attempts++
		w.ConversationID = &targetConvID
	}
	return drained, nil
}

func (t *sqliteTx) InsertInboxItem(ctx context.Context, item app.InboxItemRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO inbox_items (
			id, kind, title, body, ref_kind, ref_id, conversation_id, agent_id,
			choices, urgency, dedupe_key, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) DO NOTHING`,
		item.ID, item.Kind, item.Title, item.Body, item.RefKind, item.RefID,
		item.ConversationID, item.AgentID, defaultJSON(item.ChoicesJSON, `[]`),
		defaultJSON(item.Urgency, "normal"), item.DedupeKey, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert inbox item: %w", err)
	}
	return nil
}

func (t *sqliteTx) RecoverCrashState(ctx context.Context, nowMs int64, graceful bool, newID func() domain.ID) (int, error) {
	res, err := t.q.ExecContext(ctx, `
		UPDATE runs
		SET status = 'interrupted', ended_at = ?
		WHERE status = 'running' AND ended_at IS NULL`, nowMs)
	if err != nil {
		return 0, fmt.Errorf("mark active runs interrupted: %w", err)
	}
	interrupted64, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected for interrupted runs: %w", err)
	}

	if _, err := t.q.ExecContext(ctx, `
		UPDATE tasks SET checkout_run_id = NULL
		WHERE checkout_run_id IN (SELECT id FROM runs WHERE status = 'interrupted' AND ended_at = ?)`, nowMs); err != nil {
		return 0, fmt.Errorf("clear task checkouts on recover: %w", err)
	}

	rows, err := t.q.QueryContext(ctx, `
		SELECT id, agent_id, conversation_id, attempts
		FROM wakeups WHERE status = 'claimed'`)
	if err != nil {
		return 0, fmt.Errorf("query claimed wakeups: %w", err)
	}
	type claimedRow struct {
		id      string
		agentID string
		convID  *string
		attempt int
	}
	var claimed []claimedRow
	for rows.Next() {
		var c claimedRow
		if err := rows.Scan(&c.id, &c.agentID, &c.convID, &c.attempt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		claimed = append(claimed, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	affectedAgents := make(map[string]struct{})
	for _, c := range claimed {
		affectedAgents[c.agentID] = struct{}{}
		effAttempts := c.attempt
		if graceful && effAttempts > 0 {
			effAttempts--
		}
		if !graceful && effAttempts >= 3 {
			if _, err := t.q.ExecContext(ctx, `
				UPDATE wakeups SET status = 'dropped', run_id = NULL
				WHERE agent_id = ? AND id = ?`, c.agentID, c.id); err != nil {
				return 0, fmt.Errorf("drop poisoned wakeup %q: %w", c.id, err)
			}
			refKind := "wakeup"
			refID := c.id
			agID := c.agentID
			dedupe := "wakeup-dropped:" + c.id
			if err := t.InsertInboxItem(ctx, app.InboxItemRecord{
				ID:             newID(),
				Kind:           "error",
				Title:          "Wakeup bị bỏ sau 3 lần lỗi liên tiếp",
				Body:           fmt.Sprintf("Wakeup %s của agent %s đã thất bại 3 lần liên tiếp.", c.id, c.agentID),
				RefKind:        &refKind,
				RefID:          &refID,
				ConversationID: c.convID,
				AgentID:        &agID,
				Urgency:        "high",
				DedupeKey:      &dedupe,
				CreatedAt:      nowMs,
			}); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := t.q.ExecContext(ctx, `
			UPDATE wakeups SET status = 'queued', run_id = NULL, attempts = ?
			WHERE agent_id = ? AND id = ?`, effAttempts, c.agentID, c.id); err != nil {
			return 0, fmt.Errorf("requeue claimed wakeup %q: %w", c.id, err)
		}
	}

	agRows, err := t.q.QueryContext(ctx, `SELECT id FROM agents WHERE active_run_id IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("list agents with active_run_id: %w", err)
	}
	for agRows.Next() {
		var id string
		if err := agRows.Scan(&id); err != nil {
			_ = agRows.Close()
			return 0, err
		}
		affectedAgents[id] = struct{}{}
	}
	_ = agRows.Close()

	if _, err := t.q.ExecContext(ctx, `UPDATE agents SET active_run_id = NULL WHERE active_run_id IS NOT NULL`); err != nil {
		return 0, fmt.Errorf("clear agents active_run_id: %w", err)
	}
	if _, err := t.q.ExecContext(ctx, `UPDATE teams SET running = 0 WHERE running != 0`); err != nil {
		return 0, fmt.Errorf("reset teams running: %w", err)
	}
	for agentID := range affectedAgents {
		if err := t.RecomputeAgentReady(ctx, agentID, nowMs); err != nil {
			return 0, err
		}
	}
	return int(interrupted64), nil
}
