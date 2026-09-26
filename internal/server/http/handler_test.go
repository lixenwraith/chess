package http

import (
	"io"
	"net/http/httptest"
	"testing"

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
