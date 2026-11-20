package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetAgentBySlug(ctx context.Context, slug string) (app.AgentRecord, bool, error) {
	var id string
	err := r.q.QueryRowContext(ctx, `SELECT id FROM agents WHERE slug = ?`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return app.AgentRecord{}, false, nil
	}
	if err != nil {
		return app.AgentRecord{}, false, fmt.Errorf("get agent by slug %q: %w", slug, err)
	}
	return r.GetAgent(ctx, id)
}

func (r *sqliteReader) GetTeamBySlug(ctx context.Context, slug string) (app.TeamRecord, bool, error) {
	var tm app.TeamRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, slug, name, mandate, max_running, running, quota_tokens_day, status
		FROM teams WHERE slug = ?`, slug,
	).Scan(&tm.ID, &tm.Slug, &tm.Name, &tm.MandateJSON, &tm.MaxRunning, &tm.Running, &tm.QuotaTokensDay, &tm.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return app.TeamRecord{}, false, nil
	}
	if err != nil {
		return app.TeamRecord{}, false, fmt.Errorf("get team by slug %q: %w", slug, err)
	}
	return tm, true, nil
}

func (t *sqliteTx) EnsureTeamBySlug(ctx context.Context, slug, newID string) (domain.ID, error) {
	existing, ok, err := t.GetTeamBySlug(ctx, slug)
	if err != nil {
		return "", err
	}
	if ok {
		return existing.ID, nil
	}
	if err := t.InsertTeam(ctx, app.TeamRecord{
		ID:         newID,
		Slug:       slug,
		Name:       slug,
		MaxRunning: 2,
		Status:     "active",
	}); err != nil {
		return "", err
	}
	return newID, nil
}

func (t *sqliteTx) UpsertSyncedTeam(ctx context.Context, tm app.TeamRecord) error {
	maxRun := tm.MaxRunning
	if maxRun <= 0 {
		maxRun = 2
	}
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO teams (id, slug, name, mandate, max_running, quota_tokens_day, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			name = excluded.name,
			mandate = excluded.mandate,
			max_running = excluded.max_running,
			quota_tokens_day = excluded.quota_tokens_day`,
		tm.ID, tm.Slug, tm.Name, defaultJSON(tm.MandateJSON, `{}`),
		maxRun, tm.QuotaTokensDay, defaultJSON(tm.Status, "active"),
	)
	if err != nil {
		return fmt.Errorf("upsert synced team %q: %w", tm.Slug, err)
	}
	return nil
}

func (t *sqliteTx) UpsertSyncedAgent(ctx context.Context, a app.AgentRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO agents (
			id, slug, name, template_slug, team_id, manager_agent_id,
			engines, engine_mask, allow_ungated_engine, models,
			autonomy, web, browser, shell, skills, params, quota_tokens_day,
			config_rev, status, next_heartbeat_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			name = excluded.name,
			template_slug = excluded.template_slug,
			team_id = excluded.team_id,
			manager_agent_id = COALESCE(excluded.manager_agent_id, agents.manager_agent_id),
			engines = excluded.engines,
			engine_mask = excluded.engine_mask,
			allow_ungated_engine = excluded.allow_ungated_engine,
			models = excluded.models,
			autonomy = excluded.autonomy,
			web = excluded.web,
			browser = excluded.browser,
			shell = excluded.shell,
			skills = excluded.skills,
			params = excluded.params,
			quota_tokens_day = excluded.quota_tokens_day,
			config_rev = excluded.config_rev,
			next_heartbeat_at = COALESCE(excluded.next_heartbeat_at, agents.next_heartbeat_at)`,
		a.ID, a.Slug, a.Name, a.TemplateSlug, a.TeamID, a.ManagerAgentID,
		defaultJSON(a.EnginesJSON, `["agy"]`), a.EngineMask, boolInt(a.AllowUngatedEngine),
		defaultJSON(a.ModelsJSON, `{}`),
		string(a.Autonomy), boolInt(a.Web), string(a.Browser), boolInt(a.Shell),
		defaultJSON(a.SkillsJSON, `[]`), defaultJSON(a.ParamsJSON, `{}`), a.QuotaTokensDay,
		a.ConfigRev, defaultJSON(string(a.Status), string(domain.AgentActive)), a.NextHeartbeatAt,
	)
	if err != nil {
		return fmt.Errorf("upsert synced agent %q: %w", a.Slug, err)
	}
	return nil
}

func (t *sqliteTx) SetAgentManager(ctx context.Context, agentID domain.ID, managerID *domain.ID) error {
	_, err := t.q.ExecContext(ctx, `UPDATE agents SET manager_agent_id = ? WHERE id = ?`, managerID, agentID)
	if err != nil {
		return fmt.Errorf("set agent manager: %w", err)
	}
	return nil
}

func (t *sqliteTx) RecordScopeViolations(ctx context.Context, agentID domain.ID, addCount int, _ int64) (int, bool, error) {
	if addCount <= 0 {
		addCount = 1
	}
	if _, err := t.q.ExecContext(ctx, `
		UPDATE agents SET scope_violations = scope_violations + ? WHERE id = ?`, addCount, agentID,
	); err != nil {
		return 0, false, fmt.Errorf("increment scope_violations for %q: %w", agentID, err)
	}
	var total int
	var curStatus string
	if err := t.q.QueryRowContext(ctx, `SELECT scope_violations, status FROM agents WHERE id = ?`, agentID).Scan(&total, &curStatus); err != nil {
		return 0, false, err
	}
	paused := false
	if total >= 3 && curStatus != string(domain.AgentPaused) {
		if _, err := t.q.ExecContext(ctx, `UPDATE agents SET status = 'paused' WHERE id = ?`, agentID); err != nil {
			return total, false, err
		}
		paused = true
	}
	return total, paused, nil
}
