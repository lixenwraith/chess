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

	"github.com/gofiber/fiber/v3"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{1,40}$`)

type RegisterRequest struct {
	Username string `json:"username" validate:"required,min=1,max=40"`
	Email    string `json:"email" validate:"omitempty,max=255"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type LoginRequest struct {
	Identifier string `json:"identifier" validate:"required"` // username or email
	Password   string `json:"password" validate:"required"`
}

// DeleteAccountRequest re-confirms the password before an account is deleted.
type DeleteAccountRequest struct {
	Password string `json:"password" validate:"required"`
}

// RecoverRequest asks for a password-reset email.
type RecoverRequest struct {
	Identifier string `json:"identifier" validate:"required"` // username or email
}

// ResetPasswordRequest sets a new password with the token from the email.
type ResetPasswordRequest struct {
	Token    string `json:"token" validate:"required"`
	Password string `json:"password" validate:"required"`
}

type AuthResponse struct {
	Token     string    `json:"token"`
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type UserResponse struct {
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func (h *HTTPHandler) RegisterHandler(c fiber.Ctx) error {
	var req RegisterRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	if !usernameRegex.MatchString(req.Username) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid username format",
			Code:    core.ErrInvalidRequest,
			Details: "username must be 1-40 characters, alphanumeric and underscore only",
		})
	}

	if req.Email != "" && !emailRegex.MatchString(req.Email) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid email format",
			Code:    core.ErrInvalidRequest,
			Details: "email must be a valid email address",
		})
	}

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
	user, sessionID, err := h.svc.RegisterUser(c.Context(), req.Username, req.Email, req.Password)
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

func (h *HTTPHandler) LoginHandler(c fiber.Ctx) error {
	var req LoginRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Normalize identifier for case-insensitive lookup
	req.Identifier = strings.ToLower(req.Identifier)

	// Authenticate user and create session (invalidates previous session)
	user, sessionID, err := h.svc.AuthenticateUser(c.Context(), req.Identifier, req.Password)
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

func (h *HTTPHandler) GetCurrentUserHandler(c fiber.Ctx) error {
	userID, ok := c.Locals("userID").(string)
	if !ok || userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
			Error: "unauthorized",
			Code:  core.ErrInvalidRequest,
		})
	}

	user, err := h.svc.GetUserByID(c.Context(), userID)
	if errors.Is(err, service.ErrStorageUnavailable) {
		return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
			Error: "account storage unavailable", Code: core.ErrStorageUnavailable,
		})
	}
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

func (h *HTTPHandler) LogoutHandler(c fiber.Ctx) error {
	sessionID, ok := c.Locals("sessionID").(string)
	if !ok || sessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error: "no active session",
			Code:  core.ErrInvalidRequest,
		})
	}

	err := h.svc.InvalidateSession(c.Context(), sessionID)
	if errors.Is(err, service.ErrStorageUnavailable) {
		return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
			Error: "account storage unavailable", Code: core.ErrStorageUnavailable,
		})
	}
	if err != nil {
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
func (h *HTTPHandler) DeleteAccountHandler(c fiber.Ctx) error {
	userID, _ := c.Locals("userID").(string)
	sessionID, _ := c.Locals("sessionID").(string)
	if userID == "" || sessionID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
			Error: "unauthorized",
			Code:  core.ErrInvalidRequest,
		})
	}

	var req DeleteAccountRequest
	if err := c.Bind().Body(&req); err != nil || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: "password is required to delete the account",
		})
	}

	if err := h.svc.DeleteAccount(c.Context(), userID, sessionID, req.Password); err != nil {
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

// RecoverHandler queues a password-reset email. The answer, and how soon it
// comes, is the same whether or not the account exists or has an email; only
// a missing transport or a full queue (503) is reported.
func (h *HTTPHandler) RecoverHandler(c fiber.Ctx) error {
	var req RecoverRequest
	if err := c.Bind().Body(&req); err != nil || strings.TrimSpace(req.Identifier) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: "identifier (username or email) is required",
		})
	}
	if len(req.Identifier) > 254 {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error: "invalid identifier", Code: core.ErrInvalidRequest,
		})
	}

	if err := h.svc.RequestPasswordReset(req.Identifier); err != nil {
		switch {
		case errors.Is(err, service.ErrMailDisabled):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "password recovery is not available on this server", Code: core.ErrNotConfigured,
			})
		case errors.Is(err, service.ErrMailBusy):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: err.Error(), Code: core.ErrResourceLimit,
			})
		case errors.Is(err, service.ErrStorageDisabled):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "account storage unavailable", Code: core.ErrStorageUnavailable,
			})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
				Error: "failed to start password recovery", Code: core.ErrInternalError,
			})
		}
	}
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"message": "If the account has an email address, a reset link is on its way to it.",
	})
}

// ResetPasswordHandler sets a new password from a reset link and signs the
// user in, returning a session token like login.
func (h *HTTPHandler) ResetPasswordHandler(c fiber.Ctx) error {
	var req ResetPasswordRequest
	if err := c.Bind().Body(&req); err != nil || req.Token == "" || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: "token and password are required",
		})
	}
	if err := validatePassword(req.Password); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "weak password",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	user, sessionID, err := h.svc.ResetPassword(c.Context(), req.Token, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrResetInvalid):
			return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
				Error:   "invalid or expired reset link",
				Code:    core.ErrInvalidRequest,
				Details: "request a new link from the sign-in dialog",
			})
		case errors.Is(err, service.ErrAuthBusy):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "authentication temporarily unavailable", Code: core.ErrResourceLimit,
			})
		case errors.Is(err, service.ErrStorageDisabled), errors.Is(err, service.ErrStorageUnavailable):
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "account storage unavailable", Code: core.ErrStorageUnavailable,
			})
		default:
			return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
				Error: "failed to reset password", Code: core.ErrInternalError,
			})
		}
	}

	token, err := h.svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "failed to generate token", Code: core.ErrInternalError,
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
