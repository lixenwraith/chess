package service

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/game"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
)

var (
	ErrGameNotFound = errors.New("game not found")
	ErrGameChanged  = errors.New("game changed while move was being validated")
	ErrSlotOwner    = errors.New("player slot is owned by another user")
	ErrGameOver     = errors.New("game is over")
	ErrNotHuman     = errors.New("that side is played by the computer")
	ErrNoDrawOffer  = errors.New("no draw offer from the opponent")
	ErrOfferLimit   = errors.New("one draw offer per move: make a move before offering again")
	// ErrConcessionFinal refuses to continue a game two human players ended
	// by resignation or agreement.
	ErrConcessionFinal = errors.New("a resignation or agreed draw between two human players is final")
)

// requireControl lets actor rewind or reconfigure g only when no side is
// claimed by another user. A claim is what protects a player's moves; an
// unclaimed side is open to anyone holding the game ID.
func requireControl(g *game.Game, actor string) error {
	for _, color := range []core.Color{core.ColorWhite, core.ColorBlack} {
		if owner := g.GetSlotOwner(color); owner != "" && owner != actor {
			return ErrSlotOwner
		}
	}
	return nil
}

// requireParticipant lets actor unload g when it holds one of its claims, or
// when nothing is claimed.
func requireParticipant(g *game.Game, actor string) error {
	claimed := false
	for _, color := range []core.Color{core.ColorWhite, core.ColorBlack} {
		switch g.GetSlotOwner(color) {
		case "":
		case actor:
			return nil
		default:
			claimed = true
		}
	}
	if claimed {
		return ErrSlotOwner
	}
	return nil
}

type MoveCommit struct {
	ExpectedFEN   string
	ExpectedState core.State
	ExpectedTurn  core.Color
	ActorUserID   string
	MoveUCI       string
	NewFEN        string
	State         core.State
	Termination   core.Termination // how State was reached when terminal
	Result        *game.MoveResult
	At            time.Time
}

// EndCommit ends a game without a move: a resignation or a draw by
// agreement. Color is the side acting (resigning or accepting); an
// authenticated actor claims it if unclaimed, as a first move would.
type EndCommit struct {
	ExpectedFEN string
	Color       core.Color
	ActorUserID string
	State       core.State
	Termination core.Termination
	// OfferFrom, when set, requires that side's draw offer to still stand.
	OfferFrom core.Color
}

// CreateGame registers a new game with pre-constructed players
func (s *Service) CreateGame(
	id string,
	whitePlayer, blackPlayer *core.Player,
	initialFEN string,
	startingTurn core.Color,
	initialState core.State,
	termination core.Termination,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.games[id]; exists {
		return fmt.Errorf("game %s already exists", id)
	}

	// Check computer game limit
	hasComputer := whitePlayer.Type == core.PlayerComputer || blackPlayer.Type == core.PlayerComputer
	if hasComputer && !initialState.IsTerminal() {
		if active := s.activeComputerGamesLocked(); active >= MaxComputerGames {
			return fmt.Errorf("computer game limit reached (%d/%d)", active, MaxComputerGames)
		}
	}

	now := time.Now().UTC()
	g := game.New(initialFEN, whitePlayer, blackPlayer, startingTurn)
	g.SetOutcome(initialState, termination, now)
	s.games[id] = g

	// Persist if storage enabled
	if s.store != nil {
		result, _ := initialState.Result()
		record := storage.GameRecord{
			GameID:          id,
			InitialFEN:      initialFEN,
			WhitePlayerID:   whitePlayer.ID,
			WhiteType:       int(whitePlayer.Type),
			WhiteLevel:      whitePlayer.Level,
			WhiteSearchTime: whitePlayer.SearchTime,
			WhiteClaimedBy:  whitePlayer.ClaimedBy,
			BlackPlayerID:   blackPlayer.ID,
			BlackType:       int(blackPlayer.Type),
			BlackLevel:      blackPlayer.Level,
			BlackSearchTime: blackPlayer.SearchTime,
			BlackClaimedBy:  blackPlayer.ClaimedBy,
			Result:          result,
			Termination:     string(g.Termination()),
			StartTimeUTC:    now,
			EndTimeUTC:      g.EndTimeUTC(),
		}
		if err := s.store.RecordNewGame(record); err != nil {
			slog.Error("failed to queue game persistence", "game_id", id, "error", err)
		}
	}
	slog.Debug("game created", "game_id", id, "state", initialState.String(), "persistent", s.store != nil)

	return nil
}

