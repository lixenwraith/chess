package service

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	ErrSideTaken    = errors.New("that side is taken")
	ErrSideRequired = errors.New("color required: both sides are open")
	ErrGameOver     = errors.New("game is over")
	ErrNotHuman     = errors.New("that side is played by the computer")
	ErrNoDrawOffer  = errors.New("no draw offer from the opponent")
	ErrOfferLimit   = errors.New("one draw offer per move: make a move before offering again")
	// ErrConcessionFinal refuses to continue a game two human players ended
	// by resignation or agreement.
	ErrConcessionFinal = errors.New("a resignation or agreed draw between two human players is final")
)

// requireControl lets actor rewind or reconfigure g only when nobody else
// holds a side. A claim or a seat is what protects a player's moves; an open
// side takes them from anyone holding the game ID.
func requireControl(g *game.Game, actor core.Actor) error {
	for _, color := range []core.Color{core.ColorWhite, core.ColorBlack} {
		if !g.GetPlayer(color).Admits(actor) {
			return ErrSlotOwner
		}
	}
	return nil
}

// requireParticipant lets actor unload g when it holds one of its sides, or
// when nobody holds any.
func requireParticipant(g *game.Game, actor core.Actor) error {
	held := false
	for _, color := range []core.Color{core.ColorWhite, core.ColorBlack} {
		player := g.GetPlayer(color)
		if player.HeldBy(actor) {
			return nil
		}
		held = held || player.Held()
	}
	if held {
		return ErrSlotOwner
	}
	return nil
}

type MoveCommit struct {
	ExpectedFEN   string
	ExpectedState core.State
	ExpectedTurn  core.Color
	Actor         core.Actor
	MoveUCI       string
	NewFEN        string
	State         core.State
	Termination   core.Termination // how State was reached when terminal
	Result        *game.MoveResult
	At            time.Time
}

// EndCommit ends a game without a move: a resignation or a draw by
// agreement. Color is the side acting (resigning or accepting); a signed-in
// actor claims it if open, as a first move would.
type EndCommit struct {
	ExpectedFEN string
	Color       core.Color
	Actor       core.Actor
	State       core.State
	Termination core.Termination
	// OfferFrom, when set, requires that side's draw offer to still stand.
	OfferFrom core.Color
}

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

	now := time.Now().UTC()
	g := game.New(initialFEN, whitePlayer, blackPlayer, startingTurn, now)
	g.SetOutcome(initialState, termination, now)
	s.games[id] = g

	if s.store != nil {
		record, _ := gameRows(id, g.View())
		if err := s.store.RecordNewGame(record); err != nil {
			slog.Error("failed to queue game persistence", "game_id", id, "error", err)
		}
	}
	slog.Debug("game created", "game_id", id, "state", initialState.String(), "persistent", s.store != nil)

	return nil
}

