package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetThread(ctx context.Context, id domain.ID) (app.ThreadRecord, bool, error) {
	var th app.ThreadRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, title, learned_through_msg, last_msg_at, created_at, updated_at
		FROM threads WHERE id = ?`, id,
	).Scan(&th.ID, &th.AgentID, &th.Title, &th.LearnedThroughMsg, &th.LastMsgAt, &th.CreatedAt, &th.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ThreadRecord{}, false, nil
	}
	if err != nil {
		return app.ThreadRecord{}, false, fmt.Errorf("get thread %q: %w", id, err)
	}
	return th, true, nil
}

func (r *sqliteReader) ListThreads(ctx context.Context, limit int, beforeUpdatedMs *int64, beforeID string) ([]app.ThreadRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if beforeUpdatedMs != nil {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, agent_id, title, learned_through_msg, last_msg_at, created_at, updated_at
			FROM threads
			WHERE (updated_at < ? OR (updated_at = ? AND id < ?))
			ORDER BY updated_at DESC, id DESC LIMIT ?`,
			*beforeUpdatedMs, *beforeUpdatedMs, beforeID, limit)
	} else {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, agent_id, title, learned_through_msg, last_msg_at, created_at, updated_at
			FROM threads ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.ThreadRecord
	for rows.Next() {
		var th app.ThreadRecord
		if err := rows.Scan(&th.ID, &th.AgentID, &th.Title, &th.LearnedThroughMsg, &th.LastMsgAt, &th.CreatedAt, &th.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, th)
	}
	return out, rows.Err()
}

func (r *sqliteReader) GetConversationByThread(ctx context.Context, agentID, threadID domain.ID) (app.ConversationRecord, bool, error) {
	var c app.ConversationRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, task_id, thread_id, sessions, last_engine, waiting_for, turns_used, closed_at
		FROM conversations WHERE agent_id = ? AND thread_id = ?`,
		agentID, threadID,
	).Scan(&c.ID, &c.AgentID, &c.TaskID, &c.ThreadID, &c.Sessions, &c.LastEngine, &c.WaitingFor, &c.TurnsUsed, &c.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ConversationRecord{}, false, nil
	}
	if err != nil {
		return app.ConversationRecord{}, false, fmt.Errorf("get conversation by thread %q: %w", threadID, err)
	}
	return c, true, nil
}

func (r *sqliteReader) GetMessageByIdem(ctx context.Context, idemKey string) (app.MessageRecord, bool, error) {
	var m app.MessageRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, thread_id, role, content, attachments, run_id, idempotency_key, created_at
		FROM messages WHERE idempotency_key = ?`, idemKey,
	).Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.AttachmentsJSON, &m.RunID, &m.IdempotencyKey, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.MessageRecord{}, false, nil
	}
	if err != nil {
		return app.MessageRecord{}, false, fmt.Errorf("get message by idempotency_key: %w", err)
	}
	return m, true, nil
}

func (r *sqliteReader) ListThreadMessages(
	ctx context.Context,
	threadID domain.ID,
	limit int,
	beforeCreatedMs *int64,
	beforeID string,
) ([]app.MessageRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if beforeCreatedMs != nil {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, thread_id, role, content, attachments, run_id, idempotency_key, created_at
			FROM messages
			WHERE thread_id = ? AND (created_at < ? OR (created_at = ? AND id < ?))
			ORDER BY created_at DESC, id DESC LIMIT ?`,
			threadID, *beforeCreatedMs, *beforeCreatedMs, beforeID, limit)
	} else {
		rows, err = r.q.QueryContext(ctx, `
			SELECT id, thread_id, role, content, attachments, run_id, idempotency_key, created_at
			FROM messages
			WHERE thread_id = ?
			ORDER BY created_at DESC, id DESC LIMIT ?`,
			threadID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list thread messages: %w", err)
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

func (t *sqliteTx) InsertThread(ctx context.Context, th app.ThreadRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO threads (id, agent_id, title, learned_through_msg, last_msg_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		th.ID, th.AgentID, th.Title, th.LearnedThroughMsg, th.LastMsgAt, th.CreatedAt, th.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert thread %q: %w", th.ID, err)
	}
	return nil
}

func (t *sqliteTx) TouchThread(ctx context.Context, threadID domain.ID, msgAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE threads SET last_msg_at = ?, updated_at = ? WHERE id = ?`,
		msgAt, msgAt, threadID,
	)
	if err != nil {
		return fmt.Errorf("touch thread %q: %w", threadID, err)
	}
	return nil
}

func (t *sqliteTx) EnsureThreadConversation(ctx context.Context, agentID, threadID, newConvID domain.ID) (domain.ID, error) {
	c, ok, err := t.GetConversationByThread(ctx, agentID, threadID)
	if err != nil {
		return "", err
	}
	if ok {
		return c.ID, nil
	}
	tid := threadID
	if err := t.InsertConversation(ctx, app.ConversationRecord{
		ID:       newConvID,
		AgentID:  agentID,
		ThreadID: &tid,
		Sessions: `{}`,
	}); err != nil {
		return "", err
	}
	return newConvID, nil
}

func (t *sqliteTx) ResetThreadConversation(ctx context.Context, agentID, threadID domain.ID) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE conversations SET sessions = '{}', last_engine = NULL, waiting_for = NULL
		WHERE agent_id = ? AND thread_id = ?`, agentID, threadID)
	if err != nil {
		return fmt.Errorf("reset thread conversation: %w", err)
	}
	return nil
}

func (t *sqliteTx) InsertMessage(ctx context.Context, m app.MessageRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO messages (id, thread_id, role, content, attachments, run_id, idempotency_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ThreadID, m.Role, m.Content, defaultJSON(m.AttachmentsJSON, `[]`),
		m.RunID, m.IdempotencyKey, m.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert message %q: %w", m.ID, err)
	}
	return nil
}
