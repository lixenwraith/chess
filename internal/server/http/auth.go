// FILE: lixenwraith/chess/internal/server/http/auth.go
package http

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"chess/internal/server/core"

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

	// Create user
	user, err := h.svc.CreateUser(req.Username, req.Email, req.Password)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
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
	token, err := h.svc.GenerateUserToken(user.UserID)
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
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
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

	// Authenticate user
	user, err := h.svc.AuthenticateUser(req.Identifier, req.Password)
	if err != nil {
		// Always return same error to prevent user enumeration
		return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
			Error: "invalid credentials",
			Code:  core.ErrInvalidRequest,
		})
	}

	// Generate JWT token
	token, err := h.svc.GenerateUserToken(user.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "failed to generate token",
			Code:  core.ErrInternalError,
		})
	}

	// Update last login
	// TODO: for now, non-blocking if login time update fails, log/block in the future
	_ = h.svc.UpdateLastLogin(user.UserID)

	return c.JSON(AuthResponse{
		Token:     token,
		UserID:    user.UserID,
		Username:  user.Username,
		Email:     user.Email,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
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