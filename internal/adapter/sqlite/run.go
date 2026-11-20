package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetTask(ctx context.Context, id domain.ID) (app.TaskRecord, bool, error) {
	var (
		tk          app.TaskRecord
		origin      string
		needsReview int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, goal_id, parent_id, root_id, root_origin, depth, title, description, done_criteria,
		       status, blocked_reason, assignee_agent_id, created_by_agent, checkout_run_id,
		       needs_review, reviewer_agent_id, review_rounds, idle_runs, due_at, created_at, updated_at
		FROM tasks WHERE id = ?`, id,
	).Scan(
		&tk.ID, &tk.GoalID, &tk.ParentID, &tk.RootID, &origin, &tk.Depth, &tk.Title, &tk.Description, &tk.DoneCriteria,
		&tk.Status, &tk.BlockedReason, &tk.AssigneeAgentID, &tk.CreatedByAgent, &tk.CheckoutRunID,
		&needsReview, &tk.ReviewerAgentID, &tk.ReviewRounds, &tk.IdleRuns, &tk.DueAt, &tk.CreatedAt, &tk.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.TaskRecord{}, false, nil
	}
	if err != nil {
		return app.TaskRecord{}, false, fmt.Errorf("get task %q: %w", id, err)
	}
	tk.RootOrigin = domain.RootOrigin(origin)
	tk.NeedsReview = needsReview != 0
	return tk, true, nil
}

func (r *sqliteReader) GetLastFinishedRunForConv(ctx context.Context, convID domain.ID) (app.RunRecord, bool, error) {
	var (
		rec             app.RunRecord
		status, engName string
		prio            int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, conversation_id, task_id, status, priority, engine, model,
		       pgid, attempt, turns, input_tokens, output_tokens, cached_read_tokens,
		       thought_tokens, notional_microusd, touched_paths, error, summary, vault_commit,
		       started_at, ended_at
		FROM runs
		WHERE conversation_id = ? AND ended_at IS NOT NULL
		ORDER BY ended_at DESC, started_at DESC, id DESC
		LIMIT 1`, convID,
	).Scan(
		&rec.ID, &rec.AgentID, &rec.ConversationID, &rec.TaskID, &status, &prio, &engName, &rec.Model,
		&rec.PGID, &rec.Attempt, &rec.Turns, &rec.InputTokens, &rec.OutputTokens, &rec.CachedReadTokens,
		&rec.ThoughtTokens, &rec.NotionalMicroUSD, &rec.TouchedPathsJSON, &rec.Error, &rec.Summary, &rec.VaultCommit,
		&rec.StartedAt, &rec.EndedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.RunRecord{}, false, nil
	}
	if err != nil {
		return app.RunRecord{}, false, fmt.Errorf("get last finished run for conv %q: %w", convID, err)
	}
	rec.Status = domain.RunStatus(status)
	rec.Priority = domain.Priority(prio)
	rec.Engine = domain.EngineName(engName)
	return rec, true, nil
}

