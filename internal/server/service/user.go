package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"chess/internal/server/storage"

	"github.com/google/uuid"
	"github.com/lixenwraith/auth"
)

var (
	ErrStorageDisabled    = errors.New("storage disabled")
	ErrStorageUnavailable = errors.New("storage unavailable")
	ErrAtCapacity         = errors.New("at capacity")
	ErrPermanentSlotsFull = errors.New("permanent slots full")
)

// User represents a registered user account
type User struct {
	UserID      string
	Username    string
	Email       string
	AccountType string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
}

// CreateUser creates new user with registration limits enforcement
func (s *Service) CreateUser(username, email, password string, permanent bool) (*User, error) {
	user, _, err := s.createUser(username, email, password, permanent, false)
	return user, err
}

// RegisterUser creates the account and its initial session in one SQLite
// transaction, so a successful registration always returns a usable account.
func (s *Service) RegisterUser(username, email, password string, permanent bool) (*User, string, error) {
	return s.createUser(username, email, password, permanent, true)
}

func (s *Service) createUser(
	username, email, password string,
	permanent, withSession bool,
) (*User, string, error) {
	s.userMu.Lock()
	defer s.userMu.Unlock()

	if s.store == nil {
		return nil, "", ErrStorageDisabled
	}

	// Determine account type
	accountType := "temp"
	var expiresAt *time.Time

	if permanent {
		accountType = "permanent"
	} else {
		expiry := time.Now().UTC().Add(TempUserTTL)
		expiresAt = &expiry
	}

	// Hash password
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash password: %w", err)
	}

	// Generate unique user ID
	userID, err := s.generateUniqueUserID()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate unique ID: %w", err)
	}

	// Create user record
	user := &User{
		UserID:      userID,
		Username:    username,
		Email:       email,
		AccountType: accountType,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}

	record := storage.UserRecord{
		UserID:       userID,
		Username:     strings.ToLower(username),
		Email:        strings.ToLower(email),
		PasswordHash: passwordHash,
		AccountType:  accountType,
		CreatedAt:    user.CreatedAt,
		ExpiresAt:    expiresAt,
	}

	var sessionID string
	var session *storage.SessionRecord
	if withSession {
		sessionID = uuid.New().String()
		session = &storage.SessionRecord{
			SessionID: sessionID,
			UserID:    userID,
			CreatedAt: user.CreatedAt,
			ExpiresAt: user.CreatedAt.Add(SessionTTL),
		}
	}
	limits := storage.UserLimits{
		MaxUsers:       MaxUsers,
		PermanentSlots: PermanentSlots,
	}
	if err = s.store.CreateUserWithinLimits(record, session, limits); err != nil {
		switch {
		case errors.Is(err, storage.ErrUserAlreadyExists):
			return nil, "", fmt.Errorf("username or email already exists: %w", err)
		case errors.Is(err, storage.ErrPermanentCapacity):
			return nil, "", fmt.Errorf("%w (%d maximum)", ErrPermanentSlotsFull, PermanentSlots)
		case errors.Is(err, storage.ErrUserCapacity):
			return nil, "", fmt.Errorf("%w: no temporary account can be replaced", ErrAtCapacity)
		default:
			return nil, "", fmt.Errorf("%w: create user: %v", ErrStorageUnavailable, err)
		}
	}
	slog.Debug("user created",
		"user_id", userID,
		"account_type", accountType,
		"initial_session", withSession,
	)

	return user, sessionID, nil
}

// AuthenticateUser verifies credentials and creates a new session
func (s *Service) AuthenticateUser(identifier, password string) (*User, string, error) {
	if s.store == nil {
		return nil, "", ErrStorageDisabled
	}

	var userRecord *storage.UserRecord
	var err error

	// Check if identifier looks like email
	if strings.Contains(identifier, "@") {
		userRecord, err = s.store.GetUserByEmail(identifier)
	} else {
		userRecord, err = s.store.GetUserByUsername(identifier)
	}

	if err != nil {
		auth.HashPassword(password) // Timing attack prevention
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("%w: look up user: %v", ErrStorageUnavailable, err)
		}
		return nil, "", fmt.Errorf("invalid credentials")
	}

	// Verify password
	if err := auth.VerifyPassword(password, userRecord.PasswordHash); err != nil {
		return nil, "", fmt.Errorf("invalid credentials")
	}

	// Check if temp user expired
	if userRecord.AccountType == "temp" && userRecord.ExpiresAt != nil {
		if time.Now().UTC().After(*userRecord.ExpiresAt) {
			return nil, "", fmt.Errorf("account expired")
		}
	}

	// Create new session (invalidates any existing session)
	sessionID := uuid.New().String()
	sessionRecord := storage.SessionRecord{
		SessionID: sessionID,
		UserID:    userRecord.UserID,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(SessionTTL),
	}

	if err := s.store.CreateSession(sessionRecord); err != nil {
		return nil, "", fmt.Errorf("%w: create session: %v", ErrStorageUnavailable, err)
	}
	slog.Debug("user authenticated", "user_id", userRecord.UserID)

	// Update last login
	if err := s.store.UpdateUserLastLoginSync(userRecord.UserID, time.Now().UTC()); err != nil {
		slog.Warn("failed to record user login time", "user_id", userRecord.UserID, "error", err)
	}

	return &User{
		UserID:      userRecord.UserID,
		Username:    userRecord.Username,
		Email:       userRecord.Email,
		AccountType: userRecord.AccountType,
		CreatedAt:   userRecord.CreatedAt,
		ExpiresAt:   userRecord.ExpiresAt,
	}, sessionID, nil
}

