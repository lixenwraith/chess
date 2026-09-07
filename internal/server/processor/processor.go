package processor

import (
	"errors"
	"fmt"
	"log/slog"
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

// handleCreateGame creates a new game. The initial FEN is classified BEFORE
// persisting: a terminal initial position is terminal in the creation response,
// and engine failure fails the request instead of creating a half-valid game.
func (p *Processor) handleCreateGame(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.CreateGameRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	// Enforce minimum searchTime for computer players
	if args.White.Type == core.PlayerComputer && args.White.SearchTime < minSearchTime {
		args.White.SearchTime = minSearchTime
	}
	if args.Black.Type == core.PlayerComputer && args.Black.SearchTime < minSearchTime {
		args.Black.SearchTime = minSearchTime
	}

	// Check computer game limit
	hasComputer := args.White.Type == core.PlayerComputer || args.Black.Type == core.PlayerComputer
	if hasComputer && !p.svc.CanCreateComputerGame() {
		return p.errorResponse(
			fmt.Sprintf("computer game limit reached (%d/%d)", p.svc.GetComputerGameCount(), service.MaxComputerGames),
			core.ErrResourceLimit,
		)
	}

	gameID := p.svc.GenerateGameID()

	// Validate FEN safety, then classify via engine
	initialFEN := board.StartingFEN
	if args.FEN != "" {
		if !p.isFENSafe(args.FEN) {
			return p.errorResponse("invalid FEN format or characters", core.ErrInvalidFEN)
		}
		initialFEN = args.FEN
	}

	p.mu.Lock()
	err := p.validationEng.NewGame()
	var validatedFEN string
	initialState := core.StateOngoing
	if err == nil {
		validatedFEN, initialState, err = p.classifyLocked(initialFEN)
	}
	p.mu.Unlock()
	if err != nil {
		return p.errorResponse(fmt.Sprintf("engine validation failed: %v", err), core.ErrInternalError)
	}

	// Parse canonical FEN to get starting turn
	b, err := board.ParseFEN(validatedFEN)
	if err != nil {
		return p.errorResponse(fmt.Sprintf("FEN parse error: %v", err), core.ErrInvalidRequest)
	}

	// Create players with appropriate IDs
	whitePlayer := core.NewPlayer(args.White, core.ColorWhite)
	blackPlayer := core.NewPlayer(args.Black, core.ColorBlack)

	// Only assign authenticated user to ONE human slot.
	// If both are human, authenticated user gets white; black remains unclaimed.
	if cmd.UserID != "" {
		if args.White.Type == core.PlayerHuman {
			whitePlayer.ID = cmd.UserID
			whitePlayer.ClaimedBy = cmd.UserID
		} else if args.Black.Type == core.PlayerHuman {
			blackPlayer.ID = cmd.UserID
			blackPlayer.ClaimedBy = cmd.UserID
		}
	}

	if err = p.svc.CreateGame(gameID, whitePlayer, blackPlayer, validatedFEN, b.Turn(), initialState); err != nil {
		return p.errorResponse(fmt.Sprintf("failed to create game: %v", err), core.ErrInternalError)
	}

	g, err := p.svc.GetGameView(gameID)
	if err != nil {
		return p.errorResponse("game creation failed", core.ErrInternalError)
	}

	return ProcessorResponse{
		Success: true,
		Data:    p.buildGameResponse(gameID, g),
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

	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Block configuration changes during computer move
	if g.State == core.StatePending {
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
	g, _ = p.svc.GetGameView(cmd.GameID)
	response := p.buildGameResponse(cmd.GameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleGetGame retrieves game state and triggers computer move if needed
func (p *Processor) handleGetGame(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	response := p.buildGameResponse(cmd.GameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

// handleMakeMove processes human moves with authorization, and the "cccc"
// computer-move trigger. Post-move classification runs BEFORE the move is
// applied; move + final state + metadata commit atomically with one
// notification, so a waking long-poller can never observe "ongoing" on a
// terminal position.
func (p *Processor) handleMakeMove(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.MoveRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Validate game state
	switch g.State {
	case core.StatePending:
		return p.errorResponse("computer move in progress", core.ErrInvalidRequest)
	case core.StateStuck:
		return p.errorResponse("game is stuck due to engine error", core.ErrGameOver)
	case core.StateWhiteWins, core.StateBlackWins, core.StateDraw, core.StateStalemate:
		return p.errorResponse(fmt.Sprintf("game is over: %s", g.State), core.ErrGameOver)
	case core.StateOngoing:
		break
	default:
		return p.errorResponse("game is in invalid state", core.ErrInvalidRequest)
	}

	currentColor := g.NextTurnColor
	currentPlayer := g.NextPlayer()
	if currentPlayer == nil {
		return p.errorResponse("current player is missing", core.ErrInternalError)
	}

	// Handle computer move trigger
	if strings.TrimSpace(args.Move) == "cccc" {
		if currentPlayer.Type != core.PlayerComputer {
			return p.errorResponse("not computer player's turn", core.ErrNotHumanTurn)
		}

		if err := p.svc.BeginComputerMove(cmd.GameID, g.FEN, currentColor); err != nil {
			if errors.Is(err, service.ErrGameChanged) {
				return p.errorResponse("game changed; refresh and retry", core.ErrConflict)
			}
			return p.errorResponse(fmt.Sprintf("failed to start computer move: %v", err), core.ErrInternalError)
		}
		if err := p.triggerComputerMove(cmd.GameID, g); err != nil {
			p.svc.UpdateGameState(cmd.GameID, core.StateStuck)
			return p.errorResponse(fmt.Sprintf("failed to queue computer move: %v", err), core.ErrResourceLimit)
		}

		g, _ = p.svc.GetGameView(cmd.GameID)
		response := p.buildGameResponse(cmd.GameID, g)
		response.LastMove = &core.MoveInfo{
			PlayerColor: currentColor.String(),
		}

		return ProcessorResponse{
			Success: true,
			Pending: true,
			Data:    response,
		}
	}

	// Human move - validate authorization
	if currentPlayer.Type != core.PlayerHuman {
		return p.errorResponse("not human player's turn", core.ErrNotHumanTurn)
	}

	// Authorization: first-move-claims-slot model
	slotOwner := currentPlayer.ClaimedBy

	if slotOwner == "" {
		// An authenticated user claims only when the validated move commits.
		// Anonymous moves deliberately leave the slot unclaimed.
	} else if cmd.UserID != "" && slotOwner != cmd.UserID {
		return p.errorResponse("not your turn - slot claimed by another player", core.ErrUnauthorized)
	}
	if slotOwner != "" && cmd.UserID == "" {
		return p.errorResponse("slot claimed - authentication required", core.ErrUnauthorized)
	}

	// Normalize and validate move format
	move := strings.ToLower(strings.TrimSpace(args.Move))
	if !p.isMoveSafe(move) {
		return p.errorResponse("invalid move format", core.ErrInvalidMove)
	}

	currentFEN := g.FEN

	// Validate move and classify the resulting position in one engine session
	p.mu.Lock()
	err = p.validationEng.SetPosition(currentFEN, []string{move})
	var newFEN string
	finalState := core.StateOngoing
	if err == nil {
		newFEN, finalState, err = p.classifyCurrentLocked()
	}
	p.mu.Unlock()
	if err != nil {
		// Game untouched at pre-move position; retry runs on a respawned engine
		return p.errorResponse("engine unavailable", core.ErrInternalError)
	}
	if newFEN == currentFEN {
		return p.errorResponse("illegal move", core.ErrInvalidMove)
	}

	// Atomic commit: move + state + metadata, single notification
	if err = p.svc.ApplyMoveWithState(cmd.GameID, service.MoveCommit{
		ExpectedFEN: currentFEN, ExpectedState: core.StateOngoing, ExpectedTurn: currentColor,
		ActorUserID: cmd.UserID, MoveUCI: move, NewFEN: newFEN, State: finalState,
		Result: &game.MoveResult{Move: move, PlayerColor: currentColor, GameState: finalState},
	}); err != nil {
		if errors.Is(err, service.ErrSlotOwner) {
			return p.errorResponse("not your turn - slot claimed by another player", core.ErrUnauthorized)
		}
		if errors.Is(err, service.ErrGameChanged) {
			return p.errorResponse("game changed while move was being validated; refresh and retry", core.ErrConflict)
		}
		return p.errorResponse(fmt.Sprintf("failed to apply move: %v", err), core.ErrInternalError)
	}

	// buildGameResponse populates LastMove from the committed LastResult
	g, _ = p.svc.GetGameView(cmd.GameID)
	return ProcessorResponse{
		Success: true,
		Data:    p.buildGameResponse(cmd.GameID, g),
	}
}

// handleUndoMove reverts game state. StateStuck is deliberately permitted:
// undo -> StateOngoing is the recovery path for engine failures. Terminal
// states are also permitted so a finished game can be rewound. Any reverted-to
// snapshot had legal moves made from it, so resetting to Ongoing is sound
// without re-classification.
func (p *Processor) handleUndoMove(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	if g.State == core.StatePending {
		return p.errorResponse("cannot undo while computer move is in progress", core.ErrInvalidRequest)
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

	g, _ = p.svc.GetGameView(cmd.GameID)
	return ProcessorResponse{
		Success: true,
		Data:    p.buildGameResponse(cmd.GameID, g),
	}
}

// handleDeleteGame unloads a game from live memory.
func (p *Processor) handleDeleteGame(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Only block deletion if actively computing
	if g.State == core.StatePending {
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
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	b, err := board.ParseFEN(g.FEN)
	if err != nil {
		return p.errorResponse("error parsing FEN", core.ErrInvalidFEN)
	}
	ascii := b.ToASCII()

	return ProcessorResponse{
		Success: true,
		Data: core.BoardResponse{
			FEN:   g.FEN,
			Board: ascii,
		},
	}
}

// triggerComputerMove initiates async engine calculation. The callback
// re-classifies via the validation engine: worker output is never trusted for
// end-state determination, and no-move results are verified against the
// position rather than the IsMate info-line byproduct.
func (p *Processor) triggerComputerMove(gameID string, g game.View) error {
	fen := g.FEN
	color := g.NextTurnColor
	player := g.NextPlayer()

	return p.queue.SubmitAsync(gameID, fen, color, player, func(result EngineResult) {
		currentGame, err := p.svc.GetGameView(gameID)
		if err != nil || currentGame.State != core.StatePending || currentGame.FEN != fen {
			return // Deleted, or state resolved elsewhere
		}
		if result.Error != nil {
			slog.Error("computer engine failed", "game_id", gameID, "error", result.Error)
			p.svc.UpdateGameState(gameID, core.StateStuck)
			return
		}
		if result.Move == "" || result.Move == "(none)" {
			// Worker says no legal moves; verify against the validation engine.
			p.mu.Lock()
			_, state, cerr := p.classifyLocked(fen)
			p.mu.Unlock()
			if cerr != nil || state == core.StateOngoing {
				p.svc.UpdateGameState(gameID, core.StateStuck) // engines disagree
				return
			}
			p.svc.UpdateGameState(gameID, state)
			return
		}

		p.mu.Lock()
		aerr := p.validationEng.SetPosition(fen, []string{result.Move})
		var newFEN string
		finalState := core.StateOngoing
		if aerr == nil {
			newFEN, finalState, aerr = p.classifyCurrentLocked()
		}
		p.mu.Unlock()
		if aerr != nil || newFEN == fen {
			p.svc.UpdateGameState(gameID, core.StateStuck)
			return
		}

		if err := p.svc.ApplyMoveWithState(gameID, service.MoveCommit{
			ExpectedFEN: fen, ExpectedState: core.StatePending, ExpectedTurn: color,
			MoveUCI: result.Move, NewFEN: newFEN, State: finalState,
			Result: &game.MoveResult{
				Move: result.Move, PlayerColor: color,
				Score: result.Score, Depth: result.Depth, GameState: finalState,
			},
		}); err != nil {
			slog.Error("failed to apply computer move", "game_id", gameID, "error", err)
			if !errors.Is(err, service.ErrGameChanged) && !errors.Is(err, service.ErrGameNotFound) {
				p.svc.UpdateGameState(gameID, core.StateStuck)
			}
		}
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

// classifyCurrentLocked classifies whatever position is loaded in the
// validation engine. Caller holds p.mu, immediately after a SetPosition.
func (p *Processor) classifyCurrentLocked() (fen string, state core.State, err error) {
	diag, err := p.validationEng.Diagnose()
	if err != nil {
		return "", core.StateOngoing, err
	}
	legal, err := p.validationEng.HasLegalMoves()
	if err != nil {
		return "", core.StateOngoing, err
	}
	if legal {
		return diag.FEN, core.StateOngoing, nil
	}
	if !diag.InCheck {
		return diag.FEN, core.StateStalemate, nil
	}
	b, err := board.ParseFEN(diag.FEN)
	if err != nil {
		return "", core.StateOngoing, err
	}
	if b.Turn() == core.ColorWhite {
		return diag.FEN, core.StateBlackWins, nil
	}
	return diag.FEN, core.StateWhiteWins, nil
}

// classifyLocked sets a position from fen and classifies it. Caller holds p.mu.
func (p *Processor) classifyLocked(fen string) (string, core.State, error) {
	if err := p.validationEng.SetPosition(fen, nil); err != nil {
		return "", core.StateOngoing, err
	}
	return p.classifyCurrentLocked()
}

// checkGameEnd: retry once (second attempt runs on a respawned process), then
// fail SAFE to StateStuck. Leaving a possibly-terminal position Ongoing is the
// original bug class; Stuck is now recoverable via undo (see handleUndoMove).
func (p *Processor) checkGameEnd(gameID, fen string) {
	for attempt := 0; attempt < 2; attempt++ {
		p.mu.Lock()
		_, state, err := p.classifyLocked(fen)
		p.mu.Unlock()
		if err == nil {
			if state != core.StateOngoing {
				p.svc.UpdateGameState(gameID, state)
			}
			return
		}
		slog.Warn("game end-state check failed",
			"game_id", gameID, "attempt", attempt+1, "error", err)
	}
	p.svc.UpdateGameState(gameID, core.StateStuck)
}

// buildGameResponse constructs standard game response
func (p *Processor) buildGameResponse(gameID string, g game.View) core.GameResponse {
	resp := core.GameResponse{
		GameID: gameID,
		FEN:    g.FEN,
		Turn:   g.NextTurnColor.String(),
		State:  g.State.String(),
		Moves:  g.Moves,
		Players: core.PlayersResponse{
			White: g.WhitePlayer,
			Black: g.BlackPlayer,
		},
	}

	// Include last move if available
	if result := g.LastResult; result != nil {
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
	queueErr := p.queue.Shutdown(5 * time.Second)
	p.mu.Lock()
	engineErr := p.validationEng.Close()
	p.mu.Unlock()
	return errors.Join(queueErr, engineErr)
}
