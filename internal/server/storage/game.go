package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const gameSelectColumns = `
	g.game_id, g.initial_fen,
	g.white_player_id, g.white_type, g.white_level, g.white_search_time, g.white_claimed_by, g.white_name,
	g.black_player_id, g.black_type, g.black_level, g.black_search_time, g.black_claimed_by, g.black_name,
	g.result, g.start_time_utc, g.end_time_utc`

// claimantName snapshots a claimant's username inside the writing
// transaction; NULL when the slot is unclaimed or the account is gone.
func claimantName(param string) string {
	return "(SELECT u.username FROM users u WHERE u.user_id = " + param + ")"
}

// MaxUserGamesPage bounds QueryGamesForUser; callers request one extra row to
// detect a following page.
const MaxUserGamesPage = 101

// RecordNewGame asynchronously records a new game. Terminal custom-FEN games
// include their result in this insert rather than relying on a second write.
// Claimed slots also record the claimant's current username.
func (s *Store) RecordNewGame(record GameRecord) error {
	if record.GameID == "" || record.InitialFEN == "" || record.WhitePlayerID == "" || record.BlackPlayerID == "" {
		return errors.New("game ID, initial FEN, and player IDs are required")
	}
	if err := validateResultTime(record.Result, record.EndTimeUTC); err != nil {
		return err
	}

	return s.enqueue("record_game", record.GameID, func(ctx context.Context, tx *sql.Tx) error {
		query := `INSERT INTO games (
			game_id, initial_fen,
			white_player_id, white_type, white_level, white_search_time, white_claimed_by, white_name,
			black_player_id, black_type, black_level, black_search_time, black_claimed_by, black_name,
			start_time_utc, result, end_time_utc
		) VALUES ($1, $2, $3, $4, $5, $6, $7, ` + claimantName("$7") + `,
			$8, $9, $10, $11, $12, ` + claimantName("$12") + `, $13, $14, $15)`

		_, err := tx.ExecContext(ctx, query,
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

	return s.enqueue("record_move", record.Move.GameID, func(ctx context.Context, tx *sql.Tx) error {
		const insertMove = `INSERT INTO moves (
			game_id, move_number, move_uci, fen_after_move, player_color, move_time_utc
		) VALUES ($1, $2, $3, $4, $5, $6)`
		if _, err := tx.ExecContext(ctx, insertMove,
			record.Move.GameID,
			record.Move.MoveNumber,
			record.Move.MoveUCI,
			record.Move.FENAfterMove,
			record.Move.PlayerColor,
			record.Move.MoveTimeUTC,
		); err != nil {
			return err
		}

		if record.ClaimedBy == "" && record.Result == "" {
			return nil
		}
		// One UPDATE applies the optional claim and optional result. A claim may
		// only fill an empty slot or repeat the same claimant.
		claimColumn, nameColumn := "white_claimed_by", "white_name"
		if record.ClaimColor == "b" {
			claimColumn, nameColumn = "black_claimed_by", "black_name"
		}
		var set []string
		args := []any{record.Move.GameID}
		where := "game_id = $1"
		if record.ClaimedBy != "" {
			args = append(args, record.ClaimedBy)
			param := fmt.Sprintf("$%d", len(args))
			set = append(set, fmt.Sprintf("%s = %s, %s = COALESCE(%s, %s)",
				claimColumn, param, nameColumn, nameColumn, claimantName(param)))
			where += fmt.Sprintf(" AND (%s IS NULL OR %s = %s)", claimColumn, claimColumn, param)
		}
		if record.Result != "" {
			args = append(args, record.Result, record.EndTimeUTC)
			set = append(set, fmt.Sprintf("result = $%d, end_time_utc = $%d", len(args)-1, len(args)))
		}
		result, err := tx.ExecContext(ctx,
			"UPDATE games SET "+strings.Join(set, ", ")+" WHERE "+where, args...)
		if err != nil {
			return err
		}
		return requireOneGame(result, record.Move.GameID)
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
	return s.enqueue("record_game_result", gameID, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE games SET result = $2, end_time_utc = $3 WHERE game_id = $1`,
			gameID, result, at.UTC(),
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

// RecordPlayers keeps persisted player configuration aligned with in-memory
// configuration changes. Claims are carried over unchanged by the service, so
// their name snapshots are left as they are.
func (s *Store) RecordPlayers(gameID string, white, black PlayerRecord) error {
	if gameID == "" || white.PlayerID == "" || black.PlayerID == "" {
		return errors.New("game ID and player IDs are required")
	}
	return s.enqueue("record_players", gameID, func(ctx context.Context, tx *sql.Tx) error {
		const query = `UPDATE games SET
			white_player_id = $2, white_type = $3, white_level = $4, white_search_time = $5, white_claimed_by = $6,
			black_player_id = $7, black_type = $8, black_level = $9, black_search_time = $10, black_claimed_by = $11
			WHERE game_id = $1`
		res, err := tx.ExecContext(ctx, query, gameID,
			white.PlayerID, white.Type, white.Level, white.SearchTime, nullableString(white.ClaimedBy),
			black.PlayerID, black.Type, black.Level, black.SearchTime, nullableString(black.ClaimedBy),
		)
		if err != nil {
			return err
		}
		return requireOneGame(res, gameID)
	})
}

// RewindGame atomically removes undone moves and clears a previously terminal
// result so replay readers never observe an ongoing line with a stale outcome.
func (s *Store) RewindGame(gameID string, afterMoveNumber int) error {
	if gameID == "" || afterMoveNumber < 0 {
		return errors.New("game ID and a non-negative move number are required")
	}
	return s.enqueue("rewind_game", gameID, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM moves WHERE game_id = $1 AND move_number > $2`,
			gameID, afterMoveNumber,
		); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE games SET result = NULL, end_time_utc = NULL WHERE game_id = $1`,
			gameID,
		)
		if err != nil {
			return err
		}
		return requireOneGame(res, gameID)
	})
}

// DeleteAnonymousGames queues removal of games that no registered user
// claimed and whose last activity (start, last move, or end) precedes cutoff.
// Games still loaded in memory are listed in live and always kept, so a queued
// or future write can never target a deleted row. Moves cascade.
func (s *Store) DeleteAnonymousGames(cutoff time.Time, live []string) error {
	if cutoff.IsZero() {
		return errors.New("anonymous game cutoff is required")
	}
	if live == nil {
		live = []string{}
	}
	return s.enqueue("delete_anonymous_games", "", func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM games g
			WHERE g.white_claimed_by IS NULL AND g.black_claimed_by IS NULL
				AND g.start_time_utc < $1
				AND (g.end_time_utc IS NULL OR g.end_time_utc < $1)
				AND NOT EXISTS (
					SELECT 1 FROM moves m
					WHERE m.game_id = g.game_id AND m.move_time_utc >= $1
				)
				AND NOT (g.game_id = ANY($2::uuid[]))`,
			cutoff.UTC(), live)
		if err != nil {
			return err
		}
		if deleted, err := result.RowsAffected(); err == nil && deleted > 0 {
			slog.Info("storage anonymous games deleted", "count", deleted, "inactive_before", cutoff.UTC())
		}
		return nil
	})
}

// QueryGames is the administrative game lookup. Either filter may be empty or
// "*" for all; a player filter matches creation-time player IDs and claims.
func (s *Store) QueryGames(gameID, playerID string) ([]GameRecord, error) {
	query := `SELECT ` + gameSelectColumns + ` FROM games g WHERE true`
	var args []any
	if gameID != "" && gameID != "*" {
		if !validUUID(gameID) {
			return nil, fmt.Errorf("invalid game ID %q", gameID)
		}
		args = append(args, gameID)
		query += fmt.Sprintf(" AND g.game_id = $%d", len(args))
	}
	if playerID != "" && playerID != "*" {
		if !validUUID(playerID) {
			return nil, fmt.Errorf("invalid player ID %q", playerID)
		}
		args = append(args, playerID)
		query += fmt.Sprintf(` AND $%d IN (g.white_player_id, g.black_player_id,
			g.white_claimed_by, g.black_claimed_by)`, len(args))
	}
	query += " ORDER BY g.start_time_utc DESC, g.game_id DESC"

	if err := s.flushBeforeRead(); err != nil {
		return nil, err
	}
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	return games, nil
}

// GetGameHistory returns the game row and its ordered moves after every
// previously accepted write has committed. Both reads share one REPEATABLE READ
// snapshot, so a concurrent move or rewind cannot produce a mixed history.
func (s *Store) GetGameHistory(gameID string) (*GameRecord, []MoveRecord, error) {
	if !validUUID(gameID) {
		return nil, nil, sql.ErrNoRows
	}
	if err := s.flushBeforeRead(); err != nil {
		return nil, nil, err
	}
	started := time.Now()
	ctx, cancel := opContext()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("begin game history read: %w", err)
	}
	defer tx.Rollback()

	var record GameRecord
	row := tx.QueryRowContext(ctx, `SELECT `+gameSelectColumns+` FROM games g WHERE g.game_id = $1`, gameID)
	if err := scanGame(row, &record); err != nil {
		return nil, nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT game_id, move_number, move_uci,
		fen_after_move, player_color, move_time_utc
		FROM moves WHERE game_id = $1 ORDER BY move_number`, gameID)
	if err != nil {
		return nil, nil, fmt.Errorf("query game moves: %w", err)
	}
	defer rows.Close()
	moves := make([]MoveRecord, 0)
	for rows.Next() {
		var move MoveRecord
		if err := rows.Scan(
			&move.GameID, &move.MoveNumber, &move.MoveUCI,
			&move.FENAfterMove, &move.PlayerColor, &move.MoveTimeUTC,
		); err != nil {
			return nil, nil, fmt.Errorf("scan game move: %w", err)
		}
		move.MoveTimeUTC = move.MoveTimeUTC.UTC()
		moves = append(moves, move)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate game moves: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("finish game history read: %w", err)
	}
	slog.Debug("storage game history queried",
		"game_id", gameID, "move_count", len(moves), "duration", time.Since(started))
	return &record, moves, nil
}

// UserGamesQuery selects a page of a user's games, newest first. After, when
// set, continues strictly below that (start time, game ID) key; Offset
// remains for clients that page by position and must be 0 with After.
type UserGamesQuery struct {
	Limit  int
	Offset int
	After  *GameCursor
	Color  string // "" either, "w" or "b": the color the user claimed
	Status string // "" any, "ongoing", or "finished"
}

// GameCursor is a keyset position in a newest-first game listing.
type GameCursor struct {
	StartTimeUTC time.Time
	GameID       string
}

// QueryGamesForUser returns a page of games claimed by userID, newest first.
// The last move is read through a LATERAL probe of the moves primary key, so
// the summary costs one index lookup per game rather than a move count over
// the whole line.
func (s *Store) QueryGamesForUser(userID string, q UserGamesQuery) ([]GameSummaryRecord, error) {
	if userID == "" || q.Limit < 1 || q.Limit > MaxUserGamesPage || q.Offset < 0 {
		return nil, fmt.Errorf("user ID, limit from 1 to %d, and non-negative offset are required",
			MaxUserGamesPage)
	}
	if q.After != nil && (q.Offset != 0 || !validUUID(q.After.GameID)) {
		return nil, errors.New("a cursor requires a valid game ID and offset 0")
	}
	if !validUUID(userID) {
		return []GameSummaryRecord{}, nil
	}

	args := []any{userID, q.Limit, q.Offset}
	var where string
	switch q.Color {
	case "":
		where = "(g.white_claimed_by = $1 OR g.black_claimed_by = $1)"
	case "w":
		where = "g.white_claimed_by = $1"
	case "b":
		where = "g.black_claimed_by = $1"
	default:
		return nil, fmt.Errorf("invalid color filter %q", q.Color)
	}
	switch q.Status {
	case "":
	case "ongoing":
		where += " AND g.result IS NULL"
	case "finished":
		where += " AND g.result IS NOT NULL"
	default:
		return nil, fmt.Errorf("invalid status filter %q", q.Status)
	}
	if q.After != nil {
		args = append(args, q.After.StartTimeUTC, q.After.GameID)
		where += " AND (g.start_time_utc, g.game_id) < ($4, $5)"
	}

	if err := s.flushBeforeRead(); err != nil {
		return nil, err
	}
	started := time.Now()
	query := `SELECT ` + gameSelectColumns + `,
		COALESCE(last.move_number, 0), COALESCE(last.fen_after_move, g.initial_fen)
		FROM games g
		LEFT JOIN LATERAL (
			SELECT m.move_number, m.fen_after_move FROM moves m
			WHERE m.game_id = g.game_id
			ORDER BY m.move_number DESC
			LIMIT 1
		) last ON true
		WHERE ` + where + `
		ORDER BY g.start_time_utc DESC, g.game_id DESC
		LIMIT $2 OFFSET $3`
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query user games: %w", err)
	}
	defer rows.Close()

	games := make([]GameSummaryRecord, 0)
	for rows.Next() {
		var summary GameSummaryRecord
		if err := scanGame(rows, &summary.GameRecord, &summary.MoveCount, &summary.FinalFEN); err != nil {
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
		"limit", q.Limit,
		"offset", q.Offset,
		"cursor", q.After != nil,
		"duration", time.Since(started),
	)
	return games, nil
}

// ResolveGameID expands a game ID prefix of at least 8 hexadecimal digits,
// as printed by administrative listings, to the unique full ID.
func (s *Store) ResolveGameID(prefix string) (string, error) {
	prefix = strings.ToLower(prefix)
	if validUUID(prefix) {
		return prefix, nil
	}
	if len(prefix) < 8 || len(prefix) > 36 || strings.Trim(prefix, "0123456789abcdef-") != "" {
		return "", fmt.Errorf("game ID or a prefix of at least 8 hex digits required, got %q", prefix)
	}
	if err := s.flushBeforeRead(); err != nil {
		return "", err
	}
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT game_id FROM games WHERE game_id::text LIKE $1 || '%' ORDER BY game_id LIMIT 2`, prefix)
	if err != nil {
		return "", fmt.Errorf("resolve game ID: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("resolve game ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("resolve game ID: %w", err)
	}
	switch len(ids) {
	case 0:
		return "", sql.ErrNoRows
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("game ID prefix %q is ambiguous", prefix)
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanGame scans gameSelectColumns followed by any extra destinations.
func scanGame(scanner rowScanner, record *GameRecord, extra ...any) error {
	var whiteClaimed, whiteName, blackClaimed, blackName, result sql.NullString
	var endTime sql.NullTime
	dest := []any{
		&record.GameID, &record.InitialFEN,
		&record.WhitePlayerID, &record.WhiteType, &record.WhiteLevel, &record.WhiteSearchTime,
		&whiteClaimed, &whiteName,
		&record.BlackPlayerID, &record.BlackType, &record.BlackLevel, &record.BlackSearchTime,
		&blackClaimed, &blackName,
		&result, &record.StartTimeUTC, &endTime,
	}
	if err := scanner.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	record.WhiteClaimedBy = whiteClaimed.String
	record.WhiteName = whiteName.String
	record.BlackClaimedBy = blackClaimed.String
	record.BlackName = blackName.String
	record.Result = result.String
	record.StartTimeUTC = record.StartTimeUTC.UTC()
	record.EndTimeUTC = utcPointer(endTime)
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
