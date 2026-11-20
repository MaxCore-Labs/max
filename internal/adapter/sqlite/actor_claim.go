package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

const systemThreadSentinel = "__system__"

func (r *sqliteReader) GetTeam(ctx context.Context, id domain.ID) (app.TeamRecord, bool, error) {
	var tm app.TeamRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, slug, name, mandate, max_running, running, quota_tokens_day, status
		FROM teams WHERE id = ?`, id,
	).Scan(&tm.ID, &tm.Slug, &tm.Name, &tm.MandateJSON, &tm.MaxRunning, &tm.Running, &tm.QuotaTokensDay, &tm.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return app.TeamRecord{}, false, nil
	}
	if err != nil {
		return app.TeamRecord{}, false, fmt.Errorf("get team %q: %w", id, err)
	}
	return tm, true, nil
}

func (r *sqliteReader) GetConversation(ctx context.Context, id domain.ID) (app.ConversationRecord, bool, error) {
	var c app.ConversationRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, task_id, thread_id, sessions, last_engine, waiting_for, turns_used, closed_at
		FROM conversations WHERE id = ?`, id,
	).Scan(&c.ID, &c.AgentID, &c.TaskID, &c.ThreadID, &c.Sessions, &c.LastEngine, &c.WaitingFor, &c.TurnsUsed, &c.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ConversationRecord{}, false, nil
	}
	if err != nil {
		return app.ConversationRecord{}, false, fmt.Errorf("get conversation %q: %w", id, err)
	}
	return c, true, nil
}

func (r *sqliteReader) GetRun(ctx context.Context, id domain.ID) (app.RunRecord, bool, error) {
	var (
		rec             app.RunRecord
		status, engName string
		prio            int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, conversation_id, task_id, status, priority, engine, model,
		       pgid, attempt, turns, input_tokens, output_tokens, cached_read_tokens,
		       thought_tokens, notional_microusd, touched_paths, error, summary, vault_commit,
		       config_rev, started_at, ended_at
		FROM runs WHERE id = ?`, id,
	).Scan(
		&rec.ID, &rec.AgentID, &rec.ConversationID, &rec.TaskID, &status, &prio, &engName, &rec.Model,
		&rec.PGID, &rec.Attempt, &rec.Turns, &rec.InputTokens, &rec.OutputTokens, &rec.CachedReadTokens,
		&rec.ThoughtTokens, &rec.NotionalMicroUSD, &rec.TouchedPathsJSON, &rec.Error, &rec.Summary, &rec.VaultCommit,
		&rec.ConfigRev, &rec.StartedAt, &rec.EndedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.RunRecord{}, false, nil
	}
	if err != nil {
		return app.RunRecord{}, false, fmt.Errorf("get run %q: %w", id, err)
	}
	rec.Status = domain.RunStatus(status)
	rec.Priority = domain.Priority(prio)
	rec.Engine = domain.EngineName(engName)
	return rec, true, nil
}

func (r *sqliteReader) GetWakeup(ctx context.Context, agentID, wakeupID domain.ID) (app.WakeupRecord, bool, error) {
	var (
		w    app.WakeupRecord
		kind string
		prio int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, conversation_id, kind, priority, payload, idempotency_key,
		       status, run_id, coalesced_count, attempts, cause_run_id, hops, not_before, created_at
		FROM wakeups WHERE agent_id = ? AND id = ?`, agentID, wakeupID,
	).Scan(
		&w.ID, &w.AgentID, &w.ConversationID, &kind, &prio, &w.Payload, &w.IdempotencyKey,
		&w.Status, &w.RunID, &w.CoalescedCount, &w.Attempts, &w.CauseRunID, &w.Hops, &w.NotBefore, &w.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.WakeupRecord{}, false, nil
	}
	if err != nil {
		return app.WakeupRecord{}, false, fmt.Errorf("get wakeup %q: %w", wakeupID, err)
	}
	w.Kind = domain.WakeKind(kind)
	w.Priority = domain.Priority(prio)
	return w, true, nil
}

