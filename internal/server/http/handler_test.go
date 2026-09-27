package http

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"chess/internal/server/service"

	"github.com/gofiber/fiber/v2"
)

func TestClientIPTrustsProxyHeaderOnlyFromTrustedPeers(t *testing.T) {
	// app.Test connections originate from 0.0.0.0.
	for name, test := range map[string]struct {
		opts Options
		want string
	}{
		"no proxy configured":  {Options{ProxyHeader: "X-Real-IP"}, "0.0.0.0"},
		"untrusted peer":       {Options{TrustedProxies: []string{"10.0.0.1"}, ProxyHeader: "X-Real-IP"}, "0.0.0.0"},
		"trusted peer":         {Options{TrustedProxies: []string{"0.0.0.0/32"}, ProxyHeader: "X-Real-IP"}, "203.0.113.9"},
		"trusted, header only": {Options{TrustedProxies: []string{"0.0.0.0"}, ProxyHeader: "X-Real-IP"}, "203.0.113.9"},
	} {
		app := fiber.New(appConfig(test.opts))
		app.Get("/ip", func(c *fiber.Ctx) error { return c.SendString(clientIP(c)) })
		request := httptest.NewRequest("GET", "/ip", nil)
		request.Header.Set("X-Real-IP", "203.0.113.9")
		request.Header.Set("X-Forwarded-For", "198.51.100.1") // never consulted
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		if string(body) != test.want {
			t.Errorf("%s: client IP = %q, want %q", name, body, test.want)
		}
	}

	// A trusted proxy that omits the header must not collapse clients into one key.
	app := fiber.New(appConfig(Options{TrustedProxies: []string{"0.0.0.0"}, ProxyHeader: "X-Real-IP"}))
	app.Get("/ip", func(c *fiber.Ctx) error { return c.SendString(clientIP(c)) })
	response, err := app.Test(httptest.NewRequest("GET", "/ip", nil))
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(response.Body); string(body) != "0.0.0.0" {
		t.Errorf("missing proxy header: client IP = %q, want peer address", body)
	}
}

func TestIsValidUUIDRequiresCanonicalForm(t *testing.T) {
	for value, want := range map[string]bool{
		"5579b47e-4d3b-4eb3-abbd-846fe94cb955":          true,
		"{5579b47e-4d3b-4eb3-abbd-846fe94cb955}":        false,
		"urn:uuid:5579b47e-4d3b-4eb3-abbd-846fe94cb955": false,
		"5579b47e4d3b4eb3abbd846fe94cb955":              false,
		"not-a-uuid":                                    false,
	} {
		if got := isValidUUID(value); got != want {
			t.Errorf("isValidUUID(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestAPIRoutesAreUnversioned(t *testing.T) {
	svc, err := service.New(nil, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Route registration only; no handler runs, so no processor is needed.
	app := NewFiberApp(nil, svc, Options{DevMode: true})

	registered := make(map[string]bool)
	for _, route := range app.GetRoutes(true) {
		registered[route.Method+" "+route.Path] = true
		if strings.HasPrefix(route.Path, "/api/v1") {
			t.Errorf("versioned route still registered: %s %s", route.Method, route.Path)
		}
	}
	for _, want := range []string{
		"GET /health",
		"POST /api/auth/register",
		"POST /api/auth/login",
		"GET /api/auth/me",
		"POST /api/auth/logout",
		"POST /api/games",
		"GET /api/games/:gameId",
		"GET /api/games/:gameId/history",
		"POST /api/games/:gameId/moves",
		"GET /api/users/me/games",
	} {
		if !registered[want] {
			t.Errorf("route %s is not registered", want)
		}
	}

	response, err := app.Test(httptest.NewRequest("GET", "/api/v1/games/5579b47e-4d3b-4eb3-abbd-846fe94cb955", nil))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusNotFound {
		t.Fatalf("GET /api/v1/... status = %d, want 404", response.StatusCode)
	}
}
