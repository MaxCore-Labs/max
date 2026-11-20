package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"max/internal/app"
	"max/internal/domain"
)

func scanAgentRow(rows *sql.Rows) (app.AgentRecord, error) {
	var (
		ag                                 app.AgentRecord
		allowUngated, web, shell, ready    int
		autonomyStr, browserStr, statusStr string
	)
	if err := rows.Scan(
		&ag.ID, &ag.Slug, &ag.Name, &ag.TemplateSlug, &ag.TeamID, &ag.ManagerAgentID,
		&ag.EnginesJSON, &ag.EngineMask, &allowUngated, &ag.ModelsJSON, &ag.ScopeViolations,
		&autonomyStr, &web, &browserStr, &shell, &ag.SkillsJSON, &ag.ParamsJSON, &ag.QuotaTokensDay,
		&ag.ConfigRev, &statusStr, &ready, &ag.ReadyPriority, &ag.ReadySince,
		&ag.ActiveRunID, &ag.CooldownUntil, &ag.LastRunAt, &ag.NextHeartbeatAt,
	); err != nil {
		return app.AgentRecord{}, err
	}
	ag.AllowUngatedEngine = allowUngated != 0
	ag.Autonomy = domain.Autonomy(autonomyStr)
	ag.Web = web != 0
	ag.Browser = domain.BrowserProfile(browserStr)
	ag.Shell = shell != 0
	ag.Status = domain.AgentStatus(statusStr)
	ag.Ready = ready != 0
	return ag, nil
}

func (t *sqliteTx) EnsureTaskConversation(ctx context.Context, agentID, taskID, newConvID domain.ID) (domain.ID, error) {
	var existing string
	err := t.q.QueryRowContext(ctx, `
		SELECT id FROM conversations WHERE agent_id = ? AND task_id = ?`, agentID, taskID,
	).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("lookup task conversation: %w", err)
	}
	tkCopy := taskID
	if err := t.InsertConversation(ctx, app.ConversationRecord{
		ID:       newConvID,
		AgentID:  agentID,
		TaskID:   &tkCopy,
		Sessions: `{}`,
	}); err != nil {
		return "", err
	}
	return newConvID, nil
}

func (t *sqliteTx) InsertTaskBlocker(ctx context.Context, taskID, blockedBy domain.ID) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO task_blockers (task_id, blocked_by) VALUES (?, ?)`,
		taskID, blockedBy,
	)
	if err != nil {
		return fmt.Errorf("insert task blocker (%q <- %q): %w", taskID, blockedBy, err)
	}
	return nil
}

func (t *sqliteTx) CheckoutTaskAtomic(ctx context.Context, taskID, agentID, runID domain.ID, updatedAt int64) (bool, error) {
	res, err := t.q.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'in_progress',
		    checkout_run_id = ?,
		    updated_at = ?
		WHERE id = ?
		  AND assignee_agent_id = ?
		  AND status IN ('todo', 'in_progress')
		  AND checkout_run_id IS NULL`,
		runID, updatedAt, taskID, agentID,
	)
	if err != nil {
		return false, fmt.Errorf("checkout task %q: %w", taskID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (t *sqliteTx) UpdateTaskStatus(
	ctx context.Context,
	taskID domain.ID,
	status domain.TaskStatus,
	reason *domain.BlockedReason,
	updatedAt int64,
) error {
	var rStr *string
	if status == domain.TaskBlocked && reason != nil {
		s := string(*reason)
		rStr = &s
	}
	clearCheckout := status != domain.TaskInProgress
	query := `
		UPDATE tasks
		SET status = ?,
		    blocked_reason = ?,
		    checkout_run_id = CASE WHEN ? = 1 THEN NULL ELSE checkout_run_id END,
		    updated_at = ?
		WHERE id = ?`
	if _, err := t.q.ExecContext(ctx, query, string(status), rStr, boolInt(clearCheckout), updatedAt, taskID); err != nil {
		return fmt.Errorf("update task status %q -> %q: %w", taskID, status, err)
	}
	return nil
}

func (t *sqliteTx) UpdateTaskReviewState(
	ctx context.Context,
	taskID domain.ID,
	status domain.TaskStatus,
	reviewerID *domain.ID,
	reviewRounds int,
	updatedAt int64,
) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE tasks
		SET status = ?,
		    blocked_reason = NULL,
		    reviewer_agent_id = ?,
		    review_rounds = ?,
		    checkout_run_id = NULL,
		    updated_at = ?
		WHERE id = ?`,
		string(status), reviewerID, reviewRounds, updatedAt, taskID,
	)
	if err != nil {
		return fmt.Errorf("update task review state %q: %w", taskID, err)
	}
	return nil
}

func (t *sqliteTx) UpdateTaskAssignee(
	ctx context.Context,
	taskID domain.ID,
	assigneeID *domain.ID,
	status domain.TaskStatus,
	reason *domain.BlockedReason,
	updatedAt int64,
) error {
	var rStr *string
	if status == domain.TaskBlocked && reason != nil {
		s := string(*reason)
		rStr = &s
	}
	_, err := t.q.ExecContext(ctx, `
		UPDATE tasks
		SET assignee_agent_id = ?,
		    status = ?,
		    blocked_reason = ?,
		    checkout_run_id = NULL,
		    updated_at = ?
		WHERE id = ?`,
		assigneeID, string(status), rStr, updatedAt, taskID,
	)
	if err != nil {
		return fmt.Errorf("update task assignee %q: %w", taskID, err)
	}
	return nil
}

func (t *sqliteTx) UpdateTaskIdleRuns(ctx context.Context, taskID domain.ID, idleRuns int, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE tasks SET idle_runs = ?, updated_at = ? WHERE id = ?`,
		idleRuns, updatedAt, taskID,
	)
	if err != nil {
		return fmt.Errorf("update task idle_runs %q: %w", taskID, err)
	}
	return nil
}

