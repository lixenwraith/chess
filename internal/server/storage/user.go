package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

var (
	ErrUserAlreadyExists = errors.New("username or email already exists")
	ErrUserCapacity      = errors.New("user capacity reached")
	ErrPermanentCapacity = errors.New("permanent user capacity reached")
)

// userCreateLockKey serializes account creation across processes (server and
// CLI) so capacity checks and temporary-account eviction are deterministic.
const userCreateLockKey int64 = 0x6368657375 // "chesu"

const userSelectColumns = `user_id, username, email, password_hash, account_type,
	created_at, expires_at, last_login_at`

// UserLimits defines registration constraints
type UserLimits struct {
	MaxUsers       int
	PermanentSlots int
}

// CreateUser creates an administratively managed user without applying the
// public-registration capacity policy.
func (s *Store) CreateUser(record UserRecord) error {
	return s.createUser(record, nil, nil)
}

// CreateUserWithinLimits atomically applies registration limits, evicts the
// oldest temporary account when required, creates the user, and optionally
// creates its initial session. A duplicate username/email is rejected before
// any eviction, and a session failure rolls back the eviction and the insert.
func (s *Store) CreateUserWithinLimits(
	record UserRecord,
	session *SessionRecord,
	limits UserLimits,
) error {
	return s.createUser(record, session, &limits)
}

func (s *Store) createUser(record UserRecord, session *SessionRecord, limits *UserLimits) error {
	if session != nil && session.UserID != record.UserID {
		return errors.New("initial session user does not match new user")
	}
	ctx, cancel := opContext()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, userCreateLockKey); err != nil {
		return fmt.Errorf("lock user creation: %w", err)
	}

	// Check uniqueness before any eviction. Relying on the insert's unique
	// constraint is not enough: at capacity the eviction could delete the very
	// account whose username is being registered, handing the name to a new
	// owner. The advisory lock orders this check with concurrent creations; the
	// constraints still catch a race with renames.
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM users WHERE username = lower($1) OR email = lower($2)
	)`, record.Username, nullableString(record.Email)).Scan(&exists); err != nil {
		return fmt.Errorf("check user uniqueness: %w", err)
	}
	if exists {
		return ErrUserAlreadyExists
	}

	if limits != nil {
		var total, permanent int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),
			count(*) FILTER (WHERE account_type = 'permanent') FROM users`,
		).Scan(&total, &permanent); err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if record.AccountType == "permanent" && permanent >= limits.PermanentSlots {
			return ErrPermanentCapacity
		}
		if total >= limits.MaxUsers {
			result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE user_id = (
				SELECT user_id FROM users
				WHERE account_type = 'temp'
				ORDER BY created_at, user_id
				LIMIT 1
			)`)
			if err != nil {
				return fmt.Errorf("evict oldest temporary user: %w", err)
			}
			if deleted, err := result.RowsAffected(); err != nil {
				return fmt.Errorf("inspect temporary user eviction: %w", err)
			} else if deleted != 1 {
				return ErrUserCapacity
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO users (
		user_id, username, email, password_hash, account_type, created_at, expires_at
	) VALUES ($1, lower($2), lower($3), $4, $5, $6, $7)`,
		record.UserID, record.Username, nullableString(record.Email),
		record.PasswordHash, record.AccountType, record.CreatedAt, record.ExpiresAt,
	); err != nil {
		return mapUserConflict(err)
	}
	if session != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (session_id, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`,
			session.SessionID, session.UserID, session.CreatedAt, session.ExpiresAt,
		); err != nil {
			return fmt.Errorf("create initial session: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Debug("storage user created",
		"user_id", record.UserID,
		"account_type", record.AccountType,
		"initial_session", session != nil,
	)
	return nil
}

// mapUserConflict converts a username/email uniqueness violation, including one
// lost to a concurrent writer, into ErrUserAlreadyExists.
func mapUserConflict(err error) error {
	if constraint, ok := uniqueViolation(err); ok &&
		(constraint == "users_username_key" || constraint == "users_email_key") {
		return ErrUserAlreadyExists
	}
	return err
}

// DeleteExpiredTempUsers removes temporary users past their expiry. Sessions
// cascade; games keep their claims as historical references.
func (s *Store) DeleteExpiredTempUsers() (int64, error) {
	ctx, cancel := opContext()
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM users
		WHERE account_type = 'temp' AND expires_at < $1`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err == nil && deleted > 0 {
		slog.Debug("storage expired temporary users deleted", "count", deleted)
	}
	return deleted, err
}

