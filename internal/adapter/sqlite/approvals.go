package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetApproval(ctx context.Context, id domain.ID) (app.ApprovalRecord, bool, error) {
	var (
		ap    app.ApprovalRecord
		byMax int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, run_id, agent_id, conversation_id, tool_name, input, input_hash,
		       preview, reason, by_max, status, decision_key, requested_at, expires_at, decided_at
		FROM approvals WHERE id = ?`, id,
	).Scan(
		&ap.ID, &ap.RunID, &ap.AgentID, &ap.ConversationID, &ap.ToolName, &ap.Input, &ap.InputHash,
		&ap.Preview, &ap.Reason, &byMax, &ap.Status, &ap.DecisionKey, &ap.RequestedAt, &ap.ExpiresAt, &ap.DecidedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ApprovalRecord{}, false, nil
	}
	if err != nil {
		return app.ApprovalRecord{}, false, fmt.Errorf("get approval %q: %w", id, err)
	}
	ap.ByMax = byMax != 0
	return ap, true, nil
}

func (r *sqliteReader) FindPendingApproval(ctx context.Context, agentID domain.ID, toolName, inputHash string) (app.ApprovalRecord, bool, error) {
	var (
		ap    app.ApprovalRecord
		byMax int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, run_id, agent_id, conversation_id, tool_name, input, input_hash,
		       preview, reason, by_max, status, decision_key, requested_at, expires_at, decided_at
		FROM approvals
		WHERE agent_id = ? AND tool_name = ? AND input_hash = ? AND status = 'pending'
		ORDER BY requested_at ASC LIMIT 1`,
		agentID, toolName, inputHash,
	).Scan(
		&ap.ID, &ap.RunID, &ap.AgentID, &ap.ConversationID, &ap.ToolName, &ap.Input, &ap.InputHash,
		&ap.Preview, &ap.Reason, &byMax, &ap.Status, &ap.DecisionKey, &ap.RequestedAt, &ap.ExpiresAt, &ap.DecidedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ApprovalRecord{}, false, nil
	}
	if err != nil {
		return app.ApprovalRecord{}, false, fmt.Errorf("find pending approval: %w", err)
	}
	ap.ByMax = byMax != 0
	return ap, true, nil
}

func (t *sqliteTx) InsertApproval(ctx context.Context, ap app.ApprovalRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO approvals (
			id, run_id, agent_id, conversation_id, tool_name, input, input_hash,
			preview, reason, by_max, status, decision_key, requested_at, expires_at, decided_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ap.ID, ap.RunID, ap.AgentID, ap.ConversationID, ap.ToolName, ap.Input, ap.InputHash,
		ap.Preview, ap.Reason, boolInt(ap.ByMax), defaultJSON(ap.Status, "pending"),
		ap.DecisionKey, ap.RequestedAt, ap.ExpiresAt, ap.DecidedAt,
	)
	if err != nil {
		return fmt.Errorf("insert approval %q: %w", ap.ID, err)
	}
	return nil
}

func (t *sqliteTx) UpdateApprovalDecision(ctx context.Context, id domain.ID, status string, decisionKey *string, decidedAt *int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE approvals
		SET status = ?,
		    decision_key = COALESCE(?, decision_key),
		    decided_at = COALESCE(?, decided_at)
		WHERE id = ?`,
		status, decisionKey, decidedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update approval %q decision: %w", id, err)
	}
	return nil
}

func (t *sqliteTx) ConsumeAllowedApproval(ctx context.Context, agentID domain.ID, toolName, inputHash string, nowMs int64) (bool, error) {
	var id string
	err := t.q.QueryRowContext(ctx, `
		SELECT id FROM approvals
		WHERE agent_id = ? AND tool_name = ? AND input_hash = ? AND by_max = 0 AND status = 'allowed' AND expires_at > ?
		ORDER BY requested_at ASC LIMIT 1`,
		agentID, toolName, inputHash, nowMs,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup allowed approval: %w", err)
	}
	res, err := t.q.ExecContext(ctx, `UPDATE approvals SET status = 'consumed' WHERE id = ? AND status = 'allowed'`, id)
	if err != nil {
		return false, fmt.Errorf("consume approval %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *sqliteTx) ListExpiredPendingApprovals(ctx context.Context, nowMs int64) ([]app.ApprovalRecord, error) {
	rows, err := t.q.QueryContext(ctx, `
		SELECT id, run_id, agent_id, conversation_id, tool_name, input, input_hash,
		       preview, reason, by_max, status, decision_key, requested_at, expires_at, decided_at
		FROM approvals WHERE status = 'pending' AND expires_at <= ?`, nowMs)
	if err != nil {
		return nil, fmt.Errorf("list expired approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.ApprovalRecord
	for rows.Next() {
		var (
			ap    app.ApprovalRecord
			byMax int
		)
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

func (t *sqliteTx) ResolveInboxByRef(ctx context.Context, refKind, refID string, resolvedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE inbox_items SET resolved_at = ?
		WHERE ref_kind = ? AND ref_id = ? AND resolved_at IS NULL`,
		resolvedAt, refKind, refID,
	)
	if err != nil {
		return fmt.Errorf("resolve inbox by ref: %w", err)
	}
	return nil
}
