package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// ImportGame stores a game read from PGN with its whole move list in one
// transaction, behind every gameplay write already queued, and returns once
// it has committed. The record's ImportedBy names the owner; names, event,
// site and round come from the PGN's tags.
func (s *Store) ImportGame(record GameRecord, moves []MoveRecord) error {
	if record.GameID == "" || record.InitialFEN == "" || record.WhitePlayerID == "" || record.BlackPlayerID == "" {
		return errors.New("game ID, initial FEN, and player IDs are required")
	}
	if !validUUID(record.ImportedBy) {
		return errors.New("an imported game needs its importer")
	}
	if err := validateOutcome(record.Result, record.Termination, record.EndTimeUTC); err != nil {
		return err
	}
	for i, move := range moves {
		if move.GameID != record.GameID || move.MoveNumber != i+1 || move.MoveUCI == "" || move.FENAfterMove == "" ||
			(move.PlayerColor != "w" && move.PlayerColor != "b") {
			return fmt.Errorf("move %d is malformed", i+1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout+writeTimeout)
	defer cancel()
	return s.enqueueWait(ctx, "import_game", func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO games (
			game_id, initial_fen,
			white_player_id, white_type, white_level, white_search_time, white_claimed_by, white_name,
			black_player_id, black_type, black_level, black_search_time, black_claimed_by, black_name,
			start_time_utc, result, termination, end_time_utc,
			imported_by, event, site, round
		) VALUES ($1, $2, $3, $4, 0, 0, NULL, $5, $6, $7, 0, 0, NULL, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			record.GameID, record.InitialFEN,
			record.WhitePlayerID, record.WhiteType, nullableString(record.WhiteName),
			record.BlackPlayerID, record.BlackType, nullableString(record.BlackName),
			record.StartTimeUTC, nullableString(record.Result), nullableString(record.Termination), record.EndTimeUTC,
			record.ImportedBy, nullableString(record.Event), nullableString(record.Site), nullableString(record.Round),
		); err != nil {
			return err
		}
		for _, move := range moves {
			if _, err := tx.ExecContext(ctx, `INSERT INTO moves (
				game_id, move_number, move_uci, fen_after_move, player_color, move_time_utc
			) VALUES ($1, $2, $3, $4, $5, $6)`,
				move.GameID, move.MoveNumber, move.MoveUCI, move.FENAfterMove, move.PlayerColor, move.MoveTimeUTC,
			); err != nil {
				return err
			}
		}
		slog.Debug("storage game imported", "game_id", record.GameID, "plies", len(moves), "user_id", record.ImportedBy)
		return nil
	})
}

// RemovalOutcome says what RemoveGameForUser did.
type RemovalOutcome string

const (
	// RemovedGame: the game and its moves are gone.
	RemovedGame RemovalOutcome = "game"
	// RemovedClaim: another user still claims a side, so only the caller's
	// claim and name were cleared; the game stays with the other player.
	RemovedClaim RemovalOutcome = "claim"
)

// RemoveGameForUser deletes a stored game the user alone holds (as sole
// claimant or importer), or, when another user claims a side, releases the
// user's own claim so the game leaves their list but not the other's. It
// returns sql.ErrNoRows when the user holds nothing in the game. Ordered
// behind queued writes, it returns once committed.
func (s *Store) RemoveGameForUser(gameID, userID string) (RemovalOutcome, error) {
	if !validUUID(gameID) || !validUUID(userID) {
		return "", sql.ErrNoRows
	}
	var outcome RemovalOutcome
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout+writeTimeout)
	defer cancel()
	// A missing row or a game the user holds nothing in is an answer, not a
	// failed write: the transaction then changes nothing and outcome stays
	// empty, so the writer is never degraded by it.
	err := s.enqueueWait(ctx, "remove_game_for_user", func(ctx context.Context, tx *sql.Tx) error {
		var white, black, importer sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT white_claimed_by, black_claimed_by, imported_by FROM games WHERE game_id = $1 FOR UPDATE`,
			gameID).Scan(&white, &black, &importer)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if white.String != userID && black.String != userID && importer.String != userID {
			return nil
		}
		others := (white.Valid && white.String != userID) || (black.Valid && black.String != userID)
		if others {
			_, err := tx.ExecContext(ctx, `UPDATE games SET
				white_claimed_by = CASE WHEN white_claimed_by = $2 THEN NULL ELSE white_claimed_by END,
				white_name       = CASE WHEN white_claimed_by = $2 THEN NULL ELSE white_name END,
				black_claimed_by = CASE WHEN black_claimed_by = $2 THEN NULL ELSE black_claimed_by END,
				black_name       = CASE WHEN black_claimed_by = $2 THEN NULL ELSE black_name END
				WHERE game_id = $1`, gameID, userID)
			outcome = RemovedClaim
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM games WHERE game_id = $1`, gameID)
		outcome = RemovedGame
		return err
	})
	if err != nil {
		return "", err
	}
	if outcome == "" {
		return "", sql.ErrNoRows
	}
	slog.Debug("storage game removed for user", "game_id", gameID, "user_id", userID, "outcome", outcome)
	return outcome, nil
}
