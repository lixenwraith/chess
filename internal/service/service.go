// FILE: internal/service/service.go
package service

import (
	"fmt"
	"strings"

	"chess/internal/board"
	"chess/internal/core"
	"chess/internal/engine"
	"chess/internal/game"
)

type Service struct {
	games  map[string]*game.Game
	engine *engine.UCI
}

func New() (*Service, error) {
	eng, err := engine.New()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize engine: %v", err)
	}

	return &Service{
		games:  make(map[string]*game.Game),
		engine: eng,
	}, nil
}

func (s *Service) NewGame(id string, whiteType, blackType core.PlayerType, fen ...string) error {
	initialFEN := board.StartingFEN
	if len(fen) > 0 && fen[0] != "" {
		initialFEN = fen[0]
	}

	// Use the engine to validate and canonicalize the FEN
	s.engine.NewGame()
	s.engine.SetPosition(initialFEN, []string{})
	validatedFEN, err := s.engine.GetFEN()
	if err != nil {
		return fmt.Errorf("could not get FEN from engine: %v", err)
	}

	b, err := board.FEN(validatedFEN)
	if err != nil {
		return fmt.Errorf("engine returned invalid FEN: %v", err)
	}
	startingTurn := b.Turn()

	// Setup players based on types
	whitePlayer := &core.Player{
		ID:   "white",
		Type: whiteType,
	}
	if whiteType == core.PlayerComputer {
		whitePlayer.ID = "stockfish-white"
	}

	blackPlayer := &core.Player{
		ID:   "black",
		Type: blackType,
	}
	if blackType == core.PlayerComputer {
		blackPlayer.ID = "stockfish-black"
	}

	s.games[id] = game.New(validatedFEN, whitePlayer, blackPlayer, startingTurn)

	return nil
}

func (s *Service) MakeHumanMove(gameID, uci string) error {
	// Basic move format validation
	uci = strings.ToLower(strings.TrimSpace(uci))
	if len(uci) < 4 || len(uci) > 5 {
		return fmt.Errorf("invalid move format: expected e2e4 or e7e8q")
	}

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found")
	}

	// Check if it's human's turn
	if g.NextPlayer().Type != core.PlayerHuman {
		return fmt.Errorf("not a human player's turn")
	}

	currentFEN := g.CurrentFEN()
	humanColor := g.NextTurn()

	// Try to apply human move
	s.engine.SetPosition(currentFEN, []string{uci})

	// Get FEN after human move to check if move was legal
	humanMoveFEN, err := s.engine.GetFEN()
	if err != nil {
		return fmt.Errorf("failed to get position: %v", err)
	}

	// If position didn't change, move was illegal
	if humanMoveFEN == currentFEN {
		return fmt.Errorf("illegal move")
	}

	// Record human move
	g.AddSnapshot(humanMoveFEN, uci, core.OppositeColor(humanColor))

	// Check if opponent has any legal moves
	s.engine.SetPosition(humanMoveFEN, []string{})
	search, _ := s.engine.Search(100) // Quick search to check for legal moves

	result := &game.MoveResult{
		Move:      uci,
		Player:    humanColor,
		GameState: core.StateOngoing,
	}

	if search.BestMove == "" || search.BestMove == "(none)" {
		// Human checkmated the opponent
		if humanColor == core.ColorWhite {
			g.SetState(core.StateWhiteWins)
		} else {
			g.SetState(core.StateBlackWins)
		}
		result.GameState = g.State()
	}

	// Store result in game instead of service
	g.SetLastResult(result)
	return nil
}

func (s *Service) MakeComputerMove(gameID string) (*game.MoveResult, error) {
	g, ok := s.games[gameID]
	if !ok {
		return nil, fmt.Errorf("game not found: %s", gameID)
	}

	if g.NextPlayer().Type != core.PlayerComputer {
		return nil, fmt.Errorf("not computer's turn")
	}

	currentColor := g.NextTurn()
	s.engine.SetPosition(g.CurrentFEN(), []string{})
	search, err := s.engine.Search(1000)
	if err != nil {
		return nil, fmt.Errorf("engine error: %v", err)
	}

	result := &game.MoveResult{
		Player:    currentColor,
		Score:     search.Score,
		Depth:     search.Depth,
		GameState: core.StateOngoing,
	}

	if search.BestMove == "" || search.BestMove == "(none)" {
		// No legal moves - computer is checkmated
		if currentColor == core.ColorWhite {
			g.SetState(core.StateBlackWins)
		} else {
			g.SetState(core.StateWhiteWins)
		}
		result.GameState = g.State()
		g.SetLastResult(result)
		return result, nil
	}

	result.Move = search.BestMove

	// Apply move and get resulting FEN
	s.engine.SetPosition(g.CurrentFEN(), []string{search.BestMove})
	newFEN, err := s.engine.GetFEN()
	if err != nil {
		return nil, fmt.Errorf("failed to get position: %v", err)
	}

	g.AddSnapshot(newFEN, search.BestMove, core.OppositeColor(currentColor))

	// Check if opponent has any legal moves
	s.engine.SetPosition(newFEN, []string{})
	testSearch, _ := s.engine.Search(100)

	if testSearch.BestMove == "" || testSearch.BestMove == "(none)" {
		// Computer checkmated the opponent
		if currentColor == core.ColorWhite {
			g.SetState(core.StateWhiteWins)
		} else {
			g.SetState(core.StateBlackWins)
		}
		result.GameState = g.State()
	}

	// Store result in game
	g.SetLastResult(result)
	return result, nil
}

func (s *Service) Undo(gameID string, count int) error {
	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	return g.UndoMoves(count)
}

func (s *Service) GetCurrentBoard(gameID string) (*board.Board, error) {
	g, ok := s.games[gameID]
	if !ok {
		return nil, fmt.Errorf("game not found: %s", gameID)
	}

	return board.FEN(g.CurrentFEN())
}

func (s *Service) GetGame(gameID string) (*game.Game, error) {
	g, ok := s.games[gameID]
	if !ok {
		return nil, fmt.Errorf("game not found: %s", gameID)
	}
	return g, nil
}

func (s *Service) Close() error {
	if s.engine != nil {
		return s.engine.Close()
	}
	return nil
}