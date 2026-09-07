package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	writeQueueCapacity = 1000
	flushTimeout       = 5 * time.Second
	schemaVersion      = 2
)

var memoryStoreCounter atomic.Uint64

var (
	ErrStorageDegraded = errors.New("storage is degraded")
	ErrStoreClosed     = errors.New("storage is closed")
	ErrWriteQueueFull  = errors.New("storage write queue is full")
)

type writeRequest struct {
	operation string
	gameID    string
	run       func(*sql.Tx) error
	barrier   chan error
}

// Store handles SQLite database operations with async writes for games and sync writes for auth
type Store struct {
	db           *sql.DB
	path         string
	writeChan    chan writeRequest
	healthStatus atomic.Bool
	writeFailed  atomic.Bool
	closed       atomic.Bool
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	enqueueMu    sync.RWMutex
	closeOnce    sync.Once
	closeErr     error
}

// NewStore creates a new storage instance with async writer
func NewStore(dataSourceName string, devMode bool) (*Store, error) {
	dsn := sqliteDSN(dataSourceName)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// SQLite benefits from a small pool. WAL and busy_timeout are configured in
	// the DSN for every connection, unlike connection-local PRAGMA calls.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	ctx, cancel := context.WithCancel(context.Background())

	s := &Store{
		db:        db,
		path:      dataSourceName,
		writeChan: make(chan writeRequest, writeQueueCapacity),
		ctx:       ctx,
		cancel:    cancel,
	}

	// Initialize health as true
	s.healthStatus.Store(true)

	// Start async writer
	s.wg.Add(1)
	go s.writerLoop()
	slog.Debug("storage opened",
		"path", dataSourceName,
		"dev_mode", devMode,
		"write_queue_capacity", writeQueueCapacity,
		"max_open_connections", 8,
	)

	return s, nil
}

func sqliteDSN(dataSourceName string) string {
	if dataSourceName == ":memory:" {
		// Each Store needs a private shared-cache database: shared cache keeps
		// that Store's pooled connections on one database, while the unique name
		// prevents independent in-memory stores from leaking into each other.
		dataSourceName = fmt.Sprintf(
			"file:chess-memory-%d?mode=memory&cache=shared",
			memoryStoreCounter.Add(1),
		)
	}
	separator := "?"
	if strings.Contains(dataSourceName, "?") {
		separator = "&"
	}
	return dataSourceName + separator +
		"_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL"
}

// IsHealthy returns true if the storage is operational
func (s *Store) IsHealthy() bool {
	return s.healthStatus.Load()
}

// writerLoop processes async write operations
func (s *Store) writerLoop() {
	defer s.wg.Done()
	slog.Debug("storage writer started")
	defer slog.Debug("storage writer stopped")

	for {
		select {
		case <-s.ctx.Done():
			// Every accepted operation is drained before shutdown. The queue is
			// bounded, so shutdown remains bounded by actual database work rather
			// than an arbitrary timer that can discard replay history.
			for {
				select {
				case req := <-s.writeChan:
					s.handleWrite(req)
				default:
					return
				}
			}

		case req := <-s.writeChan:
			s.handleWrite(req)
		}
	}
}

func (s *Store) handleWrite(req writeRequest) {
	if req.run == nil {
		if req.barrier != nil {
			var err error
			if !s.healthStatus.Load() {
				err = ErrStorageDegraded
			}
			req.barrier <- err
			close(req.barrier)
		}
		return
	}

	if s.writeFailed.Load() {
		slog.Error("storage write skipped after earlier transaction failure",
			"operation", req.operation, "game_id", req.gameID)
		if req.barrier != nil {
			req.barrier <- ErrStorageDegraded
			close(req.barrier)
		}
		return
	}

	err := s.executeWrite(req)
	if req.barrier != nil {
		req.barrier <- err
		close(req.barrier)
	}
}

// executeWrite runs a transactional write operation
func (s *Store) executeWrite(req writeRequest) error {
	started := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		s.writeFailed.Store(true)
		s.healthStatus.Store(false)
		slog.Error("storage degraded: failed to begin transaction",
			"operation", req.operation, "game_id", req.gameID, "error", err)
		return err
	}

	if err := req.run(tx); err != nil {
		rollbackErr := tx.Rollback()
		s.writeFailed.Store(true)
		s.healthStatus.Store(false)
		slog.Error("storage degraded: write operation failed",
			"operation", req.operation,
			"game_id", req.gameID,
			"error", err,
			"rollback_error", rollbackErr,
		)
		return err
	}

	if err := tx.Commit(); err != nil {
		s.writeFailed.Store(true)
		s.healthStatus.Store(false)
		slog.Error("storage degraded: failed to commit",
			"operation", req.operation, "game_id", req.gameID, "error", err)
		return err
	}

	slog.Debug("storage write committed",
		"operation", req.operation,
		"game_id", req.gameID,
		"duration", time.Since(started),
		"queue_depth", len(s.writeChan),
	)
	return nil
}

