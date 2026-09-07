package service

import (
	"context"
	"errors"
	"fmt"

	"chess/internal/server/core"
	"chess/internal/server/storage"
)

// GetGameHistory returns a durable replay even after the live game has been
// evicted from memory or the server has restarted.
func (s *Service) GetGameHistory(gameID string) (*core.GameHistoryResponse, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}
	record, moves, err := s.store.GetGameHistory(gameID)
	if err != nil {
		if storage.IsGameNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
		}
		if isStorageUnavailable(err) {
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return nil, fmt.Errorf("get game history: %w", err)
	}

	history := &core.GameHistoryResponse{
		GameID:       record.GameID,
		InitialFEN:   record.InitialFEN,
		Result:       record.Result,
		StartTimeUTC: record.StartTimeUTC,
		EndTimeUTC:   record.EndTimeUTC,
		Players:      playersResponse(*record),
		Moves:        make([]core.HistoryMove, 0, len(moves)),
	}
	for _, move := range moves {
		history.Moves = append(history.Moves, core.HistoryMove{
			MoveNumber:   move.MoveNumber,
			MoveUCI:      move.MoveUCI,
			FENAfterMove: move.FENAfterMove,
			PlayerColor:  move.PlayerColor,
			MoveTimeUTC:  move.MoveTimeUTC,
		})
	}
	return history, nil
}

// GetUserGames returns a bounded page of games associated either at creation
// or by a later slot claim.
func (s *Service) GetUserGames(userID string, limit, offset int) (*core.GameListResponse, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}
	records, err := s.store.QueryGamesForUser(userID, limit+1, offset)
	if err != nil {
		if isStorageUnavailable(err) {
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return nil, fmt.Errorf("get user games: %w", err)
	}

	hasNext := len(records) > limit
	if hasNext {
		records = records[:limit]
	}
	response := &core.GameListResponse{
		Games:  make([]core.GameSummary, 0, len(records)),
		Limit:  limit,
		Offset: offset,
	}
	if hasNext {
		next := offset + limit
		response.NextOffset = &next
	}
	for _, record := range records {
		response.Games = append(response.Games, core.GameSummary{
			GameID:       record.GameID,
			InitialFEN:   record.InitialFEN,
			Result:       record.Result,
			StartTimeUTC: record.StartTimeUTC,
			EndTimeUTC:   record.EndTimeUTC,
			MoveCount:    record.MoveCount,
			Players:      playersResponse(record.GameRecord),
		})
	}
	return response, nil
}

func isStorageUnavailable(err error) bool {
	return errors.Is(err, storage.ErrStorageDegraded) ||
		errors.Is(err, storage.ErrStoreClosed) ||
		errors.Is(err, storage.ErrWriteQueueFull) ||
		errors.Is(err, context.DeadlineExceeded)
}

func playersResponse(record storage.GameRecord) core.PlayersResponse {
	return core.PlayersResponse{
		White: &core.Player{
			ID: record.WhitePlayerID, Color: core.ColorWhite, Type: core.PlayerType(record.WhiteType),
			Level: record.WhiteLevel, SearchTime: record.WhiteSearchTime, ClaimedBy: record.WhiteClaimedBy,
		},
		Black: &core.Player{
			ID: record.BlackPlayerID, Color: core.ColorBlack, Type: core.PlayerType(record.BlackType),
			Level: record.BlackLevel, SearchTime: record.BlackSearchTime, ClaimedBy: record.BlackClaimedBy,
		},
	}
}
