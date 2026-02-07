package storage

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

// UserLimits defines registration constraints
type UserLimits struct {
	MaxUsers       int
	PermanentSlots int
	TempTTL        time.Duration
}

// DefaultUserLimits returns default POC limits
func DefaultUserLimits() UserLimits {
	return UserLimits{
		MaxUsers:       100,
		PermanentSlots: 10,
		TempTTL:        24 * time.Hour,
	}
}

// GetUserCounts returns current user counts by type
func (s *Store) GetUserCounts() (total, permanent, temp int, err error) {
	query := `SELECT 
		COUNT(*) as total,
		SUM(CASE WHEN account_type = 'permanent' THEN 1 ELSE 0 END) as permanent,
		SUM(CASE WHEN account_type = 'temp' THEN 1 ELSE 0 END) as temp
	FROM users`

	err = s.db.QueryRow(query).Scan(&total, &permanent, &temp)
	return
}

// GetOldestTempUser returns the oldest temporary user for replacement
func (s *Store) GetOldestTempUser() (*UserRecord, error) {
	var user UserRecord
	query := `SELECT user_id, username, email, password_hash, account_type, created_at, expires_at, last_login_at
		FROM users 
		WHERE account_type = 'temp'
		ORDER BY created_at ASC
		LIMIT 1`

	err := s.db.QueryRow(query).Scan(
		&user.UserID, &user.Username, &user.Email,
		&user.PasswordHash, &user.AccountType, &user.CreatedAt,
		&user.ExpiresAt, &user.LastLoginAt,
	)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// DeleteExpiredTempUsers removes temporary users past their expiry
func (s *Store) DeleteExpiredTempUsers() (int64, error) {
	query := `DELETE FROM users WHERE account_type = 'temp' AND expires_at < ?`
	result, err := s.db.Exec(query, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CreateUser creates user with transaction isolation to prevent race conditions
func (s *Store) CreateUser(record UserRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Check uniqueness within transaction
	exists, err := s.userExists(tx, record.Username, record.Email)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("username or email already exists")
	}

	// Insert user
	query := `INSERT INTO users (
		user_id, username, email, password_hash, account_type, created_at, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err = tx.Exec(query,
		record.UserID, record.Username, record.Email,
		record.PasswordHash, record.AccountType, record.CreatedAt, record.ExpiresAt,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// DeleteUserByID removes a user by ID (synchronous, for replacement logic)
func (s *Store) DeleteUserByID(userID string) error {
	query := `DELETE FROM users WHERE user_id = ?`
	_, err := s.db.Exec(query, userID)
	return err
}

// PromoteToPermament upgrades a temp user to permanent
func (s *Store) PromoteToPermanent(userID string) error {
	query := `UPDATE users SET account_type = 'permanent', expires_at = NULL WHERE user_id = ?`
	_, err := s.db.Exec(query, userID)
	return err
}

// userExists verifies username/email uniqueness within a transaction
func (s *Store) userExists(tx *sql.Tx, username, email string) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE`
	args := []any{username}

	if email != "" {
		query = `SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE OR email = ? COLLATE NOCASE`
		args = append(args, email)
	}

	err := tx.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateUserPassword updates user password hash
func (s *Store) UpdateUserPassword(userID string, passwordHash string) error {
	query := `UPDATE users SET password_hash = ? WHERE user_id = ?`
	_, err := s.db.Exec(query, passwordHash, userID)
	return err
}

// UpdateUserEmail updates user email
func (s *Store) UpdateUserEmail(userID string, email string) error {
	query := `UPDATE users SET email = ? WHERE user_id = ?`
	_, err := s.db.Exec(query, email, userID)
	return err
}

// UpdateUserUsername updates username
func (s *Store) UpdateUserUsername(userID string, username string) error {
	query := `UPDATE users SET username = ? WHERE user_id = ?`
	_, err := s.db.Exec(query, username, userID)
	return err
}

// GetAllUsers retrieves all users
func (s *Store) GetAllUsers() ([]UserRecord, error) {
	query := `SELECT user_id, username, email, password_hash, account_type, created_at, expires_at, last_login_at
		FROM users ORDER BY created_at DESC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserRecord
	for rows.Next() {
		var user UserRecord
		err := rows.Scan(
			&user.UserID, &user.Username, &user.Email,
			&user.PasswordHash, &user.AccountType, &user.CreatedAt,
			&user.ExpiresAt, &user.LastLoginAt,
		)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, rows.Err()
}

// UpdateUserLastLoginSync updates user last login time
func (s *Store) UpdateUserLastLoginSync(userID string, loginTime time.Time) error {
	query := `UPDATE users SET last_login_at = ? WHERE user_id = ?`
	_, err := s.db.Exec(query, loginTime, userID)
	if err != nil {
		return fmt.Errorf("failed to update last login for user %s: %w", userID, err)
	}
	return nil
}

// GetUserByUsername retrieves user by username with case-insensitive matching
func (s *Store) GetUserByUsername(username string) (*UserRecord, error) {
	var user UserRecord
	query := `SELECT user_id, username, email, password_hash, account_type, created_at, expires_at, last_login_at
		FROM users WHERE username = ? COLLATE NOCASE`

	err := s.db.QueryRow(query, username).Scan(
		&user.UserID, &user.Username, &user.Email,
		&user.PasswordHash, &user.AccountType, &user.CreatedAt,
		&user.ExpiresAt, &user.LastLoginAt,
	)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByEmail retrieves user by email with case-insensitive matching
func (s *Store) GetUserByEmail(email string) (*UserRecord, error) {
	var user UserRecord
	query := `SELECT user_id, username, email, password_hash, account_type, created_at, expires_at, last_login_at
		FROM users WHERE email = ? COLLATE NOCASE`

	err := s.db.QueryRow(query, email).Scan(
		&user.UserID, &user.Username, &user.Email,
		&user.PasswordHash, &user.AccountType, &user.CreatedAt,
		&user.ExpiresAt, &user.LastLoginAt,
	)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByID retrieves user by unique user ID
func (s *Store) GetUserByID(userID string) (*UserRecord, error) {
	var user UserRecord
	query := `SELECT user_id, username, email, password_hash, account_type, created_at, expires_at, last_login_at
		FROM users WHERE user_id = ?`

	err := s.db.QueryRow(query, userID).Scan(
		&user.UserID, &user.Username, &user.Email,
		&user.PasswordHash, &user.AccountType, &user.CreatedAt,
		&user.ExpiresAt, &user.LastLoginAt,
	)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// DeleteUser removes a user from the database (async)
func (s *Store) DeleteUser(userID string) error {
	if !s.healthStatus.Load() {
		return nil
	}

	select {
	case s.writeChan <- func(tx *sql.Tx) error {
		query := `DELETE FROM users WHERE user_id = ?`
		_, err := tx.Exec(query, userID)
		return err
	}:
		return nil
	default:
		log.Printf("Storage write queue full, dropping user deletion")
		return nil
	}
}