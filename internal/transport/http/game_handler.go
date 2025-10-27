// FILE: internal/transport/http/game_handler.go
package http

import (
	"fmt"
	"strings"

	"chess/internal/board"
	"chess/internal/core"
	"chess/internal/game"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// CreateGame creates a new game with specified player types
func (h *HTTPHandler) CreateGame(c *fiber.Ctx) error {
	var req CreateGameRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid request body",
			Code:    ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	gameID := uuid.New().String()

	// Create game with proper type conversion
	var fenArray []string
	if req.FEN != "" {
		fenArray = []string{req.FEN}
	}

	err := h.svc.NewGame(
		gameID,
		core.PlayerType(req.White),
		core.PlayerType(req.Black),
		fenArray...,
	)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "failed to create game",
			Code:    ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Build response - cache game instance
	g, _ := h.svc.GetGame(gameID)
	response := h.buildGameResponse(gameID, g)

	// Execute computer move if computer starts
	if g.NextPlayer().Type == core.PlayerComputer && g.State() == core.StateOngoing {
		if err := h.executeComputerMove(gameID, g, &response); err != nil {
			// Log error but return game created successfully
			fmt.Printf("Warning: failed to execute initial computer move: %v\n", err)
		}
	}

	return c.Status(fiber.StatusCreated).JSON(response)
}

// GetGame retrieves current game state, executing computer move if needed
func (h *HTTPHandler) GetGame(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	g, err := h.svc.GetGame(gameID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{
			Error: "game not found",
			Code:  ErrGameNotFound,
		})
	}

	response := h.buildGameResponse(gameID, g)

	// Auto-execute computer move if it's computer's turn
	if g.NextPlayer().Type == core.PlayerComputer && g.State() == core.StateOngoing {
		if err := h.executeComputerMove(gameID, g, &response); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Error:   "failed to execute computer move",
				Code:    ErrInternalError,
				Details: err.Error(),
			})
		}
	}

	return c.JSON(response)
}

// MakeMove submits a human player move
func (h *HTTPHandler) MakeMove(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	var req MoveRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid request body",
			Code:    ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	g, err := h.svc.GetGame(gameID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{
			Error: "game not found",
			Code:  ErrGameNotFound,
		})
	}

	// Check game state BEFORE making move
	if g.State() != core.StateOngoing {
		fmt.Printf("DEBUG: Move rejected - game over (state: %s)\n", g.State())
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "game is over",
			Code:    ErrGameOver,
			Details: fmt.Sprintf("game state: %s", g.State()),
		})
	}

	// Verify it's human's turn
	currentPlayer := g.NextPlayer()
	if currentPlayer.Type != core.PlayerHuman {
		fmt.Printf("DEBUG: Move rejected - not human turn (current: %v, turn: %c)\n",
			currentPlayer.Type, g.NextTurn())
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "not human player's turn",
			Code:    ErrNotHumanTurn,
			Details: fmt.Sprintf("current turn: %c", g.NextTurn()),
		})
	}

	fmt.Printf("DEBUG: Attempting human move %s for game %s\n", req.Move, gameID)

	// Make human move
	if err := h.svc.MakeHumanMove(gameID, req.Move); err != nil {
		fmt.Printf("DEBUG: Move failed: %v\n", err)
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "invalid move",
			Code:    ErrInvalidMove,
			Details: err.Error(),
		})
	}

	// Get updated game state - refresh g
	g, _ = h.svc.GetGame(gameID)
	response := h.buildGameResponse(gameID, g)

	// Include human move info from LastResult
	if result := g.LastResult(); result != nil {
		response.LastMove = &MoveInfo{
			Move:   result.Move,
			Player: colorToString(result.Player),
		}
	}

	fmt.Printf("DEBUG: Human move successful, new state: %s, next turn: %c\n",
		g.State(), g.NextTurn())

	// Execute computer response if needed
	if g.NextPlayer().Type == core.PlayerComputer && g.State() == core.StateOngoing {
		fmt.Printf("DEBUG: Executing computer response\n")
		if err := h.executeComputerMove(gameID, g, &response); err != nil {
			// Computer move failed, but human move succeeded
			fmt.Printf("Warning: computer move failed: %v\n", err)
		}
	}

	return c.JSON(response)
}

