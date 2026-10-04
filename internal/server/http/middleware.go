package http

import (
	"errors"

	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/service"

	"github.com/gofiber/fiber/v2"
	"github.com/lixenwraith/auth"
)

// TokenValidator validates JWT tokens
type TokenValidator func(token string) (userID string, claims map[string]any, err error)

// AuthRequired enforces JWT authentication for protected endpoints
func AuthRequired(validateToken TokenValidator) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get(fiber.HeaderAuthorization)
		if header == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
				Error: "missing authorization token",
				Code:  core.ErrUnauthorized,
			})
		}
		token, err := auth.ParseBearerToken(header)
		if err != nil {
			return invalidToken(c)
		}

		return authenticate(c, validateToken, token)
	}
}

// OptionalAuth permits an absent Authorization header but rejects a malformed
// or invalid one instead of silently downgrading an intended authenticated
// request to anonymous access.
func OptionalAuth(validateToken TokenValidator) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get(fiber.HeaderAuthorization)
		if header == "" {
			return c.Next()
		}
		token, err := auth.ParseBearerToken(header)
		if err != nil {
			return invalidToken(c)
		}
		return authenticate(c, validateToken, token)
	}
}

func authenticate(c *fiber.Ctx, validateToken TokenValidator, token string) error {
	userID, claims, err := validateToken(token)
	if err != nil {
		if errors.Is(err, service.ErrStorageDisabled) || errors.Is(err, service.ErrStorageUnavailable) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
				Error: "authentication storage unavailable", Code: core.ErrStorageUnavailable,
			})
		}
		return invalidToken(c)
	}

	c.Locals("userID", userID)
	if sessionID, ok := claims["session_id"].(string); ok {
		c.Locals("sessionID", sessionID)
	}
	return c.Next()
}

func invalidToken(c *fiber.Ctx) error {
	return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
		Error: "invalid or expired token", Code: core.ErrUnauthorized,
	})
}
