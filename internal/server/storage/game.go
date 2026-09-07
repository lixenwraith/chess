package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const gameSelectColumns = `
	g.game_id, g.initial_fen,
	g.white_player_id, g.white_type, g.white_level, g.white_search_time, g.white_claimed_by,
	g.black_player_id, g.black_type, g.black_level, g.black_search_time, g.black_claimed_by,
	g.result, g.start_time_utc, g.end_time_utc`

// RecordNewGame asynchronously records a new game. Terminal custom-FEN games
// include their result in this insert rather than relying on a second write.
func (s *Store) RecordNewGame(record GameRecord) error {
	if record.GameID == "" || record.InitialFEN == "" || record.WhitePlayerID == "" || record.BlackPlayerID == "" {
		return errors.New("game ID, initial FEN, and player IDs are required")
	}
	if err := validateResultTime(record.Result, record.EndTimeUTC); err != nil {
		return err
	}

	return s.enqueue("record_game", record.GameID, func(tx *sql.Tx) error {
		const query = `INSERT INTO games (
			game_id, initial_fen,
			white_player_id, white_type, white_level, white_search_time, white_claimed_by,
			black_player_id, black_type, black_level, black_search_time, black_claimed_by,
			start_time_utc, result, end_time_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

		_, err := tx.Exec(query,
			record.GameID, record.InitialFEN,
			record.WhitePlayerID, record.WhiteType, record.WhiteLevel, record.WhiteSearchTime,
			nullableString(record.WhiteClaimedBy),
			record.BlackPlayerID, record.BlackType, record.BlackLevel, record.BlackSearchTime,
			nullableString(record.BlackClaimedBy),
			record.StartTimeUTC, nullableString(record.Result), record.EndTimeUTC,
		)
		return err
	})
}

// RecordMove atomically persists an accepted move and any first-move claim or
// terminal result caused by that move.
func (s *Store) RecordMove(record MovePersistence) error {
	if record.Move.GameID == "" || record.Move.MoveNumber < 1 ||
		record.Move.MoveUCI == "" || record.Move.FENAfterMove == "" {
		return errors.New("move game ID, positive move number, UCI, and resulting FEN are required")
	}
	if record.Move.PlayerColor != "w" && record.Move.PlayerColor != "b" {
		return fmt.Errorf("invalid move color %q", record.Move.PlayerColor)
	}
	if record.ClaimColor != "" && record.ClaimColor != "w" && record.ClaimColor != "b" {
		return fmt.Errorf("invalid claim color %q", record.ClaimColor)
	}
	if (record.ClaimColor == "") != (record.ClaimedBy == "") {
		return errors.New("claim color and claimant must be provided together")
	}
	if err := validateResultTime(record.Result, record.EndTimeUTC); err != nil {
		return err
	}

	return s.enqueue("record_move", record.Move.GameID, func(tx *sql.Tx) error {
		const insertMove = `INSERT INTO moves (
			game_id, move_number, move_uci, fen_after_move, player_color, move_time_utc
		) VALUES (?, ?, ?, ?, ?, ?)`
		if _, err := tx.Exec(insertMove,
			record.Move.GameID,
			record.Move.MoveNumber,
			record.Move.MoveUCI,
			record.Move.FENAfterMove,
			record.Move.PlayerColor,
			record.Move.MoveTimeUTC,
		); err != nil {
			return err
		}

		if record.ClaimedBy != "" {
			column := "white_claimed_by"
			if record.ClaimColor == "b" {
				column = "black_claimed_by"
			}
			query := `UPDATE games SET ` + column + ` = ?
				WHERE game_id = ? AND (` + column + ` IS NULL OR ` + column + ` = '' OR ` + column + ` = ?)`
			result, err := tx.Exec(query, record.ClaimedBy, record.Move.GameID, record.ClaimedBy)
			if err != nil {
				return err
			}
			if err := requireOneGame(result, record.Move.GameID); err != nil {
				return err
			}
		}

		if record.Result != "" {
			result, err := tx.Exec(
				`UPDATE games SET result = ?, end_time_utc = ? WHERE game_id = ?`,
				record.Result, record.EndTimeUTC, record.Move.GameID,
			)
			if err != nil {
				return err
			}
			return requireOneGame(result, record.Move.GameID)
		}
		return nil
	})
}

// RecordGameResult persists a terminal transition not accompanied by a move,
// such as a no-legal-moves engine response.
func (s *Store) RecordGameResult(gameID, result string, at time.Time) error {
	if gameID == "" {
		return errors.New("game ID is required")
	}
	if !isValidResult(result) {
		return fmt.Errorf("invalid game result %q", result)
	}
	if at.IsZero() {
		return errors.New("game result time is required")
	}
	return s.enqueue("record_game_result", gameID, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE games SET result = ?, end_time_utc = ? WHERE game_id = ?`,
			result, at.UTC(), gameID,
		)
		if err != nil {
			return err
		}
		return requireOneGame(res, gameID)
	})
}

