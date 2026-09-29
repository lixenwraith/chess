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

	"chess/internal/server/chess"
	"chess/internal/server/core"
	"chess/internal/server/engine"
	"chess/internal/server/game"
	"chess/internal/server/service"
)

const (
	minSearchTime = 100

	// A computer answers a draw offer only once both sides have made ten
	// moves, and accepts when its evaluation, from its own side, is level or
	// worse. The evaluation is a short search on the validation engine; the
	// one-offer-per-move allowance bounds how often it runs.
	drawOfferMinPlies  = 20
	drawEvalTimeMs     = 100
	drawAcceptMaxScore = 0 // centipawns
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
	case CmdResign:
		return p.handleResign(cmd)
	case CmdDraw:
		return p.handleDraw(cmd)
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
	initialFEN := chess.StartFEN
	if args.FEN != "" {
		if !p.isFENSafe(args.FEN) {
			return p.errorResponse("invalid FEN format or characters", core.ErrInvalidFEN)
		}
		// The rules core rejects positions the engine must never see (missing
		// kings, the side not to move in check) and drops castling rights
		// without their rook, so both agree on the legal moves.
		pos, err := chess.ParseFEN(args.FEN)
		if err != nil {
			return p.errorResponse(err.Error(), core.ErrInvalidFEN)
		}
		initialFEN = pos.FEN()
	}

	p.mu.Lock()
	err := p.validationEng.NewGame()
	var validatedFEN string
	initialState, termination := core.StateOngoing, core.TermNone
	if err == nil {
		validatedFEN, initialState, termination, err = p.classifyLocked(initialFEN)
	}
	p.mu.Unlock()
	if err != nil {
		return p.errorResponse(fmt.Sprintf("engine validation failed: %v", err), core.ErrInternalError)
	}
	if initialState == core.StateOngoing {
		initialState, termination = adjudicateDraw(nil, validatedFEN)
	}

	// Parse canonical FEN to get starting turn
	start, err := chess.ParseFEN(validatedFEN)
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

	if err = p.svc.CreateGame(gameID, whitePlayer, blackPlayer, validatedFEN,
		coreColor(start.Turn()), initialState, termination); err != nil {
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

// handleConfigurePlayers updates player configuration mid-game; another
// user's claim refuses it.
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
	if err = p.svc.UpdatePlayers(cmd.GameID, whitePlayer, blackPlayer, cmd.UserID); err != nil {
		switch {
		case errors.Is(err, service.ErrGameNotFound):
			return p.endGameError(err)
		case errors.Is(err, service.ErrSlotOwner):
			return p.errorResponse("another player has claimed a side of this game; players cannot be changed", core.ErrUnauthorized)
		}
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
			p.svc.UpdateGameState(cmd.GameID, core.StateStuck, core.TermNone)
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

	// A pawn move to the last rank needs its promotion piece; say so instead
	// of the engine's generic rejection.
	rules := rulesCheck(currentFEN, move)
	if errors.Is(rules.err, chess.ErrPromotionRequired) {
		return p.errorResponse("promotion piece required: append q, r, b, or n (e.g. "+move+"q)", core.ErrInvalidMove)
	}

	// Validate move and classify the resulting position in one engine session
	p.mu.Lock()
	err = p.validationEng.SetPosition(currentFEN, []string{move})
	var newFEN string
	finalState, termination := core.StateOngoing, core.TermNone
	if err == nil {
		newFEN, finalState, termination, err = p.classifyCurrentLocked()
	}
	p.mu.Unlock()
	if err != nil {
		// Game untouched at pre-move position; retry runs on a respawned engine
		return p.errorResponse("engine unavailable", core.ErrInternalError)
	}
	rules.compare(cmd.GameID, move, newFEN != currentFEN, newFEN)
	if newFEN == currentFEN {
		return p.errorResponse("illegal move", core.ErrInvalidMove)
	}
	if finalState == core.StateOngoing {
		finalState, termination = adjudicateDraw(g.FENs, newFEN)
	}

	// Atomic commit: move + state + metadata, single notification
	if err = p.svc.ApplyMoveWithState(cmd.GameID, service.MoveCommit{
		ExpectedFEN: currentFEN, ExpectedState: core.StateOngoing, ExpectedTurn: currentColor,
		ActorUserID: cmd.UserID, MoveUCI: move, NewFEN: newFEN, State: finalState, Termination: termination,
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
// states are also permitted so a finished game can be rewound, except a
// resignation or agreed draw between two humans; another user's claim refuses
// an undo. Any reverted-to snapshot had legal moves made from it, so resetting
// to Ongoing is sound
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

	if err = p.svc.UndoMoves(cmd.GameID, args.Count, cmd.UserID); err != nil {
		switch {
		case errors.Is(err, service.ErrGameNotFound), errors.Is(err, service.ErrConcessionFinal):
			return p.endGameError(err)
		case errors.Is(err, service.ErrSlotOwner):
			return p.errorResponse("another player has claimed a side of this game; moves cannot be taken back", core.ErrUnauthorized)
		}
		return p.errorResponse(err.Error(), core.ErrInvalidRequest)
	}

	g, _ = p.svc.GetGameView(cmd.GameID)
	return ProcessorResponse{
		Success: true,
		Data:    p.buildGameResponse(cmd.GameID, g),
	}
}

// handleDeleteGame unloads a game from live memory for one of its claimants,
// or anyone when it is unclaimed.
func (p *Processor) handleDeleteGame(cmd Command) ProcessorResponse {
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

	// Only block deletion if actively computing
	if g.State == core.StatePending {
		return p.errorResponse("cannot delete game while computer move is in progress", core.ErrInvalidRequest)
	}

	if err = p.svc.UnloadGame(cmd.GameID, cmd.UserID); err != nil {
		switch {
		case errors.Is(err, service.ErrGameNotFound):
			return p.endGameError(err)
		case errors.Is(err, service.ErrSlotOwner):
			return p.errorResponse("only a player who claimed a side can unload this game", core.ErrUnauthorized)
		}
		return p.errorResponse(err.Error(), core.ErrInvalidRequest)
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

	pos, err := chess.ParseFEN(g.FEN)
	if err != nil {
		return p.errorResponse("error parsing FEN", core.ErrInvalidFEN)
	}
	ascii := pos.ASCII()

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
			p.svc.UpdateGameState(gameID, core.StateStuck, core.TermNone)
			return
		}
		if result.Move == "" || result.Move == "(none)" {
			// Worker says no legal moves; verify against the validation engine.
			p.mu.Lock()
			_, state, termination, cerr := p.classifyLocked(fen)
			p.mu.Unlock()
			if cerr != nil || state == core.StateOngoing {
				p.svc.UpdateGameState(gameID, core.StateStuck, core.TermNone) // engines disagree
				return
			}
			p.svc.UpdateGameState(gameID, state, termination)
			return
		}

		p.mu.Lock()
		aerr := p.validationEng.SetPosition(fen, []string{result.Move})
		var newFEN string
		finalState, termination := core.StateOngoing, core.TermNone
		if aerr == nil {
			newFEN, finalState, termination, aerr = p.classifyCurrentLocked()
		}
		p.mu.Unlock()
		if aerr != nil || newFEN == fen {
			p.svc.UpdateGameState(gameID, core.StateStuck, core.TermNone)
			return
		}
		rulesCheck(fen, result.Move).compare(gameID, result.Move, true, newFEN)
		if finalState == core.StateOngoing {
			finalState, termination = adjudicateDraw(currentGame.FENs, newFEN)
		}

		if err := p.svc.ApplyMoveWithState(gameID, service.MoveCommit{
			ExpectedFEN: fen, ExpectedState: core.StatePending, ExpectedTurn: color,
			MoveUCI: result.Move, NewFEN: newFEN, State: finalState, Termination: termination,
			Result: &game.MoveResult{
				Move: result.Move, PlayerColor: color,
				Score: result.Score, Depth: result.Depth, GameState: finalState,
			},
		}); err != nil {
			slog.Error("failed to apply computer move", "game_id", gameID, "error", err)
			if !errors.Is(err, service.ErrGameChanged) && !errors.Is(err, service.ErrGameNotFound) {
				p.svc.UpdateGameState(gameID, core.StateStuck, core.TermNone)
			}
		}
	})
}

// classifyCurrentLocked classifies whatever position is loaded in the
// validation engine: mate and stalemate come from the engine. Caller holds
// p.mu, immediately after a SetPosition.
func (p *Processor) classifyCurrentLocked() (fen string, state core.State, termination core.Termination, err error) {
	diag, err := p.validationEng.Diagnose()
	if err != nil {
		return "", core.StateOngoing, core.TermNone, err
	}
	legal, err := p.validationEng.HasLegalMoves()
	if err != nil {
		return "", core.StateOngoing, core.TermNone, err
	}
	if legal {
		return diag.FEN, core.StateOngoing, core.TermNone, nil
	}
	if !diag.InCheck {
		return diag.FEN, core.StateStalemate, core.TermStalemate, nil
	}
	pos, err := chess.ParseFEN(diag.FEN)
	if err != nil {
		return "", core.StateOngoing, core.TermNone, err
	}
	if pos.Turn() == chess.White {
		return diag.FEN, core.StateBlackWins, core.TermCheckmate, nil
	}
	return diag.FEN, core.StateWhiteWins, core.TermCheckmate, nil
}

// classifyLocked sets a position from fen and classifies it. Caller holds p.mu.
func (p *Processor) classifyLocked(fen string) (string, core.State, core.Termination, error) {
	if err := p.validationEng.SetPosition(fen, nil); err != nil {
		return "", core.StateOngoing, core.TermNone, err
	}
	return p.classifyCurrentLocked()
}

// adjudicateDraw applies the draw rules that end a game without a claim, as
// most online servers do: dead material, threefold repetition, and the
// fifty-move rule (a claimable draw under FIDE rules). history holds the
// positions before fen, oldest first. It returns StateOngoing when no rule
// applies. The engine has already ruled out mate and stalemate, which take
// precedence.
func adjudicateDraw(history []string, fen string) (core.State, core.Termination) {
	pos, err := chess.ParseFEN(fen)
	if err != nil {
		return core.StateOngoing, core.TermNone
	}
	if pos.InsufficientMaterial() {
		return core.StateDraw, core.TermInsufficientMaterial
	}
	// Positions before the last capture or pawn move cannot repeat.
	window := min(len(history), pos.Halfmove())
	line := make([]*chess.Position, 0, window+1)
	for _, prior := range history[len(history)-window:] {
		prev, err := chess.ParseFEN(prior)
		if err != nil {
			line = line[:0] // a gap cannot be bridged; count from here on
			continue
		}
		line = append(line, prev)
	}
	if chess.Repetitions(append(line, pos)) >= 3 {
		return core.StateDraw, core.TermThreefoldRepetition
	}
	if pos.Halfmove() >= 100 {
		return core.StateDraw, core.TermFiftyMoveRule
	}
	return core.StateOngoing, core.TermNone
}

func coreColor(c chess.Color) core.Color {
	if c == chess.White {
		return core.ColorWhite
	}
	return core.ColorBlack
}

// buildGameResponse constructs standard game response
func (p *Processor) buildGameResponse(gameID string, g game.View) core.GameResponse {
	resp := core.GameResponse{
		GameID:      gameID,
		FEN:         g.FEN,
		Turn:        g.NextTurnColor.String(),
		State:       g.State.String(),
		Termination: string(g.Termination),
		Moves:       g.Moves,
		Players: core.PlayersResponse{
			White: g.WhitePlayer,
			Black: g.BlackPlayer,
		},
	}
	if g.DrawOffer != 0 {
		resp.DrawOffer = g.DrawOffer.String()
	}
	if c := g.Concession; c != nil {
		resp.Concession = &core.Concession{
			Result: c.State.String(), Termination: string(c.Termination), Ply: c.Ply,
		}
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

// rulesResult is the rules core's verdict on a move, computed alongside the
// engine's. Stockfish stays authoritative; a disagreement is logged so the
// core can be trusted, or fixed, before it validates anything.
type rulesResult struct {
	fen string // normalized FEN after the move; empty when illegal
	err error
}

func rulesCheck(fen, uci string) rulesResult {
	pos, err := chess.ParseFEN(fen)
	if err != nil {
		return rulesResult{err: err}
	}
	m, err := pos.ParseUCI(uci)
	if err != nil {
		return rulesResult{err: err}
	}
	return rulesResult{fen: pos.Play(m).FEN()}
}

func (r rulesResult) compare(gameID, uci string, engineLegal bool, engineFEN string) {
	if errors.Is(r.err, chess.ErrInvalidFEN) {
		slog.Warn("rules core cannot parse game position", "game_id", gameID, "error", r.err)
		return
	}
	if engineLegal != (r.err == nil) {
		slog.Warn("rules core and engine disagree on legality",
			"game_id", gameID, "move", uci, "engine_legal", engineLegal, "rules_error", r.err)
		return
	}
	if !engineLegal {
		return
	}
	if normalized, err := chess.NormalizeFEN(engineFEN); err != nil || normalized != r.fen {
		slog.Warn("rules core and engine disagree on resulting position",
			"game_id", gameID, "move", uci, "engine_fen", engineFEN, "rules_fen", r.fen)
	}
}

// handleResign ends the game in the opponent's favor. It is allowed while the
// computer is thinking or the engine is stuck: resigning never needs one.
func (p *Processor) handleResign(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.ResignRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}
	if g.State.IsTerminal() {
		return p.errorResponse(fmt.Sprintf("game is over: %s", g.State), core.ErrGameOver)
	}
	color, failure := actingColor(g, args.Color, cmd.UserID)
	if failure != nil {
		return *failure
	}
	winner := core.StateWhiteWins
	if color == core.ColorWhite {
		winner = core.StateBlackWins
	}
	err = p.svc.EndGame(cmd.GameID, service.EndCommit{
		ExpectedFEN: g.FEN, Color: color, ActorUserID: cmd.UserID,
		State: winner, Termination: core.TermResignation,
	})
	if err != nil {
		return p.endGameError(err)
	}
	g, _ = p.svc.GetGameView(cmd.GameID)
	return ProcessorResponse{Success: true, Data: p.buildGameResponse(cmd.GameID, g)}
}

// handleDraw offers, accepts, or declines a draw by agreement. An offer to a
// human stands until answered, or until the opponent moves instead; an offer
// to a computer is answered at once. Offering while the opponent's offer
// stands accepts it.
func (p *Processor) handleDraw(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.DrawRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}
	switch {
	case g.State.IsTerminal():
		return p.errorResponse(fmt.Sprintf("game is over: %s", g.State), core.ErrGameOver)
	case g.State != core.StateOngoing:
		return p.errorResponse("draws can be agreed only while no computer move is running", core.ErrInvalidRequest)
	}
	color, failure := actingColor(g, args.Color, cmd.UserID)
	if failure != nil {
		return *failure
	}
	opponent := core.OppositeColor(color)

	accept := func() ProcessorResponse {
		err := p.svc.EndGame(cmd.GameID, service.EndCommit{
			ExpectedFEN: g.FEN, Color: color, ActorUserID: cmd.UserID,
			State: core.StateDraw, Termination: core.TermAgreement, OfferFrom: opponent,
		})
		if err != nil {
			return p.endGameError(err)
		}
		return p.drawResponse(cmd.GameID, "accepted")
	}

	switch args.Action {
	case "accept":
		if g.DrawOffer != opponent {
			return p.errorResponse("no draw offer from the opponent", core.ErrInvalidRequest)
		}
		return accept()
	case "decline":
		if err := p.svc.DeclineDraw(cmd.GameID, color, cmd.UserID); err != nil {
			return p.endGameError(err)
		}
		return p.drawResponse(cmd.GameID, "declined")
	}

	// Offer.
	switch {
	case g.DrawOffer == opponent:
		return accept()
	case g.DrawOffer == color:
		return p.drawResponse(cmd.GameID, "offered")
	}
	if last := g.OfferPly[color]; last >= 0 && len(g.Moves) < last+2 {
		return p.errorResponse(service.ErrOfferLimit.Error(), core.ErrConflict)
	}
	if player := g.Player(opponent); player != nil && player.Type == core.PlayerComputer {
		agrees := false
		if len(g.Moves) >= drawOfferMinPlies {
			if agrees, err = p.computerAcceptsDraw(g.FEN, opponent); err != nil {
				return p.errorResponse("engine unavailable", core.ErrInternalError)
			}
		}
		if agrees {
			err = p.svc.EndGame(cmd.GameID, service.EndCommit{
				ExpectedFEN: g.FEN, Color: color, ActorUserID: cmd.UserID,
				State: core.StateDraw, Termination: core.TermAgreement,
			})
			if err != nil {
				return p.endGameError(err)
			}
			return p.drawResponse(cmd.GameID, "accepted")
		}
		if err := p.svc.RecordDeclinedOffer(cmd.GameID, color, cmd.UserID, g.FEN); err != nil {
			return p.endGameError(err)
		}
		return p.drawResponse(cmd.GameID, "declined")
	}
	if err := p.svc.OfferDraw(cmd.GameID, color, cmd.UserID, g.FEN); err != nil {
		return p.endGameError(err)
	}
	return p.drawResponse(cmd.GameID, "offered")
}

func (p *Processor) drawResponse(gameID, outcome string) ProcessorResponse {
	g, err := p.svc.GetGameView(gameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}
	response := p.buildGameResponse(gameID, g)
	response.DrawOutcome = outcome
	return ProcessorResponse{Success: true, Data: response}
}

// computerAcceptsDraw evaluates fen for the computer playing color with a
// short full-strength search. UCI scores are from the side to move.
func (p *Processor) computerAcceptsDraw(fen string, computer core.Color) (bool, error) {
	pos, err := chess.ParseFEN(fen)
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.validationEng.SetPosition(fen, nil); err != nil {
		return false, err
	}
	result, err := p.validationEng.Search(drawEvalTimeMs)
	if err != nil {
		return false, err
	}
	score := result.Score
	if coreColor(pos.Turn()) != computer {
		score = -score
	}
	return score <= drawAcceptMaxScore, nil
}

// actingColor resolves which side a resignation or draw request acts for: the
// requested color, else the one side the caller claimed, else the only human
// side. It enforces slot ownership: a claimed side acts only for its claimant.
func actingColor(g game.View, requested, userID string) (core.Color, *ProcessorResponse) {
	fail := func(message, code string) (core.Color, *ProcessorResponse) {
		return 0, &ProcessorResponse{Error: &core.ErrorResponse{Error: message, Code: code}}
	}
	var color core.Color
	switch {
	case requested != "":
		color = core.ParseColor(requested)
	default:
		var claimed, humans []core.Color
		for _, c := range []core.Color{core.ColorWhite, core.ColorBlack} {
			player := g.Player(c)
			if player == nil || player.Type != core.PlayerHuman {
				continue
			}
			humans = append(humans, c)
			if userID != "" && player.ClaimedBy == userID {
				claimed = append(claimed, c)
			}
		}
		switch {
		case len(claimed) == 1:
			color = claimed[0]
		case len(claimed) == 0 && len(humans) == 1:
			color = humans[0]
		case len(humans) == 0:
			return fail("both sides are played by the computer", core.ErrInvalidRequest)
		default:
			return fail("color required: both sides are human", core.ErrInvalidRequest)
		}
	}
	player := g.Player(color)
	if player == nil {
		return fail("invalid color", core.ErrInvalidRequest)
	}
	if player.Type != core.PlayerHuman {
		return fail(service.ErrNotHuman.Error(), core.ErrInvalidRequest)
	}
	if player.ClaimedBy != "" && player.ClaimedBy != userID {
		if userID == "" {
			return fail("slot claimed - authentication required", core.ErrUnauthorized)
		}
		return fail("slot claimed by another player", core.ErrUnauthorized)
	}
	return color, nil
}

// endGameError maps a service refusal to a response; the service re-checks
// under its lock what the processor checked on a snapshot.
func (p *Processor) endGameError(err error) ProcessorResponse {
	switch {
	case errors.Is(err, service.ErrGameNotFound):
		return p.errorResponse("game not found", core.ErrGameNotFound)
	case errors.Is(err, service.ErrGameOver):
		return p.errorResponse("game is over", core.ErrGameOver)
	case errors.Is(err, service.ErrSlotOwner):
		return p.errorResponse("slot claimed by another player", core.ErrUnauthorized)
	case errors.Is(err, service.ErrNotHuman):
		return p.errorResponse(err.Error(), core.ErrInvalidRequest)
	case errors.Is(err, service.ErrNoDrawOffer):
		return p.errorResponse("no draw offer from the opponent", core.ErrInvalidRequest)
	case errors.Is(err, service.ErrOfferLimit):
		return p.errorResponse(err.Error(), core.ErrConflict)
	case errors.Is(err, service.ErrGameChanged):
		return p.errorResponse("game changed; refresh and retry", core.ErrConflict)
	case errors.Is(err, service.ErrConcessionFinal):
		return p.errorResponse(err.Error(), core.ErrGameOver)
	}
	return p.errorResponse(fmt.Sprintf("failed to update game: %v", err), core.ErrInternalError)
}
