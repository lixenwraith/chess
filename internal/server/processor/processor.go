package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/engine"
	"github.com/lixenwraith/chess/internal/server/game"
	"github.com/lixenwraith/chess/internal/server/service"
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

	// shadowBacklog bounds the rulings waiting for the engine's second
	// opinion; past it they go unchecked, never delaying a move.
	shadowBacklog = 64
)

var fenPattern = regexp.MustCompile(`^[rnbqkpRNBQKP1-8/]+ [wb] [KQkq-]+ [a-h1-8-]+ \d+ \d+$`)

// Processor rules on moves with the rules core. Its own engine, behind mu,
// answers draw offers and re-checks those rulings off the request path.
type Processor struct {
	svc           *service.Service
	queue         *EngineQueue
	validationEng *engine.UCI
	mu            sync.Mutex
	shadows       chan shadowCheck
	quit          chan struct{}
	shadowDone    chan struct{}
}

func New(svc *service.Service) (*Processor, error) {
	validationEng, err := engine.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create validation engine: %v", err)
	}

	p := &Processor{
		svc:           svc,
		queue:         NewEngineQueue(2),
		validationEng: validationEng,
		shadows:       make(chan shadowCheck, shadowBacklog),
		quit:          make(chan struct{}),
		shadowDone:    make(chan struct{}),
	}
	go p.shadowLoop()
	return p, nil
}

func (p *Processor) Execute(ctx context.Context, cmd Command) ProcessorResponse {
	// A stored game resumes on the first command that names it; unloading
	// one needs no load.
	if cmd.GameID != "" && cmd.Type != CmdDeleteGame {
		if err := p.svc.LoadGame(ctx, cmd.GameID); errors.Is(err, service.ErrStorageUnavailable) {
			return p.errorResponse("stored game unavailable", core.ErrStorageUnavailable)
		}
	}
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
	case CmdJoinGame:
		return p.handleJoin(cmd)
	default:
		return p.errorResponse("unknown command", core.ErrInvalidRequest)
	}
}

// isFENSafe refuses control characters, which could inject UCI commands.
func (p *Processor) isFENSafe(fen string) bool {
	for _, r := range fen {
		if unicode.IsControl(r) && r != ' ' {
			return false
		}
	}

	return fenPattern.MatchString(fen)
}

func (p *Processor) isMoveSafe(move string) bool {
	for _, r := range move {
		if unicode.IsControl(r) {
			return false
		}
	}

	if len(move) < 4 || len(move) > 5 {
		return false
	}

	if move[0] < 'a' || move[0] > 'h' ||
		move[1] < '1' || move[1] > '8' ||
		move[2] < 'a' || move[2] > 'h' ||
		move[3] < '1' || move[3] > '8' {
		return false
	}

	if len(move) == 5 {
		promotion := move[4]
		if promotion != 'q' && promotion != 'r' && promotion != 'b' && promotion != 'n' {
			return false
		}
	}

	return true
}