// UpdatePlayers replaces players in an existing game on behalf of actor, who
// must not be locked out by another user's claim.
func (s *Service) UpdatePlayers(gameID string, whitePlayer, blackPlayer *core.Player, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State() == core.StatePending {
		return errors.New("cannot change players while computer is calculating")
	}
	if err := requireControl(g, actor); err != nil {
		return err
	}

	oldWhite := g.GetPlayer(core.ColorWhite)
	oldBlack := g.GetPlayer(core.ColorBlack)
	oldHasComputer := g.HasComputerPlayer()
	newHasComputer := whitePlayer.Type == core.PlayerComputer || blackPlayer.Type == core.PlayerComputer
	if !oldHasComputer && newHasComputer && !g.State().IsTerminal() {
		if active := s.activeComputerGamesLocked(); active >= MaxComputerGames {
			return fmt.Errorf("computer game limit reached (%d/%d)", active, MaxComputerGames)
		}
	}

	// Player configuration is mutable, but historical user association is not.
	// Preserve a human ID while the slot remains human, and preserve any claim
	// even if the slot later becomes computer-controlled.
	if oldWhite != nil {
		if oldWhite.Type == core.PlayerHuman && whitePlayer.Type == core.PlayerHuman {
			whitePlayer.ID = oldWhite.ID
		}
		whitePlayer.ClaimedBy = oldWhite.ClaimedBy
	}
	if oldBlack != nil {
		if oldBlack.Type == core.PlayerHuman && blackPlayer.Type == core.PlayerHuman {
			blackPlayer.ID = oldBlack.ID
		}
		blackPlayer.ClaimedBy = oldBlack.ClaimedBy
	}

	g.UpdatePlayers(whitePlayer, blackPlayer)
	g.ClearDrawOffers() // an offer made to the previous opponent lapses
	g.Touch(time.Now().UTC())
	if s.store != nil {
		err := s.store.RecordPlayers(gameID, playerRecord(whitePlayer), playerRecord(blackPlayer))
		if err != nil {
			slog.Error("failed to queue player persistence", "game_id", gameID, "error", err)
		}
	}
	slog.Debug("game players updated", "game_id", gameID)

	return nil
}

// GetGameView retrieves an immutable game snapshot by ID.
func (s *Service) GetGameView(gameID string) (game.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.games[gameID]
	if !ok {
		return game.View{}, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	return g.View(), nil
}

// BeginComputerMove is an optimistic state transition: only the request that
// observed the current ongoing position may enqueue engine work.
func (s *Service) BeginComputerMove(gameID, expectedFEN string, expectedTurn core.Color) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State() != core.StateOngoing || g.CurrentFEN() != expectedFEN || g.NextTurnColor() != expectedTurn {
		return ErrGameChanged
	}
	if player := g.NextPlayer(); player == nil || player.Type != core.PlayerComputer {
		return errors.New("current player is not a computer")
	}
	g.SetStateAt(core.StatePending, time.Now().UTC())
	s.waiter.NotifyGame(gameID, len(g.Moves()), core.StatePending)
	slog.Debug("computer move started", "game_id", gameID, "turn", expectedTurn.String())
	return nil
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

// ApplyMoveWithState verifies that the position validated by the processor is
// still current, then commits the move, optional first-move claim, and result as
// one in-memory transition and one database transaction.
func (s *Service) ApplyMoveWithState(gameID string, commit MoveCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}

	currentTurn := g.NextTurnColor()
	if g.CurrentFEN() != commit.ExpectedFEN ||
		g.State() != commit.ExpectedState ||
		currentTurn != commit.ExpectedTurn {
		return ErrGameChanged
	}

	currentPlayer := g.NextPlayer()
	claimUserID := ""
	if currentPlayer == nil {
		return errors.New("current player is missing")
	}
	if currentPlayer.Type == core.PlayerHuman {
		owner := g.GetSlotOwner(currentTurn)
		switch {
		case owner != "" && commit.ActorUserID == "":
			return ErrSlotOwner
		case owner != "" && owner != commit.ActorUserID:
			return ErrSlotOwner
		case owner == "" && commit.ActorUserID != "":
			claimUserID = commit.ActorUserID
		}
	}

	at := commit.At.UTC()
	if commit.At.IsZero() {
		at = time.Now().UTC()
	}
	if claimUserID != "" {
		if err := g.ClaimSlot(currentTurn, claimUserID); err != nil {
			return err
		}
	}
	if commit.State.IsTerminal() && !commit.Termination.ValidFor(commit.State) {
		return fmt.Errorf("termination %q cannot produce %s", commit.Termination, commit.State)
	}
	g.AddSnapshot(commit.NewFEN, commit.MoveUCI, core.OppositeColor(currentTurn))
	g.SetOutcome(commit.State, commit.Termination, at)
	if commit.Result != nil {
		g.SetLastResult(commit.Result)
	}

	if s.store != nil {
		result, _ := commit.State.Result()
		persistence := storage.MovePersistence{
			Move: storage.MoveRecord{
				GameID: gameID, MoveNumber: len(g.Moves()), MoveUCI: commit.MoveUCI,
				FENAfterMove: commit.NewFEN, PlayerColor: currentTurn.String(), MoveTimeUTC: at,
			},
			ClaimColor:  currentTurn.String(),
			ClaimedBy:   claimUserID,
			Result:      result,
			Termination: string(g.Termination()),
			EndTimeUTC:  g.EndTimeUTC(),
		}
		if claimUserID == "" {
			persistence.ClaimColor = ""
		}
		if err := s.store.RecordMove(persistence); err != nil {
			slog.Error("failed to queue move persistence",
				"game_id", gameID, "move_number", len(g.Moves()), "error", err)
		}
	}

	s.waiter.NotifyGame(gameID, len(g.Moves()), commit.State)
	slog.Debug("game move applied",
		"game_id", gameID,
		"move_number", len(g.Moves()),
		"move", commit.MoveUCI,
		"state", commit.State.String(),
		"slot_claimed", claimUserID != "",
	)
	return nil
}

