// FILE: lixenwraith/chess/internal/server/processor/processor.go

package processor

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"chess/internal/server/board"
	"chess/internal/server/core"
	"chess/internal/server/engine"
	"chess/internal/server/game"
	"chess/internal/server/service"
)

const (
	minSearchTime = 100
)

// FEN validation regex
var fenPattern = regexp.MustCompile(`^[rnbqkpRNBQKP1-8/]+ [wb] [KQkq-]+ [a-h1-8-]+ \d+ \d+$`)

// Processor handles command execution and coordinates between service and engine layers
type Processor struct {
	svc           *service.Service
	queue         *EngineQueue
	validationEng *engine.UCI // For synchronous move validation
	mu            sync.RWMutex
}

// New creates a processor with its own engine instances
func New(svc *service.Service) (*Processor, error) {
	// Create validation engine
	validationEng, err := engine.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create validation engine: %v", err)
	}

	return &Processor{
		svc:           svc,
		queue:         NewEngineQueue(2), // 2 workers for computer moves
		validationEng: validationEng,
	}, nil
}

func (p *Processor) Execute(cmd Command) ProcessorResponse {
	switch cmd.Type {
	case CmdCreateGame:
		return p.handleCreateGame(cmd)
	case CmdConfigurePlayers:
		return p.handleConfigurePlayers(cmd)
	case CmdGetGame:
		return p.handleGetGame(cmd)
	case CmdMakeMove:
		return p.handleMakeMove(cmd)
	case CmdUndoMove:
		return p.handleUndoMove(cmd)
	case CmdDeleteGame:
		return p.handleDeleteGame(cmd)
	case CmdGetBoard:
		return p.handleGetBoard(cmd)
	default:
		return p.errorResponse("unknown command", core.ErrInvalidRequest)
	}
}

// isFENSafe check for control characters that could inject UCI commands and FEN pattern match
func (p *Processor) isFENSafe(fen string) bool {
	// Check for control characters
	for _, r := range fen {
		if unicode.IsControl(r) && r != ' ' {
			return false
		}
	}

	// Validate FEN format
	return fenPattern.MatchString(fen)
}

func (p *Processor) isMoveSafe(move string) bool {
	// Check for control characters
	for _, r := range move {
		if unicode.IsControl(r) {
			return false
		}
	}

	// UCI valid moves are 4-5 characters only
	// Examples: e2e4 / e1g1 (castle) / a7a8q (promotion)
	// UCI moves: [a-h][1-8][a-h][1-8][qrbn]?
	if len(move) < 4 || len(move) > 5 {
		return false
	}

	// Check each character
	if move[0] < 'a' || move[0] > 'h' ||
		move[1] < '1' || move[1] > '8' ||
		move[2] < 'a' || move[2] > 'h' ||
		move[3] < '1' || move[3] > '8' {
		return false
	}

	// Promotion piece if present
	if len(move) == 5 {
		promotion := move[4]
		if promotion != 'q' && promotion != 'r' && promotion != 'b' && promotion != 'n' {
			return false
		}
	}

	return true
}

