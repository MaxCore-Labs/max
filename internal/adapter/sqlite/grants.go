package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetGrant(ctx context.Context, id domain.ID) (app.GrantRecord, bool, error) {
	var g app.GrantRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT id, agent_id, connection, tool, constraints, allowed_root_origins, max_per_day, used_today, used_day, expires_at, revoked_at, created_at
		FROM grants WHERE id = ?`, id,
	).Scan(
		&g.ID, &g.AgentID, &g.Connection, &g.Tool, &g.ConstraintsJSON, &g.AllowedRootOriginsJSON,
		&g.MaxPerDay, &g.UsedToday, &g.UsedDay, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return app.GrantRecord{}, false, nil
	}
	if err != nil {
		return app.GrantRecord{}, false, fmt.Errorf("get grant %q: %w", id, err)
	}
	return g, true, nil
}

func (r *sqliteReader) ListGrantsByAgent(ctx context.Context, agentID domain.ID) ([]app.GrantRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, agent_id, connection, tool, constraints, allowed_root_origins, max_per_day, used_today, used_day, expires_at, revoked_at, created_at
		FROM grants WHERE agent_id = ? ORDER BY created_at DESC, id DESC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list grants for agent %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []app.GrantRecord
	for rows.Next() {
		var g app.GrantRecord
		if err := rows.Scan(
			&g.ID, &g.AgentID, &g.Connection, &g.Tool, &g.ConstraintsJSON, &g.AllowedRootOriginsJSON,
			&g.MaxPerDay, &g.UsedToday, &g.UsedDay, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (t *sqliteTx) InsertGrant(ctx context.Context, g app.GrantRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO grants (id, agent_id, connection, tool, constraints, allowed_root_origins, max_per_day, used_today, used_day, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.AgentID, g.Connection, g.Tool,
		defaultJSON(g.ConstraintsJSON, `{}`),
		defaultJSON(g.AllowedRootOriginsJSON, `["owner","routine"]`),
		g.MaxPerDay, g.UsedToday, g.UsedDay, g.ExpiresAt, g.RevokedAt, g.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert grant %q: %w", g.ID, err)
	}
	return nil
}

func (t *sqliteTx) RevokeGrant(ctx context.Context, id domain.ID, revokedAt int64) error {
	_, err := t.q.ExecContext(ctx, `UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, revokedAt, id)
	if err != nil {
		return fmt.Errorf("revoke grant %q: %w", id, err)
	}
	return nil
}

func (t *sqliteTx) IncrementGrantUse(ctx context.Context, id domain.ID, day string) (bool, error) {
	g, ok, err := t.GetGrant(ctx, id)
	if err != nil {
		return false, err
	}
	if !ok || g.RevokedAt != nil {
		return false, nil
	}
	used := g.UsedToday
	if g.UsedDay != day {
		used = 0
	}
	if g.MaxPerDay != nil && used >= *g.MaxPerDay {
		return false, nil
	}
	used++
	if _, err := t.q.ExecContext(ctx, `UPDATE grants SET used_today = ?, used_day = ? WHERE id = ?`, used, day, id); err != nil {
		return false, fmt.Errorf("increment grant use %q: %w", id, err)
	}
	return true, nil
}