// UndoMove undoes one or more moves
func (h *HTTPHandler) UndoMove(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	var req UndoRequest
	if err := c.BodyParser(&req); err != nil {
		// Body parsing failed, use default
		req.Count = 1
	}

	if req.Count < 1 {
		req.Count = 1
	}

	if err := h.svc.Undo(gameID, req.Count); err != nil {
		// Determine if game not found or invalid undo
		if strings.Contains(err.Error(), "not found") {
			return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{
				Error: "game not found",
				Code:  ErrGameNotFound,
			})
		}
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error:   "cannot undo moves",
			Code:    ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Return updated game state
	g, _ := h.svc.GetGame(gameID)
	response := h.buildGameResponse(gameID, g)

	return c.JSON(response)
}

// DeleteGame ends and cleans up a game
func (h *HTTPHandler) DeleteGame(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	if err := h.svc.DeleteGame(gameID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{
			Error: "game not found",
			Code:  ErrGameNotFound,
		})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// GetBoard returns ASCII representation of the board
func (h *HTTPHandler) GetBoard(c *fiber.Ctx) error {
	gameID := c.Params("gameId")

	g, err := h.svc.GetGame(gameID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{
			Error: "game not found",
			Code:  ErrGameNotFound,
		})
	}

	b, _ := h.svc.GetCurrentBoard(gameID)

	// Generate ASCII board
	ascii := h.generateASCIIBoard(b)

	return c.JSON(BoardResponse{
		FEN:   g.CurrentFEN(),
		Board: ascii,
	})
}

// Helper: Build standard game response - FIXED to use GetPlayer()
func (h *HTTPHandler) buildGameResponse(gameID string, g *game.Game) GameResponse {
	whitePlayer := g.GetPlayer(core.ColorWhite)
	blackPlayer := g.GetPlayer(core.ColorBlack)

	return GameResponse{
		GameID: gameID,
		FEN:    g.CurrentFEN(),
		Turn:   colorToString(g.NextTurn()),
		State:  stateToString(g.State()),
		Moves:  g.Moves(),
		Players: PlayersInfo{
			White: PlayerType(whitePlayer.Type),
			Black: PlayerType(blackPlayer.Type),
		},
	}
}

// Helper: Execute computer move and update response - FIXED to accept game instance
func (h *HTTPHandler) executeComputerMove(gameID string, g *game.Game, response *GameResponse) error {
	result, err := h.svc.MakeComputerMove(gameID)
	if err != nil {
		return err
	}

	// Refresh game state after computer move
	g, _ = h.svc.GetGame(gameID)

	// Update response fields
	response.FEN = g.CurrentFEN()
	response.Turn = colorToString(g.NextTurn())
	response.State = stateToString(g.State())
	response.Moves = g.Moves()

	// Add computer move info
	if result != nil {
		response.LastMove = &MoveInfo{
			Move:   result.Move,
			Player: colorToString(result.Player),
			Score:  result.Score,
			Depth:  result.Depth,
		}
	}

	return nil
}

// Helper: Generate ASCII board representation
func (h *HTTPHandler) generateASCIIBoard(b *board.Board) string {
	var sb strings.Builder
	sb.WriteString("  a b c d e f g h\n")

	for r := 0; r < 8; r++ {
		sb.WriteString(fmt.Sprintf("%d ", 8-r))
		for f := 0; f < 8; f++ {
			square := fmt.Sprintf("%c%c", 'a'+f, '8'-r)
			piece := b.GetPieceAt(square)

			if piece == 0 {
				sb.WriteString(". ")
			} else {
				sb.WriteString(fmt.Sprintf("%c ", piece))
			}
		}
		sb.WriteString(fmt.Sprintf(" %d\n", 8-r))
	}
	sb.WriteString("  a b c d e f g h")

	return sb.String()
}