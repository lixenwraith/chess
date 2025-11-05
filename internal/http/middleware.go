// FILE: internal/http/middleware.go
package http

import (
	"strings"

	"chess/internal/core"

	"github.com/gofiber/fiber/v2"
)

// TokenValidator validates JWT tokens
type TokenValidator func(token string) (userID string, claims map[string]any, err error)

// AuthRequired enforces JWT authentication for protected endpoints
func AuthRequired(validateToken TokenValidator) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := extractBearerToken(c.Get("Authorization"))
		if token == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
				Error: "missing authorization token",
				Code:  core.ErrInvalidRequest,
			})
		}

		userID, _, err := validateToken(token)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
				Error: "invalid or expired token",
				Code:  core.ErrInvalidRequest,
			})
		}

		c.Locals("userID", userID)
		return c.Next()
	}
}

// OptionalAuth validates JWT if present but allows anonymous access
func OptionalAuth(validateToken TokenValidator) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := extractBearerToken(c.Get("Authorization"))
		if token == "" {
			return c.Next()
		}

		userID, _, err := validateToken(token)
		if err == nil {
			c.Locals("userID", userID)
		}
		// Continue regardless of token validity
		return c.Next()
	}
}

// extractBearerToken extracts JWT token from Authorization header
func extractBearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}