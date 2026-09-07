package http

import (
	"errors"
	"net/http/httptest"
	"testing"

	"chess/internal/server/service"

	"github.com/gofiber/fiber/v2"
)

func TestOptionalAuthAllowsAbsenceButRejectsInvalidToken(t *testing.T) {
	app := fiber.New()
	app.Get("/optional", OptionalAuth(func(string) (string, map[string]any, error) {
		return "", nil, errors.New("invalid token")
	}), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	request := httptest.NewRequest("GET", "/optional", nil)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("anonymous status = %d, want %d", response.StatusCode, fiber.StatusNoContent)
	}

	request = httptest.NewRequest("GET", "/optional", nil)
	request.Header.Set("Authorization", "Bearer invalid")
	response, err = app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("invalid-token status = %d, want %d", response.StatusCode, fiber.StatusUnauthorized)
	}
}

func TestAuthMiddlewareReportsStorageUnavailable(t *testing.T) {
	for _, middleware := range []func(TokenValidator) fiber.Handler{AuthRequired, OptionalAuth} {
		app := fiber.New()
		app.Get("/protected", middleware(func(string) (string, map[string]any, error) {
			return "", nil, service.ErrStorageUnavailable
		}), func(c *fiber.Ctx) error {
			return c.SendStatus(fiber.StatusNoContent)
		})

		request := httptest.NewRequest("GET", "/protected", nil)
		request.Header.Set("Authorization", "Bearer token")
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != fiber.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", response.StatusCode, fiber.StatusServiceUnavailable)
		}
	}
}
