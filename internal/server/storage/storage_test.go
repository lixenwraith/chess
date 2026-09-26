package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"chess/internal/server/storage/pgtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestInitDBIsIdempotentAndRejectsNewerSchema(t *testing.T) {
	dsn := pgtest.DSN(t)
	store := openStore(t, dsn)
	if err := store.InitDB(); err != nil {
		t.Fatalf("second InitDB: %v", err)
	}
	if version, err := store.SchemaVersion(); err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, %v; want %d", version, err, schemaVersion)
	}

	if _, err := store.db.Exec(`UPDATE schema_version SET version = $1`, schemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := store.InitDB(); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("InitDB error = %v, want newer-version rejection", err)
	}
	if version, _ := store.SchemaVersion(); version != schemaVersion+1 {
		t.Fatalf("newer schema version was overwritten: %d", version)
	}
}

func TestInitDBIssuesNoDDLWhenCurrent(t *testing.T) {
	dsn := pgtest.DSN(t)
	openStore(t, dsn) // migrates

	// A read-only session rejects any DDL or write, standing in for a runtime
	// role that holds only DML privileges on an owner-migrated schema.
	readOnly, err := NewStore(pgtest.WithParam(dsn, "default_transaction_read_only", "on"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	if err := readOnly.InitDB(); err != nil {
		t.Fatalf("InitDB on a current schema must not write: %v", err)
	}
}

func TestDropSchemaRemovesOwnedTables(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	if err := store.DropSchema(); err != nil {
		t.Fatal(err)
	}
	if version, err := store.SchemaVersion(); err != nil || version != 0 {
		t.Fatalf("schema version after drop = %d, %v; want 0", version, err)
	}
	if err := store.InitDB(); err != nil {
		t.Fatalf("recreate after drop: %v", err)
	}
}

func TestSchemaConstraintsRejectInconsistentRows(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	gameID := uuid.NewString()
	for name, query := range map[string]string{
		"result without end time": `INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type, result) VALUES ($1, 'fen', $1, 1, $1, 1, 'draw')`,
		"end time without result": `INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type, end_time_utc) VALUES ($1, 'fen', $1, 1, $1, 1, now())`,
		"invalid player type": `INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type) VALUES ($1, 'fen', $1, 3, $1, 1)`,
		"uppercase username": `INSERT INTO users (user_id, username, password_hash)
			VALUES ($1, 'Alice', 'hash')`,
		"permanent with expiry": `INSERT INTO users (user_id, username, password_hash, account_type, expires_at)
			VALUES ($1, 'alice', 'hash', 'permanent', now())`,
	} {
		if _, err := store.db.Exec(query, gameID); err == nil {
			t.Errorf("%s: insert succeeded", name)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
		black_player_id, black_type) VALUES ($1, 'fen', $1, 1, $1, 1)`, gameID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO moves (game_id, move_number, move_uci, fen_after_move,
		player_color) VALUES ($1, 1, 'e2e4; DROP', 'fen', 'w')`, gameID); err == nil {
		t.Error("malformed UCI move accepted")
	}
}

func TestReplayPersistenceIsAtomicAndReadAfterWriteConsistent(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	gameID, anonymous, black, user := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	started := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	ended := started.Add(5 * time.Minute)

	if err := store.RecordNewGame(GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: anonymous, WhiteType: 1,
		BlackPlayerID: black, BlackType: 1,
		StartTimeUTC: started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMove(MovePersistence{
		Move: MoveRecord{
			GameID: gameID, MoveNumber: 1, MoveUCI: "e2e4",
			FENAfterMove: "after-e2e4", PlayerColor: "w", MoveTimeUTC: ended,
		},
		ClaimColor: "w", ClaimedBy: user,
		Result: "white_wins", EndTimeUTC: &ended,
	}); err != nil {
		t.Fatal(err)
	}

	// GetGameHistory must observe both queued writes without sleeps or polling.
	record, moves, err := store.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if record.WhiteClaimedBy != user || record.Result != "white_wins" ||
		record.EndTimeUTC == nil || !record.EndTimeUTC.Equal(ended) || !record.StartTimeUTC.Equal(started) {
		t.Fatalf("durable game mutation incomplete: %+v", record)
	}
	if record.StartTimeUTC.Location() != time.UTC {
		t.Fatalf("start time location = %v, want UTC", record.StartTimeUTC.Location())
	}
	if len(moves) != 1 || moves[0].MoveNumber != 1 || moves[0].FENAfterMove != "after-e2e4" {
		t.Fatalf("moves = %+v", moves)
	}

	owned, err := store.QueryGamesForUser(user, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].MoveCount != 1 || owned[0].FinalFEN != "after-e2e4" {
		t.Fatalf("claimed game lookup = %+v", owned)
	}

	if err := store.RewindGame(gameID, 0); err != nil {
		t.Fatal(err)
	}
	record, moves, err = store.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Result != "" || record.EndTimeUTC != nil || len(moves) != 0 {
		t.Fatalf("rewind left stale replay data: game=%+v moves=%+v", record, moves)
	}
	owned, err = store.QueryGamesForUser(user, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].MoveCount != 0 || owned[0].FinalFEN != "initial" {
		t.Fatalf("rewound summary = %+v", owned)
	}

	if _, _, err := store.GetGameHistory("not-a-uuid"); !IsGameNotFound(err) {
		t.Fatalf("malformed ID error = %v, want not found", err)
	}
	if _, _, err := store.GetGameHistory(uuid.NewString()); !IsGameNotFound(err) {
		t.Fatalf("unknown ID error = %v, want not found", err)
	}
}

func TestUserGamesArePagedNewestFirstWithoutDuplicates(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	user, other := uuid.NewString(), uuid.NewString()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var want []string
	for i := range 5 {
		record := GameRecord{
			GameID: uuid.NewString(), InitialFEN: "initial",
			WhitePlayerID: uuid.NewString(), WhiteType: 1,
			BlackPlayerID: uuid.NewString(), BlackType: 1,
			StartTimeUTC: base.Add(time.Duration(i) * time.Minute),
		}
		switch i {
		case 0:
			record.WhiteClaimedBy = user
		case 1:
			record.BlackClaimedBy = user
		case 2:
			record.WhiteClaimedBy, record.BlackClaimedBy = user, user // self-play
		case 3:
			record.WhiteClaimedBy = other
		case 4:
			record.BlackClaimedBy, record.WhiteClaimedBy = user, other
		}
		if err := store.RecordNewGame(record); err != nil {
			t.Fatal(err)
		}
		if i != 3 {
			want = append([]string{record.GameID}, want...)
		}
	}

	var got []string
	for offset := 0; ; offset += 2 {
		page, err := store.QueryGamesForUser(user, 2, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, game := range page {
			got = append(got, game.GameID)
		}
		if len(page) < 2 {
			break
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("user games = %v, want %v", got, want)
	}
	if games, err := store.QueryGamesForUser("not-a-uuid", 10, 0); err != nil || len(games) != 0 {
		t.Fatalf("malformed user ID = %v, %v; want empty", games, err)
	}
}

func TestQueryPlansUsePurposeBuiltIndexes(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	userID := uuid.NewString()

	tests := []struct {
		name  string
		query string
		args  []any
		want  []string
	}{
		{
			name: "user games use both claim indexes",
			query: `SELECT game_id FROM games g
				WHERE g.white_claimed_by = $1 OR g.black_claimed_by = $1
				ORDER BY g.start_time_utc DESC, g.game_id DESC LIMIT 50`,
			args: []any{userID},
			want: []string{"games_white_claimed_idx", "games_black_claimed_idx"},
		},
		{
			name: "last move probe uses the moves primary key",
			query: `SELECT move_number FROM moves WHERE game_id = $1
				ORDER BY move_number DESC LIMIT 1`,
			args: []any{userID},
			want: []string{"moves_pkey"},
		},
		{
			name: "temporary expiry cleanup",
			query: `SELECT user_id FROM users
				WHERE account_type = 'temp' AND expires_at < $1`,
			args: []any{time.Now()},
			want: []string{"users_temp_expires_at_idx"},
		},
		{
			name: "oldest temporary account",
			query: `SELECT user_id FROM users WHERE account_type = 'temp'
				ORDER BY created_at, user_id LIMIT 1`,
			want: []string{"users_temp_created_at_idx"},
		},
		{
			name:  "session expiry cleanup",
			query: `SELECT session_id FROM sessions WHERE expires_at < $1`,
			args:  []any{time.Now()},
			want:  []string{"sessions_expires_at_idx"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := explain(t, store.db, test.query, test.args...)
			for _, index := range test.want {
				if !strings.Contains(plan, index) {
					t.Errorf("query plan does not use %s:\n%s", index, plan)
				}
			}
		})
	}
}

func TestAcceptedWriteCommitsAfterQueueAdmissionFailureMarksHealthDegraded(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	store.healthStatus.Store(false) // Queue saturation rejects new work but accepted work must drain.
	userID := uuid.NewString()
	done := make(chan error, 1)
	store.handleWrite(writeRequest{
		operation: "accepted_before_saturation",
		run: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO users
				(user_id, username, password_hash, account_type, created_at)
				VALUES ($1, 'alice', 'hash', 'permanent', now())`, userID)
			return err
		},
		barrier: done,
	})
	if err := <-done; err != nil {
		t.Fatalf("accepted write was discarded: %v", err)
	}
	if _, err := store.GetUserByID(userID); err != nil {
		t.Fatalf("accepted write was not committed: %v", err)
	}
}

func TestWritesAfterTransactionFailureAreSkippedExplicitly(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	failed := make(chan error, 1)
	store.handleWrite(writeRequest{
		operation: "forced_failure",
		run:       func(context.Context, *sql.Tx) error { return errors.New("forced failure") },
		barrier:   failed,
	})
	if err := <-failed; err == nil {
		t.Fatal("forced transaction failure was not reported")
	}
	if store.IsHealthy() {
		t.Fatal("permanent failure did not degrade storage")
	}

	ran := false
	skipped := make(chan error, 1)
	store.handleWrite(writeRequest{
		operation: "after_failure",
		run: func(context.Context, *sql.Tx) error {
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

func TestWriteSurvivesTerminatedConnection(t *testing.T) {
	dsn := pgtest.DSN(t)
	store := openStore(t, dsn)
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	var pid int
	if err := store.db.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var terminated bool
	if err := admin.QueryRow(`SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Skipf("cannot terminate own backend (terminated=%v): %v", terminated, err)
	}

	// The pooled connection is now dead. The ordered writer must reconnect and
	// commit instead of degrading storage.
	gameID := uuid.NewString()
	if err := store.RecordNewGame(GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: uuid.NewString(), WhiteType: 1,
		BlackPlayerID: uuid.NewString(), BlackType: 1,
		StartTimeUTC: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetGameHistory(gameID); err != nil {
		t.Fatalf("write after connection loss: %v", err)
	}
	if !store.IsHealthy() {
		t.Fatal("transient connection loss degraded storage")
	}
}

func TestTransientErrorClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "57P01"}, true},
		{&pgconn.PgError{Code: "08006"}, true},
		{&pgconn.PgError{Code: "40001"}, true},
		{fmt.Errorf("wrapped: %w", io.ErrUnexpectedEOF), true},
		{&pgconn.PgError{Code: "23505"}, false},
		{&pgconn.PgError{Code: "42P01"}, false},
		{context.DeadlineExceeded, false},
		{errors.New("forced"), false},
	} {
		if got := isTransient(test.err); got != test.want {
			t.Errorf("isTransient(%v) = %v, want %v", test.err, got, test.want)
		}
	}
}

func TestForeignKeyCascadeAppliesToSessions(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	userID, sessionID := uuid.NewString(), uuid.NewString()
	if err := store.CreateUser(UserRecord{
		UserID: userID, Username: "user1", PasswordHash: "hash",
		AccountType: "permanent", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(SessionRecord{
		SessionID: sessionID, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByID(userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.LastLoginAt == nil || !user.LastLoginAt.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("session creation did not record login time: %v", user.LastLoginAt)
	}
	if err := store.DeleteUser(userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session survived user cascade: %v", err)
	}
	if err := store.DeleteUser(userID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a missing user = %v, want sql.ErrNoRows", err)
	}
}

func TestLimitedUserCreationIsAtomicWithInitialSession(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	limits := UserLimits{MaxUsers: 1, PermanentSlots: 1}

	first := UserRecord{
		UserID: uuid.NewString(), Username: "alice", Email: "Alice@Example.com", PasswordHash: "hash",
		AccountType: "temp", CreatedAt: now, ExpiresAt: timePointer(now.Add(time.Hour)),
	}
	firstSession := SessionRecord{
		SessionID: uuid.NewString(), UserID: first.UserID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateUserWithinLimits(first, &firstSession, limits); err != nil {
		t.Fatal(err)
	}

	for _, duplicate := range []UserRecord{
		{Username: "ALICE", Email: "other@example.com"},
		{Username: "carol", Email: "alice@EXAMPLE.com"},
	} {
		duplicate.UserID = uuid.NewString()
		duplicate.PasswordHash, duplicate.AccountType, duplicate.CreatedAt = "hash", "temp", now
		if err := store.CreateUserWithinLimits(duplicate, nil, limits); !errors.Is(err, ErrUserAlreadyExists) {
			t.Fatalf("duplicate %+v error = %v, want ErrUserAlreadyExists", duplicate, err)
		}
	}
	if _, err := store.GetUserByID(first.UserID); err != nil {
		t.Fatalf("duplicate registration evicted existing user: %v", err)
	}

	second := UserRecord{
		UserID: uuid.NewString(), Username: "bob", PasswordHash: "hash",
		AccountType: "temp", CreatedAt: now.Add(time.Minute),
		ExpiresAt: timePointer(now.Add(2 * time.Hour)),
	}
	secondSession := SessionRecord{
		SessionID: uuid.NewString(), UserID: second.UserID,
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
		UserID: uuid.NewString(), Username: "charlie", PasswordHash: "hash",
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

func TestConcurrentRegistrationsRespectCapacity(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	limits := UserLimits{MaxUsers: 3, PermanentSlots: 1}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Now().UTC()
			errs <- store.CreateUserWithinLimits(UserRecord{
				UserID: uuid.NewString(), Username: fmt.Sprintf("user%d", i), PasswordHash: "hash",
				AccountType: "temp", CreatedAt: now, ExpiresAt: timePointer(now.Add(time.Hour)),
			}, nil, limits)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("registration failed: %v", err)
		}
	}
	users, err := store.GetAllUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != limits.MaxUsers {
		t.Fatalf("user count = %d, want %d", len(users), limits.MaxUsers)
	}
}

func TestUserLookupsAndUpdates(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	alice := UserRecord{
		UserID: uuid.NewString(), Username: "Alice", Email: "Alice@Example.com",
		PasswordHash: "hash", AccountType: "temp", CreatedAt: now, ExpiresAt: timePointer(now.Add(time.Hour)),
	}
	bob := UserRecord{
		UserID: uuid.NewString(), Username: "bob", PasswordHash: "hash",
		AccountType: "permanent", CreatedAt: now,
	}
	for _, user := range []UserRecord{alice, bob} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}

	byName, err := store.GetUserByUsername("ALICE")
	if err != nil || byName.UserID != alice.UserID || byName.Username != "alice" || byName.Email != "alice@example.com" {
		t.Fatalf("username lookup = %+v, %v", byName, err)
	}
	if byEmail, err := store.GetUserByEmail("ALICE@example.COM"); err != nil || byEmail.UserID != alice.UserID {
		t.Fatalf("email lookup = %+v, %v", byEmail, err)
	}
	if byID, err := store.GetUserByID(bob.UserID); err != nil || byID.Email != "" {
		t.Fatalf("user without email = %+v, %v", byID, err)
	}
	if _, err := store.GetUserByEmail(""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty email lookup = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.GetUserByID("not-a-uuid"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("malformed ID lookup = %v, want sql.ErrNoRows", err)
	}

	if err := store.UpdateUserUsername(bob.UserID, "ALICE"); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("conflicting rename = %v, want ErrUserAlreadyExists", err)
	}
	if err := store.UpdateUserEmail(bob.UserID, "alice@example.com"); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("conflicting email = %v, want ErrUserAlreadyExists", err)
	}
	if err := store.UpdateUserEmail(alice.UserID, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEmail(bob.UserID, "alice@example.com"); err != nil {
		t.Fatalf("released email not reusable: %v", err)
	}
	if err := store.UpdateUserPassword(uuid.NewString(), "hash"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("update of missing user = %v, want sql.ErrNoRows", err)
	}

	if err := store.PromoteToPermanent(alice.UserID); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.GetUserByID(alice.UserID)
	if err != nil || promoted.AccountType != "permanent" || promoted.ExpiresAt != nil {
		t.Fatalf("promoted user = %+v, %v", promoted, err)
	}
}

func TestExpiredAccountsAndSessionsAreDeleted(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	expired, current := uuid.NewString(), uuid.NewString()
	for _, user := range []UserRecord{
		{UserID: expired, Username: "expired", PasswordHash: "hash", AccountType: "temp",
			CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: timePointer(now.Add(-time.Hour))},
		{UserID: current, Username: "current", PasswordHash: "hash", AccountType: "temp",
			CreatedAt: now, ExpiresAt: timePointer(now.Add(time.Hour))},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	oldSession := uuid.NewString()
	if err := store.CreateSession(SessionRecord{
		SessionID: oldSession, UserID: current,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if valid, err := store.IsSessionValidForUser(oldSession, current); err != nil || valid {
		t.Fatalf("expired session valid = %v, %v", valid, err)
	}
	if deleted, err := store.DeleteExpiredTempUsers(); err != nil || deleted != 1 {
		t.Fatalf("expired users deleted = %d, %v; want 1", deleted, err)
	}
	if deleted, err := store.DeleteExpiredSessions(); err != nil || deleted != 1 {
		t.Fatalf("expired sessions deleted = %d, %v; want 1", deleted, err)
	}
	if valid, err := store.IsSessionValidForUser("not-a-uuid", current); err != nil || valid {
		t.Fatalf("malformed session valid = %v, %v", valid, err)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func openStore(t *testing.T, dsn string) *Store {
	t.Helper()
	store, err := NewStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	return store
}

// explain returns the plan with sequential scans disabled, so the assertion is
// about index usability rather than the planner's choice for tiny tables.
func explain(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "RESET enable_seqscan")

	rows, err := conn.QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
