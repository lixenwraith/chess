package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

var (
	ErrUserAlreadyExists = errors.New("username or email already exists")
	ErrUserCapacity      = errors.New("user capacity reached")
)

// userCreateLockKey serializes account creation across processes (server and
// CLI) so the capacity check cannot be raced past.
const userCreateLockKey int64 = 0x6368657375 // "chesu"

const userSelectColumns = `user_id, username, email, password_hash, created_at, last_login_at`

// UserLimits defines public-registration constraints.
type UserLimits struct {
	MaxUsers int // total accounts at which registration closes
}

// CreateUser creates an administratively managed user without applying the
// public-registration capacity policy.
func (s *Store) CreateUser(record UserRecord) error {
	return s.createUser(record, nil, nil)
}

// CreateUserWithinLimits atomically applies the registration limit, creates
// the user, and optionally creates its initial session. A session failure
// rolls back the account.
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

	// Report a duplicate as such even when registration is full. The advisory
	// lock orders this check with concurrent creations; the unique constraints
	// still catch a race with renames.
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM users WHERE username = lower($1) OR email = lower($2)
	)`, record.Username, nullableString(record.Email)).Scan(&exists); err != nil {
		return fmt.Errorf("check user uniqueness: %w", err)
	}
	if exists {
		return ErrUserAlreadyExists
	}

	if limits != nil && limits.MaxUsers > 0 {
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&total); err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if total >= limits.MaxUsers {
			return ErrUserCapacity
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO users (
		user_id, username, email, password_hash, created_at
	) VALUES ($1, lower($2), lower($3), $4, $5)`,
		record.UserID, record.Username, nullableString(record.Email),
		record.PasswordHash, record.CreatedAt,
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
	slog.Debug("storage user created", "user_id", record.UserID, "initial_session", session != nil)
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

// DeleteUser removes a user synchronously; sessions cascade, while games keep
// the claim ID and name snapshot. It returns sql.ErrNoRows when no such user
// exists.
func (s *Store) DeleteUser(userID string) error {
	if err := s.execUserUpdate(`DELETE FROM users WHERE user_id = $1`, userID); err != nil {
		return err
	}
	slog.Debug("storage user deleted", "user_id", userID)
	return nil
}

// DeleteAccount removes an account at its owner's request. Unlike DeleteUser,
// which keeps the claim IDs and name snapshots for the record, it first clears
// the user's claims and names from every stored game, so nothing in the
// database still names the person, and a game nobody else claimed becomes
// anonymous and falls to the inactivity purge. Sessions cascade.
//
// It runs on the ordered game writer, behind every gameplay write already
// queued, so a queued claim cannot land on a game after it was cleared. It
// returns sql.ErrNoRows when no such user exists.
func (s *Store) DeleteAccount(userID string) error {
	if !validUUID(userID) {
		return sql.ErrNoRows
	}
	// Set inside the transaction, read after the writer reports back: the
	// barrier channel orders the two. A missing row is an answer, not a
	// failed write, so it must not degrade the writer.
	var deleted int64
	// Behind the queue, then its own transaction.
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout+writeTimeout)
	defer cancel()
	err := s.enqueueWait(ctx, "delete_account", func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE games SET
				white_claimed_by = CASE WHEN white_claimed_by = $1 THEN NULL ELSE white_claimed_by END,
				white_name       = CASE WHEN white_claimed_by = $1 THEN NULL ELSE white_name END,
				black_claimed_by = CASE WHEN black_claimed_by = $1 THEN NULL ELSE black_claimed_by END,
				black_name       = CASE WHEN black_claimed_by = $1 THEN NULL ELSE black_name END
			WHERE white_claimed_by = $1 OR black_claimed_by = $1`, userID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE user_id = $1`, userID)
		if err != nil {
			return err
		}
		deleted, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return err
	}
	if deleted == 0 {
		return sql.ErrNoRows
	}
	slog.Debug("storage account deleted", "user_id", userID)
	return nil
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
	var lastLoginAt sql.NullTime
	if err := scanner.Scan(
		&user.UserID, &user.Username, &email,
		&user.PasswordHash, &user.CreatedAt, &lastLoginAt,
	); err != nil {
		return err
	}
	user.Email = email.String
	user.CreatedAt = user.CreatedAt.UTC()
	user.LastLoginAt = utcPointer(lastLoginAt)
	return nil
}
