// FILE: internal/service/service.go
package service

import (
	"fmt"
	"sync"

	"chess/internal/core"
	"chess/internal/game"

	"github.com/google/uuid"
)

// Service is a pure state manager for chess games
// It has NO knowledge of chess rules or engine interactions
type Service struct {
	games map[string]*game.Game
	mu    sync.RWMutex
}

// New creates a new service instance
func New() (*Service, error) {
	return &Service{
		games: make(map[string]*game.Game),
	}, nil
}

// CreateGame creates game with player configuration
func (s *Service) CreateGame(id string, whiteConfig, blackConfig core.PlayerConfig, initialFEN string, startingTurn core.Color) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.games[id]; exists {
		return fmt.Errorf("game %s already exists", id)
	}

	// Create players with UUIDs and config
	whitePlayer := core.NewPlayer(whiteConfig, core.ColorWhite)
	blackPlayer := core.NewPlayer(blackConfig, core.ColorBlack)

	s.games[id] = game.New(initialFEN, whitePlayer, blackPlayer, startingTurn)
	return nil
}

// UpdatePlayers replaces players in an existing game
func (s *Service) UpdatePlayers(gameID string, whiteConfig, blackConfig core.PlayerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Create new player instances with new UUIDs
	whitePlayer := core.NewPlayer(whiteConfig, core.ColorWhite)
	blackPlayer := core.NewPlayer(blackConfig, core.ColorBlack)

	// Update the game's players
	g.UpdatePlayers(whitePlayer, blackPlayer)

	return nil
}

// GetGame retrieves a game by ID
func (s *Service) GetGame(gameID string) (*game.Game, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.games[gameID]
	if !ok {
		return nil, fmt.Errorf("game not found: %s", gameID)
	}
	return g, nil
}

// GenerateGameID creates a new unique game ID
func (s *Service) GenerateGameID() string {
	return uuid.New().String()
}

// ApplyMove adds a validated move to the game history
// The processor has already validated this move and calculated the new FEN
func (s *Service) ApplyMove(gameID, moveUCI, newFEN string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Determine whose turn it was before this move
	currentTurn := g.NextTurnColor()
	nextTurn := core.OppositeColor(currentTurn)

	// Add the new position to game history
	g.AddSnapshot(newFEN, moveUCI, nextTurn)

	return nil
}

// UpdateGameState sets the game's end state (checkmate, stalemate, etc)
func (s *Service) UpdateGameState(gameID string, state core.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	g.SetState(state)
	return nil
}

// SetLastMoveResult stores metadata about the last move (score, depth, etc)
// Used by processor to track computer move evaluations
func (s *Service) SetLastMoveResult(gameID string, result *game.MoveResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	g.SetLastResult(result)
	return nil
}

// UndoMoves removes the specified number of moves from game history
func (s *Service) UndoMoves(gameID string, count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	return g.UndoMoves(count)
}

// DeleteGame removes a game from memory
func (s *Service) DeleteGame(gameID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.games[gameID]; !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	delete(s.games, gameID)
	return nil
}

// Close cleans up resources (currently a no-op as no engine to close)
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear all games
	s.games = make(map[string]*game.Game)
	return nil
}