// DeleteUser removes a user synchronously; sessions cascade. It returns
// sql.ErrNoRows when no such user exists.
func (s *Store) DeleteUser(userID string) error {
	if err := s.execUserUpdate(`DELETE FROM users WHERE user_id = $1`, userID); err != nil {
		return err
	}
	slog.Debug("storage user deleted", "user_id", userID)
	return nil
}

// PromoteToPermanent upgrades a temporary user to a permanent account.
func (s *Store) PromoteToPermanent(userID string) error {
	return s.execUserUpdate(
		`UPDATE users SET account_type = 'permanent', expires_at = NULL WHERE user_id = $1`, userID)
}

// UpdateUserPassword updates user password hash
func (s *Store) UpdateUserPassword(userID, passwordHash string) error {
	return s.execUserUpdate(`UPDATE users SET password_hash = $2 WHERE user_id = $1`,
		userID, passwordHash)
}

// UpdateUserEmail updates the email; an empty value removes it.
func (s *Store) UpdateUserEmail(userID, email string) error {
	return s.execUserUpdate(`UPDATE users SET email = lower($2) WHERE user_id = $1`,
		userID, nullableString(email))
}

// UpdateUserUsername updates username
func (s *Store) UpdateUserUsername(userID, username string) error {
	return s.execUserUpdate(`UPDATE users SET username = lower($2) WHERE user_id = $1`,
		userID, username)
}

// execUserUpdate runs a single-row statement keyed by user_id ($1).
func (s *Store) execUserUpdate(query, userID string, args ...any) error {
	if !validUUID(userID) {
		return sql.ErrNoRows
	}
	ctx, cancel := opContext()
	defer cancel()
	result, err := s.db.ExecContext(ctx, query, append([]any{userID}, args...)...)
	if err != nil {
		return mapUserConflict(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetAllUsers retrieves all users, newest first
func (s *Store) GetAllUsers() ([]UserRecord, error) {
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSelectColumns+` FROM users ORDER BY created_at DESC, user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserRecord
	for rows.Next() {
		var user UserRecord
		if err := scanUser(rows, &user); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// GetUserByUsername retrieves a user by case-insensitive username.
func (s *Store) GetUserByUsername(username string) (*UserRecord, error) {
	return s.getUser(`username = lower($1)`, username)
}

// GetUserByEmail retrieves a user by case-insensitive email.
func (s *Store) GetUserByEmail(email string) (*UserRecord, error) {
	if email == "" {
		return nil, sql.ErrNoRows
	}
	return s.getUser(`email = lower($1)`, email)
}

// GetUserByID retrieves user by unique user ID
func (s *Store) GetUserByID(userID string) (*UserRecord, error) {
	if !validUUID(userID) {
		return nil, sql.ErrNoRows
	}
	return s.getUser(`user_id = $1`, userID)
}

func (s *Store) getUser(predicate string, arg any) (*UserRecord, error) {
	ctx, cancel := opContext()
	defer cancel()
	var user UserRecord
	row := s.db.QueryRowContext(ctx, `SELECT `+userSelectColumns+` FROM users WHERE `+predicate, arg)
	if err := scanUser(row, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func scanUser(scanner rowScanner, user *UserRecord) error {
	var email sql.NullString
	var expiresAt, lastLoginAt sql.NullTime
	if err := scanner.Scan(
		&user.UserID, &user.Username, &email,
		&user.PasswordHash, &user.AccountType, &user.CreatedAt,
		&expiresAt, &lastLoginAt,
	); err != nil {
		return err
	}
	user.Email = email.String
	user.CreatedAt = user.CreatedAt.UTC()
	user.ExpiresAt = utcPointer(expiresAt)
	user.LastLoginAt = utcPointer(lastLoginAt)
	return nil
}
