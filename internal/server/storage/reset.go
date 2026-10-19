package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// PasswordResetRecord is a pending password reset: the SHA-256 of the token
// sent to the account's email, and when it stops working.
type PasswordResetRecord struct {
	UserID    string
	TokenHash []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreatePasswordReset stores a reset for the user, replacing any pending one,
// so only the latest link works. It returns sql.ErrNoRows for an unknown
// user.
func (s *Store) CreatePasswordReset(record PasswordResetRecord) error {
	if !validUUID(record.UserID) {
		return sql.ErrNoRows
	}
	if len(record.TokenHash) != 32 {
		return errors.New("token hash must be 32 bytes")
	}
	ctx, cancel := opContext()
	defer cancel()
	result, err := s.db.ExecContext(ctx, `INSERT INTO password_resets (user_id, token_hash, created_at, expires_at)
		SELECT user_id, $2, $3, $4 FROM users WHERE user_id = $1
		ON CONFLICT (user_id) DO UPDATE SET
			token_hash = excluded.token_hash,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		record.UserID, record.TokenHash, record.CreatedAt, record.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create password reset: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return sql.ErrNoRows
	}
	slog.Debug("storage password reset created", "user_id", record.UserID, "expires_at", record.ExpiresAt)
	return nil
}

// ConsumePasswordReset deletes the reset matching tokenHash and returns its
// user, or sql.ErrNoRows when none is pending or it has expired. A token
// works once.
func (s *Store) ConsumePasswordReset(tokenHash []byte, now time.Time) (string, error) {
	if len(tokenHash) != 32 {
		return "", sql.ErrNoRows
	}
	ctx, cancel := opContext()
	defer cancel()
	var userID string
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM password_resets WHERE token_hash = $1 AND expires_at > $2 RETURNING user_id`,
		tokenHash, now.UTC()).Scan(&userID)
	if err != nil {
		return "", err
	}
	return userID, nil
}

// DeleteExpiredPasswordResets removes resets past their expiry.
func (s *Store) DeleteExpiredPasswordResets() (int64, error) {
	ctx, cancel := opContext()
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM password_resets WHERE expires_at < $1`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err == nil && deleted > 0 {
		slog.Debug("storage expired password resets deleted", "count", deleted)
	}
	return deleted, err
}

// ResetUserPassword sets a new password hash and ends the user's sessions in
// one transaction: whoever held the old password is signed out everywhere.
// It returns sql.ErrNoRows for an unknown user.
func (s *Store) ResetUserPassword(userID, passwordHash string) error {
	if !validUUID(userID) {
		return sql.ErrNoRows
	}
	ctx, cancel := opContext()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = $2 WHERE user_id = $1`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clear password resets: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Debug("storage password reset", "user_id", userID)
	return nil
}
