package http

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"chess/internal/server/core"
	"chess/internal/server/processor"
	"chess/internal/server/service"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

const rateLimitRate = 10 // req/sec

// HTTPHandler handles HTTP requests and routes them to the processor
type HTTPHandler struct {
	proc *processor.Processor
	svc  *service.Service
}

func NewHTTPHandler(proc *processor.Processor, svc *service.Service) *HTTPHandler {
	return &HTTPHandler{proc: proc, svc: svc}
}

func NewFiberApp(proc *processor.Processor, svc *service.Service, devMode bool) *fiber.App {
	// Create handler
	h := NewHTTPHandler(proc, svc)

	// Initialize Fiber app
	app := fiber.New(fiber.Config{
		ErrorHandler: customErrorHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 35 * time.Second,
		IdleTimeout:  60 * time.Second,
	})

	// Global middleware (order matters)
	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "${time} ${status} ${method} ${path} ${latency}\n",
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization",
	}))

	// Health check (no rate limit)
	app.Get("/health", h.Health)

	// API v1 routes
	api := app.Group("/api/v1")

	// Auth routes with specific rate limiting
	auth := api.Group("/auth")

	// Register: 5 req/min per IP
	auth.Post("/register", limiter.New(limiter.Config{
		Max:        5,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(core.ErrorResponse{
				Error:   "rate limit exceeded",
				Code:    core.ErrRateLimitExceeded,
				Details: "5 registrations per minute allowed",
			})
		},
	}), h.RegisterHandler)

	// Login: 10 req/min per IP
	auth.Post("/login", limiter.New(limiter.Config{
		Max:        10,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(core.ErrorResponse{
				Error:   "rate limit exceeded",
				Code:    core.ErrRateLimitExceeded,
				Details: "10 login attempts per minute allowed",
			})
		},
	}), h.LoginHandler)

	// Create token validator closure
	validateToken := svc.ValidateToken

	// Current user (requires auth)
	auth.Get("/me", AuthRequired(validateToken), h.GetCurrentUserHandler)

	// Logout
	auth.Post("/logout", AuthRequired(validateToken), h.LogoutHandler)

	// Game routes with standard rate limiting
	maxReq := rateLimitRate
	if devMode {
		maxReq = rateLimitRate * 2
	}
	api.Use(limiter.New(limiter.Config{
		Max:        maxReq,
		Expiration: 1 * time.Second,
		KeyGenerator: func(c *fiber.Ctx) string {
			if xff := c.Get("X-Forwarded-For"); xff != "" {
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
	}))

	// Content-Type validation for POST and PUT requests
	api.Use(contentTypeValidator)

	// Middleware validation for sanitization
	api.Use(validationMiddleware)

	// Register game routes with auth middleware
	api.Post("/games", OptionalAuth(validateToken), h.CreateGame) // Optional auth for player ID association
	api.Put("/games/:gameId/players", h.ConfigurePlayers)
	api.Get("/games/:gameId", h.GetGame)
	api.Delete("/games/:gameId", h.DeleteGame)
	api.Post("/games/:gameId/moves", OptionalAuth(validateToken), h.MakeMove)
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

// Health check endpoint with storage status
func (h *HTTPHandler) Health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":  "healthy",
		"time":    time.Now().Unix(),
		"storage": h.svc.GetStorageHealth(),
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

	// Retrieve authenticated user ID if available
	userID, _ := c.Locals("userID").(string)

	// Generate game ID via service with optional user context
	cmd := processor.NewCreateGameCommand(req)
	cmd.UserID = userID // Add user ID to command if authenticated

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

	// Check for long-polling parameters
	waitStr := c.Query("wait", "false")
	moveCountStr := c.Query("moveCount", "-1")

	// Non-wait path - existing behavior
	if waitStr != "true" {
		// Create command and execute
		cmd := processor.NewGetGameCommand(gameID)
		resp := h.proc.Execute(cmd)

		// Return appropriate HTTP response
		if !resp.Success {
			return c.Status(fiber.StatusNotFound).JSON(resp.Error)
		}

		return c.JSON(resp.Data)
	}

	// Long-polling path
	moveCount, err := strconv.Atoi(moveCountStr)
	if err != nil {
		moveCount = -1
	}

	// First check if game exists and get current state
	g, err := h.svc.GetGame(gameID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(core.ErrorResponse{
			Error: "game not found",
			Code:  core.ErrGameNotFound,
		})
	}

	currentMoveCount := len(g.Moves())

	// If move count already different, return immediately
	if moveCount != currentMoveCount {
		cmd := processor.NewGetGameCommand(gameID)
		resp := h.proc.Execute(cmd)
		if !resp.Success {
			return c.Status(fiber.StatusNotFound).JSON(resp.Error)
		}
		return c.JSON(resp.Data)
	}

	// Register wait with service
	ctx := c.Context()
	notify := h.svc.RegisterWait(gameID, moveCount, ctx)

	// Wait for notification, timeout, or client disconnect
	select {
	case <-notify:
		// State changed or timeout, get fresh game state
		cmd := processor.NewGetGameCommand(gameID)
		resp := h.proc.Execute(cmd)

		// Game might have been deleted
		if !resp.Success {
			return c.Status(fiber.StatusNotFound).JSON(resp.Error)
		}

		return c.JSON(resp.Data)

	case <-ctx.Done():
		// Client disconnected
		return nil
	}
}

// MakeMove submits a move
func (h *HTTPHandler) MakeMove(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	if !isValidUUID(gameID) {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid game ID format",
			Code:    core.ErrInvalidRequest,
			Details: "game ID must be a valid UUID",
		})
	}

	validated, ok := c.Locals("validated").(bool)
	if !ok || !validated {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation bypass detected",
			Code:  core.ErrInternalError,
		})
	}

	validatedBody := c.Locals("validatedBody")
	if validatedBody == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(core.ErrorResponse{
			Error: "validation data missing",
			Code:  core.ErrInternalError,
		})
	}
	var req core.MoveRequest
	req = *(validatedBody.(*core.MoveRequest))

	// Get authenticated user ID if present
	userID, _ := c.Locals("userID").(string)

	cmd := processor.NewMakeMoveCommand(gameID, req)
	cmd.UserID = userID // Pass user context for authorization

	resp := h.proc.Execute(cmd)

	if !resp.Success {
		statusCode := fiber.StatusBadRequest
		switch resp.Error.Code {
		case core.ErrGameNotFound:
			statusCode = fiber.StatusNotFound
		case core.ErrUnauthorized:
			statusCode = fiber.StatusForbidden
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

