// FILE: internal/http/handler.go
package http

import (
	"chess/internal/core"
	"chess/internal/processor"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

const rateLimitRate = 10 // req/sec

type HTTPHandler struct {
	proc *processor.Processor
}

func NewHTTPHandler(proc *processor.Processor) *HTTPHandler {
	return &HTTPHandler{proc: proc}
}

func NewFiberApp(proc *processor.Processor, devMode bool) *fiber.App {
	// Create handler
	h := NewHTTPHandler(proc)

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
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept",
	}))

	// Health check (no rate limit)
	app.Get("/health", h.Health)

	// API v1 routes with rate limiting
	api := app.Group("/api/v1")

	// Rate limiter: 10/20 req/sec per IP with expiry
	maxReq := rateLimitRate
	if devMode {
		maxReq = rateLimitRate * 2 // Loosen rate limiter for testing
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
			return c.Status(fiber.StatusTooManyRequests).JSON(core.ErrorResponse{
				Error:   "rate limit exceeded",
				Code:    core.ErrRateLimitExceeded,
				Details: fmt.Sprintf("%d requests per second allowed", maxReq),
			})
		},
		Storage:                nil, // Use in-memory storage (default)
		SkipFailedRequests:     false,
		SkipSuccessfulRequests: false,
	}))

	// Content-Type validation for POST and PUT requests
	api.Use(contentTypeValidator)

	// Middleware validation for sanitization
	api.Use(validationMiddleware)

	// Register game routes
	api.Post("/games", h.CreateGame)
	api.Put("/games/:gameId/players", h.ConfigurePlayers)
	api.Get("/games/:gameId", h.GetGame)
	api.Delete("/games/:gameId", h.DeleteGame)
	api.Post("/games/:gameId/moves", h.MakeMove)
	api.Post("/games/:gameId/undo", h.UndoMove)
	api.Get("/games/:gameId/board", h.GetBoard)

	return app
}

// contentTypeValidator ensures POST and PUT requests have application/json
func contentTypeValidator(c *fiber.Ctx) error {
	method := c.Method()
	if method == fiber.MethodPost || method == fiber.MethodPut {
		contentType := c.Get("Content-Type")
		if contentType != "application/json" && contentType != "" {
			return c.Status(fiber.StatusUnsupportedMediaType).JSON(core.ErrorResponse{
				Error:   "unsupported media type",
				Code:    core.ErrInvalidContent,
				Details: "Content-Type must be application/json",
			})
		}
	}
	return c.Next()
}

// customErrorHandler provides consistent error responses
func customErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	response := core.ErrorResponse{
		Error: "internal server error",
		Code:  core.ErrInternalError,
	}

	// Check if it's a Fiber error
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		response.Error = e.Message

		// Map HTTP status to error codes
		switch code {
		case fiber.StatusNotFound:
			response.Code = core.ErrGameNotFound
		case fiber.StatusBadRequest:
			response.Code = core.ErrInvalidRequest
		case fiber.StatusTooManyRequests:
			response.Code = core.ErrRateLimitExceeded
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

// CreateGame creates a new game with specified player types
func (h *HTTPHandler) CreateGame(c *fiber.Ctx) error {
	// Ensure middleware validation ran
	validated, ok := c.Locals("validated").(bool)
	if !ok || !validated {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation bypass detected",
			Code:  core.ErrInternalError,
		})
	}

	// Retrieve validated parsed body
	validatedBody := c.Locals("validatedBody")
	if validatedBody == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation data missing",
			Code:  core.ErrInternalError,
		})
	}
	var req core.CreateGameRequest
	req = *(validatedBody.(*core.CreateGameRequest))

	// Let processor generate game ID via service
	cmd := processor.NewCreateGameCommand(req)

	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response
	if !resp.Success {
		return c.Status(fiber.StatusBadRequest).JSON(resp.Error)
	}

	return c.Status(fiber.StatusCreated).JSON(resp.Data)
}

