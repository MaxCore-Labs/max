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

func buildListAgentsQuery(f app.AgentFilter) (string, []any) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	var conds []string
	var args []any
	if f.TeamID != "" {
		conds = append(conds, `team_id = ?`)
		args = append(args, f.TeamID)
	}
	if f.Template != "" {
		conds = append(conds, `template_slug = ?`)
		args = append(args, f.Template)
	}
	if f.Status != "" {
		conds = append(conds, `status = ?`)
		args = append(args, f.Status)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pat := "%" + strings.ToLower(q) + "%"
		conds = append(conds, `(LOWER(name) LIKE ? OR LOWER(slug) LIKE ?)`)
		args = append(args, pat, pat)
	}
	if f.AfterSlug != "" {
		conds = append(conds, `slug > ?`)
		args = append(args, f.AfterSlug)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)
	sqlText := `
		SELECT id, slug, name, template_slug, team_id, manager_agent_id,
		       engines, engine_mask, allow_ungated_engine, models, scope_violations,
		       autonomy, web, browser, shell, skills, params, quota_tokens_day,
		       config_rev, status, ready, ready_priority, ready_since,
		       active_run_id, cooldown_until, last_run_at, next_heartbeat_at
		FROM agents ` + where + `
		ORDER BY slug ASC
		LIMIT ?`
	return sqlText, args
}

func (r *sqliteReader) ListAgentsPage(ctx context.Context, f app.AgentFilter) ([]app.AgentRecord, error) {
	sqlText, args := buildListAgentsQuery(f)
	rows, err := r.q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("list agents page: %w", err)
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

// ExplainListAgentsPage returns SQLite EXPLAIN QUERY PLAN detail lines for ListAgentsPage.
func (s *Store) ExplainListAgentsPage(ctx context.Context, f app.AgentFilter) ([]string, error) {
	sqlText, args := buildListAgentsQuery(f)
	rows, err := s.rdb.QueryContext(ctx, "EXPLAIN QUERY PLAN "+sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("explain list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	return details, rows.Err()
}

func (r *sqliteReader) ListTeamsPage(ctx context.Context, afterSlug string, limit int) ([]app.TeamSummaryRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var cond string
	var args []any
	if afterSlug != "" {
		cond = `WHERE t.slug > ?`
		args = append(args, afterSlug)
	}
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, `
		SELECT t.id, t.slug, t.name, t.mandate, t.max_running, t.running, t.quota_tokens_day, t.status,
		       (SELECT COUNT(*) FROM agents a WHERE a.team_id = t.id AND a.status != 'archived')
		FROM teams t `+cond+`
		ORDER BY t.slug ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list teams page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.TeamSummaryRecord
	for rows.Next() {
		var rec app.TeamSummaryRecord
		if err := rows.Scan(
			&rec.ID, &rec.Slug, &rec.Name, &rec.MandateJSON, &rec.MaxRunning,
			&rec.Running, &rec.QuotaTokensDay, &rec.Status, &rec.AgentCount,
		); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *sqliteReader) CountAgentsByTeam(ctx context.Context, teamID domain.ID) (int, error) {
	var n int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE team_id = ? AND status != 'archived'`, teamID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count agents by team: %w", err)
	}
	return n, nil
}

func (r *sqliteReader) GetGoal(ctx context.Context, id domain.ID) (app.GoalRecord, bool, error) {
	var g app.GoalRecord
	err := r.q.QueryRowContext(ctx, `SELECT id, title, description, status, created_at FROM goals WHERE id = ?`, id).
		Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.GoalRecord{}, false, nil
	}
	if err != nil {
		return app.GoalRecord{}, false, fmt.Errorf("get goal %q: %w", id, err)
	}
	return g, true, nil
}

func (r *sqliteReader) ListGoalsPage(ctx context.Context, status string, limit int, beforeTS *int64, beforeID string) ([]app.GoalRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, title, description, status, created_at FROM goals WHERE 1=1`
	var args []any
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if beforeTS != nil {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, *beforeTS, *beforeTS, beforeID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list goals page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.GoalRecord
	for rows.Next() {
		var g app.GoalRecord
		if err := rows.Scan(&g.ID, &g.Title, &g.Description, &g.Status, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (t *sqliteTx) InsertGoal(ctx context.Context, g app.GoalRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO goals (id, title, description, status, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		g.ID, g.Title, g.Description, defaultJSON(g.Status, "active"), g.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert goal %q: %w", g.ID, err)
	}
	return nil
}

func (r *sqliteReader) ListTaskEventsPage(ctx context.Context, taskID domain.ID, limit int, afterTS *int64, afterID string) ([]app.TaskEventRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, task_id, kind, actor_kind, actor_id, target_agent_id, body, created_at
	          FROM task_events WHERE task_id = ?`
	args := []any{taskID}
	if afterTS != nil {
		query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
		args = append(args, *afterTS, *afterTS, afterID)
	}
	query += ` ORDER BY created_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list task events page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.TaskEventRecord
	for rows.Next() {
		var ev app.TaskEventRecord
		if err := rows.Scan(&ev.ID, &ev.TaskID, &ev.Kind, &ev.ActorKind, &ev.ActorID, &ev.TargetAgentID, &ev.Body, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