// ValidateSession checks if a session is valid
func (s *Service) ValidateSession(sessionID string) (bool, error) {
	if s.store == nil {
		return false, ErrStorageDisabled
	}
	valid, err := s.store.IsSessionValid(sessionID)
	if err != nil {
		return false, fmt.Errorf("%w: validate session: %v", ErrStorageUnavailable, err)
	}
	return valid, nil
}

// InvalidateSession removes a session (logout)
func (s *Service) InvalidateSession(sessionID string) error {
	if s.store == nil {
		return ErrStorageDisabled
	}
	if err := s.store.DeleteSession(sessionID); err != nil {
		return fmt.Errorf("%w: invalidate session: %v", ErrStorageUnavailable, err)
	}
	return nil
}

// GetUserByID retrieves user information by user ID
func (s *Service) GetUserByID(userID string) (*User, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}

	userRecord, err := s.store.GetUserByID(userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("%w: get user: %v", ErrStorageUnavailable, err)
	}

	return &User{
		UserID:      userRecord.UserID,
		Username:    userRecord.Username,
		Email:       userRecord.Email,
		AccountType: userRecord.AccountType,
		CreatedAt:   userRecord.CreatedAt,
		ExpiresAt:   userRecord.ExpiresAt,
	}, nil
}

// GenerateUserToken creates a JWT token for the specified user with session ID
func (s *Service) GenerateUserToken(userID, sessionID string) (string, error) {
	user, err := s.GetUserByID(userID)
	if err != nil {
		return "", err
	}

	claims := map[string]any{
		"username":   user.Username,
		"email":      user.Email,
		"session_id": sessionID,
	}

	return auth.GenerateHS256Token(s.jwtSecret, userID, claims, SessionTTL)
}

// ValidateToken verifies JWT token and session validity
func (s *Service) ValidateToken(token string) (string, map[string]any, error) {
	userID, claims, err := auth.ValidateHS256Token(s.jwtSecret, token)
	if err != nil {
		return "", nil, err
	}

	if s.store == nil {
		return "", nil, ErrStorageDisabled
	}
	sessionID, ok := claims["session_id"].(string)
	if !ok || sessionID == "" {
		return "", nil, fmt.Errorf("token has no persisted session")
	}
	valid, err := s.store.IsSessionValidForUser(sessionID, userID)
	if err != nil {
		return "", nil, fmt.Errorf("%w: validate token session: %v", ErrStorageUnavailable, err)
	}
	if !valid {
		return "", nil, fmt.Errorf("session invalidated")
	}

	return userID, claims, nil
}

// generateUniqueUserID creates a unique user ID with collision detection
func (s *Service) generateUniqueUserID() (string, error) {
	const maxAttempts = 10

	for i := 0; i < maxAttempts; i++ {
		id := uuid.New().String()
		if _, err := s.store.GetUserByID(id); errors.Is(err, sql.ErrNoRows) {
			return id, nil
		} else if err != nil {
			return "", fmt.Errorf("%w: check generated user ID: %v", ErrStorageUnavailable, err)
		}
	}

	return "", fmt.Errorf("failed to generate unique user ID")
}

// CreateUserSession creates a session for a trusted internal caller without
// re-authenticating. Public registration uses RegisterUser so account and
// initial session creation remain atomic.
func (s *Service) CreateUserSession(userID string) (string, error) {
	if s.store == nil {
		return "", ErrStorageDisabled
	}

	sessionID := uuid.New().String()
	sessionRecord := storage.SessionRecord{
		SessionID: sessionID,
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(SessionTTL),
	}

	if err := s.store.CreateSession(sessionRecord); err != nil {
		return "", fmt.Errorf("%w: create session: %v", ErrStorageUnavailable, err)
	}
	slog.Debug("user session created", "user_id", userID)

	return sessionID, nil
}
