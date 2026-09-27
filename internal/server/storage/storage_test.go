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
		"empty player name": `INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type, white_name) VALUES ($1, 'fen', $1, 1, $1, 1, '')`,
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

	owned, err := store.QueryGamesForUser(user, UserGamesQuery{Limit: 10})
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
	owned, err = store.QueryGamesForUser(user, UserGamesQuery{Limit: 10})
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
			end := record.StartTimeUTC.Add(time.Minute)
			record.Result, record.EndTimeUTC = "white_wins", &end
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
		page, err := store.QueryGamesForUser(user, UserGamesQuery{Limit: 2, Offset: offset})
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
	if games, err := store.QueryGamesForUser("not-a-uuid", UserGamesQuery{Limit: 10}); err != nil || len(games) != 0 {
		t.Fatalf("malformed user ID = %v, %v; want empty", games, err)
	}

	// Keyset pages match offset pages, and a game inserted at the head
	// between pages does not shift the next page.
	got = nil
	var after *GameCursor
	for page := 0; ; page++ {
		games, err := store.QueryGamesForUser(user, UserGamesQuery{Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, game := range games {
			got = append(got, game.GameID)
		}
		if len(games) < 2 {
			break
		}
		last := games[len(games)-1]
		after = &GameCursor{StartTimeUTC: last.StartTimeUTC, GameID: last.GameID}
		if page == 0 {
			newer := GameRecord{
				GameID: uuid.NewString(), InitialFEN: "initial",
				WhitePlayerID: user, WhiteType: 1, WhiteClaimedBy: user,
				BlackPlayerID: uuid.NewString(), BlackType: 1,
				StartTimeUTC: base.Add(time.Hour),
			}
			if err := store.RecordNewGame(newer); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyset pages = %v, want %v", got, want)
	}

	count := func(q UserGamesQuery) int {
		t.Helper()
		q.Limit = 50
		games, err := store.QueryGamesForUser(user, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(games)
	}
	// Five games claimed by user: indexes 0, 1, 2, 4 and the newer one.
	for _, tc := range []struct {
		q    UserGamesQuery
		want int
	}{
		{UserGamesQuery{}, 5},
		{UserGamesQuery{Color: "w"}, 3},
		{UserGamesQuery{Color: "b"}, 3},
		{UserGamesQuery{Status: "finished"}, 1},
		{UserGamesQuery{Status: "ongoing"}, 4},
		{UserGamesQuery{Color: "b", Status: "finished"}, 1},
	} {
		if got := count(tc.q); got != tc.want {
			t.Errorf("QueryGamesForUser(%+v) = %d games, want %d", tc.q, got, tc.want)
		}
	}
	for _, bad := range []UserGamesQuery{
		{Limit: 1, Color: "x"}, {Limit: 1, Status: "x"},
		{Limit: 1, Offset: 1, After: &GameCursor{GameID: user}},
		{Limit: 1, After: &GameCursor{GameID: "x"}},
	} {
		if _, err := store.QueryGamesForUser(user, bad); err == nil {
			t.Errorf("QueryGamesForUser(%+v) succeeded", bad)
		}
	}

	prefix := want[0][:8]
	if id, err := store.ResolveGameID(prefix); err != nil || id != want[0] {
		t.Errorf("ResolveGameID(%s) = %q, %v; want %s", prefix, id, err, want[0])
	}
	if _, err := store.ResolveGameID("0000000"); err == nil {
		t.Error("ResolveGameID accepted a 7-digit prefix")
	}
	if _, err := store.ResolveGameID("ffffffff-ffff"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ResolveGameID(unknown) error = %v, want sql.ErrNoRows", err)
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
			name: "keyset page of one color uses its claim index",
			query: `SELECT game_id FROM games g
				WHERE g.white_claimed_by = $1 AND (g.start_time_utc, g.game_id) < ($2, $3)
				ORDER BY g.start_time_utc DESC, g.game_id DESC LIMIT 50`,
			args: []any{userID, time.Now(), userID},
			want: []string{"games_white_claimed_idx"},
		},
		{
			name: "last move probe uses the moves primary key",
			query: `SELECT move_number FROM moves WHERE game_id = $1
				ORDER BY move_number DESC LIMIT 1`,
			args: []any{userID},
			want: []string{"moves_pkey"},
		},
		{
			name: "anonymous purge candidates",
			query: `SELECT game_id FROM games g
				WHERE g.white_claimed_by IS NULL AND g.black_claimed_by IS NULL
				AND g.start_time_utc < $1`,
			args: []any{time.Now()},
			want: []string{"games_anonymous_start_idx"},
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
				(user_id, username, password_hash, created_at)
				VALUES ($1, 'alice', 'hash', now())`, userID)
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
		UserID: userID, Username: "user1", PasswordHash: "hash", CreatedAt: now,
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
	limits := UserLimits{MaxUsers: 2}

	first := UserRecord{
		UserID: uuid.NewString(), Username: "alice", Email: "Alice@Example.com",
		PasswordHash: "hash", CreatedAt: now,
	}
	firstSession := SessionRecord{
		SessionID: uuid.NewString(), UserID: first.UserID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateUserWithinLimits(first, &firstSession, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(firstSession.SessionID); err != nil {
		t.Fatalf("initial session not committed with user: %v", err)
	}

	// A session failure rolls back the account.
	conflicting := UserRecord{UserID: uuid.NewString(), Username: "carol", PasswordHash: "hash", CreatedAt: now}
	conflictingSession := SessionRecord{
		SessionID: firstSession.SessionID, UserID: conflicting.UserID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateUserWithinLimits(conflicting, &conflictingSession, limits); err == nil {
		t.Fatal("expected duplicate session failure")
	}
	if _, err := store.GetUserByID(conflicting.UserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session failure did not roll back user: %v", err)
	}

	second := UserRecord{UserID: uuid.NewString(), Username: "bob", PasswordHash: "hash", CreatedAt: now}
	if err := store.CreateUserWithinLimits(second, nil, limits); err != nil {
		t.Fatal(err)
	}

	// At capacity, a duplicate is still reported as a duplicate, a new name
	// is refused, and nobody is removed to make room.
	for _, duplicate := range []UserRecord{
		{Username: "ALICE", Email: "other@example.com"},
		{Username: "dave", Email: "alice@EXAMPLE.com"},
	} {
		duplicate.UserID, duplicate.PasswordHash, duplicate.CreatedAt = uuid.NewString(), "hash", now
		if err := store.CreateUserWithinLimits(duplicate, nil, limits); !errors.Is(err, ErrUserAlreadyExists) {
			t.Fatalf("duplicate %+v error = %v, want ErrUserAlreadyExists", duplicate, err)
		}
	}
	third := UserRecord{UserID: uuid.NewString(), Username: "erin", PasswordHash: "hash", CreatedAt: now}
	if err := store.CreateUserWithinLimits(third, nil, limits); !errors.Is(err, ErrUserCapacity) {
		t.Fatalf("registration at capacity = %v, want ErrUserCapacity", err)
	}
	for _, existing := range []string{first.UserID, second.UserID} {
		if _, err := store.GetUserByID(existing); err != nil {
			t.Fatalf("existing user %s removed at capacity: %v", existing, err)
		}
	}

	// Administrative creation and a zero limit are not capped.
	if err := store.CreateUser(third); err != nil {
		t.Fatalf("CLI creation beyond the public limit: %v", err)
	}
	unlimited := UserRecord{UserID: uuid.NewString(), Username: "frank", PasswordHash: "hash", CreatedAt: now}
	if err := store.CreateUserWithinLimits(unlimited, nil, UserLimits{}); err != nil {
		t.Fatalf("zero limit: %v", err)
	}
}

func TestConcurrentRegistrationsRespectCapacity(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	limits := UserLimits{MaxUsers: 3}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.CreateUserWithinLimits(UserRecord{
				UserID: uuid.NewString(), Username: fmt.Sprintf("user%d", i),
				PasswordHash: "hash", CreatedAt: time.Now().UTC(),
			}, nil, limits)
		}()
	}
	wg.Wait()
	close(errs)
	created := 0
	for err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, ErrUserCapacity):
			t.Errorf("registration failed: %v", err)
		}
	}
	users, err := store.GetAllUsers()
	if err != nil {
		t.Fatal(err)
	}
	if created != limits.MaxUsers || len(users) != limits.MaxUsers {
		t.Fatalf("created %d, stored %d; want %d", created, len(users), limits.MaxUsers)
	}
}

func TestUserLookupsAndUpdates(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	alice := UserRecord{
		UserID: uuid.NewString(), Username: "Alice", Email: "Alice@Example.com",
		PasswordHash: "hash", CreatedAt: now,
	}
	bob := UserRecord{UserID: uuid.NewString(), Username: "bob", PasswordHash: "hash", CreatedAt: now}
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
}

func TestExpiredSessionsAreDeleted(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	userID := uuid.NewString()
	if err := store.CreateUser(UserRecord{
		UserID: userID, Username: "user", PasswordHash: "hash", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	oldSession := uuid.NewString()
	if err := store.CreateSession(SessionRecord{
		SessionID: oldSession, UserID: userID,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if valid, err := store.IsSessionValidForUser(oldSession, userID); err != nil || valid {
		t.Fatalf("expired session valid = %v, %v", valid, err)
	}
	if deleted, err := store.DeleteExpiredSessions(); err != nil || deleted != 1 {
		t.Fatalf("expired sessions deleted = %d, %v; want 1", deleted, err)
	}
	if _, err := store.GetUserByID(userID); err != nil {
		t.Fatalf("session cleanup removed the account: %v", err)
	}
	if valid, err := store.IsSessionValidForUser("not-a-uuid", userID); err != nil || valid {
		t.Fatalf("malformed session valid = %v, %v", valid, err)
	}
}

func TestAnonymousGamesArePurgedAfterInactivity(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	cutoff := now.Add(-24 * time.Hour)
	old := now.Add(-48 * time.Hour)
	userID := uuid.NewString()

	newGame := func(started time.Time, claimedBy string, result string, ended *time.Time) string {
		t.Helper()
		record := GameRecord{
			GameID: uuid.NewString(), InitialFEN: "initial",
			WhitePlayerID: uuid.NewString(), WhiteType: 1, WhiteClaimedBy: claimedBy,
			BlackPlayerID: uuid.NewString(), BlackType: 2, BlackLevel: 5, BlackSearchTime: 100,
			StartTimeUTC: started, Result: result, EndTimeUTC: ended,
		}
		if err := store.RecordNewGame(record); err != nil {
			t.Fatal(err)
		}
		return record.GameID
	}
	move := func(gameID string, number int, at time.Time) {
		t.Helper()
		color := "w"
		if number%2 == 0 {
			color = "b"
		}
		if err := store.RecordMove(MovePersistence{Move: MoveRecord{
			GameID: gameID, MoveNumber: number, MoveUCI: "e2e4",
			FENAfterMove: "after", PlayerColor: color, MoveTimeUTC: at,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	idle := newGame(old, "", "", nil)
	move(idle, 1, old.Add(time.Minute))
	recentMove := newGame(old, "", "", nil)
	move(recentMove, 1, now.Add(-time.Hour))
	recentEnd := newGame(old, "", "stalemate", timePointer(now.Add(-time.Hour)))
	recentStart := newGame(now.Add(-time.Hour), "", "", nil)
	live := newGame(old, "", "", nil)
	claimed := newGame(old, userID, "", nil)
	idleFinished := newGame(old, "", "white_wins", timePointer(old.Add(time.Hour)))

	if err := store.DeleteAnonymousGames(cutoff, []string{live}); err != nil {
		t.Fatal(err)
	}
	for gameID, wantKept := range map[string]bool{
		idle: false, idleFinished: false,
		recentMove: true, recentEnd: true, recentStart: true, live: true, claimed: true,
	} {
		_, moves, err := store.GetGameHistory(gameID)
		switch {
		case wantKept && err != nil:
			t.Errorf("game %s deleted: %v", gameID, err)
		case !wantKept && !IsGameNotFound(err):
			t.Errorf("game %s kept (err=%v, moves=%d)", gameID, err, len(moves))
		}
	}
	var orphanMoves int
	if err := store.db.QueryRow(`SELECT count(*) FROM moves WHERE game_id = $1`, idle).Scan(&orphanMoves); err != nil || orphanMoves != 0 {
		t.Fatalf("moves of deleted game = %d, %v", orphanMoves, err)
	}
	if err := store.DeleteAnonymousGames(cutoff, nil); err != nil {
		t.Fatalf("nil live set: %v", err)
	}
	if _, _, err := store.GetGameHistory(live); !IsGameNotFound(err) {
		t.Fatalf("idle game kept after leaving memory: %v", err)
	}
}

func TestClaimsSnapshotPlayerNames(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	alice, bob := uuid.NewString(), uuid.NewString()
	for id, name := range map[string]string{alice: "alice", bob: "bob"} {
		if err := store.CreateUser(UserRecord{UserID: id, Username: name, PasswordHash: "hash", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	gameID := uuid.NewString()
	if err := store.RecordNewGame(GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: alice, WhiteType: 1, WhiteClaimedBy: alice,
		BlackPlayerID: uuid.NewString(), BlackType: 1,
		StartTimeUTC: now,
	}); err != nil {
		t.Fatal(err)
	}
	for number, claim := range map[int]string{1: "", 2: bob} {
		color := map[int]string{1: "w", 2: "b"}[number]
		persistence := MovePersistence{Move: MoveRecord{
			GameID: gameID, MoveNumber: number, MoveUCI: "e2e4",
			FENAfterMove: "after", PlayerColor: color, MoveTimeUTC: now,
		}}
		if claim != "" {
			persistence.ClaimColor, persistence.ClaimedBy = color, claim
		}
		if err := store.RecordMove(persistence); err != nil {
			t.Fatal(err)
		}
	}
	record, _, err := store.GetGameHistory(gameID)
	if err != nil || record.WhiteName != "alice" || record.BlackName != "bob" {
		t.Fatalf("names = %q/%q, %v", record.WhiteName, record.BlackName, err)
	}

	// The snapshot survives a rename and account deletion.
	if err := store.UpdateUserUsername(bob, "robert"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(alice); err != nil {
		t.Fatal(err)
	}
	record, _, err = store.GetGameHistory(gameID)
	if err != nil || record.WhiteName != "alice" || record.BlackName != "bob" || record.WhiteClaimedBy != alice {
		t.Fatalf("names after rename/delete = %q/%q claim=%q, %v",
			record.WhiteName, record.BlackName, record.WhiteClaimedBy, err)
	}

	// A claimant without an account (removed before the write) has no name.
	orphan := uuid.NewString()
	if err := store.RecordNewGame(GameRecord{
		GameID: orphan, InitialFEN: "initial",
		WhitePlayerID: uuid.NewString(), WhiteType: 1, WhiteClaimedBy: uuid.NewString(),
		BlackPlayerID: uuid.NewString(), BlackType: 2,
		StartTimeUTC: now,
	}); err != nil {
		t.Fatal(err)
	}
	if record, _, err := store.GetGameHistory(orphan); err != nil || record.WhiteName != "" {
		t.Fatalf("orphan claim name = %q, %v", record.WhiteName, err)
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
