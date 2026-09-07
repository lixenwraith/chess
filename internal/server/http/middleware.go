package http

import (
	"errors"
	"strings"

	"chess/internal/server/core"
	"chess/internal/server/service"

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
				Code:  core.ErrUnauthorized,
			})
		}

		userID, claims, err := validateToken(token)
		if err != nil {
			if errors.Is(err, service.ErrStorageDisabled) || errors.Is(err, service.ErrStorageUnavailable) {
				return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
					Error: "authentication storage unavailable", Code: core.ErrStorageUnavailable,
				})
			}
			return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
				Error: "invalid or expired token",
				Code:  core.ErrUnauthorized,
			})
		}

		c.Locals("userID", userID)
		if sessionID, ok := claims["session_id"].(string); ok {
			c.Locals("sessionID", sessionID)
		}
		return c.Next()
	}
}

// OptionalAuth permits an absent token but rejects an invalid token instead of
// silently downgrading an intended authenticated request to anonymous access.
func OptionalAuth(validateToken TokenValidator) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := extractBearerToken(c.Get("Authorization"))
		if token == "" {
			return c.Next()
		}

		userID, claims, err := validateToken(token)
		if err != nil {
			if errors.Is(err, service.ErrStorageDisabled) || errors.Is(err, service.ErrStorageUnavailable) {
				return c.Status(fiber.StatusServiceUnavailable).JSON(core.ErrorResponse{
					Error: "authentication storage unavailable", Code: core.ErrStorageUnavailable,
				})
			}
			return c.Status(fiber.StatusUnauthorized).JSON(core.ErrorResponse{
				Error: "invalid or expired token", Code: core.ErrUnauthorized,
			})
		}

		c.Locals("userID", userID)
		if sessionID, ok := claims["session_id"].(string); ok {
			c.Locals("sessionID", sessionID)
		}
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