func (s *Store) enqueue(operation, gameID string, fn func(*sql.Tx) error) error {
	s.enqueueMu.RLock()
	defer s.enqueueMu.RUnlock()

	if s.closed.Load() {
		return ErrStoreClosed
	}
	if !s.healthStatus.Load() {
		return ErrStorageDegraded
	}

	select {
	case s.writeChan <- writeRequest{operation: operation, gameID: gameID, run: fn}:
		slog.Debug("storage write queued",
			"operation", operation,
			"game_id", gameID,
			"queue_depth", len(s.writeChan),
		)
		return nil
	default:
		s.healthStatus.Store(false)
		slog.Error("storage degraded: write queue full",
			"operation", operation,
			"game_id", gameID,
			"queue_capacity", cap(s.writeChan),
		)
		return ErrWriteQueueFull
	}
}

// Flush waits until every write queued before this call has completed. Replay
// reads use this barrier to provide read-after-write consistency while normal
// gameplay retains the low-latency async write path.
func (s *Store) Flush(ctx context.Context) error {
	s.enqueueMu.RLock()
	if s.closed.Load() {
		s.enqueueMu.RUnlock()
		return ErrStoreClosed
	}
	if !s.healthStatus.Load() {
		s.enqueueMu.RUnlock()
		return ErrStorageDegraded
	}

	done := make(chan error, 1)
	select {
	case s.writeChan <- writeRequest{operation: "flush", barrier: done}:
		s.enqueueMu.RUnlock()
	case <-ctx.Done():
		s.enqueueMu.RUnlock()
		return ctx.Err()
	}

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) flushBeforeRead() error {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	return s.Flush(ctx)
}

// Close gracefully closes the database connection
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.enqueueMu.Lock()
		s.closed.Store(true)
		s.cancel()
		s.enqueueMu.Unlock()

		s.wg.Wait()
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
	})
	return s.closeErr
}

// InitDB creates the database schema
func (s *Store) InitDB() error {
	started := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var currentVersion int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&currentVersion); err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}
	if currentVersion > schemaVersion {
		return fmt.Errorf(
			"database schema version %d is newer than supported version %d",
			currentVersion,
			schemaVersion,
		)
	}

	if _, err := tx.Exec(Schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	columns := []struct {
		name       string
		definition string
	}{
		{"result", "TEXT CHECK(result IS NULL OR result IN ('white_wins', 'black_wins', 'draw', 'stalemate'))"},
		{"end_time_utc", "DATETIME"},
		{"white_claimed_by", "TEXT"},
		{"black_claimed_by", "TEXT"},
	}
	for _, column := range columns {
		if err := ensureColumn(tx, "games", column.name, column.definition); err != nil {
			return err
		}
	}

	// These indexes duplicate UNIQUE constraints or are superseded by targeted
	// partial/composite indexes. Drop them during upgrades as well as omitting
	// them from new databases.
	for _, name := range []string{
		"idx_users_username",
		"idx_users_email",
		"idx_users_account_type",
		"idx_users_expires_at",
		"idx_sessions_user_id",
		"idx_moves_game_id",
		"idx_games_finished_end_time",
	} {
		if _, err := tx.Exec("DROP INDEX IF EXISTS " + name); err != nil {
			return fmt.Errorf("failed to remove redundant index %s: %w", name, err)
		}
	}

	if _, err := tx.Exec(Indexes); err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("failed to record schema version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit schema: %w", err)
	}
	slog.Debug("storage schema ready", "version", schemaVersion, "duration", time.Since(started))
	return nil
}

func ensureColumn(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("failed to inspect %s schema: %w", table, err)
	}

	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("failed to inspect %s column: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close %s schema rows: %w", table, err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to inspect %s schema: %w", table, err)
	}
	if found {
		return nil
	}

	if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("failed to add %s.%s: %w", table, column, err)
	}
	slog.Debug("storage schema column added", "table", table, "column", column)
	return nil
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
