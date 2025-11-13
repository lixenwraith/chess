// FILE: lixenwraith/chess/internal/server/storage/game.go
package storage

import (
	"database/sql"
	"fmt"
	"log"
)

// RecordNewGame asynchronously records a new game
func (s *Store) RecordNewGame(record GameRecord) error {
	if !s.healthStatus.Load() {
		return nil // Silently drop if degraded
	}

	select {
	case s.writeChan <- func(tx *sql.Tx) error {
		query := `INSERT INTO games (
			game_id, initial_fen, 
			white_player_id, white_type, white_level, white_search_time,
			black_player_id, black_type, black_level, black_search_time,
			start_time_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

		_, err := tx.Exec(query,
			record.GameID, record.InitialFEN,
			record.WhitePlayerID, record.WhiteType, record.WhiteLevel, record.WhiteSearchTime,
			record.BlackPlayerID, record.BlackType, record.BlackLevel, record.BlackSearchTime,
			record.StartTimeUTC,
		)
		return err
	}:
		return nil
	default:
		// Channel full, drop write
		log.Printf("Storage write queue full, dropping game record")
		return nil
	}
}

// RecordMove asynchronously records a move
func (s *Store) RecordMove(record MoveRecord) error {
	if !s.healthStatus.Load() {
		return nil // Silently drop if degraded
	}

	select {
	case s.writeChan <- func(tx *sql.Tx) error {
		query := `INSERT INTO moves (
			game_id, move_number, move_uci, fen_after_move, player_color, move_time_utc
		) VALUES (?, ?, ?, ?, ?, ?)`

		_, err := tx.Exec(query,
			record.GameID, record.MoveNumber, record.MoveUCI,
			record.FENAfterMove, record.PlayerColor, record.MoveTimeUTC,
		)
		return err
	}:
		return nil
	default:
		// Channel full, drop write
		log.Printf("Storage write queue full, dropping move record")
		return nil
	}
}

// DeleteUndoneMoves asynchronously deletes moves after undo
func (s *Store) DeleteUndoneMoves(gameID string, afterMoveNumber int) error {
	if !s.healthStatus.Load() {
		return nil // Silently drop if degraded
	}

	select {
	case s.writeChan <- func(tx *sql.Tx) error {
		query := `DELETE FROM moves WHERE game_id = ? AND move_number > ?`
		_, err := tx.Exec(query, gameID, afterMoveNumber)
		return err
	}:
		return nil
	default:
		// Channel full, drop write
		log.Printf("Storage write queue full, dropping undo operation")
		return nil
	}
}

// QueryGames retrieves games with optional filtering
func (s *Store) QueryGames(gameID, playerID string) ([]GameRecord, error) {
	query := `SELECT 
		game_id, initial_fen, 
		white_player_id, white_type, white_level, white_search_time,
		black_player_id, black_type, black_level, black_search_time,
		start_time_utc
	FROM games WHERE 1=1`

	var args []interface{}

	// Handle gameID filtering
	if gameID != "" && gameID != "*" {
		query += " AND game_id = ?"
		args = append(args, gameID)
	}

	// Handle playerID filtering
	if playerID != "" && playerID != "*" {
		query += " AND (white_player_id = ? OR black_player_id = ?)"
		args = append(args, playerID, playerID)
	}

	query += " ORDER BY start_time_utc DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var games []GameRecord
	for rows.Next() {
		var g GameRecord
		err := rows.Scan(
			&g.GameID, &g.InitialFEN,
			&g.WhitePlayerID, &g.WhiteType, &g.WhiteLevel, &g.WhiteSearchTime,
			&g.BlackPlayerID, &g.BlackType, &g.BlackLevel, &g.BlackSearchTime,
			&g.StartTimeUTC,
		)
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		games = append(games, g)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return games, nil
}