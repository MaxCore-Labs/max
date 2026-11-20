package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetInboxItem(ctx context.Context, id domain.ID) (app.InboxItemRecord, bool, error) {
	var it app.InboxItemRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, kind, title, body, ref_kind, ref_id, conversation_id, agent_id,
		       choices, urgency, dedupe_key, read_at, resolved_at, created_at
		FROM inbox_items WHERE id = ?`, id,
	).Scan(
		&it.ID, &it.Kind, &it.Title, &it.Body, &it.RefKind, &it.RefID, &it.ConversationID, &it.AgentID,
		&it.ChoicesJSON, &it.Urgency, &it.DedupeKey, &it.ReadAt, &it.ResolvedAt, &it.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.InboxItemRecord{}, false, nil
	}
	if err != nil {
		return app.InboxItemRecord{}, false, err
	}
	return it, true, nil
}

func (r *sqliteReader) ListApprovalsPage(ctx context.Context, status string, limit int, beforeTS *int64, beforeID string) ([]app.ApprovalRecord, error) {
	query := `SELECT id, run_id, agent_id, conversation_id, tool_name, input, input_hash,
	                 preview, reason, by_max, status, decision_key, requested_at, expires_at, decided_at
	          FROM approvals WHERE 1=1`
	var args []any
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if beforeTS != nil {
		query += ` AND (requested_at < ? OR (requested_at = ? AND id < ?))`
		args = append(args, *beforeTS, *beforeTS, beforeID)
	}
	query += ` ORDER BY requested_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.ApprovalRecord
	for rows.Next() {
		var ap app.ApprovalRecord
		var byMax int
		if err := rows.Scan(
			&ap.ID, &ap.RunID, &ap.AgentID, &ap.ConversationID, &ap.ToolName, &ap.Input, &ap.InputHash,
			&ap.Preview, &ap.Reason, &byMax, &ap.Status, &ap.DecisionKey, &ap.RequestedAt, &ap.ExpiresAt, &ap.DecidedAt,
		); err != nil {
			return nil, err
		}
		ap.ByMax = byMax != 0
		out = append(out, ap)
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListInboxPage(ctx context.Context, unreadOnly bool, limit int, beforeTS *int64, beforeID string) ([]app.InboxItemRecord, error) {
	query := `SELECT id, kind, title, body, ref_kind, ref_id, conversation_id, agent_id,
	                 choices, urgency, dedupe_key, read_at, resolved_at, created_at
	          FROM inbox_items WHERE 1=1`
	var args []any
	if unreadOnly {
		query += ` AND read_at IS NULL`
	}
	if beforeTS != nil {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, *beforeTS, *beforeTS, beforeID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.InboxItemRecord
	for rows.Next() {
		var it app.InboxItemRecord
		if err := rows.Scan(
			&it.ID, &it.Kind, &it.Title, &it.Body, &it.RefKind, &it.RefID, &it.ConversationID, &it.AgentID,
			&it.ChoicesJSON, &it.Urgency, &it.DedupeKey, &it.ReadAt, &it.ResolvedAt, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListRunsPage(ctx context.Context, agentID, status string, limit int, beforeTS *int64, beforeID string) ([]app.RunRecord, error) {
	query := `SELECT id, agent_id, conversation_id, task_id, status, priority, engine, model,
	                 pgid, attempt, turns, input_tokens, output_tokens, cached_read_tokens,
	                 thought_tokens, notional_microusd, touched_paths, error, summary, vault_commit,
	                 started_at, ended_at
	          FROM runs WHERE 1=1`
	var args []any
	if agentID != "" {
		query += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if beforeTS != nil {
		query += ` AND (started_at < ? OR (started_at = ? AND id < ?))`
		args = append(args, *beforeTS, *beforeTS, beforeID)
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.RunRecord
	for rows.Next() {
		var rec app.RunRecord
		var stStr, engStr string
		var prio int
		if err := rows.Scan(
			&rec.ID, &rec.AgentID, &rec.ConversationID, &rec.TaskID, &stStr, &prio, &engStr, &rec.Model,
			&rec.PGID, &rec.Attempt, &rec.Turns, &rec.InputTokens, &rec.OutputTokens, &rec.CachedReadTokens,
			&rec.ThoughtTokens, &rec.NotionalMicroUSD, &rec.TouchedPathsJSON, &rec.Error, &rec.Summary, &rec.VaultCommit,
			&rec.StartedAt, &rec.EndedAt,
		); err != nil {
			return nil, err
		}
		rec.Status = domain.RunStatus(stStr)
		rec.Priority = domain.Priority(prio)
		rec.Engine = domain.EngineName(engStr)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListUsagePage(ctx context.Context, sinceDay string, limit, offset int) ([]app.DailyUsageRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT day, agent_id, team_id, engine, runs, tokens
		FROM usage_daily
		WHERE (? = '' OR day >= ?)
		ORDER BY day DESC, agent_id ASC, engine ASC
		LIMIT ? OFFSET ?`, sinceDay, sinceDay, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.DailyUsageRecord
	for rows.Next() {
		var u app.DailyUsageRecord
		var eng string
		if err := rows.Scan(&u.Day, &u.AgentID, &u.TeamID, &eng, &u.Runs, &u.Tokens); err != nil {
			return nil, err
		}
		u.Engine = domain.EngineName(eng)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *sqliteReader) CountQueuedByPriority(ctx context.Context) (map[int]int, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT priority, COUNT(*) FROM wakeups WHERE status = 'queued' GROUP BY priority`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int]int)
	for rows.Next() {
		var p, n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

func (r *sqliteReader) ListAuditLogs(ctx context.Context, limit int) ([]app.AuditLogRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, ts, actor, action, ref, detail FROM audit_log ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.AuditLogRecord
	for rows.Next() {
		var rec app.AuditLogRecord
		if err := rows.Scan(&rec.ID, &rec.TS, &rec.Actor, &rec.Action, &rec.Ref, &rec.DetailJSON); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (t *sqliteTx) MarkInboxItemRead(ctx context.Context, id domain.ID, readAt int64) error {
	_, err := t.q.ExecContext(ctx, `UPDATE inbox_items SET read_at = COALESCE(read_at, ?) WHERE id = ?`, readAt, id)
	return err
}

func (t *sqliteTx) ResolveInboxItem(ctx context.Context, id domain.ID, resolvedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE inbox_items SET read_at = COALESCE(read_at, ?), resolved_at = COALESCE(resolved_at, ?)
		WHERE id = ?`, resolvedAt, resolvedAt, id)
	return err
}

func (t *sqliteTx) AppendAuditLog(ctx context.Context, rec app.AuditLogRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO audit_log (ts, actor, action, ref, detail)
		VALUES (?, ?, ?, ?, ?)`,
		rec.TS, defaultJSON(rec.Actor, "owner"), rec.Action, rec.Ref, defaultJSON(rec.DetailJSON, `{}`),
	)
	if err != nil {
		return fmt.Errorf("append audit_log: %w", err)
	}
	return nil
}

func (r *sqliteReader) GetEngineBinary(ctx context.Context, engine domain.EngineName) (app.EngineBinaryRecord, bool, error) {
	var rec app.EngineBinaryRecord
	var engStr string
	err := r.q.QueryRowContext(ctx, `
		SELECT engine, path, sha256, version, confirmed_at
		FROM engine_binaries WHERE engine = ?`, string(engine),
	).Scan(&engStr, &rec.Path, &rec.SHA256, &rec.Version, &rec.ConfirmedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.EngineBinaryRecord{}, false, nil
	}
	if err != nil {
		return app.EngineBinaryRecord{}, false, err
	}
	rec.Engine = domain.EngineName(engStr)
	return rec, true, nil
}

func (t *sqliteTx) UpsertEngineBinary(ctx context.Context, rec app.EngineBinaryRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO engine_binaries (engine, path, sha256, version, confirmed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(engine) DO UPDATE SET
			path = excluded.path,
			sha256 = excluded.sha256,
			version = excluded.version,
			confirmed_at = excluded.confirmed_at`,
		string(rec.Engine), rec.Path, rec.SHA256, rec.Version, rec.ConfirmedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert engine_binary: %w", err)
	}
	return nil
}
