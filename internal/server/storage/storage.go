// FILE: lixenwraith/chess/internal/server/storage/storage.go
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

// Store handles SQLite database operations with async writes for games and sync writes for auth
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

// IsHealthy returns true if the storage is operational
func (s *Store) IsHealthy() bool {
	return s.healthStatus.Load()
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