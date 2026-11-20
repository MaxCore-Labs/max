package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) ListThreadMessagesAsc(ctx context.Context, threadID domain.ID, afterMsgID *domain.ID, limit int) ([]app.MessageRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	var (
		rows *sql.Rows
		err  error
	)
	if afterMsgID != nil && *afterMsgID != "" {
		var afterTS int64
		qErr := r.q.QueryRowContext(ctx, `SELECT created_at FROM messages WHERE id = ? AND thread_id = ?`, *afterMsgID, threadID).Scan(&afterTS)
		if qErr == nil {
			rows, err = r.q.QueryContext(ctx, `
				SELECT id, thread_id, role, content, attachments, run_id, idempotency_key, created_at
				FROM messages
				WHERE thread_id = ? AND (created_at > ? OR (created_at = ? AND id > ?))
				ORDER BY created_at ASC, id ASC LIMIT ?`,
				threadID, afterTS, afterTS, *afterMsgID, limit)
		}
	}
	if rows == nil && err == nil {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, thread_id, role, content, attachments, run_id, idempotency_key, created_at
			FROM messages WHERE thread_id = ? ORDER BY created_at ASC, id ASC LIMIT ?`,
			threadID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list thread messages asc: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.MessageRecord
	for rows.Next() {
		var m app.MessageRecord
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.AttachmentsJSON, &m.RunID, &m.IdempotencyKey, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (t *sqliteTx) SetThreadLearnedThrough(ctx context.Context, threadID, msgID domain.ID) error {
	_, err := t.q.ExecContext(ctx, `UPDATE threads SET learned_through_msg = ? WHERE id = ?`, msgID, threadID)
	if err != nil {
		return fmt.Errorf("set learned_through_msg on thread %q: %w", threadID, err)
	}
	return nil
}

func (t *sqliteTx) InsertProposal(ctx context.Context, p app.ProposalRecord) error {
	status := p.Status
	if status == "" {
		status = "pending"
	}
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO proposals (id, kind, agent_id, target_path, title, payload, status, decided_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Kind, p.AgentID, p.TargetPath, p.Title, defaultJSON(p.PayloadJSON, `{}`), status, p.DecidedAt, p.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert proposal %q: %w", p.ID, err)
	}
	return nil
}

func (r *sqliteReader) GetProposal(ctx context.Context, id domain.ID) (app.ProposalRecord, bool, error) {
	var p app.ProposalRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, kind, agent_id, target_path, title, payload, status, decided_at, created_at
		FROM proposals WHERE id = ?`, id,
	).Scan(&p.ID, &p.Kind, &p.AgentID, &p.TargetPath, &p.Title, &p.PayloadJSON, &p.Status, &p.DecidedAt, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ProposalRecord{}, false, nil
	}
	if err != nil {
		return app.ProposalRecord{}, false, fmt.Errorf("get proposal %q: %w", id, err)
	}
	return p, true, nil
}

func (r *sqliteReader) ListProposals(ctx context.Context, status string, limit int) ([]app.ProposalRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if status != "" {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, kind, agent_id, target_path, title, payload, status, decided_at, created_at
			FROM proposals WHERE status = ? ORDER BY created_at DESC, id DESC LIMIT ?`, status, limit)
	} else {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, kind, agent_id, target_path, title, payload, status, decided_at, created_at
			FROM proposals ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.ProposalRecord
	for rows.Next() {
		var p app.ProposalRecord
		if err := rows.Scan(&p.ID, &p.Kind, &p.AgentID, &p.TargetPath, &p.Title, &p.PayloadJSON, &p.Status, &p.DecidedAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *sqliteTx) UpdateProposalStatus(ctx context.Context, id domain.ID, status string, decidedAt int64) error {
	_, err := t.q.ExecContext(ctx, `UPDATE proposals SET status = ?, decided_at = ? WHERE id = ?`, status, decidedAt, id)
	if err != nil {
		return fmt.Errorf("update proposal %q status: %w", id, err)
	}
	return nil
}