// handleCreateGame creates a new game. The rules classify the starting
// position first, so one that is already over is created over.
func (p *Processor) handleCreateGame(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.CreateGameRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	if args.White.Type == core.PlayerComputer && args.White.SearchTime < minSearchTime {
		args.White.SearchTime = minSearchTime
	}
	if args.Black.Type == core.PlayerComputer && args.Black.SearchTime < minSearchTime {
		args.Black.SearchTime = minSearchTime
	}

	gameID := p.svc.GenerateGameID()

	fen := chess.StartFEN
	if args.FEN != "" {
		if !p.isFENSafe(args.FEN) {
			return p.errorResponse("invalid FEN format or characters", core.ErrInvalidFEN)
		}
		fen = args.FEN
	}
	// The rules refuse positions no game reaches (missing kings, the side not
	// to move in check) and drop castling rights without their rook.
	start, err := chess.ParseFEN(fen)
	if err != nil {
		return p.errorResponse(err.Error(), core.ErrInvalidFEN)
	}
	initialState, termination := outcome(nil, start)

	whitePlayer := core.NewPlayer(args.White, core.ColorWhite)
	blackPlayer := core.NewPlayer(args.Black, core.ColorBlack)

	// The creator takes one side: the one asked for, else, signed in, White
	// if human and otherwise Black. The other human side stays open.
	sides := map[core.Color]*core.Player{core.ColorWhite: whitePlayer, core.ColorBlack: blackPlayer}
	side := core.ParseColor(args.Side)
	switch {
	case side != 0 && sides[side].Type != core.PlayerHuman:
		return p.errorResponse("side must name a human side", core.ErrInvalidRequest)
	case side == 0 && cmd.Actor.UserID != "" && args.White.Type == core.PlayerHuman:
		side = core.ColorWhite
	case side == 0 && cmd.Actor.UserID != "" && args.Black.Type == core.PlayerHuman:
		side = core.ColorBlack
	}
	token := ""
	switch taken := sides[side]; {
	case taken == nil:
	case cmd.Actor.UserID != "":
		taken.ID, taken.ClaimedBy, taken.Name = cmd.Actor.UserID, cmd.Actor.UserID, cmd.Actor.Name
	default:
		if token, taken.Seat, err = service.NewSeat(); err != nil {
			return p.errorResponse("failed to create game", core.ErrInternalError)
		}
		taken.Seated = true
	}

	if err = p.svc.CreateGame(gameID, whitePlayer, blackPlayer, start.FEN(),
		coreColor(start.Turn()), initialState, termination); err != nil {
		return p.errorResponse(fmt.Sprintf("failed to create game: %v", err), core.ErrInternalError)
	}

	g, err := p.svc.GetGameView(gameID)
	if err != nil {
		return p.errorResponse("game creation failed", core.ErrInternalError)
	}
	response := p.buildGameResponse(gameID, g)
	if side != 0 {
		response.Side, response.Seat = side.String(), token
	}
	return ProcessorResponse{Success: true, Data: response}
}

// handleJoin gives the caller an open human side without a move: a claim
// when signed in, else a guest's seat, whose secret only this answer carries.
func (p *Processor) handleJoin(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.JoinRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}
	token, seat := "", []byte(nil)
	if cmd.Actor.UserID == "" {
		var err error
		if token, seat, err = service.NewSeat(); err != nil {
			return p.errorResponse("failed to join game", core.ErrInternalError)
		}
	}
	side, taken, err := p.svc.JoinGame(cmd.GameID, core.ParseColor(args.Color), cmd.Actor, seat)
	if err != nil {
		return p.endGameError(err)
	}
	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}
	response := p.buildGameResponse(cmd.GameID, g)
	response.Side = side.String()
	if taken {
		response.Seat = token
	}
	return ProcessorResponse{Success: true, Data: response}
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

	if g.State == core.StatePending {
		return p.errorResponse("cannot change players while computer is calculating", core.ErrInvalidRequest)
	}

	whitePlayer := core.NewPlayer(args.White, core.ColorWhite)
	blackPlayer := core.NewPlayer(args.Black, core.ColorBlack)

	if err = p.svc.UpdatePlayers(cmd.GameID, whitePlayer, blackPlayer, cmd.Actor); err != nil {
		switch {
		case errors.Is(err, service.ErrGameNotFound):
			return p.endGameError(err)
		case errors.Is(err, service.ErrSlotOwner):
			return p.errorResponse("another player holds a side of this game; players cannot be changed", core.ErrUnauthorized)
		}
		return p.errorResponse(fmt.Sprintf("failed to update players: %v", err), core.ErrInternalError)
	}

	g, _ = p.svc.GetGameView(cmd.GameID)
	response := p.buildGameResponse(cmd.GameID, g)

	return ProcessorResponse{
		Success: true,
		Data:    response,
	}
}

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

