// FILE: lixenwraith/chess/internal/server/service/user.go
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
	UserID    string
	Username  string
	Email     string
	CreatedAt time.Time
}

// CreateUser creates new user with transactional consistency
func (s *Service) CreateUser(username, email, password string) (*User, error) {
	if s.store == nil {
		return nil, fmt.Errorf("storage disabled")
	}

	// Hash password
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Generate guaranteed unique user ID with proper collision handling
	userID, err := s.generateUniqueUserID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate unique ID: %w", err)
	}

	// Create user record
	user := &User{
		UserID:    userID,
		Username:  username,
		Email:     email,
		CreatedAt: time.Now().UTC(),
	}

	// Use transactional storage method
	record := storage.UserRecord{
		UserID:       userID,
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		CreatedAt:    user.CreatedAt,
	}

	if err = s.store.CreateUser(record); err != nil {
		return nil, err
	}

	return user, nil
}

// AuthenticateUser verifies user credentials and returns user information
// AuthenticateUser verifies user credentials and returns user information
func (s *Service) AuthenticateUser(identifier, password string) (*User, error) {
	if s.store == nil {
		return nil, fmt.Errorf("storage disabled")
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
		// Always hash to prevent timing attacks
		auth.HashPassword(password)
		return nil, fmt.Errorf("invalid credentials")
	}

	// Verify password
	if err := auth.VerifyPassword(password, userRecord.PasswordHash); err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	return &User{
		UserID:    userRecord.UserID,
		Username:  userRecord.Username,
		Email:     userRecord.Email,
		CreatedAt: userRecord.CreatedAt,
	}, nil
}

// UpdateLastLogin updates the last login timestamp for a user
func (s *Service) UpdateLastLogin(userID string) error {
	if s.store == nil {
		return fmt.Errorf("storage disabled")
	}

	err := s.store.UpdateUserLastLoginSync(userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to update last login time for user %s: %w\n", userID, err)
	}

	return nil
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
		UserID:    userRecord.UserID,
		Username:  userRecord.Username,
		Email:     userRecord.Email,
		CreatedAt: userRecord.CreatedAt,
	}, nil
}

// GenerateUserToken creates a JWT token for the specified user
func (s *Service) GenerateUserToken(userID string) (string, error) {
	user, err := s.GetUserByID(userID)
	if err != nil {
		return "", err
	}

	claims := map[string]any{
		"username": user.Username,
		"email":    user.Email,
	}

	return auth.GenerateHS256Token(s.jwtSecret, userID, claims, 7*24*time.Hour)
}

// ValidateToken verifies JWT token and returns user ID with claims
func (s *Service) ValidateToken(token string) (string, map[string]any, error) {
	return auth.ValidateHS256Token(s.jwtSecret, token)
}

// generateUniqueUserID creates a unique user ID with collision detection
func (s *Service) generateUniqueUserID() (string, error) {
	const maxAttempts = 10

	for i := 0; i < maxAttempts; i++ {
		id := uuid.New().String()

		// Check for collision
		if _, err := s.store.GetUserByID(id); err != nil {
			// Error means not found, ID is unique
			return id, nil
		}

		// Collision detected, try again
		if i == maxAttempts-1 {
			// After max attempts, fail and don't risk collision
			return "", fmt.Errorf("failed to generate unique ID after %d attempts", maxAttempts)
		}
	}

	return "", fmt.Errorf("failed to generate unique user ID")
}