// FILE: internal/storage/storage.go
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store handles SQLite database operations with async writes
type Store struct {
	db           *sql.DB
	path         string
	writeChan    chan func(*sql.Tx) error
	healthStatus atomic.Bool
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewStore creates a new storage instance with async writer
func NewStore(dataSourceName string, devMode bool) (*Store, error) {
	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode in development for better concurrency
	if devMode {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
		}
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)

	ctx, cancel := context.WithCancel(context.Background())

	s := &Store{
		db:        db,
		path:      dataSourceName,
		writeChan: make(chan func(*sql.Tx) error, 1000), // Buffered for async writes
		ctx:       ctx,
		cancel:    cancel,
	}

	// Initialize health as true
	s.healthStatus.Store(true)

	// Start async writer
	s.wg.Add(1)
	go s.writerLoop()

	return s, nil
}

// writerLoop processes async write operations
func (s *Store) writerLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			// Drain remaining writes with timeout
			deadline := time.After(2 * time.Second)
			for {
				select {
				case fn := <-s.writeChan:
					if s.healthStatus.Load() {
						s.executeWrite(fn)
					}
				case <-deadline:
					return
				default:
					return
				}
			}

		case fn := <-s.writeChan:
			// Skip if already degraded
			if !s.healthStatus.Load() {
				continue
			}
			s.executeWrite(fn)
		}
	}
}

// executeWrite runs a transactional write operation
func (s *Store) executeWrite(fn func(*sql.Tx) error) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("Storage degraded: failed to begin transaction: %v", err)
		s.healthStatus.Store(false)
		return
	}

	if err := fn(tx); err != nil {
		tx.Rollback()
		log.Printf("Storage degraded: write operation failed: %v", err)
		s.healthStatus.Store(false)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Storage degraded: failed to commit: %v", err)
		s.healthStatus.Store(false)
		return
	}
}

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

// IsHealthy returns the current health status
func (s *Store) IsHealthy() bool {
	return s.healthStatus.Load()
}

// Close gracefully closes the database connection
func (s *Store) Close() error {
	// Signal writer to stop
	s.cancel()

	// Wait for writer with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Writer finished cleanly
	case <-time.After(2 * time.Second):
		log.Printf("Warning: storage writer shutdown timeout, some writes may be lost")
	}

	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// InitDB creates the database schema
func (s *Store) InitDB() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(Schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	return tx.Commit()
}

// DeleteDB removes the database file
func (s *Store) DeleteDB() error {
	// Close connection first
	if err := s.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	// ☣ DESTRUCTIVE: Removes database file
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete database file: %w", err)
	}

	return nil
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