// handleMakeMove rules on a human move, or starts the computer's on "cccc".
// The position is classified before the move is applied, and move, state and
// claim commit together with one notification: a waking long-poll never sees
// "ongoing" on a finished position.
func (p *Processor) handleMakeMove(cmd Command) ProcessorResponse {
	args, ok := cmd.Args.(core.MoveRequest)
	if !ok {
		return p.errorResponse("invalid arguments", core.ErrInvalidRequest)
	}

	g, err := p.svc.GetGameView(cmd.GameID)
	if err != nil {
		return p.errorResponse("game not found", core.ErrGameNotFound)
	}

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

	if strings.TrimSpace(args.Move) == "cccc" {
		if currentPlayer.Type != core.PlayerComputer {
			return p.errorResponse("not computer player's turn", core.ErrNotHumanTurn)
		}

		err := p.svc.BeginComputerMove(cmd.GameID, g.FEN, currentColor, func() error {
			return p.triggerComputerMove(cmd.GameID, g)
		})
		switch {
		case errors.Is(err, service.ErrGameChanged):
			return p.errorResponse("game changed; refresh and retry", core.ErrConflict)
		case errors.Is(err, errEngineBusy):
			return p.errorResponse(err.Error(), core.ErrResourceLimit)
		case err != nil:
			return p.errorResponse(fmt.Sprintf("failed to start computer move: %v", err), core.ErrInternalError)
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

	if currentPlayer.Type != core.PlayerHuman {
		return p.errorResponse("not human player's turn", core.ErrNotHumanTurn)
	}

	// A held side moves only for its holder. An open one takes the move, and
	// is claimed by it when the mover is signed in.
	if refusal := heldBy(currentPlayer, cmd.Actor); refusal != "" {
		return p.errorResponse(refusal, core.ErrUnauthorized)
	}

	move := strings.ToLower(strings.TrimSpace(args.Move))
	if !p.isMoveSafe(move) {
		return p.errorResponse("invalid move format", core.ErrInvalidMove)
	}

	currentFEN := g.FEN
	next, err := play(currentFEN, move)
	switch {
	case errors.Is(err, chess.ErrPromotionRequired):
		return p.errorResponse("promotion piece required: append q, r, b, or n (e.g. "+move+"q)", core.ErrInvalidMove)
	case errors.Is(err, chess.ErrInvalidFEN):
		slog.Warn("rules core cannot parse game position", "game_id", cmd.GameID, "error", err)
		return p.errorResponse("game position is unreadable", core.ErrInternalError)
	case err != nil:
		p.shadow(shadowCheck{gameID: cmd.GameID, fen: currentFEN, uci: move})
		return p.errorResponse("illegal move", core.ErrInvalidMove)
	}
	newFEN := next.FEN()
	finalState, termination := outcome(g.FENs, next)
	p.shadow(shadowCheck{gameID: cmd.GameID, fen: currentFEN, uci: move, ruled: newFEN})

	if err = p.svc.ApplyMoveWithState(cmd.GameID, service.MoveCommit{
		ExpectedFEN: currentFEN, ExpectedState: core.StateOngoing, ExpectedTurn: currentColor,
		Actor: cmd.Actor, MoveUCI: move, NewFEN: newFEN, State: finalState, Termination: termination,
		Result: &game.MoveResult{Move: move, PlayerColor: currentColor, GameState: finalState},
	}); err != nil {
		if errors.Is(err, service.ErrSlotOwner) {
			return p.errorResponse("that side is held by another player", core.ErrUnauthorized)
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

// handleUndoMove reverts game state. Stuck and terminal states may be undone:
// undo recovers from engine failures and rewinds a finished game, except a
// resignation or agreed draw between two humans; another user's claim refuses
// it. A reverted-to snapshot had legal moves made from it, so Ongoing needs no
// re-classification.
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

	if err = p.svc.UndoMoves(cmd.GameID, args.Count, cmd.Actor); err != nil {
		switch {
		case errors.Is(err, service.ErrGameNotFound), errors.Is(err, service.ErrConcessionFinal):
			return p.endGameError(err)
		case errors.Is(err, service.ErrSlotOwner):
			return p.errorResponse("another player holds a side of this game; moves cannot be taken back", core.ErrUnauthorized)
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

	if g.State == core.StatePending {
		return p.errorResponse("cannot delete game while computer move is in progress", core.ErrInvalidRequest)
	}

	if err = p.svc.UnloadGame(cmd.GameID, cmd.Actor); err != nil {
		switch {
		case errors.Is(err, service.ErrGameNotFound):
			return p.endGameError(err)
		case errors.Is(err, service.ErrSlotOwner):
			return p.errorResponse("only a player who holds a side can unload this game", core.ErrUnauthorized)
		}
		return p.errorResponse(err.Error(), core.ErrInvalidRequest)
	}

	return ProcessorResponse{
		Success: true,
	}
}

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

// triggerComputerMove queues the engine's search. Its callback trusts the
// worker for the move alone: the rules check it and classify the position,
// and a worker that finds no move is believed only when the rules agree.
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
		history := currentGame.FENs[:len(currentGame.FENs)-1]
		if result.Move == "" || result.Move == "(none)" {
			state, termination := core.StateOngoing, core.TermNone
			if pos, err := chess.ParseFEN(fen); err == nil {
				state, termination = outcome(history, pos)
			}
			if state == core.StateOngoing {
				state, termination = core.StateStuck, core.TermNone // the rules find a move
			}
			p.svc.UpdateGameState(gameID, state, termination)
			return
		}

		next, err := play(fen, result.Move)
		if err != nil {
			slog.Warn("rules core and engine disagree on legality",
				"game_id", gameID, "move", result.Move, "engine_legal", true, "rules_error", err)
			p.svc.UpdateGameState(gameID, core.StateStuck, core.TermNone)
			return
		}
		newFEN := next.FEN()
		finalState, termination := outcome(currentGame.FENs, next)
		p.shadow(shadowCheck{gameID: gameID, fen: fen, uci: result.Move, ruled: newFEN})

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

// play applies uci to fen by the rules. The error wraps chess.ErrInvalidFEN,
// chess.ErrPromotionRequired or chess.ErrIllegalMove.
func play(fen, uci string) (*chess.Position, error) {
	pos, err := chess.ParseFEN(fen)
	if err != nil {
		return nil, err
	}
	m, err := pos.ParseUCI(uci)
	if err != nil {
		return nil, err
	}
	return pos.Play(m), nil
}

// outcome classifies a position by the rules: mate or stalemate when the side
// to move has no move, else the draws that need no claim (dead material,
// threefold repetition, the fifty-move rule, claimable under FIDE), else
// ongoing. history holds the positions before it, oldest first.
func outcome(history []string, pos *chess.Position) (core.State, core.Termination) {
	switch {
	case pos.HasLegalMoves():
	case !pos.InCheck():
		return core.StateStalemate, core.TermStalemate
	case pos.Turn() == chess.White:
		return core.StateBlackWins, core.TermCheckmate
	default:
		return core.StateWhiteWins, core.TermCheckmate
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

func (p *Processor) buildGameResponse(gameID string, g game.View) core.GameResponse {
	resp := core.GameResponse{
		GameID:      gameID,
		InitialFEN:  g.InitialFEN,
		FEN:         g.FEN,
		Turn:        g.NextTurnColor.String(),
		State:       g.State.String(),
		Termination: string(g.Termination),
		Moves:       g.Moves,
		SAN:         notateLine(g.FENs, g.Moves),
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

func (p *Processor) errorResponse(message, code string) ProcessorResponse {
	return ProcessorResponse{
		Success: false,
		Error: &core.ErrorResponse{
			Error: message,
			Code:  code,
		},
	}
}

func (p *Processor) Close() error {
	queueErr := p.queue.Shutdown(5 * time.Second)
	close(p.quit)
	<-p.shadowDone
	p.mu.Lock()
	engineErr := p.validationEng.Close()
	p.mu.Unlock()
	return errors.Join(queueErr, engineErr)
}

// notateLine derives SAN for each live move from the position before it,
// fens[i] being the position before moves[i]. Each ply stands alone, so a
// position the core cannot parse leaves only that entry empty (and is logged
// once, by the shadow check, when the move was made).
func notateLine(fens, moves []string) []string {
	san := make([]string, len(moves))
	for i, uci := range moves {
		if i >= len(fens) {
			break
		}
		pos, err := chess.ParseFEN(fens[i])
		if err != nil {
			continue
		}
		if m, err := pos.ParseUCI(uci); err == nil {
			san[i] = pos.SAN(m)
		}
	}
	return san
}

// shadowCheck is a move the rules ruled on, for the engine to rule on too.
// ruled is the position the rules gave, empty when they refused the move.
type shadowCheck struct {
	gameID, fen, uci, ruled string
}

// shadow hands a ruling to the engine without waiting; a full backlog drops it.
func (p *Processor) shadow(check shadowCheck) {
	select {
	case p.shadows <- check:
	default:
	}
}

// shadowLoop has the engine replay each ruling and logs where it differs, on
// legality or on the resulting position; mate and stalemate are not compared.
// The rules stay the authority: the log is how they are held to account.
func (p *Processor) shadowLoop() {
	defer close(p.shadowDone)
	for {
		select {
		case <-p.quit:
			return
		case check := <-p.shadows:
			p.mu.Lock()
			err := p.validationEng.SetPosition(check.fen, []string{check.uci})
			var diagnosis engine.Diagnosis
			if err == nil {
				diagnosis, err = p.validationEng.Diagnose()
			}
			p.mu.Unlock()
			if err != nil {
				slog.Debug("engine shadow check skipped", "game_id", check.gameID, "error", err)
				continue
			}
			check.compare(diagnosis.FEN)
		}
	}
}

// compare logs a disagreement between the ruling and the position the engine
// holds after the move, which it leaves unchanged for an illegal one.
func (c shadowCheck) compare(engineFEN string) {
	before, errBefore := chess.NormalizeFEN(c.fen)
	after, errAfter := chess.NormalizeFEN(engineFEN)
	if errBefore != nil || errAfter != nil {
		slog.Warn("rules core cannot parse game position",
			"game_id", c.gameID, "fen", c.fen, "engine_fen", engineFEN)
		return
	}
	engineLegal := after != before
	switch {
	case engineLegal != (c.ruled != ""):
		slog.Warn("rules core and engine disagree on legality",
			"game_id", c.gameID, "move", c.uci, "engine_legal", engineLegal)
	case engineLegal && after != c.ruled:
		slog.Warn("rules core and engine disagree on resulting position",
			"game_id", c.gameID, "move", c.uci, "engine_fen", engineFEN, "rules_fen", c.ruled)
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
	color, failure := actingColor(g, args.Color, cmd.Actor)
	if failure != nil {
		return *failure
	}
	winner := core.StateWhiteWins
	if color == core.ColorWhite {
		winner = core.StateBlackWins
	}
	err = p.svc.EndGame(cmd.GameID, service.EndCommit{
		ExpectedFEN: g.FEN, Color: color, Actor: cmd.Actor,
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
	color, failure := actingColor(g, args.Color, cmd.Actor)
	if failure != nil {
		return *failure
	}
	opponent := core.OppositeColor(color)

	accept := func() ProcessorResponse {
		err := p.svc.EndGame(cmd.GameID, service.EndCommit{
			ExpectedFEN: g.FEN, Color: color, Actor: cmd.Actor,
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
		if err := p.svc.DeclineDraw(cmd.GameID, color, cmd.Actor); err != nil {
			return p.endGameError(err)
		}
		return p.drawResponse(cmd.GameID, "declined")
	}

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
				ExpectedFEN: g.FEN, Color: color, Actor: cmd.Actor,
				State: core.StateDraw, Termination: core.TermAgreement,
			})
			if err != nil {
				return p.endGameError(err)
			}
			return p.drawResponse(cmd.GameID, "accepted")
		}
		if err := p.svc.RecordDeclinedOffer(cmd.GameID, color, cmd.Actor, g.FEN); err != nil {
			return p.endGameError(err)
		}
		return p.drawResponse(cmd.GameID, "declined")
	}
	if err := p.svc.OfferDraw(cmd.GameID, color, cmd.Actor, g.FEN); err != nil {
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
// requested color, else the one side the caller holds, else the only human
// side. A held side acts only for its holder.
func actingColor(g game.View, requested string, actor core.Actor) (core.Color, *ProcessorResponse) {
	fail := func(message, code string) (core.Color, *ProcessorResponse) {
		return 0, &ProcessorResponse{Error: &core.ErrorResponse{Error: message, Code: code}}
	}
	var color core.Color
	switch {
	case requested != "":
		color = core.ParseColor(requested)
	default:
		var held, humans []core.Color
		for _, c := range []core.Color{core.ColorWhite, core.ColorBlack} {
			player := g.Player(c)
			if player == nil || player.Type != core.PlayerHuman {
				continue
			}
			humans = append(humans, c)
			if player.HeldBy(actor) {
				held = append(held, c)
			}
		}
		switch {
		case len(held) == 1:
			color = held[0]
		case len(held) == 0 && len(humans) == 1:
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
	if refusal := heldBy(player, actor); refusal != "" {
		return fail(refusal, core.ErrUnauthorized)
	}
	return color, nil
}

// heldBy says why actor may not act for player's side, or nothing when it may.
func heldBy(player *core.Player, actor core.Actor) string {
	switch {
	case player.Admits(actor):
		return ""
	case player.Seated:
		return "side held by a guest - its seat is required"
	case actor.UserID == "":
		return "slot claimed - authentication required"
	}
	return "slot claimed by another player"
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
	case errors.Is(err, service.ErrSideTaken):
		return p.errorResponse(err.Error(), core.ErrConflict)
	case errors.Is(err, service.ErrSideRequired):
		return p.errorResponse(err.Error(), core.ErrInvalidRequest)
	}
	return p.errorResponse(fmt.Sprintf("failed to update game: %v", err), core.ErrInternalError)
}