// UpdateGameState sets an operational state (pending, stuck) or a terminal
// state found without a move, such as an engine reporting no legal move.
func (s *Service) UpdateGameState(gameID string, state core.State, termination core.Termination) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if state.IsTerminal() && !termination.ValidFor(state) {
		return fmt.Errorf("termination %q cannot produce %s", termination, state)
	}

	previousState := g.State()
	now := time.Now().UTC()
	g.SetOutcome(state, termination, now)
	if s.store != nil && state.IsTerminal() && !previousState.IsTerminal() {
		result, _ := state.Result()
		if err := s.store.RecordGameEnd(storage.GameEnd{
			GameID: gameID, Result: result, Termination: string(termination), EndTimeUTC: now,
		}); err != nil {
			slog.Error("failed to queue game result persistence", "game_id", gameID, "error", err)
		}
	}
	// Notify unconditionally; the registry decides.
	s.waiter.NotifyGame(gameID, len(g.Moves()), state)
	slog.Debug("game state updated", "game_id", gameID, "from", previousState.String(), "to", state.String())

	return nil
}

// EndGame applies a resignation or an agreed draw. It re-checks, under the
// service lock, everything the processor validated: the game is unfinished
// and unchanged, the acting side is human and the actor may play it, and an
// accepted offer still stands.
func (s *Service) EndGame(gameID string, commit EndCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State().IsTerminal() {
		return ErrGameOver
	}
	if !commit.State.IsTerminal() || !commit.Termination.ValidFor(commit.State) {
		return fmt.Errorf("termination %q cannot produce %s", commit.Termination, commit.State)
	}
	if g.CurrentFEN() != commit.ExpectedFEN {
		return ErrGameChanged
	}
	if commit.OfferFrom != 0 && g.DrawOffer() != commit.OfferFrom {
		return ErrNoDrawOffer
	}
	claimUserID, err := actorClaim(g, commit.Color, commit.ActorUserID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if claimUserID != "" {
		if err := g.ClaimSlot(commit.Color, claimUserID); err != nil {
			return err
		}
	}
	g.SetOutcome(commit.State, commit.Termination, now)

	if s.store != nil {
		result, _ := commit.State.Result()
		end := storage.GameEnd{
			GameID: gameID, Result: result, Termination: string(commit.Termination), EndTimeUTC: now,
			Ply: g.Plies(),
		}
		if claimUserID != "" {
			end.ClaimColor, end.ClaimedBy = commit.Color.String(), claimUserID
		}
		if err := s.store.RecordGameEnd(end); err != nil {
			slog.Error("failed to queue game end persistence", "game_id", gameID, "error", err)
		}
	}
	s.waiter.NotifyGame(gameID, len(g.Moves()), commit.State)
	slog.Debug("game ended without a move", "game_id", gameID,
		"state", commit.State.String(), "termination", commit.Termination, "side", commit.Color.String())
	return nil
}

// actorClaim authorizes actor to act for color and returns the user ID to
// record as the slot's claim, empty when nothing is to be claimed.
func actorClaim(g *game.Game, color core.Color, actor string) (string, error) {
	player := g.GetPlayer(color)
	if player == nil {
		return "", errors.New("invalid color")
	}
	if player.Type != core.PlayerHuman {
		return "", ErrNotHuman
	}
	owner := g.GetSlotOwner(color)
	switch {
	case owner != "" && owner != actor:
		return "", ErrSlotOwner
	case owner == "" && actor != "":
		return actor, nil
	}
	return "", nil
}

// OfferDraw records color's offer for a human opponent to answer. Repeating
// a standing offer is a no-op; a new offer needs a move since the last one.
func (s *Service) OfferDraw(gameID string, color core.Color, actor, expectedFEN string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, err := s.drawTarget(gameID, color, actor, expectedFEN)
	if err != nil {
		return err
	}
	if g.DrawOffer() == color {
		return nil
	}
	if err := offerAllowed(g, color); err != nil {
		return err
	}
	g.OfferDraw(color)
	g.Touch(time.Now().UTC())
	s.waiter.NotifyAll(gameID)
	slog.Debug("draw offered", "game_id", gameID, "side", color.String())
	return nil
}

// RecordDeclinedOffer counts an offer the computer answered at once against
// color's one-offer-per-move allowance.
func (s *Service) RecordDeclinedOffer(gameID string, color core.Color, actor, expectedFEN string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, err := s.drawTarget(gameID, color, actor, expectedFEN)
	if err != nil {
		return err
	}
	if err := offerAllowed(g, color); err != nil {
		return err
	}
	g.RecordDeclinedOffer(color)
	g.Touch(time.Now().UTC())
	slog.Debug("draw offer declined by the computer", "game_id", gameID, "side", color.String())
	return nil
}

// DeclineDraw lets color turn down the opponent's standing offer.
func (s *Service) DeclineDraw(gameID string, color core.Color, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, err := s.drawTarget(gameID, color, actor, "")
	if err != nil {
		return err
	}
	if g.DrawOffer() != core.OppositeColor(color) {
		return ErrNoDrawOffer
	}
	g.DeclineDraw()
	g.Touch(time.Now().UTC())
	s.waiter.NotifyAll(gameID)
	slog.Debug("draw declined", "game_id", gameID, "side", color.String())
	return nil
}

// drawTarget returns an unfinished game whose side color the actor may play,
// optionally still at expectedFEN. Caller holds s.mu.
func (s *Service) drawTarget(gameID string, color core.Color, actor, expectedFEN string) (*game.Game, error) {
	g, ok := s.games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State().IsTerminal() {
		return nil, ErrGameOver
	}
	if g.State() != core.StateOngoing || (expectedFEN != "" && g.CurrentFEN() != expectedFEN) {
		return nil, ErrGameChanged
	}
	if _, err := actorClaim(g, color, actor); err != nil {
		return nil, err
	}
	return g, nil
}

func offerAllowed(g *game.Game, color core.Color) error {
	if last := g.LastOfferPly(color); last >= 0 && g.Plies() < last+2 {
		return ErrOfferLimit
	}
	return nil
}

// UndoMoves takes back count moves on behalf of actor. Another user's claim
// refuses it, so two players cannot rewind each other; a resignation or agreed
// draw between two humans is final. Against the computer an undo reopens a
// finished game and the first concession stays on record.
func (s *Service) UndoMoves(gameID string, count int, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State() == core.StatePending {
		return errors.New("cannot undo while computer move is in progress")
	}
	if err := requireControl(g, actor); err != nil {
		return err
	}
	if g.State().IsTerminal() && g.Termination().IsConcession() && g.BothHuman() {
		return ErrConcessionFinal
	}

	originalMoveCount := len(g.Moves())

	if err := g.UndoMoves(count); err != nil {
		return err
	}
	g.Touch(time.Now().UTC())

	// Notify waiting clients about the undo
	s.waiter.NotifyGame(gameID, len(g.Moves()), g.State())

	// Delete undone moves from storage if enabled
	if s.store != nil {
		remainingMoves := originalMoveCount - count
		if err := s.store.RewindGame(gameID, remainingMoves); err != nil {
			slog.Error("failed to queue game rewind persistence", "game_id", gameID, "error", err)
		}
	}
	slog.Debug("game moves undone", "game_id", gameID, "count", count, "remaining_moves", len(g.Moves()))

	return nil
}

// UnloadGame removes a live game on behalf of actor, who must hold one of
// its claims when it has any. Durable history is kept.
func (s *Service) UnloadGame(gameID string, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if err := requireParticipant(g, actor); err != nil {
		return err
	}
	return s.deleteGameLocked(gameID, g)
}

// DeleteGame removes a game from the service
func (s *Service) DeleteGame(gameID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	return s.deleteGameLocked(gameID, g)
}

func (s *Service) deleteGameLocked(gameID string, g *game.Game) error {
	if g.State() == core.StatePending {
		return errors.New("cannot delete game while computer move is in progress")
	}

	// Remove from wait registry
	s.waiter.RemoveGame(gameID)

	delete(s.games, gameID)
	slog.Debug("game unloaded from memory", "game_id", gameID)
	return nil
}

func playerRecord(player *core.Player) storage.PlayerRecord {
	return storage.PlayerRecord{
		PlayerID:   player.ID,
		Type:       int(player.Type),
		Level:      player.Level,
		SearchTime: player.SearchTime,
		ClaimedBy:  player.ClaimedBy,
	}
}
