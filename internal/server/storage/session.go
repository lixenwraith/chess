package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// CreateSession creates or replaces the user's single session and records the
// login time in the same statement, so a login is one round trip.
func (s *Store) CreateSession(record SessionRecord) error {
	ctx, cancel := opContext()
	defer cancel()
	const query = `WITH session AS (
			INSERT INTO sessions (session_id, user_id, created_at, expires_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id) DO UPDATE SET
				session_id = excluded.session_id,
				created_at = excluded.created_at,
				expires_at = excluded.expires_at
			RETURNING user_id
		)
		UPDATE users SET last_login_at = $3 WHERE user_id = (SELECT user_id FROM session)`
	if _, err := s.db.ExecContext(ctx, query,
		record.SessionID, record.UserID, record.CreatedAt, record.ExpiresAt,
	); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	slog.Debug("storage session created", "user_id", record.UserID, "expires_at", record.ExpiresAt)
	return nil
}

// GetSession retrieves a session by ID
func (s *Store) GetSession(sessionID string) (*SessionRecord, error) {
	if !validUUID(sessionID) {
		return nil, sql.ErrNoRows
	}
	ctx, cancel := opContext()
	defer cancel()
	var session SessionRecord
	err := s.db.QueryRowContext(ctx,
		`SELECT session_id, user_id, created_at, expires_at FROM sessions WHERE session_id = $1`,
		sessionID,
	).Scan(&session.SessionID, &session.UserID, &session.CreatedAt, &session.ExpiresAt)
	if err != nil {
		return nil, err
	}
	session.CreatedAt = session.CreatedAt.UTC()
	session.ExpiresAt = session.ExpiresAt.UTC()
	return &session, nil
}

// DeleteSession removes a session (logout). Deleting an absent session is not
// an error.
func (s *Store) DeleteSession(sessionID string) error {
	if !validUUID(sessionID) {
		return nil
	}
	ctx, cancel := opContext()
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE session_id = $1`, sessionID); err != nil {
		return err
	}
	slog.Debug("storage session deleted")
	return nil
}

// DeleteExpiredSessions removes expired sessions
func (s *Store) DeleteExpiredSessions() (int64, error) {
	ctx, cancel := opContext()
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < $1`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err == nil && deleted > 0 {
		slog.Debug("storage expired sessions deleted", "count", deleted)
	}
	return deleted, err
}

// IsSessionValidForUser verifies both expiry and the binding between a JWT
// subject and its persisted session. Checking only the session ID would allow
// a malformed server-issued token to authenticate as the wrong subject.
func (s *Store) IsSessionValidForUser(sessionID, userID string) (bool, error) {
	if !validUUID(sessionID) || !validUUID(userID) {
		return false, nil
	}
	ctx, cancel := opContext()
	defer cancel()
	var valid bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM sessions
		WHERE session_id = $1 AND user_id = $2 AND expires_at > $3
	)`, sessionID, userID, time.Now().UTC()).Scan(&valid)
	if err != nil {
		return false, err
	}
	return valid, nil
}
