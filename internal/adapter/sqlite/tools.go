package sqlite

import (
	"context"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) ListOutboxPending(ctx context.Context, limit int) ([]app.OutboxRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, topic, payload, dedupe_key, urgency, attempts, next_attempt_at, sent_at
		FROM outbox WHERE sent_at IS NULL ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.OutboxRecord
	for rows.Next() {
		var rec app.OutboxRecord
		if err := rows.Scan(
			&rec.ID, &rec.Topic, &rec.PayloadJSON, &rec.DedupeKey,
			&rec.Urgency, &rec.Attempts, &rec.NextAttemptAt, &rec.SentAt,
		); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *sqliteReader) CountQuestionsByRun(ctx context.Context, runID domain.ID) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM inbox_items
		WHERE kind = 'question' AND ref_kind = 'run' AND ref_id = ?`, runID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count questions for run %q: %w", runID, err)
	}
	return n, nil
}

func (t *sqliteTx) SetConversationWaitingFor(ctx context.Context, convID domain.ID, waitingFor *string) error {
	_, err := t.q.ExecContext(ctx, `UPDATE conversations SET waiting_for = ? WHERE id = ?`, waitingFor, convID)
	if err != nil {
		return fmt.Errorf("set conversation waiting_for %q: %w", convID, err)
	}
	return nil
}

func (t *sqliteTx) InsertOutbox(ctx context.Context, rec app.OutboxRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO outbox (topic, payload, dedupe_key, urgency, attempts, next_attempt_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) DO NOTHING`,
		rec.Topic, defaultJSON(rec.PayloadJSON, `{}`), rec.DedupeKey,
		defaultJSON(rec.Urgency, "normal"), rec.Attempts, rec.NextAttemptAt, rec.SentAt,
	)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}