// ConfigurePlayers updates player configuration mid-game
func (h *HTTPHandler) ConfigurePlayers(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	// Validate UUID format
	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	// Ensure middleware validation ran
	validated, ok := c.Locals("validated").(bool)
	if !ok || !validated {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation bypass detected",
			Code:  core.ErrInternalError,
		})
	}

	// Retrieve validated parsed body
	validatedBody := c.Locals("validatedBody")
	if validatedBody == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation data missing",
			Code:  core.ErrInternalError,
		})
	}
	var req core.ConfigurePlayersRequest
	req = *(validatedBody.(*core.ConfigurePlayersRequest))

	// Create command and execute
	cmd := processor.NewConfigurePlayersCommand(gameID, req)
	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response
	if !resp.Success {
		statusCode := fiber.StatusBadRequest
		if resp.Error.Code == core.ErrGameNotFound {
			statusCode = fiber.StatusNotFound
		}
		return c.Status(statusCode).JSON(resp.Error)
	}

	return c.JSON(resp.Data)
}

// GetGame retrieves current game state
func (h *HTTPHandler) GetGame(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	// Validate UUID format
	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	// Create command and execute
	cmd := processor.NewGetGameCommand(gameID)
	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response
	if !resp.Success {
		return c.Status(fiber.StatusNotFound).JSON(resp.Error)
	}

	return c.JSON(resp.Data)
}

// MakeMove submits a move
func (h *HTTPHandler) MakeMove(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	// Validate UUID format
	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	// Ensure middleware validation ran
	validated, ok := c.Locals("validated").(bool)
	if !ok || !validated {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation bypass detected",
			Code:  core.ErrInternalError,
		})
	}

	// Retrieve validated parsed body
	validatedBody := c.Locals("validatedBody")
	if validatedBody == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation data missing",
			Code:  core.ErrInternalError,
		})
	}
	var req core.MoveRequest
	req = *(validatedBody.(*core.MoveRequest))

	// Create command and execute
	cmd := processor.NewMakeMoveCommand(gameID, req)
	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response with correct status code
	if !resp.Success {
		statusCode := fiber.StatusBadRequest
		if resp.Error.Code == core.ErrGameNotFound {
			statusCode = fiber.StatusNotFound
		}
		return c.Status(statusCode).JSON(resp.Error)
	}

	return c.JSON(resp.Data)
}

// UndoMove undoes one or more moves
func (h *HTTPHandler) UndoMove(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	// Validate UUID format
	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	// Ensure middleware validation ran
	validated, ok := c.Locals("validated").(bool)
	if !ok || !validated {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation bypass detected",
			Code:  core.ErrInternalError,
		})
	}

	// Retrieve validated parsed body
	validatedBody := c.Locals("validatedBody")
	if validatedBody == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation data missing",
			Code:  core.ErrInternalError,
		})
	}
	var req core.UndoRequest
	req = *(validatedBody.(*core.UndoRequest))

	// Create command and execute
	cmd := processor.NewUndoMoveCommand(gameID, req)
	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response
	if !resp.Success {
		statusCode := fiber.StatusBadRequest
		if resp.Error.Code == core.ErrGameNotFound {
			statusCode = fiber.StatusNotFound
		}
		return c.Status(statusCode).JSON(resp.Error)
	}

	return c.JSON(resp.Data)
}

// DeleteGame ends and cleans up a game
func (h *HTTPHandler) DeleteGame(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	// Validate UUID format
	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	// Create command and execute
	cmd := processor.NewDeleteGameCommand(gameID)
	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response
	if !resp.Success {
		return c.Status(fiber.StatusNotFound).JSON(resp.Error)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// GetBoard returns ASCII representation of the board
func (h *HTTPHandler) GetBoard(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	// Validate UUID format
	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	// Create command and execute
	cmd := processor.NewGetBoardCommand(gameID)
	resp := h.proc.Execute(cmd)

	// Return appropriate HTTP response
	if !resp.Success {
		return c.Status(fiber.StatusNotFound).JSON(resp.Error)
	}

	return c.JSON(resp.Data)
}