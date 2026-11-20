package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"max/internal/app"
	"max/internal/domain"
)

func (r *sqliteReader) GetOwnerUser(ctx context.Context) (app.UserRecord, bool, error) {
	var (
		u    app.UserRecord
		totp int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, username, password_hash, totp_enabled, created_at, updated_at
		FROM users ORDER BY created_at ASC LIMIT 1`,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &totp, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.UserRecord{}, false, nil
	}
	if err != nil {
		return app.UserRecord{}, false, fmt.Errorf("get owner user: %w", err)
	}
	u.TOTPEnabled = totp != 0
	return u, true, nil
}

func (r *sqliteReader) GetUserByID(ctx context.Context, id domain.ID) (app.UserRecord, bool, error) {
	var (
		u    app.UserRecord
		totp int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, username, password_hash, totp_enabled, created_at, updated_at
		FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &totp, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.UserRecord{}, false, nil
	}
	if err != nil {
		return app.UserRecord{}, false, fmt.Errorf("get user %q: %w", id, err)
	}
	u.TOTPEnabled = totp != 0
	return u, true, nil
}

func (r *sqliteReader) GetAuthSession(ctx context.Context, tokenHash string) (app.AuthSessionRecord, bool, error) {
	var s app.AuthSessionRecord
	err := r.q.QueryRowContext(ctx, `
		SELECT token_hash, user_id, expires_at, created_at
		FROM auth_sessions WHERE token_hash = ?`, tokenHash,
	).Scan(&s.TokenHash, &s.UserID, &s.ExpiresAt, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.AuthSessionRecord{}, false, nil
	}
	if err != nil {
		return app.AuthSessionRecord{}, false, fmt.Errorf("get auth session: %w", err)
	}
	return s, true, nil
}

func (r *sqliteReader) ListAllAuthSessionHashes(ctx context.Context) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT token_hash FROM auth_sessions`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (t *sqliteTx) UpsertOwnerUser(ctx context.Context, u app.UserRecord) error {
	username := defaultJSON(u.Username, "owner")
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, totp_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET
			password_hash = excluded.password_hash,
			updated_at = excluded.updated_at`,
		u.ID, username, u.PasswordHash, boolInt(u.TOTPEnabled), u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert owner user: %w", err)
	}
	return nil
}

func (t *sqliteTx) InsertAuthSession(ctx context.Context, s app.AuthSessionRecord) error {
	_, err := t.q.ExecContext(ctx, `
		INSERT INTO auth_sessions (token_hash, user_id, expires_at, created_at)
		VALUES (?, ?, ?, ?)`,
		s.TokenHash, s.UserID, s.ExpiresAt, s.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert auth session: %w", err)
	}
	return nil
}

func (t *sqliteTx) DeleteAuthSession(ctx context.Context, tokenHash string) error {
	_, err := t.q.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete auth session: %w", err)
	}
	return nil
}

func (r *sqliteReader) GetOwnerTOTP(ctx context.Context) (app.TOTPRecord, bool, error) {
	var (
		rec  app.TOTPRecord
		totp int
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT id, username, totp_secret, totp_enabled, totp_pending, totp_failed_attempts, totp_locked_until
		FROM users ORDER BY created_at ASC LIMIT 1`,
	).Scan(&rec.UserID, &rec.Username, &rec.TOTPSecret, &totp, &rec.TOTPPending, &rec.FailedAttempts, &rec.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return app.TOTPRecord{}, false, nil
	}
	if err != nil {
		return app.TOTPRecord{}, false, fmt.Errorf("get owner totp: %w", err)
	}
	rec.TOTPEnabled = totp != 0
	return rec, true, nil
}

func (t *sqliteTx) SetOwnerTOTPPending(ctx context.Context, userID domain.ID, pending []byte, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `UPDATE users SET totp_pending = ?, updated_at = ? WHERE id = ?`, pending, updatedAt, userID)
	return err
}

func (t *sqliteTx) ConfirmOwnerTOTP(ctx context.Context, userID domain.ID, secret []byte, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE users SET totp_secret = ?, totp_enabled = 1, totp_pending = NULL, totp_failed_attempts = 0, totp_locked_until = NULL, updated_at = ?
		WHERE id = ?`, secret, updatedAt, userID)
	return err
}

func (t *sqliteTx) RecordOwnerTOTPFailure(ctx context.Context, userID domain.ID, attempts int, lockedUntil *int64, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE users SET totp_failed_attempts = ?, totp_locked_until = ?, updated_at = ? WHERE id = ?`,
		attempts, lockedUntil, updatedAt, userID)
	return err
}

func (t *sqliteTx) ResetOwnerTOTPFailures(ctx context.Context, userID domain.ID, updatedAt int64) error {
	_, err := t.q.ExecContext(ctx, `
		UPDATE users SET totp_failed_attempts = 0, totp_locked_until = NULL, updated_at = ? WHERE id = ?`,
		updatedAt, userID)
	return err
}
