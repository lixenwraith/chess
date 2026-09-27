package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	writeQueueCapacity = 1000
	flushTimeout       = 5 * time.Second
	queryTimeout       = 5 * time.Second
	writeTimeout       = 10 * time.Second
	migrationTimeout   = time.Minute

	// A transient failure (PostgreSQL restart, dropped connection) is retried
	// before storage degrades. Backoff doubles from writeRetryBase, bounding
	// the stall of the ordered writer to roughly three seconds.
	writeAttempts  = 6
	writeRetryBase = 100 * time.Millisecond

	maxOpenConns = 10
	maxIdleConns = 5

	// schemaLockKey serializes migrations between the server and the CLI.
	// Advisory locks are scoped to the current database.
	schemaLockKey int64 = 0x6368657373 // "chess"
)

var (
	ErrStorageDegraded = errors.New("storage is degraded")
	ErrStoreClosed     = errors.New("storage is closed")
	ErrWriteQueueFull  = errors.New("storage write queue is full")
)

type writeRequest struct {
	operation string
	gameID    string
	run       func(context.Context, *sql.Tx) error
	barrier   chan error
}

// Store handles PostgreSQL operations with ordered async writes for gameplay
// and synchronous writes for accounts and sessions.
type Store struct {
	db           *sql.DB
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

// NewStore connects to PostgreSQL and starts the async writer. dsn is a
// libpq-style keyword/value string or postgres:// URL; unset fields fall back
// to the standard PG* environment variables and ~/.pgpass. The schema is
// resolved through search_path, so the role's default or a search_path DSN
// parameter selects it.
func NewStore(dsn string) (*Store, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Parse errors may echo the DSN, which can contain a password.
		return nil, errors.New("invalid database connection string")
	}
	if _, ok := config.RuntimeParams["application_name"]; !ok {
		config.RuntimeParams["application_name"] = "chess-server"
	}

	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	writerCtx, writerCancel := context.WithCancel(context.Background())
	s := &Store{
		db:        db,
		writeChan: make(chan writeRequest, writeQueueCapacity),
		ctx:       writerCtx,
		cancel:    writerCancel,
	}
	s.healthStatus.Store(true)

	s.wg.Add(1)
	go s.writerLoop()
	slog.Debug("storage opened",
		"host", config.Host,
		"database", config.Database,
		"user", config.User,
		"write_queue_capacity", writeQueueCapacity,
		"max_open_connections", maxOpenConns,
	)
	return s, nil
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

// executeWrite runs one logical mutation as one transaction. A transient error
// raised before COMMIT is retried: the transaction was rolled back, so a retry
// cannot duplicate it, and the single writer preserves ordering while it waits.
// A COMMIT error has an unknown outcome and is never retried. Any unrecovered
// failure degrades storage permanently, because skipping one write would leave
// later writes applied to an incomplete history.
func (s *Store) executeWrite(req writeRequest) error {
	started := time.Now()
	var err error
	for attempt := 1; ; attempt++ {
		var committing bool
		committing, err = s.runWriteTx(req.run)
		if err == nil {
			slog.Debug("storage write committed",
				"operation", req.operation,
				"game_id", req.gameID,
				"attempts", attempt,
				"duration", time.Since(started),
				"queue_depth", len(s.writeChan),
			)
			return nil
		}
		if committing || attempt >= writeAttempts || !isTransient(err) {
			break
		}
		backoff := writeRetryBase << (attempt - 1)
		slog.Warn("storage write failed transiently; retrying",
			"operation", req.operation, "game_id", req.gameID,
			"attempt", attempt, "backoff", backoff, "error", err)
		time.Sleep(backoff)
	}

	s.writeFailed.Store(true)
	s.healthStatus.Store(false)
	slog.Error("storage degraded: write operation failed",
		"operation", req.operation, "game_id", req.gameID, "error", err)
	return err
}

func (s *Store) runWriteTx(run func(context.Context, *sql.Tx) error) (committing bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin transaction: %w", err)
	}
	if err := run(ctx, tx); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return true, fmt.Errorf("commit transaction: %w", err)
	}
	return true, nil
}

func (s *Store) enqueue(operation, gameID string, fn func(context.Context, *sql.Tx) error) error {
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

// Close drains accepted writes and closes the connection pool.
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

// InitDB applies pending migrations. It holds a transaction-scoped advisory
// lock so concurrent migrators serialize, and issues no DDL when the schema is
// current: a runtime role limited to DML can start the server once an owner
// role has migrated the schema.
func (s *Store) InitDB() error {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaLockKey); err != nil {
		return fmt.Errorf("failed to lock schema: %w", err)
	}
	current, err := readSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d",
			current, schemaVersion)
	}
	if current == schemaVersion {
		slog.Debug("storage schema current", "version", current)
		return tx.Commit()
	}

	for version := current + 1; version <= schemaVersion; version++ {
		if _, err := tx.ExecContext(ctx, migrations[version-1]); err != nil {
			return fmt.Errorf("failed to apply schema version %d: %w", version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_version (version) VALUES ($1)
		ON CONFLICT (singleton) DO UPDATE SET version = excluded.version, updated_at = now()`,
		schemaVersion,
	); err != nil {
		return fmt.Errorf("failed to record schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit schema: %w", err)
	}
	slog.Info("storage schema migrated", "from", current, "to", schemaVersion,
		"duration", time.Since(started))
	return nil
}

// SchemaVersion reports the version recorded in the database; 0 means no
// schema has been created in the current search_path.
func (s *Store) SchemaVersion() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	return readSchemaVersion(ctx, tx)
}

func readSchemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT to_regclass('schema_version') IS NOT NULL`,
	).Scan(&exists); err != nil {
		return 0, fmt.Errorf("failed to inspect schema: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var version int
	err := tx.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read schema version: %w", err)
	}
	return version, nil
}

// DropSchema removes every chess table from the current search_path. It is
// destructive and requires ownership of the tables.
func (s *Store) DropSchema() error {
	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()
	// RESTRICT (the default) refuses to drop objects that others depend on,
	// such as an administrator's reporting view.
	_, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+strings.Join(ownedTables, ", "))
	if err != nil {
		return fmt.Errorf("failed to drop schema: %w", err)
	}
	return nil
}

// opContext bounds a synchronous operation so a stalled database cannot hold a
// request handler indefinitely.
func opContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), queryTimeout)
}

// isTransient reports errors after which the same transaction may succeed on a
// fresh connection.
func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", // serialization_failure
			"40P01", // deadlock_detected
			"57P01", // admin_shutdown
			"57P02", // crash_shutdown
			"57P03": // cannot_connect_now
			return true
		}
		return strings.HasPrefix(pgErr.Code, "08") // connection_exception class
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connectErr) ||
		errors.As(err, &netErr) ||
		errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.SafeToRetry(err)
}

// uniqueViolation returns the violated constraint name for SQLSTATE 23505.
func uniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// validUUID accepts the canonical 36-character form. Lookups treat anything
// else as not found instead of sending PostgreSQL a value that fails to cast.
func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func utcPointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

// ErrSchemaMismatch reports a database whose schema is absent or at a version
// other than the one this binary supports.
var ErrSchemaMismatch = errors.New("database schema version mismatch")

// CheckSchema verifies that the schema is current without changing it.
func (s *Store) CheckSchema() error {
	version, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	switch {
	case version == 0:
		return fmt.Errorf("%w: schema not initialized (run 'chess-server db init')", ErrSchemaMismatch)
	case version != schemaVersion:
		return fmt.Errorf("%w: database has version %d, binary supports %d",
			ErrSchemaMismatch, version, schemaVersion)
	}
	return nil
}
