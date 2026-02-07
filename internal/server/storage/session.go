package storage

import (
	"fmt"
	"time"
)

// CreateSession creates or replaces the session for a user (single session per user)
func (s *Store) CreateSession(record SessionRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete any existing session for this user
	deleteQuery := `DELETE FROM sessions WHERE user_id = ?`
	if _, err := tx.Exec(deleteQuery, record.UserID); err != nil {
		return fmt.Errorf("failed to delete existing session: %w", err)
	}

	// Insert new session
	insertQuery := `INSERT INTO sessions (session_id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`
	if _, err := tx.Exec(insertQuery, record.SessionID, record.UserID, record.CreatedAt, record.ExpiresAt); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	return tx.Commit()
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
	return err
}

// DeleteSessionByUserID removes all sessions for a user
func (s *Store) DeleteSessionByUserID(userID string) error {
	query := `DELETE FROM sessions WHERE user_id = ?`
	_, err := s.db.Exec(query, userID)
	return err
}

// DeleteExpiredSessions removes expired sessions
func (s *Store) DeleteExpiredSessions() (int64, error) {
	query := `DELETE FROM sessions WHERE expires_at < ?`
	result, err := s.db.Exec(query, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// IsSessionValid checks if a session exists and is not expired
func (s *Store) IsSessionValid(sessionID string) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM sessions WHERE session_id = ? AND expires_at > ?`
	err := s.db.QueryRow(query, sessionID, time.Now().UTC()).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}