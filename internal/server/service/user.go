package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	ErrUserExists         = errors.New("username or email already exists")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAuthBusy           = errors.New("authentication capacity exhausted")
)

const (
	// JWTIssuer and JWTAudience scope chess tokens so a token minted by another
	// service sharing a key is rejected.
	JWTIssuer   = "chess-server"
	JWTAudience = "chess-api"

	// MaxConcurrentKDF bounds simultaneous Argon2id derivations (64 MiB each at
	// the auth defaults), capping password-hashing memory near 256 MiB.
	MaxConcurrentKDF = 4
	kdfWaitTimeout   = 5 * time.Second
)

// dummyPasswordHash is verified for unknown identifiers so a login for a
// missing account costs the same Argon2id work as one with a wrong password.
var dummyPasswordHash = sync.OnceValues(func() (string, error) {
	return auth.HashPassword(uuid.NewString())
})

// User represents a registered user account
type User struct {
	UserID      string
	Username    string
	Email       string
	AccountType string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
}

// acquireKDF reserves one Argon2id slot, waiting at most kdfWaitTimeout.
func (s *Service) acquireKDF() (release func(), err error) {
	timer := time.NewTimer(kdfWaitTimeout)
	defer timer.Stop()
	select {
	case s.kdf <- struct{}{}:
		return func() { <-s.kdf }, nil
	case <-timer.C:
		return nil, ErrAuthBusy
	}
}

func (s *Service) hashPassword(password string) (string, error) {
	release, err := s.acquireKDF()
	if err != nil {
		return "", err
	}
	defer release()
	return auth.HashPassword(password)
}

func (s *Service) verifyPassword(password, hash string) error {
	release, err := s.acquireKDF()
	if err != nil {
		return err
	}
	defer release()
	return auth.VerifyPassword(password, hash)
}

// RegisterUser creates the account and its initial session in one database
// transaction, so a successful registration always returns a usable account.
func (s *Service) RegisterUser(username, email, password string, permanent bool) (*User, string, error) {
	if s.store == nil {
		return nil, "", ErrStorageDisabled
	}

	// Hash before touching storage; account creation itself is serialized by
	// the store, so concurrent registrations only contend for KDF slots.
	passwordHash, err := s.hashPassword(password)
	if err != nil {
		if errors.Is(err, ErrAuthBusy) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("failed to hash password: %w", err)
	}

	now := time.Now().UTC()
	accountType := "temp"
	var expiresAt *time.Time
	if permanent {
		accountType = "permanent"
	} else {
		expiry := now.Add(TempUserTTL)
		expiresAt = &expiry
	}

	// A random UUIDv4 needs no existence probe; the primary key rejects the
	// negligible collision case.
	userID := uuid.NewString()
	user := &User{
		UserID:      userID,
		Username:    strings.ToLower(username),
		Email:       strings.ToLower(email),
		AccountType: accountType,
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
	}
	record := storage.UserRecord{
		UserID:       userID,
		Username:     user.Username,
		Email:        user.Email,
		PasswordHash: passwordHash,
		AccountType:  accountType,
		CreatedAt:    now,
		ExpiresAt:    expiresAt,
	}
	sessionID := uuid.NewString()
	session := &storage.SessionRecord{
		SessionID: sessionID,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
	}
	limits := storage.UserLimits{
		MaxUsers:       MaxUsers,
		PermanentSlots: PermanentSlots,
	}
	if err = s.store.CreateUserWithinLimits(record, session, limits); err != nil {
		switch {
		case errors.Is(err, storage.ErrUserAlreadyExists):
			return nil, "", ErrUserExists
		case errors.Is(err, storage.ErrPermanentCapacity):
			return nil, "", fmt.Errorf("%w (%d maximum)", ErrPermanentSlotsFull, PermanentSlots)
		case errors.Is(err, storage.ErrUserCapacity):
			return nil, "", fmt.Errorf("%w: no temporary account can be replaced", ErrAtCapacity)
		default:
			return nil, "", fmt.Errorf("%w: create user: %v", ErrStorageUnavailable, err)
		}
	}
	slog.Debug("user created", "user_id", userID, "account_type", accountType)

	return user, sessionID, nil
}

// AuthenticateUser verifies credentials and creates a new session. Unknown
// identifiers, wrong passwords, and expired accounts all return
// ErrInvalidCredentials after the same Argon2id work.
func (s *Service) AuthenticateUser(identifier, password string) (*User, string, error) {
	if s.store == nil {
		return nil, "", ErrStorageDisabled
	}

	var userRecord *storage.UserRecord
	var err error
	if strings.Contains(identifier, "@") {
		userRecord, err = s.store.GetUserByEmail(identifier)
	} else {
		userRecord, err = s.store.GetUserByUsername(identifier)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("%w: look up user: %v", ErrStorageUnavailable, err)
	}

	hash := ""
	if userRecord != nil {
		hash = userRecord.PasswordHash
	} else if hash, err = dummyPasswordHash(); err != nil {
		return nil, "", fmt.Errorf("prepare credential check: %w", err)
	}
	if err := s.verifyPassword(password, hash); err != nil || userRecord == nil {
		if errors.Is(err, ErrAuthBusy) {
			return nil, "", err
		}
		return nil, "", ErrInvalidCredentials
	}

	now := time.Now().UTC()
	if userRecord.AccountType == "temp" && userRecord.ExpiresAt != nil && now.After(*userRecord.ExpiresAt) {
		return nil, "", ErrInvalidCredentials
	}

	// Replaces any existing session and records the login time.
	sessionID := uuid.NewString()
	if err := s.store.CreateSession(storage.SessionRecord{
		SessionID: sessionID,
		UserID:    userRecord.UserID,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
	}); err != nil {
		return nil, "", fmt.Errorf("%w: create session: %v", ErrStorageUnavailable, err)
	}
	slog.Debug("user authenticated", "user_id", userRecord.UserID)

	return &User{
		UserID:      userRecord.UserID,
		Username:    userRecord.Username,
		Email:       userRecord.Email,
		AccountType: userRecord.AccountType,
		CreatedAt:   userRecord.CreatedAt,
		ExpiresAt:   userRecord.ExpiresAt,
	}, sessionID, nil
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

// GenerateUserToken signs a scoped JWT bound to the persisted session. The
// token carries only the subject and session ID: JWT payloads are readable by
// the holder, so profile data such as email stays behind /auth/me.
func (s *Service) GenerateUserToken(userID, sessionID string) (string, error) {
	return s.jwt.GenerateToken(userID, map[string]any{"session_id": sessionID})
}

// ValidateToken verifies JWT token and session validity
func (s *Service) ValidateToken(token string) (string, map[string]any, error) {
	userID, claims, err := s.jwt.ValidateToken(token)
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
