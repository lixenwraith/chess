package http

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/service"

	"github.com/gofiber/fiber/v2"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{1,40}$`)

// RegisterRequest defines the user registration payload
type RegisterRequest struct {
	Username string `json:"username" validate:"required,min=1,max=40"`
	Email    string `json:"email" validate:"omitempty,max=255"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

// LoginRequest defines the authentication payload
type LoginRequest struct {
	Identifier string `json:"identifier" validate:"required"` // username or email
	Password   string `json:"password" validate:"required"`
}

// DeleteAccountRequest re-confirms the password before an account is deleted.
type DeleteAccountRequest struct {
	Password string `json:"password" validate:"required"`
}

// AuthResponse contains JWT token and user information
type AuthResponse struct {
	Token     string    `json:"token"`
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// UserResponse contains current user information
type UserResponse struct {
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// RegisterHandler creates a new user account
func (h *HTTPHandler) RegisterHandler(c *fiber.Ctx) error {
	var req RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Validate username format
	if !usernameRegex.MatchString(req.Username) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid username format",
			Code:    core.ErrInvalidRequest,
			Details: "username must be 1-40 characters, alphanumeric and underscore only",
		})
	}

	// Validate email format if provided
	if req.Email != "" && !emailRegex.MatchString(req.Email) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid email format",
			Code:    core.ErrInvalidRequest,
			Details: "email must be a valid email address",
		})
	}

	// Validate password strength
	if err := validatePassword(req.Password); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "weak password",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Normalize for case-insensitive storage
	req.Username = strings.ToLower(req.Username)
	if req.Email != "" {
		req.Email = strings.ToLower(req.Email)
	}

	// Create the user and initial session atomically.
	user, sessionID, err := h.svc.RegisterUser(req.Username, req.Email, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrStorageDisabled) || errors.Is(err, service.ErrStorageUnavailable) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "authentication storage unavailable", Code: core.ErrStorageUnavailable,
			})
		}
		if errors.Is(err, service.ErrAtCapacity) || errors.Is(err, service.ErrAuthBusy) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error:   "registration temporarily unavailable",
				Code:    core.ErrResourceLimit,
				Details: err.Error(),
			})
		}
		if errors.Is(err, service.ErrUserExists) {
			return c.Status(fiber.StatusConflict).JSON(core.ErrorResponse{
				Error:   "user already exists",
				Code:    core.ErrInvalidRequest,
				Details: "username or email already taken",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "failed to create user",
			Code:  core.ErrInternalError,
		})
	}

	// Generate JWT token
	token, err := h.svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "failed to generate token",
			Code:  core.ErrInternalError,
		})
	}

	return c.Status(fiber.StatusCreated).JSON(AuthResponse{
		Token:     token,
		UserID:    user.UserID,
		Username:  user.Username,
		Email:     user.Email,
		ExpiresAt: time.Now().Add(service.SessionTTL),
	})
}

// validatePassword checks password strength requirements
func validatePassword(password string) error {
	const (
		minPasswordLength = 8
		maxPasswordLength = 128
	)
	if len(password) < minPasswordLength {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if len(password) > maxPasswordLength {
		return fmt.Errorf("password must not exceed 128 characters")
	}

	// Check for at least one letter and one number
	hasLetter := false
	hasNumber := false
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsNumber(r):
			hasNumber = true
		}
		if hasLetter && hasNumber {
			break
		}
	}

	if !hasLetter || !hasNumber {
		return fmt.Errorf("password must contain at least one letter and one number")
	}

	return nil
}

// LoginHandler authenticates user and returns JWT token
func (h *HTTPHandler) LoginHandler(c *fiber.Ctx) error {
	var req LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Normalize identifier for case-insensitive lookup
	req.Identifier = strings.ToLower(req.Identifier)

	// Authenticate user and create session (invalidates previous session)
	user, sessionID, err := h.svc.AuthenticateUser(req.Identifier, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrStorageDisabled) || errors.Is(err, service.ErrStorageUnavailable) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "authentication storage unavailable", Code: core.ErrStorageUnavailable,
			})
		}
		if errors.Is(err, service.ErrAuthBusy) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "authentication temporarily unavailable", Code: core.ErrResourceLimit,
			})
		}
		return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
			Error: "invalid credentials",
			Code:  core.ErrInvalidRequest,
		})
	}

	// Generate JWT token with session ID
	token, err := h.svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "failed to generate token",
			Code:  core.ErrInternalError,
		})
	}

	return c.JSON(AuthResponse{
		Token:     token,
		UserID:    user.UserID,
		Username:  user.Username,
		Email:     user.Email,
		ExpiresAt: time.Now().Add(service.SessionTTL),
	})
}

// GetCurrentUserHandler returns authenticated user information
func (h *HTTPHandler) GetCurrentUserHandler(c *fiber.Ctx) error {
	userID, ok := c.Locals("userID").(string)
	if !ok || userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
			Error: "unauthorized",
			Code:  core.ErrInvalidRequest,
		})
	}

	user, err := h.svc.GetUserByID(userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(core.ErrorResponse{
			Error: "user not found",
			Code:  core.ErrInvalidRequest,
		})
	}

	return c.JSON(UserResponse{
		UserID:    user.UserID,
		Username:  user.Username,
		Email:     user.Email,
		CreatedAt: user.CreatedAt,
	})
}

// LogoutHandler invalidates the current session
func (h *HTTPHandler) LogoutHandler(c *fiber.Ctx) error {
	// Extract session ID from token claims
	sessionID, ok := c.Locals("sessionID").(string)
	if !ok || sessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error: "no active session",
			Code:  core.ErrInvalidRequest,
		})
	}

	if err := h.svc.InvalidateSession(sessionID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "failed to logout",
			Code:  core.ErrInternalError,
		})
	}

	return c.JSON(fiber.Map{"message": "logged out"})
}

// DeleteAccountHandler deletes the caller's account after re-checking the
// password. The caller's games lose the claim and the name; see
// Service.DeleteAccount.
func (h *HTTPHandler) DeleteAccountHandler(c *fiber.Ctx) error {
	userID, _ := c.Locals("userID").(string)
	sessionID, _ := c.Locals("sessionID").(string)
	if userID == "" || sessionID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
			Error: "unauthorized",
			Code:  core.ErrInvalidRequest,
		})
	}

	var req DeleteAccountRequest
	if err := c.BodyParser(&req); err != nil || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: "password is required to delete the account",
		})
	}

	if err := h.svc.DeleteAccount(userID, sessionID, req.Password); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidCredentials):
			return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
				Error: "invalid credentials",
				Code:  core.ErrInvalidRequest,
			})
		case errors.Is(err, service.ErrAuthBusy):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "authentication temporarily unavailable", Code: core.ErrResourceLimit,
			})
		case errors.Is(err, service.ErrStorageDisabled) || errors.Is(err, service.ErrStorageUnavailable):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "account storage unavailable", Code: core.ErrStorageUnavailable,
			})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
				Error: "failed to delete account",
				Code:  core.ErrInternalError,
			})
		}
	}

	return c.SendStatus(fiber.StatusNoContent)
}