func (t *sqliteTx) InsertTaskEvent(ctx context.Context, ev app.TaskEventRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO task_events (id, task_id, kind, actor_kind, actor_id, target_agent_id, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.TaskID, ev.Kind, ev.ActorKind, ev.ActorID, ev.TargetAgentID, ev.Body, ev.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert task event for %q: %w", ev.TaskID, err)
	}
	return nil
}

func buildPlaceholders(ids []domain.ID) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return strings.Join(ph, ","), args
}

func (t *sqliteTx) ExpirePendingApprovalsByTasks(ctx context.Context, taskIDs []domain.ID, nowMs int64) (int, error) {
	if len(taskIDs) == 0 {
		return 0, nil
	}
	inClause, args := buildPlaceholders(taskIDs)
	allArgs := append([]any{nowMs}, args...)
	res, err := t.q.ExecContext(ctx, `
		UPDATE approvals
		SET status = 'expired', decided_at = ?
		WHERE status = 'pending'
		  AND conversation_id IN (SELECT id FROM conversations WHERE task_id IN (`+inClause+`))`,
		allArgs...,
	)
	if err != nil {
		return 0, fmt.Errorf("expire pending approvals by tasks: %w", err)
	}
	if _, err := t.q.ExecContext(ctx, `
		UPDATE conversations SET waiting_for = NULL WHERE task_id IN (`+inClause+`)`, args...,
	); err != nil {
		return 0, fmt.Errorf("clear waiting_for by tasks: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (t *sqliteTx) ListActiveRunsByTasks(ctx context.Context, taskIDs []domain.ID) ([]app.RunRecord, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	inClause, args := buildPlaceholders(taskIDs)
	rows, err := t.q.QueryContext(ctx, `
		SELECT id, agent_id, conversation_id, task_id, status, priority, engine, model,
		       pgid, attempt, turns, input_tokens, output_tokens, cached_read_tokens,
		       thought_tokens, notional_microusd, touched_paths, error, summary, vault_commit,
		       started_at, ended_at
		FROM runs
		WHERE status = 'running' AND ended_at IS NULL
		  AND (task_id IN (`+inClause+`) OR conversation_id IN (SELECT id FROM conversations WHERE task_id IN (`+inClause+`)))`,
		append(args, args...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("list active runs by tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.RunRecord
	for rows.Next() {
		var (
			rec             app.RunRecord
			status, engName string
			prio            int
		)
		if err := rows.Scan(
			&rec.ID, &rec.AgentID, &rec.ConversationID, &rec.TaskID, &status, &prio, &engName, &rec.Model,
			&rec.PGID, &rec.Attempt, &rec.Turns, &rec.InputTokens, &rec.OutputTokens, &rec.CachedReadTokens,
			&rec.ThoughtTokens, &rec.NotionalMicroUSD, &rec.TouchedPathsJSON, &rec.Error, &rec.Summary, &rec.VaultCommit,
			&rec.StartedAt, &rec.EndedAt,
		); err != nil {
			return nil, err
		}
		rec.Status = domain.RunStatus(status)
		rec.Priority = domain.Priority(prio)
		rec.Engine = domain.EngineName(engName)
		out = append(out, rec)
	}
	return out, rows.Err()
}
