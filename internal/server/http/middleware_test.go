package http

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/lixenwraith/chess/internal/server/service"

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

func TestAuthMiddlewareParsesBearerHeaderStrictly(t *testing.T) {
	validate := func(token string) (string, map[string]any, error) {
		if token != "good.token.value" {
			return "", nil, errors.New("invalid token")
		}
		return "user", map[string]any{"session_id": "session"}, nil
	}
	for _, middleware := range []func(TokenValidator) fiber.Handler{AuthRequired, OptionalAuth} {
		app := fiber.New()
		app.Get("/route", middleware(validate), func(c *fiber.Ctx) error {
			return c.SendString(c.Locals("userID").(string))
		})
		for header, want := range map[string]int{
			"Bearer good.token.value": fiber.StatusOK,
			"bearer good.token.value": fiber.StatusOK, // RFC 6750 scheme is case-insensitive
			"Basic dXNlcjpwYXNz":      fiber.StatusUnauthorized,
			"Bearer good token":       fiber.StatusUnauthorized,
			"Bearer ":                 fiber.StatusUnauthorized,
			"good.token.value":        fiber.StatusUnauthorized,
		} {
			request := httptest.NewRequest("GET", "/route", nil)
			request.Header.Set("Authorization", header)
			response, err := app.Test(request)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != want {
				t.Errorf("header %q: status = %d, want %d", header, response.StatusCode, want)
			}
		}
	}
}
