package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/game"
	"github.com/lixenwraith/chess/internal/server/replay"
	"github.com/lixenwraith/chess/internal/server/storage"
)

// gameLoad is one read of a stored game that will put it back in memory.
// s.mu guards it and the map that holds it.
type gameLoad struct {
	done  chan struct{}
	err   error
	stale bool // its rows changed under the read, which is taken again
}

// LoadGame puts an unfinished stored game back in memory when it is not
// loaded, reading it behind every queued write; concurrent misses share one
// read. Finished, imported and unverifiable games stay replay-only
// (ErrGameNotFound); ErrStorageUnavailable means the rows could not be read.
func (s *Service) LoadGame(ctx context.Context, gameID string) error {
	s.mu.RLock()
	_, loaded := s.games[gameID]
	s.mu.RUnlock()
	if loaded {
		return nil
	}
	if s.store == nil {
		return fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	s.mu.Lock()
	if _, ok := s.games[gameID]; ok {
		s.mu.Unlock()
		return nil
	}
	load, joined := s.loading[gameID]
	if !joined {
		load = &gameLoad{done: make(chan struct{})}
		s.loading[gameID] = load
	}
	s.mu.Unlock()

	if joined {
		select {
		case <-load.done:
			return load.err
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrStorageUnavailable, ctx.Err())
		}
	}
	load.err = s.readGame(ctx, gameID, load)
	close(load.done)
	return load.err
}

// readGame inserts the game its rows describe. A removal or a released claim
// that crossed the read marks the load stale, and the rows are read again.
func (s *Service) readGame(ctx context.Context, gameID string, load *gameLoad) error {
	for {
		g, err := s.storedGame(ctx, gameID)
		s.mu.Lock()
		if err == nil && load.stale {
			load.stale = false
			s.mu.Unlock()
			continue
		}
		delete(s.loading, gameID)
		if err == nil {
			s.games[gameID] = g
		}
		s.mu.Unlock()
		if err == nil {
			slog.Info("game resumed from storage", "game_id", gameID, "plies", g.Plies())
		}
		return err
	}
}

func (s *Service) storedGame(ctx context.Context, gameID string) (*game.Game, error) {
	record, moves, err := s.loadHistory(ctx, gameID)
	switch {
	case errors.Is(err, ErrGameNotFound):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	if record.Result != "" || record.ImportedBy != "" {
		return nil, fmt.Errorf("%w: %s is replay-only", ErrGameNotFound, gameID)
	}
	// The rules decide every later move from these positions.
	if problems := replay.Verify(record, moves); len(problems) > 0 {
		slog.Warn("stored game not resumed: it fails verification", "game_id", gameID, "problem", problems[0])
		return nil, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
	}
	view, err := storedView(record, moves)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrGameNotFound, gameID, err)
	}
	return game.Restore(view), nil
}

// storedView is the view of a game as its rows describe it.
func storedView(record *storage.GameRecord, moves []storage.MoveRecord) (game.View, error) {
	players := playersResponse(*record)
	view := game.View{
		FEN:          record.InitialFEN,
		InitialFEN:   record.InitialFEN,
		Moves:        make([]string, 0, len(moves)),
		FENs:         append(make([]string, 0, len(moves)+1), record.InitialFEN),
		MoveTimes:    make([]time.Time, 0, len(moves)),
		WhitePlayer:  players.White,
		BlackPlayer:  players.Black,
		Termination:  core.Termination(record.Termination),
		StartTimeUTC: record.StartTimeUTC,
		EndTimeUTC:   record.EndTimeUTC,
	}
	for _, move := range moves {
		view.Moves = append(view.Moves, move.MoveUCI)
		view.FENs = append(view.FENs, move.FENAfterMove)
		view.MoveTimes = append(view.MoveTimes, move.MoveTimeUTC)
		view.FEN = move.FENAfterMove
	}
	pos, err := chess.ParseFEN(view.FEN)
	if err != nil {
		return game.View{}, err
	}
	view.NextTurnColor = core.ParseColor(pos.Turn().String())
	view.State, _ = core.ResultState(record.Result)
	if record.ConcessionResult != "" && record.ConcessionTimeUTC != nil {
		state, _ := core.ResultState(record.ConcessionResult)
		view.Concession = &game.Concession{
			State: state, Termination: core.Termination(record.ConcessionTermination),
			Ply: record.ConcessionPly, At: *record.ConcessionTimeUTC,
		}
	}
	return view, nil
}

// gameRows is the row and line of a view: the inverse of storedView. Names
// are left to the store, which snapshots them when it writes a claim.
func gameRows(gameID string, v game.View) (storage.GameRecord, []storage.MoveRecord) {
	result, _ := v.State.Result()
	record := storage.GameRecord{
		GameID:          gameID,
		InitialFEN:      v.InitialFEN,
		WhitePlayerID:   v.WhitePlayer.ID,
		WhiteType:       int(v.WhitePlayer.Type),
		WhiteLevel:      v.WhitePlayer.Level,
		WhiteSearchTime: v.WhitePlayer.SearchTime,
		WhiteClaimedBy:  v.WhitePlayer.ClaimedBy,
		BlackPlayerID:   v.BlackPlayer.ID,
		BlackType:       int(v.BlackPlayer.Type),
		BlackLevel:      v.BlackPlayer.Level,
		BlackSearchTime: v.BlackPlayer.SearchTime,
		BlackClaimedBy:  v.BlackPlayer.ClaimedBy,
		Result:          result,
		Termination:     string(v.Termination),
		StartTimeUTC:    v.StartTimeUTC,
		EndTimeUTC:      v.EndTimeUTC,
	}
	if c := v.Concession; c != nil {
		at := c.At
		record.ConcessionResult, _ = c.State.Result()
		record.ConcessionTermination, record.ConcessionPly, record.ConcessionTimeUTC = string(c.Termination), c.Ply, &at
	}
	moves := make([]storage.MoveRecord, len(v.Moves))
	mover := v.NextTurnColor
	for i := len(moves) - 1; i >= 0; i-- {
		mover = core.OppositeColor(mover)
		moves[i] = storage.MoveRecord{
			GameID: gameID, MoveNumber: i + 1, MoveUCI: v.Moves[i], FENAfterMove: v.FENs[i+1],
			PlayerColor: mover.String(), MoveTimeUTC: v.MoveTimes[i],
		}
	}
	return record, moves
}

// recoverStorage tries degraded storage again; resyncGames restores what it
// lost in the meantime.
func (s *Service) recoverStorage(ctx context.Context) {
	if s.store == nil || s.store.IsHealthy() {
		return
	}
	if err := s.store.Recover(ctx, s.resyncGames); err != nil {
		slog.Debug("storage still degraded", "error", err)
	}
}

// resyncGames queues every game whose writes were lost, whole, as memory has
// it. It holds the lock from readmit to the last one, so each game's later
// writes queue behind its whole row. A game unloaded since keeps its rows.
func (s *Service) resyncGames(readmit func() []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, gameID := range readmit() {
		g, ok := s.games[gameID]
		if !ok {
			continue
		}
		if err := s.store.SyncGame(gameRows(gameID, g.View())); err != nil {
			slog.Error("failed to queue game resync", "game_id", gameID, "error", err)
		}
	}
}
