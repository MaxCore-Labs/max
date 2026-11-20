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

func scanTaskRows(rows *sql.Rows) ([]app.TaskRecord, error) {
	var out []app.TaskRecord
	for rows.Next() {
		var (
			tk          app.TaskRecord
			origin      string
			needsReview int
		)
		if err := rows.Scan(
			&tk.ID, &tk.GoalID, &tk.ParentID, &tk.RootID, &origin, &tk.Depth, &tk.Title, &tk.Description, &tk.DoneCriteria,
			&tk.Status, &tk.BlockedReason, &tk.AssigneeAgentID, &tk.CreatedByAgent, &tk.CheckoutRunID,
			&needsReview, &tk.ReviewerAgentID, &tk.ReviewRounds, &tk.IdleRuns, &tk.DueAt, &tk.CreatedAt, &tk.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tk.RootOrigin = domain.RootOrigin(origin)
		tk.NeedsReview = needsReview != 0
		out = append(out, tk)
	}
	return out, rows.Err()
}

const taskCols = `t.id, t.goal_id, t.parent_id, t.root_id, t.root_origin, t.depth, t.title, t.description, t.done_criteria,
	t.status, t.blocked_reason, t.assignee_agent_id, t.created_by_agent, t.checkout_run_id,
	t.needs_review, t.reviewer_agent_id, t.review_rounds, t.idle_runs, t.due_at, t.created_at, t.updated_at`

func (r *sqliteReader) ListTaskBlockers(ctx context.Context, taskID domain.ID) ([]domain.ID, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT blocked_by FROM task_blockers WHERE task_id = ? ORDER BY blocked_by`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task blockers for %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListAllBlockerEdges(ctx context.Context) (map[domain.ID][]domain.ID, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT task_id, blocked_by FROM task_blockers`)
	if err != nil {
		return nil, fmt.Errorf("list blocker edges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	edges := make(map[domain.ID][]domain.ID)
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		edges[from] = append(edges[from], to)
	}
	return edges, rows.Err()
}

func (r *sqliteReader) ListDependents(ctx context.Context, blockerID domain.ID) ([]app.TaskRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+taskCols+`
		FROM tasks t
		JOIN task_blockers tb ON tb.task_id = t.id
		WHERE tb.blocked_by = ?
		ORDER BY t.created_at ASC, t.id ASC`, blockerID)
	if err != nil {
		return nil, fmt.Errorf("list dependents of %q: %w", blockerID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

func (r *sqliteReader) ListChildTasks(ctx context.Context, parentID domain.ID) ([]app.TaskRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+taskCols+`
		FROM tasks t
		WHERE t.parent_id = ?
		ORDER BY t.created_at ASC, t.id ASC`, parentID)
	if err != nil {
		return nil, fmt.Errorf("list child tasks of %q: %w", parentID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

func (r *sqliteReader) ListTaskEvents(ctx context.Context, taskID domain.ID, limit int) ([]app.TaskEventRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, task_id, kind, actor_kind, actor_id, target_agent_id, body, created_at
		FROM task_events
		WHERE task_id = ?
		ORDER BY created_at ASC, id ASC
		LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("list task events for %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.TaskEventRecord
	for rows.Next() {
		var ev app.TaskEventRecord
		if err := rows.Scan(
			&ev.ID, &ev.TaskID, &ev.Kind, &ev.ActorKind, &ev.ActorID, &ev.TargetAgentID, &ev.Body, &ev.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (r *sqliteReader) CountTaskEvents(ctx context.Context, taskID domain.ID) (int, error) {
	var n int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = ?`, taskID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count task events for %q: %w", taskID, err)
	}
	return n, nil
}

func (r *sqliteReader) CountPairMessagesSince(ctx context.Context, taskID, agentA, agentB domain.ID, sinceMs int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM task_events
		WHERE task_id = ?
		  AND kind = 'message'
		  AND created_at >= ?
		  AND ((actor_id = ? AND target_agent_id = ?) OR (actor_id = ? AND target_agent_id = ?))`,
		taskID, sinceMs, agentA, agentB, agentB, agentA,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pair messages: %w", err)
	}
	return n, nil
}

func (r *sqliteReader) ListScopedTasks(ctx context.Context, f app.TaskFilter) ([]app.TaskRecord, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var conds []string
	var args []any

	if f.ViewerAgentID != "" {
		conds = append(conds, `(t.assignee_agent_id = ? OR t.created_by_agent = ? OR t.reviewer_agent_id = ? OR EXISTS (
			SELECT 1 FROM agents a WHERE a.team_id = ? AND (a.id = t.assignee_agent_id OR a.id = t.created_by_agent)
		))`)
		args = append(args, f.ViewerAgentID, f.ViewerAgentID, f.ViewerAgentID, f.ViewerTeamID)
	}
	if f.Status != "" {
		conds = append(conds, `t.status = ?`)
		args = append(args, f.Status)
	}
	if f.AssigneeID != "" {
		conds = append(conds, `t.assignee_agent_id = ?`)
		args = append(args, f.AssigneeID)
	}
	if f.GoalID != "" {
		conds = append(conds, `t.goal_id = ?`)
		args = append(args, f.GoalID)
	}
	if f.RootID != "" {
		conds = append(conds, `t.root_id = ?`)
		args = append(args, f.RootID)
	}
	if f.BeforeTS != nil {
		conds = append(conds, `(t.updated_at < ? OR (t.updated_at = ? AND t.id < ?))`)
		args = append(args, *f.BeforeTS, *f.BeforeTS, f.BeforeID)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)

	rows, err := r.q.QueryContext(ctx, `
		SELECT `+taskCols+`
		FROM tasks t
		`+where+`
		ORDER BY t.updated_at DESC, t.id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list scoped tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

func (r *sqliteReader) ListTeamAgents(ctx context.Context, teamID domain.ID) ([]app.AgentRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, slug, name, template_slug, team_id, manager_agent_id,
		       engines, engine_mask, allow_ungated_engine, models, scope_violations,
		       autonomy, web, browser, shell, skills, params, quota_tokens_day,
		       config_rev, status, ready, ready_priority, ready_since,
		       active_run_id, cooldown_until, last_run_at, next_heartbeat_at
		FROM agents
		WHERE team_id = ? AND status = 'active'
		ORDER BY slug ASC`, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team agents %q: %w", teamID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.AgentRecord
	for rows.Next() {
		ag, err := scanAgentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ag)
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListWatchdogCandidateTasks(ctx context.Context, staleBeforeMs, nowMs int64) ([]app.TaskRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+taskCols+`
		FROM tasks t
		WHERE t.parent_id IS NOT NULL
		  AND t.status NOT IN ('done', 'cancelled')
		  AND (t.updated_at <= ? OR (t.due_at IS NOT NULL AND t.due_at <= ?))
		ORDER BY t.updated_at ASC, t.id ASC
		LIMIT 200`, staleBeforeMs, nowMs)
	if err != nil {
		return nil, fmt.Errorf("list watchdog candidate tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

func (r *sqliteReader) GetConversationByTask(ctx context.Context, agentID, taskID domain.ID) (app.ConversationRecord, bool, error) {
	var c app.ConversationRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, task_id, thread_id, sessions, last_engine, waiting_for, turns_used, closed_at
		FROM conversations
		WHERE agent_id = ? AND task_id = ?`, agentID, taskID,
	).Scan(&c.ID, &c.AgentID, &c.TaskID, &c.ThreadID, &c.Sessions, &c.LastEngine, &c.WaitingFor, &c.TurnsUsed, &c.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ConversationRecord{}, false, nil
	}
	if err != nil {
		return app.ConversationRecord{}, false, fmt.Errorf("get conversation by task: %w", err)
	}
	return c, true, nil
}

func (r *sqliteReader) GetMaxWakeupHopsForTask(ctx context.Context, agentID, taskID domain.ID) (int, error) {
	var hops int
	err := r.q.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(w.hops), 0)
		FROM wakeups w
		JOIN conversations c ON c.id = w.conversation_id
		WHERE w.agent_id = ? AND c.task_id = ?`, agentID, taskID,
	).Scan(&hops)
	if err != nil {
		return 0, fmt.Errorf("get max wakeup hops for task: %w", err)
	}
	return hops, nil
}