// handleCreateGame creates a new game and triggers computer move if needed
func (p *Processor) handleCreateGame(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.CreateGameRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	// Enforce minimum searchTime for computer players
	if args.White.Type == core.PlayerComputer && args.White.SearchTime < 100 {
		args.White.SearchTime = minSearchTime
	}
	if args.Black.Type == core.PlayerComputer && args.Black.SearchTime < 100 {
		args.Black.SearchTime = minSearchTime
	}

	// Generate game ID
	gameID := p.svc.GenerateGameID()

	// Validate and canonicalize FEN if provided
	initialFEN := board.StartingFEN
	if args.FEN != "" {
		if !p.isFENSafe(args.FEN) {
			return p.errorResponse("invalid FEN format or characters", core.ErrInvalidFEN)
		}
		initialFEN = args.FEN
	}

	p.mu.Lock()
	p.validationEng.NewGame()
	p.validationEng.SetPosition(initialFEN, []string{})
	validatedFEN, err := p.validationEng.GetFEN()
	p.mu.Unlock()

	if err != nil {
		return p.errorResponse(fmt.Sprintf("invalid FEN: %v", err), core.ErrInvalidRequest)
	}

	// Parse to get starting turn
	b, err := board.ParseFEN(validatedFEN)
	if err != nil {
		return p.errorResponse(fmt.Sprintf("FEN parse error: %v", err), core.ErrInvalidRequest)
	}

	// Create players with appropriate IDs
	whitePlayer := core.NewPlayer(args.White, core.ColorWhite)
	blackPlayer := core.NewPlayer(args.Black, core.ColorBlack)

	// Override player IDs for authenticated human players
	if args.White.Type == core.PlayerHuman && cmd.UserID != "" {
		whitePlayer.ID = cmd.UserID
	}
	if args.Black.Type == core.PlayerHuman && cmd.UserID != "" {
		blackPlayer.ID = cmd.UserID
	}

	// Create game in service with fully-formed players
	if err = p.svc.CreateGame(gameID, whitePlayer, blackPlayer, validatedFEN, b.Turn()); err != nil {
		return p.errorResponse(fmt.Sprintf("failed to create game: %v", err), core.ErrInternalError)
	}

	// Check if the initial FEN represents a completed game
	p.checkGameEnd(gameID, validatedFEN, core.OppositeColor(b.Turn()))

	// Get created game
	g, err := p.svc.GetGame(gameID)
	if err != nil {
		return p.errorResponse("game creation failed", core.ErrInternalError)
	}

	// Build response
	response := p.buildGameResponse(gameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleConfigurePlayers updates player configuration mid-game
func (p *Processor) handleConfigurePlayers(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.ConfigurePlayersRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	if args.White.Type == core.PlayerComputer && args.White.SearchTime < 100 {
		args.White.SearchTime = minSearchTime
	}
	if args.Black.Type == core.PlayerComputer && args.Black.SearchTime < 100 {
		args.Black.SearchTime = minSearchTime
	}

	g, err := p.svc.GetGame(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Block configuration changes during computer move
	if g.State() == core.StatePending {
		return p.errorResponse("cannot change players while computer is calculating", core.ErrInvalidRequest)
	}

	// Create new player instances
	whitePlayer := core.NewPlayer(args.White, core.ColorWhite)
	blackPlayer := core.NewPlayer(args.Black, core.ColorBlack)

	// Update players in service
	if err = p.svc.UpdatePlayers(cmd.GameID, whitePlayer, blackPlayer); err != nil {
		return p.errorResponse(fmt.Sprintf("failed to update players: %v", err), core.ErrInternalError)
	}

	// Get updated game
	g, _ = p.svc.GetGame(cmd.GameID)
	response := p.buildGameResponse(cmd.GameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleGetGame retrieves game state and triggers computer move if needed
func (p *Processor) handleGetGame(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGame(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	response := p.buildGameResponse(cmd.GameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleMakeMove processes human moves
func (p *Processor) handleMakeMove(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.MoveRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	g, err := p.svc.GetGame(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Validate game state
	switch g.State() {
	case core.StatePending:
		return p.errorResponse("computer move in progress", core.ErrInvalidRequest)
	case core.StateStuck:
		return p.errorResponse("game is stuck due to engine error", core.ErrGameOver)
	case core.StateWhiteWins, core.StateBlackWins, core.StateDraw, core.StateStalemate:
		return p.errorResponse(fmt.Sprintf("game is over: %s", g.State()), core.ErrGameOver)
	case core.StateOngoing:
		break
	default:
		return p.errorResponse("game is in invalid state", core.ErrInvalidRequest)
	}

	// Handle empty move string - trigger computer move
	if strings.TrimSpace(args.Move) == "cccc" {
		if g.NextPlayer().Type != core.PlayerComputer {
			return p.errorResponse("not computer player's turn", core.ErrNotHumanTurn)
		}

		// Set state to pending and trigger computer move
		p.svc.UpdateGameState(cmd.GameID, core.StatePending)
		p.triggerComputerMove(cmd.GameID, g)

		// Re-fetch for updated state
		g, _ = p.svc.GetGame(cmd.GameID)
		response := p.buildGameResponse(cmd.GameID, g)
		response.LastMove = &core.MoveInfo{
			PlayerColor: g.NextTurnColor().String(),
		}

		return ProcessorResponse{
			Success: true,
			Pending: true,
			Data:    response,
		}
	}

	// Handle human move
	if g.NextPlayer().Type != core.PlayerHuman {
		return p.errorResponse("not human player's turn", core.ErrNotHumanTurn)
	}

	// Normalize and validate move format
	move := strings.ToLower(strings.TrimSpace(args.Move))
	if !p.isMoveSafe(move) {
		return p.errorResponse("invalid move format", core.ErrInvalidMove)
	}

	currentFEN := g.CurrentFEN()
	currentColor := g.NextTurnColor()

	// Validate move with engine
	p.mu.Lock()
	p.validationEng.SetPosition(currentFEN, []string{move})
	newFEN, err := p.validationEng.GetFEN()
	p.mu.Unlock()

	if err != nil || newFEN == currentFEN {
		return p.errorResponse("illegal move", core.ErrInvalidMove)
	}

	// Apply move to game state via service
	if err = p.svc.ApplyMove(cmd.GameID, move, newFEN); err != nil {
		return p.errorResponse(fmt.Sprintf("failed to apply move: %v", err), core.ErrInternalError)
	}

	// Store move result metadata
	p.svc.SetLastMoveResult(cmd.GameID, &game.MoveResult{
		Move:        move,
		PlayerColor: currentColor,
		GameState:   core.StateOngoing,
	})

	// Check for checkmate/stalemate
	p.checkGameEnd(cmd.GameID, newFEN, currentColor)

	// Get updated game
	g, _ = p.svc.GetGame(cmd.GameID)
	response := p.buildGameResponse(cmd.GameID, g)

	// Add human move info
	response.LastMove = &core.MoveInfo{
		Move:        move,
		PlayerColor: currentColor.String(),
	}

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleUndoMove reverts game state
func (p *Processor) handleUndoMove(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGame(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Check game state
	switch g.State() {
	case core.StatePending:
		return p.errorResponse("cannot undo while computer move is in progress", core.ErrInvalidRequest)
	case core.StateStuck:
		return p.errorResponse("cannot undo in stuck game", core.ErrInvalidRequest)
	}

	args := core.UndoRequest{Count: 1}
	if cmd.Args != nil {
		if req, ok := cmd.Args.(core.UndoRequest); ok {
			args = req
		}
	}

	if err = p.svc.UndoMoves(cmd.GameID, args.Count); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return p.errorResponse("game not found", core.ErrGameNotFound)
		}
		return p.errorResponse(err.Error(), core.ErrInvalidRequest)
	}

	// Reset game state to ongoing after undo
	p.svc.UpdateGameState(cmd.GameID, core.StateOngoing)

	g, _ = p.svc.GetGame(cmd.GameID)
	response := p.buildGameResponse(cmd.GameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleDeleteGame removes a game
func (p *Processor) handleDeleteGame(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGame(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Only block deletion if actively computing
	if g.State() == core.StatePending {
		return p.errorResponse("cannot delete game while computer move is in progress", core.ErrInvalidRequest)
	}

	if err = p.svc.DeleteGame(cmd.GameID); err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	return ProcessorResponse{
		Success: true,
	}
}

// handleGetBoard returns board visualization
func (p *Processor) handleGetBoard(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGame(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	b, err := board.ParseFEN(g.CurrentFEN())
	if err != nil {
		return p.errorResponse("error parsing FEN", core.ErrInvalidFEN)
	}
	ascii := b.ToASCII()

	return ProcessorResponse{
		Success: true,
		Data: core.BoardResponse{
			FEN:   g.CurrentFEN(),
			Board: ascii,
		},
	}
}

// triggerComputerMove initiates async engine calculation
func (p *Processor) triggerComputerMove(gameID string, g *game.Game) {
	fen := g.CurrentFEN()
	color := g.NextTurnColor()
	player := g.NextPlayer()

	// Submit to queue with callback and computer config
	p.queue.SubmitAsync(gameID, fen, color, player, func(result EngineResult) {
		// Check if game still exists
		currentGame, err := p.svc.GetGame(gameID)
		if err != nil {
			return // Game was deleted
		}

		// Only process if still in pending state
		if currentGame.State() != core.StatePending {
			return
		}

		if result.Error != nil {
			log.Printf("Engine error for game %s: %v", gameID, result.Error)
			p.svc.UpdateGameState(gameID, core.StateStuck)
			return
		}

		// Use centralized state determination
		state := p.determineGameEndState(core.OppositeColor(color), &engine.SearchResult{
			BestMove: result.Move,
			Score:    result.Score,
			Depth:    result.Depth,
			IsMate:   result.IsMate,
			MateIn:   result.MateIn,
		})

		if state != core.StateOngoing {
			p.svc.UpdateGameState(gameID, state)
			return
		}

		// Apply computer move
		p.mu.Lock()
		p.validationEng.SetPosition(fen, []string{result.Move})
		newFEN, _ := p.validationEng.GetFEN()
		p.mu.Unlock()

		p.svc.ApplyMove(gameID, result.Move, newFEN)
		p.svc.SetLastMoveResult(gameID, &game.MoveResult{
			Move:        result.Move,
			PlayerColor: color,
			Score:       result.Score,
			Depth:       result.Depth,
		})

		// Reset to ongoing first
		p.svc.UpdateGameState(gameID, core.StateOngoing)

		// Check if opponent is checkmated
		p.checkGameEnd(gameID, newFEN, color)
	})
}

// determineGameEndState centralized function to determine game end state based on engine evaluation
func (p *Processor) determineGameEndState(lastMoveBy core.Color, searchResult *engine.SearchResult) core.State {
	// No legal moves detected
	if searchResult.BestMove == "" || searchResult.BestMove == "(none)" {
		if searchResult.IsMate {
			// It's a checkmate - the side that just moved wins
			if lastMoveBy == core.ColorWhite {
				return core.StateWhiteWins
			}
			return core.StateBlackWins
		}
		// Stalemate - no legal moves but not in check
		return core.StateStalemate
	}

	// Game continues
	return core.StateOngoing
}

// checkGameEnd determines if game has ended
func (p *Processor) checkGameEnd(gameID, fen string, lastMoveBy core.Color) {
	p.mu.Lock()
	p.validationEng.SetPosition(fen, []string{})
	search, _ := p.validationEng.Search(100)
	p.mu.Unlock()

	// Use centralized state determination
	state := p.determineGameEndState(lastMoveBy, search)
	if state != core.StateOngoing {
		p.svc.UpdateGameState(gameID, state)
	}
}

// buildGameResponse constructs standard game response
func (p *Processor) buildGameResponse(gameID string, g *game.Game) core.GameResponse {
	resp := core.GameResponse{
		GameID: gameID,
		FEN:    g.CurrentFEN(),
		Turn:   g.NextTurnColor().String(),
		State:  g.State().String(),
		Moves:  g.Moves(),
		Players: core.PlayersResponse{
			White: g.GetPlayer(core.ColorWhite),
			Black: g.GetPlayer(core.ColorBlack),
		},
	}

	// Include last move if available
	if result := g.LastResult(); result != nil {
		resp.LastMove = &core.MoveInfo{
			Move:        result.Move,
			PlayerColor: result.PlayerColor.String(),
			Score:       result.Score,
			Depth:       result.Depth,
		}
	}

	return resp
}

// errorResponse creates error response
func (p *Processor) errorResponse(message, code string) ProcessorResponse {
	return ProcessorResponse{
		Success: false,
		Error: &core.ErrorResponse{
			Error: message,
			Code:  code,
		},
	}
}

// Close cleans up resources
func (p *Processor) Close() error {
	p.queue.Shutdown(5 * time.Second)
	return p.validationEng.Close()
}