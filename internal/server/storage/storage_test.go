package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestInitDBMigratesLegacySchemaAndRemovesRedundantIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	const legacy = `
		CREATE TABLE users (
			user_id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL COLLATE NOCASE,
			email TEXT COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			account_type TEXT NOT NULL DEFAULT 'temp',
			created_at DATETIME NOT NULL,
			expires_at DATETIME,
			last_login_at DATETIME
		);
		CREATE TABLE sessions (
			session_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
		);
		CREATE TABLE games (
			game_id TEXT PRIMARY KEY,
			initial_fen TEXT NOT NULL,
			white_player_id TEXT NOT NULL,
			white_type INTEGER NOT NULL,
			white_level INTEGER NOT NULL DEFAULT 0,
			white_search_time INTEGER NOT NULL DEFAULT 1000,
			black_player_id TEXT NOT NULL,
			black_type INTEGER NOT NULL,
			black_level INTEGER NOT NULL DEFAULT 0,
			black_search_time INTEGER NOT NULL DEFAULT 1000,
			start_time_utc DATETIME NOT NULL
		);
		CREATE TABLE moves (
			move_id INTEGER PRIMARY KEY AUTOINCREMENT,
			game_id TEXT NOT NULL,
			move_number INTEGER NOT NULL,
			move_uci TEXT NOT NULL,
			fen_after_move TEXT NOT NULL,
			player_color TEXT NOT NULL,
			move_time_utc DATETIME NOT NULL,
			FOREIGN KEY (game_id) REFERENCES games(game_id) ON DELETE CASCADE,
			UNIQUE(game_id, move_number)
		);
		CREATE INDEX idx_users_username ON users(username);
		CREATE INDEX idx_users_email ON users(email);
		CREATE INDEX idx_users_account_type ON users(account_type);
		CREATE INDEX idx_users_expires_at ON users(expires_at);
		CREATE INDEX idx_sessions_user_id ON sessions(user_id);
		CREATE INDEX idx_moves_game_id ON moves(game_id);`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.InitDB(); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	columns := tableColumns(t, store.db, "games")
	for _, column := range []string{"result", "end_time_utc", "white_claimed_by", "black_claimed_by"} {
		if !columns[column] {
			t.Errorf("migration did not add games.%s", column)
		}
	}

	indexes := schemaIndexes(t, store.db)
	for _, obsolete := range []string{
		"idx_users_username", "idx_users_email", "idx_users_account_type",
		"idx_users_expires_at", "idx_sessions_user_id", "idx_moves_game_id",
		"idx_games_finished_end_time",
	} {
		if indexes[obsolete] {
			t.Errorf("redundant index %s remains", obsolete)
		}
	}
	for _, required := range []string{
		"idx_users_email_unique", "idx_users_temp_created_at", "idx_users_temp_expires_at",
		"idx_sessions_expires_at", "idx_games_white_player", "idx_games_black_player",
		"idx_games_white_claimed", "idx_games_black_claimed",
	} {
		if !indexes[required] {
			t.Errorf("required index %s is missing", required)
		}
	}

	var version, foreignKeys int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
	}
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestReplayPersistenceIsAtomicAndReadAfterWriteConsistent(t *testing.T) {
	store := newTestStore(t)
	started := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	ended := started.Add(5 * time.Minute)

	if err := store.RecordNewGame(GameRecord{
		GameID: "game-1", InitialFEN: "initial",
		WhitePlayerID: "anonymous-white", WhiteType: 1,
		BlackPlayerID: "black", BlackType: 1,
		StartTimeUTC: started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMove(MovePersistence{
		Move: MoveRecord{
			GameID: "game-1", MoveNumber: 1, MoveUCI: "e2e4",
			FENAfterMove: "after-e2e4", PlayerColor: "w", MoveTimeUTC: ended,
		},
		ClaimColor: "w", ClaimedBy: "user-1",
		Result: "white_wins", EndTimeUTC: &ended,
	}); err != nil {
		t.Fatal(err)
	}

	// GetGameHistory must observe both queued writes without sleeps or polling.
	gameRecord, moves, err := store.GetGameHistory("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if gameRecord.WhiteClaimedBy != "user-1" || gameRecord.Result != "white_wins" || gameRecord.EndTimeUTC == nil {
		t.Fatalf("durable game mutation incomplete: %+v", gameRecord)
	}
	if len(moves) != 1 || moves[0].MoveNumber != 1 || moves[0].FENAfterMove != "after-e2e4" {
		t.Fatalf("moves = %+v", moves)
	}

	owned, err := store.QueryGamesForUser("user-1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].MoveCount != 1 {
		t.Fatalf("claimed game lookup = %+v", owned)
	}

	if err := store.RewindGame("game-1", 0); err != nil {
		t.Fatal(err)
	}
	gameRecord, moves, err = store.GetGameHistory("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if gameRecord.Result != "" || gameRecord.EndTimeUTC != nil || len(moves) != 0 {
		t.Fatalf("rewind left stale replay data: game=%+v moves=%+v", gameRecord, moves)
	}
}

func TestConnectionSettingsApplyAcrossPool(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 8)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	// Keep each connection checked out so the pool must create eight distinct
	// SQLite connections, then verify connection-local PRAGMAs on every one.
	for range 8 {
		connection, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	for i, connection := range connections {
		var foreignKeys, busyTimeout, synchronous int
		var journalMode string
		if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || synchronous != 1 || !strings.EqualFold(journalMode, "wal") {
			t.Errorf(
				"connection %d settings: foreign_keys=%d busy_timeout=%d synchronous=%d journal_mode=%s",
				i, foreignKeys, busyTimeout, synchronous, journalMode,
			)
		}
	}
}

func TestAcceptedWriteCommitsAfterQueueAdmissionFailureMarksHealthDegraded(t *testing.T) {
	store := newTestStore(t)
	store.healthStatus.Store(false) // Queue saturation rejects new work but accepted work must drain.
	done := make(chan error, 1)
	store.handleWrite(writeRequest{
		operation: "accepted_before_saturation",
		run: func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO users
				(user_id, username, password_hash, account_type, created_at)
				VALUES ('user-1', 'alice', 'hash', 'permanent', ?)`, time.Now().UTC())
			return err
		},
		barrier: done,
	})
	if err := <-done; err != nil {
		t.Fatalf("accepted write was discarded: %v", err)
	}
	if _, err := store.GetUserByID("user-1"); err != nil {
		t.Fatalf("accepted write was not committed: %v", err)
	}
}

func TestWritesAfterTransactionFailureAreSkippedExplicitly(t *testing.T) {
	store := newTestStore(t)
	failed := make(chan error, 1)
	store.handleWrite(writeRequest{
		operation: "forced_failure",
		run:       func(*sql.Tx) error { return errors.New("forced failure") },
		barrier:   failed,
	})
	if err := <-failed; err == nil {
		t.Fatal("forced transaction failure was not reported")
	}

	ran := false
	skipped := make(chan error, 1)
	store.handleWrite(writeRequest{
		operation: "after_failure",
		run: func(*sql.Tx) error {
			ran = true
			return nil
		},
		barrier: skipped,
	})
	if err := <-skipped; !errors.Is(err, ErrStorageDegraded) {
		t.Fatalf("skipped write error = %v, want ErrStorageDegraded", err)
	}
	if ran {
		t.Fatal("write ran after an earlier transaction broke ordering")
	}
}

func TestNewerSchemaVersionIsRejected(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.db.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	if err := store.InitDB(); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("InitDB error = %v, want newer-version rejection", err)
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("newer schema version was overwritten: %d", version)
	}
}

func TestQueryPlansUseOnlyPurposeBuiltOrConstraintIndexes(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()

	tests := []struct {
		name  string
		query string
		args  []any
		want  []string
	}{
		{
			name: "email partial uniqueness",
			query: `SELECT user_id FROM users
				WHERE email = ? COLLATE NOCASE AND email IS NOT NULL AND email != ''`,
			args: []any{"alice@example.com"}, want: []string{"idx_users_email_unique"},
		},
		{
			name: "temporary expiry cleanup",
			query: `SELECT user_id FROM users
				WHERE account_type = 'temp' AND expires_at IS NOT NULL AND expires_at < ?`,
			args: []any{now}, want: []string{"idx_users_temp_expires_at"},
		},
		{
			name: "oldest temporary account",
			query: `SELECT user_id FROM users
				WHERE account_type = 'temp' ORDER BY created_at ASC LIMIT 1`,
			want: []string{"idx_users_temp_created_at"},
		},
		{
			name: "ordered moves use composite unique constraint",
			query: `SELECT move_uci FROM moves
				WHERE game_id = ? ORDER BY move_number ASC`,
			args: []any{"game-1"}, want: []string{"sqlite_autoindex_moves_1"},
		},
		{
			name: "all user association branches",
			query: `SELECT game_id,
				(SELECT COUNT(*) FROM moves m WHERE m.game_id = games.game_id)
				FROM games WHERE white_player_id = ? OR black_player_id = ?
				OR white_claimed_by = ? OR black_claimed_by = ?
				ORDER BY start_time_utc DESC, game_id DESC LIMIT ? OFFSET ?`,
			args: []any{"user-1", "user-1", "user-1", "user-1", 50, 0},
			want: []string{
				"idx_games_white_player", "idx_games_black_player",
				"idx_games_white_claimed", "idx_games_black_claimed",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := explainQueryPlan(t, store.db, test.query, test.args...)
			for _, index := range test.want {
				if !strings.Contains(plan, index) {
					t.Errorf("query plan does not use %s:\n%s", index, plan)
				}
			}
		})
	}
}

func TestForeignKeyCascadeAppliesToSessions(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.CreateUser(UserRecord{
		UserID: "user-1", Username: "user1", PasswordHash: "hash",
		AccountType: "permanent", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(SessionRecord{
		SessionID: "session-1", UserID: "user-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser("user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession("session-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session survived user cascade: %v", err)
	}
}

func TestLimitedUserCreationIsAtomicWithInitialSession(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	limits := UserLimits{MaxUsers: 1, PermanentSlots: 1}

	first := UserRecord{
		UserID: "user-1", Username: "alice", PasswordHash: "hash",
		AccountType: "temp", CreatedAt: now, ExpiresAt: timePointer(now.Add(time.Hour)),
	}
	firstSession := SessionRecord{
		SessionID: "session-1", UserID: first.UserID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateUserWithinLimits(first, &firstSession, limits); err != nil {
		t.Fatal(err)
	}

	duplicate := first
	duplicate.UserID = "user-duplicate"
	if err := store.CreateUserWithinLimits(duplicate, nil, limits); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("duplicate error = %v, want ErrUserAlreadyExists", err)
	}
	if _, err := store.GetUserByID(first.UserID); err != nil {
		t.Fatalf("duplicate registration evicted existing user: %v", err)
	}

	second := UserRecord{
		UserID: "user-2", Username: "bob", PasswordHash: "hash",
		AccountType: "temp", CreatedAt: now.Add(time.Minute),
		ExpiresAt: timePointer(now.Add(2 * time.Hour)),
	}
	secondSession := SessionRecord{
		SessionID: "session-2", UserID: second.UserID,
		CreatedAt: now.Add(time.Minute), ExpiresAt: now.Add(2 * time.Hour),
	}
	if err := store.CreateUserWithinLimits(second, &secondSession, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetUserByID(first.UserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("oldest temporary user was not replaced: %v", err)
	}
	if _, err := store.GetSession(firstSession.SessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("evicted user's session survived cascade: %v", err)
	}
	if _, err := store.GetSession(secondSession.SessionID); err != nil {
		t.Fatalf("initial session not committed with user: %v", err)
	}

	third := UserRecord{
		UserID: "user-3", Username: "charlie", PasswordHash: "hash",
		AccountType: "temp", CreatedAt: now.Add(2 * time.Minute),
	}
	conflictingSession := SessionRecord{
		SessionID: secondSession.SessionID, UserID: third.UserID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	wideLimits := UserLimits{MaxUsers: 10, PermanentSlots: 2}
	if err := store.CreateUserWithinLimits(third, &conflictingSession, wideLimits); err == nil {
		t.Fatal("expected duplicate session failure")
	}
	if _, err := store.GetUserByID(third.UserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session failure did not roll back user: %v", err)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "chess.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	return store
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func schemaIndexes(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'index'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexes := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return indexes
}

func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}
