// FILE: internal/service/service.go
package service

import (
	"fmt"
	"sync"
	"time"

	"chess/internal/core"
	"chess/internal/game"
	"chess/internal/storage"

	"github.com/google/uuid"
)

// Service is a pure state manager for chess games with optional persistence
type Service struct {
	games map[string]*game.Game
	mu    sync.RWMutex
	store *storage.Store // nil if persistence disabled
}

// New creates a new service instance with optional storage
func New(store *storage.Store) (*Service, error) {
	return &Service{
		games: make(map[string]*game.Game),
		store: store,
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

	// Persist if storage enabled
	if s.store != nil {
		record := storage.GameRecord{
			GameID:          id,
			InitialFEN:      initialFEN,
			WhitePlayerID:   whitePlayer.ID,
			WhiteType:       int(whitePlayer.Type),
			WhiteLevel:      whitePlayer.Level,
			WhiteSearchTime: whitePlayer.SearchTime,
			BlackPlayerID:   blackPlayer.ID,
			BlackType:       int(blackPlayer.Type),
			BlackLevel:      blackPlayer.Level,
			BlackSearchTime: blackPlayer.SearchTime,
			StartTimeUTC:    time.Now().UTC(),
		}
		s.store.RecordNewGame(record)
	}

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
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Ensure UUID uniqueness (handle potential conflicts)
	for {
		id := uuid.New().String()
		if _, exists := s.games[id]; !exists {
			return id
		}
	}
}

// ApplyMove adds a validated move to the game history
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

	// Persist if storage enabled
	if s.store != nil {
		moveNumber := len(g.Moves())
		record := storage.MoveRecord{
			GameID:       gameID,
			MoveNumber:   moveNumber,
			MoveUCI:      moveUCI,
			FENAfterMove: newFEN,
			PlayerColor:  currentTurn.String(),
			MoveTimeUTC:  time.Now().UTC(),
		}
		s.store.RecordMove(record)
	}

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

// SetLastMoveResult stores metadata about the last move
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

	originalMoveCount := len(g.Moves())

	if err := g.UndoMoves(count); err != nil {
		return err
	}

	// Delete undone moves from storage if enabled
	if s.store != nil {
		remainingMoves := originalMoveCount - count
		s.store.DeleteUndoneMoves(gameID, remainingMoves)
	}

	return nil
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

// GetStorageHealth returns the storage component status
func (s *Service) GetStorageHealth() string {
	if s.store == nil {
		return "disabled"
	}
	if s.store.IsHealthy() {
		return "ok"
	}
	return "degraded"
}

// Close cleans up resources
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear all games
	s.games = make(map[string]*game.Game)

	// Close storage if enabled
	if s.store != nil {
		return s.store.Close()
	}

	return nil
}