func (r *sqliteReader) ListInboxItems(ctx context.Context, limit int) ([]app.InboxItemRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, kind, title, body, ref_kind, ref_id, conversation_id, agent_id, choices, urgency, dedupe_key, created_at
		FROM inbox_items ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list inbox items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.InboxItemRecord
	for rows.Next() {
		var it app.InboxItemRecord
		if err := rows.Scan(
			&it.ID, &it.Kind, &it.Title, &it.Body, &it.RefKind, &it.RefID,
			&it.ConversationID, &it.AgentID, &it.ChoicesJSON, &it.Urgency, &it.DedupeKey, &it.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan inbox item: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (t *sqliteTx) SetAgentCooldown(ctx context.Context, agentID domain.ID, cooldownUntil int64) error {
	_, err := t.q.ExecContext(ctx, `UPDATE agents SET cooldown_until = ? WHERE id = ?`, cooldownUntil, agentID)
	if err != nil {
		return fmt.Errorf("set agent cooldown: %w", err)
	}
	return nil
}

func (t *sqliteTx) EnsureSystemConversation(ctx context.Context, agentID, newConvID domain.ID) (domain.ID, error) {
	var existing string
	err := t.q.QueryRowContext(ctx, `
		SELECT id FROM conversations WHERE agent_id = ? AND thread_id = ?`,
		agentID, systemThreadSentinel,
	).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("lookup system conversation: %w", err)
	}
	sentinel := systemThreadSentinel
	if err := t.InsertConversation(ctx, app.ConversationRecord{
		ID:       newConvID,
		AgentID:  agentID,
		ThreadID: &sentinel,
		Sessions: `{}`,
	}); err != nil {
		return "", err
	}
	return newConvID, nil
}

func (t *sqliteTx) InsertRun(ctx context.Context, r app.RunRecord) error {
	attempt := r.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	model := defaultJSON(r.Model, "default")
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO runs (
			id, agent_id, conversation_id, task_id, status, priority, engine, model,
			pgid, attempt, turns, input_tokens, output_tokens, cached_read_tokens,
			thought_tokens, notional_microusd, touched_paths, error, summary, vault_commit,
			config_rev, started_at, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.AgentID, r.ConversationID, r.TaskID, string(r.Status), int(r.Priority), string(r.Engine), model,
		r.PGID, attempt, r.Turns, r.InputTokens, r.OutputTokens, r.CachedReadTokens,
		r.ThoughtTokens, r.NotionalMicroUSD, defaultJSON(r.TouchedPathsJSON, `[]`),
		r.Error, r.Summary, r.VaultCommit, r.ConfigRev, r.StartedAt, r.EndedAt,
	)
	if err != nil {
		return fmt.Errorf("insert run %q: %w", r.ID, err)
	}
	return nil
}

func (t *sqliteTx) AdjustTeamRunning(ctx context.Context, teamID domain.ID, delta int) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE teams SET running = MAX(0, running + ?) WHERE id = ?`, delta, teamID)
	if err != nil {
		return fmt.Errorf("adjust team running: %w", err)
	}
	return nil
}

func (t *sqliteTx) ListCandidateAgents(ctx context.Context, nowMs int64, engineMask, limit int) ([]app.CandidateAgent, error) {
	rows, err := t.q.QueryContext(ctx, `
		SELECT a.id, a.team_id, a.engines, a.models, a.config_rev, a.cooldown_until, a.ready_priority, t.running, t.max_running
		FROM agents a
		JOIN teams t ON t.id = a.team_id
		WHERE a.ready = 1 AND a.active_run_id IS NULL AND a.status = 'active'
		  AND (a.cooldown_until <= ? OR a.ready_priority >= 100)
		  AND t.running < t.max_running
		  AND (a.engine_mask & ?) != 0
		ORDER BY a.ready_priority DESC, a.ready_since ASC, a.id ASC
		LIMIT ?`, nowMs, engineMask, limit)
	if err != nil {
		return nil, fmt.Errorf("list candidate agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.CandidateAgent
	for rows.Next() {
		var c app.CandidateAgent
		if err := rows.Scan(
			&c.ID, &c.TeamID, &c.EnginesJSON, &c.ModelsJSON, &c.ConfigRev,
			&c.CooldownUntil, &c.ReadyPriority, &c.TeamRunning, &c.TeamMaxRun,
		); err != nil {
			return nil, fmt.Errorf("scan candidate agent: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (t *sqliteTx) PeekTopDueWakeup(ctx context.Context, agentID domain.ID, nowMs int64) (app.WakeupRecord, bool, error) {
	var (
		w    app.WakeupRecord
		kind string
		prio int
	)
	err := t.q.QueryRowContext(ctx, `
		SELECT id, agent_id, conversation_id, kind, priority, payload, idempotency_key,
		       status, run_id, coalesced_count, attempts, cause_run_id, hops, not_before, created_at
		FROM wakeups
		WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
		ORDER BY priority DESC, created_at ASC, id ASC
		LIMIT 1`, agentID, nowMs,
	).Scan(
		&w.ID, &w.AgentID, &w.ConversationID, &kind, &prio, &w.Payload, &w.IdempotencyKey,
		&w.Status, &w.RunID, &w.CoalescedCount, &w.Attempts, &w.CauseRunID, &w.Hops, &w.NotBefore, &w.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.WakeupRecord{}, false, nil
	}
	if err != nil {
		return app.WakeupRecord{}, false, fmt.Errorf("peek top wakeup: %w", err)
	}
	w.Kind = domain.WakeKind(kind)
	w.Priority = domain.Priority(prio)
	return w, true, nil
}