// UpdatePlayers replaces players in an existing game on behalf of actor, who
// must not be locked out by a side another player holds.
func (s *Service) UpdatePlayers(gameID string, whitePlayer, blackPlayer *core.Player, actor core.Actor) error {
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

	// Player configuration is mutable, but who holds a side is not: a human ID
	// stays while the slot remains human, and a claim or a seat stays even if
	// the slot becomes computer-controlled.
	for _, pair := range [][2]*core.Player{{oldWhite, whitePlayer}, {oldBlack, blackPlayer}} {
		old, replacement := pair[0], pair[1]
		if old == nil {
			continue
		}
		if old.Type == core.PlayerHuman && replacement.Type == core.PlayerHuman {
			replacement.ID = old.ID
		}
		replacement.ClaimedBy, replacement.Name = old.ClaimedBy, old.Name
		replacement.Seat, replacement.Seated = old.Seat, old.Seated
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

func (s *Service) GetGameView(gameID string) (game.View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.games[gameID]
	if !ok {
		return game.View{}, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	return g.View(), nil
}

// BeginComputerMove marks the observed ongoing position pending once queue has
// taken its engine task. Both happen under the lock the task's result needs, so
// a refused task leaves the game ongoing and no result finds it before pending.
func (s *Service) BeginComputerMove(gameID, expectedFEN string, expectedTurn core.Color, queue func() error) error {
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
	if err := queue(); err != nil {
		return err
	}
	g.SetStateAt(core.StatePending, time.Now().UTC())
	s.waiter.NotifyGame(gameID, len(g.Moves()), core.StatePending)
	slog.Debug("computer move started", "game_id", gameID, "turn", expectedTurn.String())
	return nil
}

func (s *Service) GenerateGameID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
		var err error
		if claimUserID, err = actorClaim(g, currentTurn, commit.Actor); err != nil {
			return err
		}
	}

	at := commit.At.UTC()
	if commit.At.IsZero() {
		at = time.Now().UTC()
	}
	if claimUserID != "" {
		if err := g.ClaimSlot(currentTurn, claimUserID, commit.Actor.Name); err != nil {
			return err
		}
	}
	if commit.State.IsTerminal() && !commit.Termination.ValidFor(commit.State) {
		return fmt.Errorf("termination %q cannot produce %s", commit.Termination, commit.State)
	}
	g.AddSnapshot(commit.NewFEN, commit.MoveUCI, core.OppositeColor(currentTurn), at)
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
	claimUserID, err := actorClaim(g, commit.Color, commit.Actor)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if claimUserID != "" {
		if err := g.ClaimSlot(commit.Color, claimUserID, commit.Actor.Name); err != nil {
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

// actorClaim authorizes actor to act for color's human side and returns the
// user ID to record as its claim: a signed-in actor claims an open side, a
// guest's act leaves it open.
func actorClaim(g *game.Game, color core.Color, actor core.Actor) (string, error) {
	player := g.GetPlayer(color)
	switch {
	case player == nil:
		return "", errors.New("invalid color")
	case player.Type != core.PlayerHuman:
		return "", ErrNotHuman
	case !player.Admits(actor):
		return "", ErrSlotOwner
	case !player.Held():
		return actor.UserID, nil
	}
	return "", nil
}

// JoinGame gives actor an open human side without a move: color, or the only
// open one. A signed-in actor claims it; a guest takes it with seat, whose
// secret the caller then hands them. It returns the side and whether it was
// taken now: an actor who holds one already keeps it, and nothing changes.
func (s *Service) JoinGame(gameID string, color core.Color, actor core.Actor, seat []byte) (core.Color, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return 0, false, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State().IsTerminal() {
		return 0, false, ErrGameOver
	}
	var open []core.Color
	for _, c := range []core.Color{core.ColorWhite, core.ColorBlack} {
		player := g.GetPlayer(c)
		if (color == 0 || color == c) && player.HeldBy(actor) {
			return c, false, nil
		}
		if player.Type == core.PlayerHuman && !player.Held() {
			open = append(open, c)
		}
	}
	switch {
	case color != 0 && g.GetPlayer(color).Type != core.PlayerHuman:
		return 0, false, ErrNotHuman
	case color != 0 && !slices.Contains(open, color), len(open) == 0:
		return 0, false, ErrSideTaken
	case color == 0 && len(open) == 2:
		return 0, false, ErrSideRequired
	case color == 0:
		color = open[0]
	}

	claimedBy := actor.UserID
	if claimedBy != "" {
		seat = nil
		if err := g.ClaimSlot(color, claimedBy, actor.Name); err != nil {
			return 0, false, err
		}
	} else if err := g.SeatGuest(color, seat); err != nil {
		return 0, false, err
	}
	g.Touch(time.Now().UTC())
	if s.store != nil {
		if err := s.store.RecordJoin(gameID, color.String(), claimedBy, seat); err != nil {
			slog.Error("failed to queue join persistence", "game_id", gameID, "error", err)
		}
	}
	s.waiter.NotifyAll(gameID)
	slog.Debug("side joined", "game_id", gameID, "side", color.String(), "guest", claimedBy == "")
	return color, true, nil
}

// OfferDraw records color's offer for a human opponent to answer. Repeating
// a standing offer is a no-op; a new offer needs a move since the last one.
func (s *Service) OfferDraw(gameID string, color core.Color, actor core.Actor, expectedFEN string) error {
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
func (s *Service) RecordDeclinedOffer(gameID string, color core.Color, actor core.Actor, expectedFEN string) error {
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
func (s *Service) DeclineDraw(gameID string, color core.Color, actor core.Actor) error {
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
func (s *Service) drawTarget(gameID string, color core.Color, actor core.Actor, expectedFEN string) (*game.Game, error) {
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

// UndoMoves takes back count moves on behalf of actor. A side another player
// holds refuses it, so two players cannot rewind each other; a resignation or
// agreed draw between two humans is final. Against the computer an undo
// reopens a finished game and the first concession stays on record.
func (s *Service) UndoMoves(gameID string, count int, actor core.Actor) error {
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

	s.waiter.NotifyGame(gameID, len(g.Moves()), g.State())

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
// its sides when anyone does. Durable history is kept.
func (s *Service) UnloadGame(gameID string, actor core.Actor) error {
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