// RecordSlotClaim persists a claim made independently from a move.
func (s *Store) RecordSlotClaim(gameID, color, userID string) error {
	if gameID == "" || userID == "" {
		return errors.New("game ID and claimant are required")
	}
	if color != "w" && color != "b" {
		return fmt.Errorf("invalid claim color %q", color)
	}
	column := "white_claimed_by"
	if color == "b" {
		column = "black_claimed_by"
	}
	return s.enqueue("record_slot_claim", gameID, func(tx *sql.Tx) error {
		query := `UPDATE games SET ` + column + ` = ?
			WHERE game_id = ? AND (` + column + ` IS NULL OR ` + column + ` = '' OR ` + column + ` = ?)`
		res, err := tx.Exec(query, userID, gameID, userID)
		if err != nil {
			return err
		}
		return requireOneGame(res, gameID)
	})
}

// RecordPlayers keeps persisted player configuration aligned with in-memory
// configuration changes.
func (s *Store) RecordPlayers(gameID string, white, black PlayerRecord) error {
	if gameID == "" || white.PlayerID == "" || black.PlayerID == "" {
		return errors.New("game ID and player IDs are required")
	}
	return s.enqueue("record_players", gameID, func(tx *sql.Tx) error {
		const query = `UPDATE games SET
			white_player_id = ?, white_type = ?, white_level = ?, white_search_time = ?, white_claimed_by = ?,
			black_player_id = ?, black_type = ?, black_level = ?, black_search_time = ?, black_claimed_by = ?
			WHERE game_id = ?`
		res, err := tx.Exec(query,
			white.PlayerID, white.Type, white.Level, white.SearchTime, nullableString(white.ClaimedBy),
			black.PlayerID, black.Type, black.Level, black.SearchTime, nullableString(black.ClaimedBy),
			gameID,
		)
		if err != nil {
			return err
		}
		return requireOneGame(res, gameID)
	})
}

// PlayerRecord is the persistence subset of a player configuration.
type PlayerRecord struct {
	PlayerID   string
	Type       int
	Level      int
	SearchTime int
	ClaimedBy  string
}

