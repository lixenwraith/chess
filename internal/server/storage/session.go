package storage

import (
	"fmt"
	"log/slog"
	"time"
)

// CreateSession creates or replaces the session for a user (single session per user)
func (s *Store) CreateSession(record SessionRecord) error {
	const query = `INSERT INTO sessions (session_id, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			session_id = excluded.session_id,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at`
	if _, err := s.db.Exec(query, record.SessionID, record.UserID, record.CreatedAt, record.ExpiresAt); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	slog.Debug("storage session created", "user_id", record.UserID, "expires_at", record.ExpiresAt)
	return nil
}

// GetSession retrieves a session by ID
func (s *Store) GetSession(sessionID string) (*SessionRecord, error) {
	var session SessionRecord
	query := `SELECT session_id, user_id, created_at, expires_at FROM sessions WHERE session_id = ?`

	err := s.db.QueryRow(query, sessionID).Scan(
		&session.SessionID, &session.UserID, &session.CreatedAt, &session.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// GetSessionByUserID retrieves the active session for a user
func (s *Store) GetSessionByUserID(userID string) (*SessionRecord, error) {
	var session SessionRecord
	query := `SELECT session_id, user_id, created_at, expires_at FROM sessions WHERE user_id = ?`

	err := s.db.QueryRow(query, userID).Scan(
		&session.SessionID, &session.UserID, &session.CreatedAt, &session.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// DeleteSession removes a session
func (s *Store) DeleteSession(sessionID string) error {
	query := `DELETE FROM sessions WHERE session_id = ?`
	_, err := s.db.Exec(query, sessionID)
	if err == nil {
		slog.Debug("storage session deleted")
	}
	return err
}

// DeleteSessionByUserID removes all sessions for a user
func (s *Store) DeleteSessionByUserID(userID string) error {
	query := `DELETE FROM sessions WHERE user_id = ?`
	_, err := s.db.Exec(query, userID)
	if err == nil {
		slog.Debug("storage user sessions deleted", "user_id", userID)
	}
	return err
}

// DeleteExpiredSessions removes expired sessions
func (s *Store) DeleteExpiredSessions() (int64, error) {
	query := `DELETE FROM sessions WHERE expires_at < ?`
	result, err := s.db.Exec(query, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err == nil && deleted > 0 {
		slog.Debug("storage expired sessions deleted", "count", deleted)
	}
	return deleted, err
}

// IsSessionValid checks if a session exists and is not expired
func (s *Store) IsSessionValid(sessionID string) (bool, error) {
	var valid bool
	const query = `SELECT EXISTS(
		SELECT 1 FROM sessions WHERE session_id = ? AND expires_at > ?
	)`
	err := s.db.QueryRow(query, sessionID, time.Now().UTC()).Scan(&valid)
	if err != nil {
		return false, err
	}
	return valid, nil
}

// IsSessionValidForUser verifies both expiry and the binding between a JWT
// subject and its persisted session. Checking only the session ID would allow
// a malformed server-issued token to authenticate as the wrong subject.
func (s *Store) IsSessionValidForUser(sessionID, userID string) (bool, error) {
	var valid bool
	const query = `SELECT EXISTS(
		SELECT 1 FROM sessions
		WHERE session_id = ? AND user_id = ? AND expires_at > ?
	)`
	err := s.db.QueryRow(query, sessionID, userID, time.Now().UTC()).Scan(&valid)
	if err != nil {
		return false, err
	}
	return valid, nil
}
