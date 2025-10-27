// FILE: internal/transport/http/handler.go
package http

import (
	"fmt"
	"strings"
	"time"

	"chess/internal/service"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

type HTTPHandler struct {
	svc *service.Service
}

func NewHTTPHandler(svc *service.Service) *HTTPHandler {
	return &HTTPHandler{svc: svc}
}

func NewFiberApp(svc *service.Service, devMode bool) *fiber.App {
	// Create handler
	h := NewHTTPHandler(svc)

	// Initialize Fiber app
	app := fiber.New(fiber.Config{
		ErrorHandler: customErrorHandler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	})

	// Global middleware (order matters)
	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "${time} ${status} ${method} ${path} ${latency}\n",
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,DELETE,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept",
	}))

	// Health check (no rate limit)
	app.Get("/health", h.Health)

	// API v1 routes with rate limiting
	api := app.Group("/api/v1")

	// Rate limiter: 1/10 req/sec per IP with expiry
	maxReq := 1
	if devMode {
		maxReq = 10
	}
	api.Use(limiter.New(limiter.Config{
		Max:        maxReq,          // Allow requests per second
		Expiration: 1 * time.Second, // Per second
		KeyGenerator: func(c *fiber.Ctx) string {
			// Check X-Forwarded-For first, then X-Real-IP, then RemoteIP
			if xff := c.Get("X-Forwarded-For"); xff != "" {
				// Take the first IP from X-Forwarded-For chain
				if idx := strings.Index(xff, ","); idx != -1 {
					return strings.TrimSpace(xff[:idx])
				}
				return xff
			}
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(ErrorResponse{
				Error:   "rate limit exceeded",
				Code:    ErrRateLimitExceeded,
				Details: fmt.Sprintf("%d requests per second allowed", maxReq),
			})
		},
		Storage:                nil, // Use in-memory storage (default)
		SkipFailedRequests:     false,
		SkipSuccessfulRequests: false,
	}))

	// Content-Type validation for POST requests
	api.Use(contentTypeValidator)

	// Register game routes
	api.Post("/games", h.CreateGame)
	api.Get("/games/:gameId", h.GetGame)
	api.Delete("/games/:gameId", h.DeleteGame)
	api.Post("/games/:gameId/moves", h.MakeMove)
	api.Post("/games/:gameId/undo", h.UndoMove)
	api.Get("/games/:gameId/board", h.GetBoard)

	return app
}

// contentTypeValidator ensures POST requests have application/json
func contentTypeValidator(c *fiber.Ctx) error {
	if c.Method() == "POST" {
		contentType := c.Get("Content-Type")
		if contentType != "application/json" && contentType != "" {
			return c.Status(fiber.StatusUnsupportedMediaType).JSON(ErrorResponse{
				Error:   "unsupported media type",
				Code:    ErrInvalidContent,
				Details: "Content-Type must be application/json",
			})
		}
	}
	return c.Next()
}

// customErrorHandler provides consistent error responses
func customErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	response := ErrorResponse{
		Error: "internal server error",
		Code:  ErrInternalError,
	}

	// Check if it's a Fiber error
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		response.Error = e.Message

		// Map HTTP status to error codes
		switch code {
		case fiber.StatusNotFound:
			response.Code = ErrGameNotFound
		case fiber.StatusBadRequest:
			response.Code = ErrInvalidRequest
		case fiber.StatusTooManyRequests:
			response.Code = ErrRateLimitExceeded
		}
	}

	return c.Status(code).JSON(response)
}

// Health check endpoint
func (h *HTTPHandler) Health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status": "healthy",
		"time":   time.Now().Unix(),
	})
}