// RewindGame atomically removes undone moves and clears a previously terminal
// result so replay readers never observe an ongoing line with a stale outcome.
func (s *Store) RewindGame(gameID string, afterMoveNumber int) error {
	if gameID == "" || afterMoveNumber < 0 {
		return errors.New("game ID and a non-negative move number are required")
	}
	return s.enqueue("rewind_game", gameID, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM moves WHERE game_id = ? AND move_number > ?`,
			gameID, afterMoveNumber,
		); err != nil {
			return err
		}
		res, err := tx.Exec(
			`UPDATE games SET result = NULL, end_time_utc = NULL WHERE game_id = ?`,
			gameID,
		)
		if err != nil {
			return err
		}
		return requireOneGame(res, gameID)
	})
}

// QueryGames retrieves games with optional filtering. A player filter matches
// both creation-time player IDs and claims made after game creation.
func (s *Store) QueryGames(gameID, playerID string) ([]GameRecord, error) {
	if err := s.flushBeforeRead(); err != nil {
		return nil, err
	}
	started := time.Now()
	query := `SELECT ` + gameSelectColumns + ` FROM games g WHERE 1=1`
	var args []any

	if gameID != "" && gameID != "*" {
		query += " AND g.game_id = ?"
		args = append(args, gameID)
	}
	if playerID != "" && playerID != "*" {
		query += ` AND (g.white_player_id = ? OR g.black_player_id = ?
			OR g.white_claimed_by = ? OR g.black_claimed_by = ?)`
		args = append(args, playerID, playerID, playerID, playerID)
	}
	query += " ORDER BY g.start_time_utc DESC, g.game_id DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query games: %w", err)
	}
	defer rows.Close()

	games := make([]GameRecord, 0)
	for rows.Next() {
		var record GameRecord
		if err := scanGame(rows, &record); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		games = append(games, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate games: %w", err)
	}
	slog.Debug("storage games queried", "count", len(games), "duration", time.Since(started))
	return games, nil
}

func (s *Store) GetGameRecord(gameID string) (*GameRecord, error) {
	if err := s.flushBeforeRead(); err != nil {
		return nil, err
	}
	return getGameRecord(s.db, gameID)
}

type gameQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func getGameRecord(queryer gameQueryer, gameID string) (*GameRecord, error) {
	var record GameRecord
	row := queryer.QueryRow(`SELECT `+gameSelectColumns+` FROM games g WHERE g.game_id = ?`, gameID)
	if err := scanGame(row, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// GetMovesForGame returns the complete, undo-consistent replay line.
func (s *Store) GetMovesForGame(gameID string) ([]MoveRecord, error) {
	if err := s.flushBeforeRead(); err != nil {
		return nil, err
	}
	return getMovesForGame(s.db, gameID)
}

func getMovesForGame(queryer gameQueryer, gameID string) ([]MoveRecord, error) {
	const query = `SELECT move_id, game_id, move_number, move_uci,
		fen_after_move, player_color, move_time_utc
		FROM moves WHERE game_id = ? ORDER BY move_number ASC`
	rows, err := queryer.Query(query, gameID)
	if err != nil {
		return nil, fmt.Errorf("query game moves: %w", err)
	}
	defer rows.Close()

	moves := make([]MoveRecord, 0)
	for rows.Next() {
		var move MoveRecord
		if err := rows.Scan(
			&move.MoveID, &move.GameID, &move.MoveNumber, &move.MoveUCI,
			&move.FENAfterMove, &move.PlayerColor, &move.MoveTimeUTC,
		); err != nil {
			return nil, fmt.Errorf("scan game move: %w", err)
		}
		moves = append(moves, move)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate game moves: %w", err)
	}
	return moves, nil
}

// GetGameHistory uses one write barrier and one read transaction for a
// consistent game-and-moves snapshot.
func (s *Store) GetGameHistory(gameID string) (*GameRecord, []MoveRecord, error) {
	if err := s.flushBeforeRead(); err != nil {
		return nil, nil, err
	}
	started := time.Now()
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("begin game history read: %w", err)
	}
	defer tx.Rollback()

	record, err := getGameRecord(tx, gameID)
	if err != nil {
		return nil, nil, err
	}
	moves, err := getMovesForGame(tx, gameID)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("finish game history read: %w", err)
	}
	slog.Debug("storage game history queried",
		"game_id", gameID, "move_count", len(moves), "duration", time.Since(started))
	return record, moves, nil
}

func (s *Store) QueryGamesForUser(userID string, limit, offset int) ([]GameSummaryRecord, error) {
	if userID == "" || limit < 1 || limit > 101 || offset < 0 {
		return nil, errors.New("user ID, limit from 1 to 101, and non-negative offset are required")
	}
	if err := s.flushBeforeRead(); err != nil {
		return nil, err
	}
	started := time.Now()
	query := `SELECT ` + gameSelectColumns + `,
		(SELECT COUNT(*) FROM moves m WHERE m.game_id = g.game_id) AS move_count
		FROM games g
		WHERE g.white_player_id = ? OR g.black_player_id = ?
			OR g.white_claimed_by = ? OR g.black_claimed_by = ?
		ORDER BY g.start_time_utc DESC, g.game_id DESC
		LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, userID, userID, userID, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query user games: %w", err)
	}
	defer rows.Close()

	games := make([]GameSummaryRecord, 0)
	for rows.Next() {
		var summary GameSummaryRecord
		if err := scanGameSummary(rows, &summary); err != nil {
			return nil, fmt.Errorf("scan user game: %w", err)
		}
		games = append(games, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user games: %w", err)
	}
	slog.Debug("storage user games queried",
		"user_id", userID,
		"count", len(games),
		"limit", limit,
		"offset", offset,
		"duration", time.Since(started),
	)
	return games, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGame(scanner rowScanner, record *GameRecord) error {
	var whiteClaimed, blackClaimed, result sql.NullString
	var endTime sql.NullTime
	if err := scanner.Scan(
		&record.GameID, &record.InitialFEN,
		&record.WhitePlayerID, &record.WhiteType, &record.WhiteLevel, &record.WhiteSearchTime, &whiteClaimed,
		&record.BlackPlayerID, &record.BlackType, &record.BlackLevel, &record.BlackSearchTime, &blackClaimed,
		&result, &record.StartTimeUTC, &endTime,
	); err != nil {
		return err
	}
	record.WhiteClaimedBy = whiteClaimed.String
	record.BlackClaimedBy = blackClaimed.String
	record.Result = result.String
	if endTime.Valid {
		ended := endTime.Time
		record.EndTimeUTC = &ended
	}
	return nil
}

func scanGameSummary(scanner rowScanner, summary *GameSummaryRecord) error {
	var whiteClaimed, blackClaimed, result sql.NullString
	var endTime sql.NullTime
	if err := scanner.Scan(
		&summary.GameID, &summary.InitialFEN,
		&summary.WhitePlayerID, &summary.WhiteType, &summary.WhiteLevel, &summary.WhiteSearchTime, &whiteClaimed,
		&summary.BlackPlayerID, &summary.BlackType, &summary.BlackLevel, &summary.BlackSearchTime, &blackClaimed,
		&result, &summary.StartTimeUTC, &endTime, &summary.MoveCount,
	); err != nil {
		return err
	}
	summary.WhiteClaimedBy = whiteClaimed.String
	summary.BlackClaimedBy = blackClaimed.String
	summary.Result = result.String
	if endTime.Valid {
		ended := endTime.Time
		summary.EndTimeUTC = &ended
	}
	return nil
}

func requireOneGame(result sql.Result, gameID string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("game %s was not updated", gameID)
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func isValidResult(result string) bool {
	switch result {
	case "white_wins", "black_wins", "draw", "stalemate":
		return true
	default:
		return false
	}
}

func validateResultTime(result string, ended *time.Time) error {
	if result == "" {
		if ended != nil {
			return errors.New("end time requires a game result")
		}
		return nil
	}
	if !isValidResult(result) {
		return fmt.Errorf("invalid game result %q", result)
	}
	if ended == nil || ended.IsZero() {
		return errors.New("terminal game result requires an end time")
	}
	return nil
}

// IsGameNotFound keeps callers independent from database/sql details.
func IsGameNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
