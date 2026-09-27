package service

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"chess/internal/server/core"
	"chess/internal/server/game"
	"chess/internal/server/storage"

	"github.com/google/uuid"
)

var (
	ErrGameNotFound = errors.New("game not found")
	ErrGameChanged  = errors.New("game changed while move was being validated")
	ErrSlotOwner    = errors.New("player slot is owned by another user")
)

type MoveCommit struct {
	ExpectedFEN   string
	ExpectedState core.State
	ExpectedTurn  core.Color
	ActorUserID   string
	MoveUCI       string
	NewFEN        string
	State         core.State
	Result        *game.MoveResult
	At            time.Time
}

// CreateGame registers a new game with pre-constructed players
func (s *Service) CreateGame(
	id string,
	whitePlayer, blackPlayer *core.Player,
	initialFEN string,
	startingTurn core.Color,
	initialState core.State,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.games[id]; exists {
		return fmt.Errorf("game %s already exists", id)
	}

	// Check computer game limit
	hasComputer := whitePlayer.Type == core.PlayerComputer || blackPlayer.Type == core.PlayerComputer
	if hasComputer {
		if s.computerGames.Load() >= MaxComputerGames {
			return fmt.Errorf("computer game limit reached (%d/%d)", s.computerGames.Load(), MaxComputerGames)
		}
		s.computerGames.Add(1)
	}

	now := time.Now().UTC()
	g := game.New(initialFEN, whitePlayer, blackPlayer, startingTurn)
	g.SetStateAt(initialState, now)
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

// UpdatePlayers replaces players in an existing game
func (s *Service) UpdatePlayers(gameID string, whitePlayer, blackPlayer *core.Player) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}
	if g.State() == core.StatePending {
		return errors.New("cannot change players while computer is calculating")
	}

	oldWhite := g.GetPlayer(core.ColorWhite)
	oldBlack := g.GetPlayer(core.ColorBlack)
	oldHasComputer := g.HasComputerPlayer()
	newHasComputer := whitePlayer.Type == core.PlayerComputer || blackPlayer.Type == core.PlayerComputer
	if !oldHasComputer && newHasComputer && s.computerGames.Load() >= MaxComputerGames {
		return fmt.Errorf("computer game limit reached (%d/%d)", s.computerGames.Load(), MaxComputerGames)
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
	g.Touch(time.Now().UTC())
	if oldHasComputer != newHasComputer {
		if newHasComputer {
			s.computerGames.Add(1)
		} else {
			s.computerGames.Add(-1)
		}
	}
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
	g.AddSnapshot(commit.NewFEN, commit.MoveUCI, core.OppositeColor(currentTurn))
	g.SetStateAt(commit.State, at)
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
			ClaimColor: currentTurn.String(),
			ClaimedBy:  claimUserID,
			Result:     result,
			EndTimeUTC: g.EndTimeUTC(),
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

// UpdateGameState sets the game's end state (checkmate, stalemate, etc)
func (s *Service) UpdateGameState(gameID string, state core.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}

	previousState := g.State()
	now := time.Now().UTC()
	g.SetStateAt(state, now)
	if s.store != nil && state.IsTerminal() && !previousState.IsTerminal() {
		result, _ := state.Result()
		if err := s.store.RecordGameResult(gameID, result, now); err != nil {
			slog.Error("failed to queue game result persistence", "game_id", gameID, "error", err)
		}
	}
	// Notify unconditionally; the registry decides.
	s.waiter.NotifyGame(gameID, len(g.Moves()), state)
	slog.Debug("game state updated", "game_id", gameID, "from", previousState.String(), "to", state.String())

	return nil
}

// UndoMoves removes the specified number of moves from game history
func (s *Service) UndoMoves(gameID string, count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State() == core.StatePending {
		return errors.New("cannot undo while computer move is in progress")
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

// DeleteGame removes a game from the service
func (s *Service) DeleteGame(gameID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	if g.State() == core.StatePending {
		return errors.New("cannot delete game while computer move is in progress")
	}

	// Decrement computer game count if applicable
	if g.HasComputerPlayer() {
		s.computerGames.Add(-1)
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
