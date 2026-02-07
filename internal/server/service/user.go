package service

import (
	"fmt"
	"strings"
	"time"

	"chess/internal/server/storage"

	"github.com/google/uuid"
	"github.com/lixenwraith/auth"
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
	if s.store == nil {
		return nil, fmt.Errorf("storage disabled")
	}

	// Check registration limits
	total, permCount, _, err := s.store.GetUserCounts()
	if err != nil {
		return nil, fmt.Errorf("failed to check user limits: %w", err)
	}

	// Determine account type
	accountType := "temp"
	var expiresAt *time.Time

	if permanent {
		if permCount >= PermanentSlots {
			return nil, fmt.Errorf("permanent user slots full (%d/%d)", permCount, PermanentSlots)
		}
		accountType = "permanent"
	} else {
		expiry := time.Now().UTC().Add(TempUserTTL)
		expiresAt = &expiry
	}

	// Handle capacity - remove oldest temp user if at max
	if total >= MaxUsers {
		if err := s.removeOldestTempUser(); err != nil {
			return nil, fmt.Errorf("at capacity and cannot make room: %w", err)
		}
	}

	// Hash password
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Generate unique user ID
	userID, err := s.generateUniqueUserID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate unique ID: %w", err)
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

	if err = s.store.CreateUser(record); err != nil {
		return nil, err
	}

	return user, nil
}

// removeOldestTempUser removes the oldest temporary user to make room
func (s *Service) removeOldestTempUser() error {
	oldest, err := s.store.GetOldestTempUser()
	if err != nil {
		return fmt.Errorf("no temp users to remove: %w", err)
	}

	// Delete their session first
	_ = s.store.DeleteSessionByUserID(oldest.UserID)

	// Delete the user
	if err := s.store.DeleteUserByID(oldest.UserID); err != nil {
		return fmt.Errorf("failed to remove oldest user: %w", err)
	}

	return nil
}

// AuthenticateUser verifies credentials and creates a new session
func (s *Service) AuthenticateUser(identifier, password string) (*User, string, error) {
	if s.store == nil {
		return nil, "", fmt.Errorf("storage disabled")
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
		return nil, "", fmt.Errorf("failed to create session: %w", err)
	}

	// Update last login
	_ = s.store.UpdateUserLastLoginSync(userRecord.UserID, time.Now().UTC())

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
		return false, fmt.Errorf("storage disabled")
	}
	return s.store.IsSessionValid(sessionID)
}

// InvalidateSession removes a session (logout)
func (s *Service) InvalidateSession(sessionID string) error {
	if s.store == nil {
		return fmt.Errorf("storage disabled")
	}
	return s.store.DeleteSession(sessionID)
}

// GetUserByID retrieves user information by user ID
func (s *Service) GetUserByID(userID string) (*User, error) {
	if s.store == nil {
		return nil, fmt.Errorf("storage disabled")
	}

	userRecord, err := s.store.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("user not found")
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

	// Validate session is still active
	if sessionID, ok := claims["session_id"].(string); ok && s.store != nil {
		valid, err := s.store.IsSessionValid(sessionID)
		if err != nil || !valid {
			return "", nil, fmt.Errorf("session invalidated")
		}
	}

	return userID, claims, nil
}

// generateUniqueUserID creates a unique user ID with collision detection
func (s *Service) generateUniqueUserID() (string, error) {
	const maxAttempts = 10

	for i := 0; i < maxAttempts; i++ {
		id := uuid.New().String()
		if _, err := s.store.GetUserByID(id); err != nil {
			return id, nil
		}
	}

	return "", fmt.Errorf("failed to generate unique user ID")
}

// CreateUserSession creates a session for a user without re-authenticating
// Used after registration to avoid redundant password hashing
func (s *Service) CreateUserSession(userID string) (string, error) {
	if s.store == nil {
		return "", fmt.Errorf("storage disabled")
	}

	sessionID := uuid.New().String()
	sessionRecord := storage.SessionRecord{
		SessionID: sessionID,
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(SessionTTL),
	}

	if err := s.store.CreateSession(sessionRecord); err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	return sessionID, nil
}