func (r *sqliteReader) GetQuotaState(ctx context.Context, engine domain.EngineName) (app.QuotaStateRecord, bool, error) {
	var (
		qs  app.QuotaStateRecord
		eng string
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT engine, est_short_tokens, est_week_tokens, limited_until, last_limit_at, probe_attempts
		FROM quota_state WHERE engine = ?`, string(engine),
	).Scan(&eng, &qs.EstShortTokens, &qs.EstWeekTokens, &qs.LimitedUntil, &qs.LastLimitAt, &qs.ProbeAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return app.QuotaStateRecord{}, false, nil
	}
	if err != nil {
		return app.QuotaStateRecord{}, false, fmt.Errorf("get quota_state %q: %w", engine, err)
	}
	qs.Engine = domain.EngineName(eng)
	return qs, true, nil
}

func (r *sqliteReader) ListClaimedWakeupsByRun(ctx context.Context, runID domain.ID) ([]app.WakeupRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, agent_id, conversation_id, kind, priority, payload, idempotency_key,
		       status, run_id, coalesced_count, attempts, cause_run_id, hops, not_before, created_at
		FROM wakeups WHERE run_id = ? AND status = 'claimed'`, runID)
	if err != nil {
		return nil, fmt.Errorf("list claimed wakeups for run %q: %w", runID, err)
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
			return nil, err
		}
		w.Kind = domain.WakeKind(kind)
		w.Priority = domain.Priority(prio)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListRunEvents(ctx context.Context, runID domain.ID) ([]app.RunEventRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, run_id, agent_id, type, data, created_at
		FROM run_events WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run events for %q: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.RunEventRecord
	for rows.Next() {
		var ev app.RunEventRecord
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.AgentID, &ev.Type, &ev.DataJSON, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (t *sqliteTx) SetAgentLastRunAt(ctx context.Context, agentID domain.ID, lastRunAt int64) error {
	_, err := t.q.ExecContext(ctx, `UPDATE agents SET last_run_at = ? WHERE id = ?`, lastRunAt, agentID)
	if err != nil {
		return fmt.Errorf("set agent last_run_at: %w", err)
	}
	return nil
}

func (t *sqliteTx) UpdateConversationAfterRun(
	ctx context.Context,
	convID domain.ID,
	sessionsJSON string,
	lastEngine *string,
	addTurns int,
) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE conversations
		SET sessions = ?,
		    last_engine = COALESCE(?, last_engine),
		    turns_used = turns_used + ?
		WHERE id = ?`,
		defaultJSON(sessionsJSON, `{}`), lastEngine, addTurns, convID,
	)
	if err != nil {
		return fmt.Errorf("update conversation after run %q: %w", convID, err)
	}
	return nil
}

func (t *sqliteTx) InsertTask(ctx context.Context, tk app.TaskRecord) error {
	rootID := tk.RootID
	if rootID == "" {
		rootID = tk.ID
	}
	origin := string(tk.RootOrigin)
	if origin == "" {
		origin = string(domain.OriginOwner)
	}
	status := defaultJSON(tk.Status, "todo")
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO tasks (
			id, goal_id, parent_id, root_id, root_origin, depth, title, description, done_criteria,
			status, blocked_reason, assignee_agent_id, created_by_agent, checkout_run_id,
			needs_review, reviewer_agent_id, review_rounds, idle_runs, due_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tk.ID, tk.GoalID, tk.ParentID, rootID, origin, tk.Depth, tk.Title, tk.Description, tk.DoneCriteria,
		status, tk.BlockedReason, tk.AssigneeAgentID, tk.CreatedByAgent, tk.CheckoutRunID,
		boolInt(tk.NeedsReview), tk.ReviewerAgentID, tk.ReviewRounds, tk.IdleRuns, tk.DueAt, tk.CreatedAt, tk.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert task %q: %w", tk.ID, err)
	}
	return nil
}

func (t *sqliteTx) ClearTaskCheckout(ctx context.Context, runID domain.ID, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE tasks SET checkout_run_id = NULL, updated_at = ?
		WHERE checkout_run_id = ?`, updatedAt, runID)
	if err != nil {
		return fmt.Errorf("clear task checkout for run %q: %w", runID, err)
	}
	return nil
}

func (t *sqliteTx) BlockTask(ctx context.Context, taskID domain.ID, reason string, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE tasks SET status = 'blocked', blocked_reason = ?, checkout_run_id = NULL, updated_at = ?
		WHERE id = ?`, reason, updatedAt, taskID)
	if err != nil {
		return fmt.Errorf("block task %q: %w", taskID, err)
	}
	return nil
}

func (t *sqliteTx) UpdateRunFinal(ctx context.Context, r app.RunRecord) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE runs
		SET status = ?,
		    turns = ?,
		    input_tokens = ?,
		    output_tokens = ?,
		    touched_paths = ?,
		    error = ?,
		    summary = ?,
		    vault_commit = ?,
		    ended_at = ?
		WHERE id = ?`,
		string(r.Status), r.Turns, r.InputTokens, r.OutputTokens,
		defaultJSON(r.TouchedPathsJSON, `[]`), r.Error, r.Summary, r.VaultCommit, r.EndedAt, r.ID,
	)
	if err != nil {
		return fmt.Errorf("update run final %q: %w", r.ID, err)
	}
	return nil
}

func (t *sqliteTx) InsertRunEvent(ctx context.Context, ev app.RunEventRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO run_events (run_id, agent_id, type, data, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		ev.RunID, ev.AgentID, ev.Type, defaultJSON(ev.DataJSON, `{}`), ev.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert run event: %w", err)
	}
	return nil
}

func (t *sqliteTx) FinishWakeupsForRun(ctx context.Context, runID domain.ID, status string) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE wakeups
		SET status = ?, run_id = CASE WHEN ? = 'dropped' THEN NULL ELSE run_id END
		WHERE run_id = ? AND status = 'claimed'`, status, status, runID)
	if err != nil {
		return fmt.Errorf("finish wakeups for run %q: %w", runID, err)
	}
	return nil
}

func (t *sqliteTx) RequeueWakeupsForRun(ctx context.Context, runID domain.ID, refundAttempt bool) error {
	if refundAttempt {
		_, err := t.q.ExecContext(ctx, `
			UPDATE wakeups
			SET status = 'queued', run_id = NULL, attempts = MAX(0, attempts - 1)
			WHERE run_id = ? AND status = 'claimed'`, runID)
		if err != nil {
			return fmt.Errorf("requeue wakeups (refund) for run %q: %w", runID, err)
		}
		return nil
	}
	_, err := t.q.ExecContext(ctx, `
		UPDATE wakeups SET status = 'queued', run_id = NULL
		WHERE run_id = ? AND status = 'claimed'`, runID)
	if err != nil {
		return fmt.Errorf("requeue wakeups for run %q: %w", runID, err)
	}
	return nil
}

func (t *sqliteTx) UpsertQuotaLimit(ctx context.Context, engine domain.EngineName, limitedUntil, nowMs int64) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO quota_state (engine, limited_until, last_limit_at, probe_attempts)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(engine) DO UPDATE SET
			limited_until = excluded.limited_until,
			last_limit_at = excluded.last_limit_at,
			probe_attempts = quota_state.probe_attempts + 1`,
		string(engine), limitedUntil, nowMs,
	)
	if err != nil {
		return fmt.Errorf("upsert quota limit %q: %w", engine, err)
	}
	return nil
}
