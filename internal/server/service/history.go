package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/replay"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
)

var (
	// ErrInvalidListQuery reports a malformed cursor or filter.
	ErrInvalidListQuery = errors.New("invalid list query")
	// ErrPlyOutOfRange reports a PGN ply beyond the stored line.
	ErrPlyOutOfRange = replay.ErrPlyOutOfRange
	// ErrNotation reports a stored move the rules core cannot notate.
	ErrNotation = replay.ErrNotation
)

// GetGameHistory returns a durable replay even after the live game has been
// evicted from memory or the server has restarted. SAN is derived on read.
func (s *Service) GetGameHistory(gameID string) (*core.GameHistoryResponse, error) {
	record, moves, err := s.loadHistory(gameID)
	if err != nil {
		return nil, err
	}

	history := &core.GameHistoryResponse{
		GameID:       record.GameID,
		InitialFEN:   record.InitialFEN,
		Result:       record.Result,
		PGNResult:    chess.ResultToken(record.Result),
		Termination:  record.Termination,
		StartTimeUTC: record.StartTimeUTC,
		EndTimeUTC:   record.EndTimeUTC,
		Players:      playersResponse(*record),
		Moves:        make([]core.HistoryMove, 0, len(moves)),
	}
	if record.ConcessionResult != "" {
		history.Concession = &core.Concession{
			Result: record.ConcessionResult, Termination: record.ConcessionTermination,
			Ply: record.ConcessionPly,
		}
	}
	san, err := replay.Notate(record.InitialFEN, moves)
	if err != nil {
		slog.Warn("stored game has moves without notation", "game_id", record.GameID, "error", err)
	}
	for i, move := range moves {
		history.Moves = append(history.Moves, core.HistoryMove{
			MoveNumber:   move.MoveNumber,
			MoveUCI:      move.MoveUCI,
			SAN:          san[i],
			FENAfterMove: move.FENAfterMove,
			PlayerColor:  move.PlayerColor,
			MoveTimeUTC:  move.MoveTimeUTC,
		})
	}
	return history, nil
}

// GetGamePGN exports the stored game in PGN; see replay.BuildPGN for ply.
func (s *Service) GetGamePGN(gameID string, ply int) (*replay.PGN, error) {
	record, moves, err := s.loadHistory(gameID)
	if err != nil {
		return nil, err
	}
	pgn, err := replay.BuildPGN(record, moves, ply)
	if errors.Is(err, replay.ErrNotation) {
		slog.Warn("stored game cannot be exported as PGN", "game_id", gameID, "error", err)
	}
	return pgn, err
}

func (s *Service) loadHistory(gameID string) (*storage.GameRecord, []storage.MoveRecord, error) {
	if s.store == nil {
		return nil, nil, ErrStorageDisabled
	}
	record, moves, err := s.store.GetGameHistory(gameID)
	if err != nil {
		if storage.IsGameNotFound(err) {
			return nil, nil, fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
		}
		if isStorageUnavailable(err) {
			return nil, nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return nil, nil, fmt.Errorf("get game history: %w", err)
	}
	return record, moves, nil
}

// UserGamesOptions selects a page of the caller's games. Cursor, when set,
// continues a previous page and excludes Offset.
type UserGamesOptions struct {
	Limit  int
	Offset int
	Cursor string
	Color  string // "", "white", or "black"
	Status string // "", "ongoing", or "finished"
}

// GetUserGames returns a bounded page of games associated either at creation
// or by a later slot claim.
func (s *Service) GetUserGames(userID string, opts UserGamesOptions) (*core.GameListResponse, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}
	query := storage.UserGamesQuery{Limit: opts.Limit + 1, Offset: opts.Offset, Status: opts.Status}
	switch opts.Color {
	case "":
	case "white":
		query.Color = "w"
	case "black":
		query.Color = "b"
	default:
		return nil, fmt.Errorf("%w: color must be white or black", ErrInvalidListQuery)
	}
	switch opts.Status {
	case "", "ongoing", "finished":
	default:
		return nil, fmt.Errorf("%w: status must be ongoing or finished", ErrInvalidListQuery)
	}
	if opts.Cursor != "" {
		if opts.Offset != 0 {
			return nil, fmt.Errorf("%w: cursor and offset are exclusive", ErrInvalidListQuery)
		}
		after, err := decodeCursor(opts.Cursor)
		if err != nil {
			return nil, err
		}
		query.After = after
	}

	records, err := s.store.QueryGamesForUser(userID, query)
	if err != nil {
		if isStorageUnavailable(err) {
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return nil, fmt.Errorf("get user games: %w", err)
	}

	hasNext := len(records) > opts.Limit
	if hasNext {
		records = records[:opts.Limit]
	}
	response := &core.GameListResponse{
		Games:  make([]core.GameSummary, 0, len(records)),
		Limit:  opts.Limit,
		Offset: opts.Offset,
	}
	if hasNext {
		last := records[len(records)-1]
		response.NextCursor = encodeCursor(last.StartTimeUTC, last.GameID)
		if opts.Cursor == "" {
			next := opts.Offset + opts.Limit
			response.NextOffset = &next
		}
	}
	for _, record := range records {
		response.Games = append(response.Games, core.GameSummary{
			GameID:       record.GameID,
			InitialFEN:   record.InitialFEN,
			Result:       record.Result,
			PGNResult:    chess.ResultToken(record.Result),
			StartTimeUTC: record.StartTimeUTC,
			EndTimeUTC:   record.EndTimeUTC,
			MoveCount:    record.MoveCount,
			FinalFEN:     record.FinalFEN,
			Players:      playersResponse(record.GameRecord),
		})
	}
	return response, nil
}

// Cursors are opaque to clients: base64url of "<start µs>.<game ID>". The
// microsecond value round-trips PostgreSQL timestamptz exactly.
func encodeCursor(start time.Time, gameID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(start.UnixMicro(), 10) + "." + gameID))
}

func decodeCursor(cursor string) (*storage.GameCursor, error) {
	invalid := fmt.Errorf("%w: malformed cursor", ErrInvalidListQuery)
	if len(cursor) > 128 {
		return nil, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, invalid
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return nil, invalid
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil || uuid.Validate(id) != nil {
		return nil, invalid
	}
	return &storage.GameCursor{StartTimeUTC: time.UnixMicro(us).UTC(), GameID: id}, nil
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
			Name: record.WhiteName,
		},
		Black: &core.Player{
			ID: record.BlackPlayerID, Color: core.ColorBlack, Type: core.PlayerType(record.BlackType),
			Level: record.BlackLevel, SearchTime: record.BlackSearchTime, ClaimedBy: record.BlackClaimedBy,
			Name: record.BlackName,
		},
	}
}
