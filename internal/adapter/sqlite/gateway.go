package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetConnection(ctx context.Context, name string) (app.ConnectionRecord, bool, error) {
	var c app.ConnectionRecord
	var enabled int
	err := r.q.QueryRowContext(ctx, `
		SELECT name, kind, config, tools, enabled, created_at, updated_at
		FROM connections WHERE name = ?`, name,
	).Scan(&c.Name, &c.Kind, &c.ConfigCiphertext, &c.ToolsJSON, &enabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ConnectionRecord{}, false, nil
	}
	if err != nil {
		return app.ConnectionRecord{}, false, fmt.Errorf("get connection %q: %w", name, err)
	}
	c.Enabled = enabled != 0
	return c, true, nil
}

func (r *sqliteReader) ListConnections(ctx context.Context) ([]app.ConnectionRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT name, kind, config, tools, enabled, created_at, updated_at
		FROM connections ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.ConnectionRecord
	for rows.Next() {
		var c app.ConnectionRecord
		var enabled int
		if err := rows.Scan(&c.Name, &c.Kind, &c.ConfigCiphertext, &c.ToolsJSON, &enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (t *sqliteTx) UpsertConnection(ctx context.Context, c app.ConnectionRecord) error {
	en := 0
	if c.Enabled {
		en = 1
	}
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO connections (name, kind, config, tools, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			kind = excluded.kind,
			config = CASE WHEN length(excluded.config) > 0 THEN excluded.config ELSE connections.config END,
			tools = excluded.tools,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at`,
		c.Name, c.Kind, c.ConfigCiphertext, defaultJSON(c.ToolsJSON, `[]`), en, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert connection %q: %w", c.Name, err)
	}
	return nil
}

func (t *sqliteTx) DeleteConnection(ctx context.Context, name string) error {
	_, err := t.q.ExecContext(ctx, `DELETE FROM connections WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete connection %q: %w", name, err)
	}
	return nil
}

func scanToolCall(row interface{ Scan(dest ...any) error }) (app.ToolCallRecord, error) {
	var tc app.ToolCallRecord
	err := row.Scan(
		&tc.RunID, &tc.ToolUseID, &tc.ConversationID, &tc.ToolName, &tc.Class,
		&tc.TargetValue, &tc.InputJSON, &tc.IdempotencyKey, &tc.Status, &tc.Result,
		&tc.StartedAt, &tc.EndedAt,
	)
	return tc, err
}

func (r *sqliteReader) GetToolCall(ctx context.Context, runID domain.ID, toolUseID string) (app.ToolCallRecord, bool, error) {
	tc, err := scanToolCall(r.q.QueryRowContext(ctx, `
		SELECT run_id, tool_use_id, conversation_id, tool_name, class, target_value, input_json, idempotency_key, status, result, started_at, ended_at
		FROM tool_calls WHERE run_id = ? AND tool_use_id = ?`, runID, toolUseID))
	if errors.Is(err, sql.ErrNoRows) {
		return app.ToolCallRecord{}, false, nil
	}
	if err != nil {
		return app.ToolCallRecord{}, false, err
	}
	return tc, true, nil
}

func (r *sqliteReader) FindToolCallByIdem(ctx context.Context, idemKey string) (app.ToolCallRecord, bool, error) {
	tc, err := scanToolCall(r.q.QueryRowContext(ctx, `
		SELECT run_id, tool_use_id, conversation_id, tool_name, class, target_value, input_json, idempotency_key, status, result, started_at, ended_at
		FROM tool_calls
		WHERE idempotency_key = ? AND status IN ('started','done','ambiguous')
		LIMIT 1`, idemKey))
	if errors.Is(err, sql.ErrNoRows) {
		return app.ToolCallRecord{}, false, nil
	}
	if err != nil {
		return app.ToolCallRecord{}, false, err
	}
	return tc, true, nil
}

func (r *sqliteReader) FindDoneToolCallByTarget(ctx context.Context, convID domain.ID, toolName, targetVal string) (app.ToolCallRecord, bool, error) {
	tc, err := scanToolCall(r.q.QueryRowContext(ctx, `
		SELECT run_id, tool_use_id, conversation_id, tool_name, class, target_value, input_json, idempotency_key, status, result, started_at, ended_at
		FROM tool_calls
		WHERE conversation_id = ? AND tool_name = ? AND target_value = ? AND status = 'done'
		ORDER BY started_at DESC LIMIT 1`, convID, toolName, targetVal))
	if errors.Is(err, sql.ErrNoRows) {
		return app.ToolCallRecord{}, false, nil
	}
	if err != nil {
		return app.ToolCallRecord{}, false, err
	}
	return tc, true, nil
}

func (r *sqliteReader) ListToolCallsByRun(ctx context.Context, runID domain.ID) ([]app.ToolCallRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT run_id, tool_use_id, conversation_id, tool_name, class, target_value, input_json, idempotency_key, status, result, started_at, ended_at
		FROM tool_calls WHERE run_id = ? ORDER BY started_at ASC, tool_use_id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.ToolCallRecord
	for rows.Next() {
		tc, err := scanToolCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

func (t *sqliteTx) InsertToolCallStarted(ctx context.Context, tc app.ToolCallRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO tool_calls (run_id, tool_use_id, conversation_id, tool_name, class, target_value, input_json, idempotency_key, status, result, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'started', NULL, ?, NULL)`,
		tc.RunID, tc.ToolUseID, tc.ConversationID, tc.ToolName, tc.Class, tc.TargetValue, tc.InputJSON, tc.IdempotencyKey, tc.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("insert tool_call started: %w", err)
	}
	return nil
}

func (t *sqliteTx) TransitionToolCallStatus(
	ctx context.Context,
	runID domain.ID,
	toolUseID, fromStatus, toStatus string,
	result *string,
	endedAt *int64,
) (bool, error) {
	res, err := t.q.ExecContext(ctx, `
		UPDATE tool_calls SET status = ?, result = COALESCE(?, result), ended_at = COALESCE(?, ended_at)
		WHERE run_id = ? AND tool_use_id = ? AND status = ?`,
		toStatus, result, endedAt, runID, toolUseID, fromStatus,
	)
	if err != nil {
		return false, fmt.Errorf("transition tool_call %s/%s: %w", runID, toolUseID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
