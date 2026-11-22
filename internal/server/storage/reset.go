package storage

import (
	"context"
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

// CreatePasswordReset stores a reset for the user in place of a pending one,
// so only the latest link works. It returns sql.ErrNoRows for an unknown user.
func (s *Store) CreatePasswordReset(ctx context.Context, record PasswordResetRecord) error {
	if !validUUID(record.UserID) {
		return sql.ErrNoRows
	}
	if len(record.TokenHash) != 32 {
		return errors.New("token hash must be 32 bytes")
	}
	ctx, cancel := opContext(ctx)
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
	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	slog.Debug("storage password reset created", "user_id", record.UserID, "expires_at", record.ExpiresAt)
	return nil
}

// ResetMailLimits bound password-reset mail. An address gets a message at
// once, a second no sooner than Spacing after it, and then none until Window
// has passed since the second. All addresses together get Total in a Window.
type ResetMailLimits struct {
	Window  time.Duration
	Spacing time.Duration
	Total   int
}

var (
	// ErrResetMailHeld reports an address that has had its mail for now.
	ErrResetMailHeld = errors.New("the address has had its reset mail for now")
	// ErrResetMailCapped reports that the server has sent its mail for now.
	ErrResetMailCapped = errors.New("the server has sent its reset mail for now")
)

// resetMailLockKey serializes ReserveResetMail, so two requests cannot both
// take the last mail a limit allows.
const resetMailLockKey int64 = 0x6368657373726d // "chessrm"

// ReserveResetMail records a mail to the address, known by its hash, at now
// when the limits allow one; otherwise it returns ErrResetMailHeld or
// ErrResetMailCapped. A mail that is then not sent is given back with
// ReleaseResetMail.
func (s *Store) ReserveResetMail(ctx context.Context, addressHash []byte, now time.Time, limits ResetMailLimits) error {
	if len(addressHash) != 32 {
		return errors.New("address hash must be 32 bytes")
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reserve reset mail: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, resetMailLockKey); err != nil {
		return fmt.Errorf("reserve reset mail: %w", err)
	}
	// The address's last two mails, newest first, NULL where there is none.
	var last, before sql.NullTime
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT sent_at FROM reset_mails WHERE address_hash = $1 ORDER BY sent_at DESC LIMIT 1),
		(SELECT sent_at FROM reset_mails WHERE address_hash = $1 ORDER BY sent_at DESC LIMIT 1 OFFSET 1),
		(SELECT count(*) FROM reset_mails WHERE sent_at > $2)`,
		addressHash, now.Add(-limits.Window)).Scan(&last, &before, &total); err != nil {
		return fmt.Errorf("reserve reset mail: %w", err)
	}
	if last.Valid && now.Sub(last.Time) < limits.Window {
		second := before.Valid && last.Time.Sub(before.Time) < limits.Window
		if second || now.Sub(last.Time) < limits.Spacing {
			return ErrResetMailHeld
		}
	}
	if total >= limits.Total {
		return ErrResetMailCapped
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reset_mails (address_hash, sent_at) VALUES ($1, $2)`,
		addressHash, now); err != nil {
		return fmt.Errorf("reserve reset mail: %w", err)
	}
	return tx.Commit()
}

// ReleaseResetMail takes back the mail reserved for the address at that time.
func (s *Store) ReleaseResetMail(ctx context.Context, addressHash []byte, at time.Time) error {
	ctx, cancel := opContext(ctx)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM reset_mails WHERE address_hash = $1 AND sent_at = $2`, addressHash, at)
	return err
}

// DeleteResetMailsBefore forgets the mail sent before cutoff. A limit looks
// back two windows: at an address's last mail and the one before it.
func (s *Store) DeleteResetMailsBefore(ctx context.Context, cutoff time.Time) error {
	ctx, cancel := opContext(ctx)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM reset_mails WHERE sent_at < $1`, cutoff)
	return err
}

// ConsumePasswordReset deletes the reset matching tokenHash and returns its
// user, or sql.ErrNoRows when none is pending or it has expired. A token
// works once.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash []byte, now time.Time) (string, error) {
	if len(tokenHash) != 32 {
		return "", sql.ErrNoRows
	}
	ctx, cancel := opContext(ctx)
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

func (s *Store) DeleteExpiredPasswordResets(ctx context.Context) (int64, error) {
	ctx, cancel := opContext(ctx)
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
func (s *Store) ResetUserPassword(ctx context.Context, userID, passwordHash string) error {
	if !validUUID(userID) {
		return sql.ErrNoRows
	}
	ctx, cancel := opContext(ctx)
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
