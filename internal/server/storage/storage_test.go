package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/storage/pgtest"

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
		Result: "white_wins", Termination: "checkmate", EndTimeUTC: &ended,
	}); err != nil {
		t.Fatal(err)
	}

	// GetGameHistory must observe both queued writes without sleeps or polling.
	record, moves, err := store.GetGameHistory(t.Context(), gameID)
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

	owned, err := store.QueryGamesForUser(t.Context(), user, UserGamesQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].MoveCount != 1 || owned[0].FinalFEN != "after-e2e4" {
		t.Fatalf("claimed game lookup = %+v", owned)
	}

	if err := store.RewindGame(gameID, 0); err != nil {
		t.Fatal(err)
	}
	record, moves, err = store.GetGameHistory(t.Context(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Result != "" || record.EndTimeUTC != nil || len(moves) != 0 {
		t.Fatalf("rewind left stale replay data: game=%+v moves=%+v", record, moves)
	}
	owned, err = store.QueryGamesForUser(t.Context(), user, UserGamesQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].MoveCount != 0 || owned[0].FinalFEN != "initial" {
		t.Fatalf("rewound summary = %+v", owned)
	}

	if _, _, err := store.GetGameHistory(t.Context(), "not-a-uuid"); !IsGameNotFound(err) {
		t.Fatalf("malformed ID error = %v, want not found", err)
	}
	if _, _, err := store.GetGameHistory(t.Context(), uuid.NewString()); !IsGameNotFound(err) {
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
			record.Result, record.Termination, record.EndTimeUTC = "white_wins", "resignation", &end
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
		page, err := store.QueryGamesForUser(t.Context(), user, UserGamesQuery{Limit: 2, Offset: offset})
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
	if games, err := store.QueryGamesForUser(t.Context(), "not-a-uuid", UserGamesQuery{Limit: 10}); err != nil || len(games) != 0 {
		t.Fatalf("malformed user ID = %v, %v; want empty", games, err)
	}

	// Keyset pages match offset pages, and a game inserted at the head
	// between pages does not shift the next page.
	got = nil
	var after *GameCursor
	for page := 0; ; page++ {
		games, err := store.QueryGamesForUser(t.Context(), user, UserGamesQuery{Limit: 2, After: after})
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
		games, err := store.QueryGamesForUser(t.Context(), user, q)
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
		if _, err := store.QueryGamesForUser(t.Context(), user, bad); err == nil {
			t.Errorf("QueryGamesForUser(%+v) succeeded", bad)
		}
	}

	prefix := want[0][:8]
	if id, err := store.ResolveGameID(t.Context(), prefix); err != nil || id != want[0] {
		t.Errorf("ResolveGameID(%s) = %q, %v; want %s", prefix, id, err, want[0])
	}
	if _, err := store.ResolveGameID(t.Context(), "0000000"); err == nil {
		t.Error("ResolveGameID accepted a 7-digit prefix")
	}
	if _, err := store.ResolveGameID(t.Context(), "ffffffff-ffff"); !errors.Is(err, sql.ErrNoRows) {
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
				WHERE g.white_claimed_by IS NULL AND g.black_claimed_by IS NULL AND g.imported_by IS NULL
				AND g.origin <> 'archive' AND g.start_time_utc < $1`,
			args: []any{time.Now()},
			want: []string{"games_anonymous_start_idx"},
		},
		{
			name: "keyset page of an archive collection",
			query: `SELECT a.game_id FROM archive_games a
				WHERE a.collection = $1 AND (a.start_time_utc, a.game_id) < ($2, $3)
				ORDER BY a.start_time_utc DESC, a.game_id DESC LIMIT 50`,
			args: []any{"classics", time.Now(), userID},
			want: []string{"archive_games_collection_idx"},
		},
		{
			name: "archive player prefix on either side",
			query: `SELECT a.game_id FROM archive_games a
				WHERE a.white_key >= $1 AND a.white_key < $2 OR a.black_key >= $1 AND a.black_key < $2`,
			args: []any{"fis", prefixEnd("fis")},
			want: []string{"archive_games_white_key_idx", "archive_games_black_key_idx"},
		},
		{
			name:  "archive opening prefix",
			query: `SELECT a.game_id FROM archive_games a WHERE a.eco >= $1 AND a.eco < $2`,
			args:  []any{"B9", prefixEnd("B9")},
			want:  []string{"archive_games_eco_idx"},
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
	if _, err := store.GetUserByID(t.Context(), userID); err != nil {
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

// TestRecoverReadmitsWritesAndNamesTheGamesThatLostThem: the failed write,
// the one skipped behind it and the one refused at the queue are what the
// resync is handed, and writes are stored again once it readmits them.
func TestRecoverReadmitsWritesAndNamesTheGamesThatLostThem(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	failed, skipped, refused := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store.handleWrite(writeRequest{operation: "forced_failure", gameID: failed,
		run: func(context.Context, *sql.Tx) error { return errors.New("forced failure") }})
	store.handleWrite(writeRequest{operation: "queued_behind", gameID: skipped,
		run: func(context.Context, *sql.Tx) error { return nil }})
	if err := store.RewindGame(refused, 0); !errors.Is(err, ErrStorageDegraded) {
		t.Fatalf("write while degraded: %v, want ErrStorageDegraded", err)
	}

	// A resync that readmits nothing leaves storage degraded and the games lost.
	if err := store.Recover(t.Context(), func(func() []string) {}); !errors.Is(err, ErrStorageDegraded) || store.IsHealthy() {
		t.Fatalf("recover without readmission: %v, healthy %v", err, store.IsHealthy())
	}
	var lost []string
	if err := store.Recover(t.Context(), func(readmit func() []string) { lost = readmit() }); err != nil {
		t.Fatal(err)
	}
	slices.Sort(lost)
	want := []string{failed, skipped, refused}
	slices.Sort(want)
	if !slices.Equal(lost, want) || !store.IsHealthy() {
		t.Fatalf("lost games = %v, want %v; healthy %v", lost, want, store.IsHealthy())
	}

	gameID := uuid.NewString()
	if err := store.RecordNewGame(GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: uuid.NewString(), WhiteType: 1,
		BlackPlayerID: uuid.NewString(), BlackType: 1,
		StartTimeUTC: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetGameHistory(t.Context(), gameID); err != nil {
		t.Fatalf("write after recovery: %v", err)
	}
	if err := store.Recover(t.Context(), func(readmit func() []string) { t.Error("healthy storage was resynced") }); err != nil {
		t.Fatal(err)
	}
}

// TestSyncGameStoresTheGameWhole: the row and the line become what memory
// holds, over a stored line that diverged; the first concession and the
// claimant's name stay, and a game with no row gets one.
func TestSyncGameStoresTheGameWhole(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC().Truncate(time.Microsecond)
	alice := uuid.NewString()
	if err := store.CreateUser(t.Context(), UserRecord{UserID: alice, Username: "alice", PasswordHash: "hash", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	gameID, black := uuid.NewString(), uuid.NewString()
	stored := GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: alice, WhiteType: 1, WhiteClaimedBy: alice,
		BlackPlayerID: black, BlackType: 2, BlackLevel: 3, BlackSearchTime: 100,
		StartTimeUTC: now,
	}
	if err := store.RecordNewGame(stored); err != nil {
		t.Fatal(err)
	}
	for i, uci := range []string{"e2e4", "e7e5", "g1f3"} {
		if err := store.RecordMove(MovePersistence{Move: MoveRecord{
			GameID: gameID, MoveNumber: i + 1, MoveUCI: uci, FENAfterMove: "stored" + uci,
			PlayerColor: "wb"[i%2 : i%2+1], MoveTimeUTC: now,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// White resigns at ply 3 and an undo plays on: the concession is on record.
	if err := store.RecordGameEnd(GameEnd{
		GameID: gameID, Result: "black_wins", Termination: "resignation", EndTimeUTC: now, Ply: 3,
	}); err != nil {
		t.Fatal(err)
	}
	// The rename follows the queued writes that took the name.
	if err := errors.Join(store.Flush(t.Context()), store.UpdateUserUsername(t.Context(), alice, "alicia")); err != nil {
		t.Fatal(err)
	}

	ended := now.Add(time.Minute)
	memory := stored
	memory.BlackLevel, memory.Result, memory.Termination, memory.EndTimeUTC = 7, "draw", "agreement", &ended
	memory.ConcessionResult, memory.ConcessionTermination, memory.ConcessionPly, memory.ConcessionTimeUTC = "draw", "agreement", 2, &ended
	line := []MoveRecord{
		{GameID: gameID, MoveNumber: 1, MoveUCI: "e2e4", FENAfterMove: "storede2e4", PlayerColor: "w", MoveTimeUTC: now},
		{GameID: gameID, MoveNumber: 2, MoveUCI: "c7c5", FENAfterMove: "memoryc7c5", PlayerColor: "b", MoveTimeUTC: ended},
	}
	fresh := GameRecord{
		GameID: uuid.NewString(), InitialFEN: "initial",
		WhitePlayerID: alice, WhiteType: 1, WhiteClaimedBy: alice,
		BlackPlayerID: uuid.NewString(), BlackType: 1, StartTimeUTC: now,
	}
	if err := errors.Join(store.SyncGame(memory, line), store.SyncGame(fresh, nil)); err != nil {
		t.Fatal(err)
	}

	record, moves, err := store.GetGameHistory(t.Context(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 || moves[1] != line[1] || moves[0] != line[0] {
		t.Errorf("line = %+v, want %+v", moves, line)
	}
	if record.Result != "draw" || record.Termination != "agreement" || !record.EndTimeUTC.Equal(ended) ||
		record.BlackLevel != 7 || record.WhiteClaimedBy != alice || record.WhiteName != "alice" {
		t.Errorf("row = %+v", record)
	}
	if record.ConcessionResult != "black_wins" || record.ConcessionTermination != "resignation" || record.ConcessionPly != 3 {
		t.Errorf("first concession overwritten: %+v", record)
	}
	if record, moves, err := store.GetGameHistory(t.Context(), fresh.GameID); err != nil || len(moves) != 0 ||
		record.WhiteName != "alicia" || record.Result != "" {
		t.Errorf("game without a row: %+v, %v", record, err)
	}
}

// TestRefusedWholeGameIsDroppedWithoutDegrading: a row the database will
// never take costs its game alone.
func TestRefusedWholeGameIsDroppedWithoutDegrading(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	dropped := make(chan string, 1)
	store.SetGameDroppedHandler(func(gameID string, _ error) { dropped <- gameID })
	gameID := uuid.NewString()
	if err := store.SyncGame(GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: uuid.NewString(), WhiteType: 1,
		BlackPlayerID: uuid.NewString(), BlackType: 2, BlackLevel: 99, // games_black_level_check
		StartTimeUTC: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(t.Context()); err != nil || !store.IsHealthy() {
		t.Fatalf("a refused whole game degraded storage: %v", err)
	}
	select {
	case id := <-dropped:
		if id != gameID {
			t.Fatalf("handler got %s, want %s", id, gameID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dropped game was not reported")
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
	if _, _, err := store.GetGameHistory(t.Context(), gameID); err != nil {
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
	if err := store.CreateUser(t.Context(), UserRecord{
		UserID: userID, Username: "user1", PasswordHash: "hash", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(t.Context(), SessionRecord{
		SessionID: sessionID, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByID(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.LastLoginAt == nil || !user.LastLoginAt.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("session creation did not record login time: %v", user.LastLoginAt)
	}
	if err := store.DeleteUser(t.Context(), userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(t.Context(), sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session survived user cascade: %v", err)
	}
	if err := store.DeleteUser(t.Context(), userID); !errors.Is(err, sql.ErrNoRows) {
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
	if err := store.CreateUserWithinLimits(t.Context(), first, &firstSession, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(t.Context(), firstSession.SessionID); err != nil {
		t.Fatalf("initial session not committed with user: %v", err)
	}

	// A session failure rolls back the account.
	conflicting := UserRecord{UserID: uuid.NewString(), Username: "carol", PasswordHash: "hash", CreatedAt: now}
	conflictingSession := SessionRecord{
		SessionID: firstSession.SessionID, UserID: conflicting.UserID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateUserWithinLimits(t.Context(), conflicting, &conflictingSession, limits); err == nil {
		t.Fatal("expected duplicate session failure")
	}
	if _, err := store.GetUserByID(t.Context(), conflicting.UserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session failure did not roll back user: %v", err)
	}

	second := UserRecord{UserID: uuid.NewString(), Username: "bob", PasswordHash: "hash", CreatedAt: now}
	if err := store.CreateUserWithinLimits(t.Context(), second, nil, limits); err != nil {
		t.Fatal(err)
	}

	// At capacity, a duplicate is still reported as a duplicate, a new name
	// is refused, and nobody is removed to make room.
	for _, duplicate := range []UserRecord{
		{Username: "ALICE", Email: "other@example.com"},
		{Username: "dave", Email: "alice@EXAMPLE.com"},
	} {
		duplicate.UserID, duplicate.PasswordHash, duplicate.CreatedAt = uuid.NewString(), "hash", now
		if err := store.CreateUserWithinLimits(t.Context(), duplicate, nil, limits); !errors.Is(err, ErrUserAlreadyExists) {
			t.Fatalf("duplicate %+v error = %v, want ErrUserAlreadyExists", duplicate, err)
		}
	}
	third := UserRecord{UserID: uuid.NewString(), Username: "erin", PasswordHash: "hash", CreatedAt: now}
	if err := store.CreateUserWithinLimits(t.Context(), third, nil, limits); !errors.Is(err, ErrUserCapacity) {
		t.Fatalf("registration at capacity = %v, want ErrUserCapacity", err)
	}
	for _, existing := range []string{first.UserID, second.UserID} {
		if _, err := store.GetUserByID(t.Context(), existing); err != nil {
			t.Fatalf("existing user %s removed at capacity: %v", existing, err)
		}
	}

	// Administrative creation and a zero limit are not capped.
	if err := store.CreateUser(t.Context(), third); err != nil {
		t.Fatalf("CLI creation beyond the public limit: %v", err)
	}
	unlimited := UserRecord{UserID: uuid.NewString(), Username: "frank", PasswordHash: "hash", CreatedAt: now}
	if err := store.CreateUserWithinLimits(t.Context(), unlimited, nil, UserLimits{}); err != nil {
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
			errs <- store.CreateUserWithinLimits(t.Context(), UserRecord{
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
	users, err := store.GetAllUsers(t.Context())
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
		if err := store.CreateUser(t.Context(), user); err != nil {
			t.Fatal(err)
		}
	}

	byName, err := store.GetUserByUsername(t.Context(), "ALICE")
	if err != nil || byName.UserID != alice.UserID || byName.Username != "alice" || byName.Email != "alice@example.com" {
		t.Fatalf("username lookup = %+v, %v", byName, err)
	}
	if byEmail, err := store.GetUserByEmail(t.Context(), "ALICE@example.COM"); err != nil || byEmail.UserID != alice.UserID {
		t.Fatalf("email lookup = %+v, %v", byEmail, err)
	}
	if byID, err := store.GetUserByID(t.Context(), bob.UserID); err != nil || byID.Email != "" {
		t.Fatalf("user without email = %+v, %v", byID, err)
	}
	if _, err := store.GetUserByEmail(t.Context(), ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty email lookup = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.GetUserByID(t.Context(), "not-a-uuid"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("malformed ID lookup = %v, want sql.ErrNoRows", err)
	}

	if err := store.UpdateUserUsername(t.Context(), bob.UserID, "ALICE"); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("conflicting rename = %v, want ErrUserAlreadyExists", err)
	}
	if err := store.UpdateUserEmail(t.Context(), bob.UserID, "alice@example.com"); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("conflicting email = %v, want ErrUserAlreadyExists", err)
	}
	if err := store.UpdateUserEmail(t.Context(), alice.UserID, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEmail(t.Context(), bob.UserID, "alice@example.com"); err != nil {
		t.Fatalf("released email not reusable: %v", err)
	}
	if err := store.UpdateUserPassword(t.Context(), uuid.NewString(), "hash"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("update of missing user = %v, want sql.ErrNoRows", err)
	}
}

func TestExpiredSessionsAreDeleted(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	userID := uuid.NewString()
	if err := store.CreateUser(t.Context(), UserRecord{
		UserID: userID, Username: "user", PasswordHash: "hash", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	oldSession := uuid.NewString()
	if err := store.CreateSession(t.Context(), SessionRecord{
		SessionID: oldSession, UserID: userID,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionUser(t.Context(), oldSession, userID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired session: %v, want sql.ErrNoRows", err)
	}
	if deleted, err := store.DeleteExpiredSessions(t.Context()); err != nil || deleted != 1 {
		t.Fatalf("expired sessions deleted = %d, %v; want 1", deleted, err)
	}
	if _, err := store.GetUserByID(t.Context(), userID); err != nil {
		t.Fatalf("session cleanup removed the account: %v", err)
	}
	if _, err := store.SessionUser(t.Context(), "not-a-uuid", userID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("malformed session: %v, want sql.ErrNoRows", err)
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
		switch result {
		case "stalemate":
			record.Termination = "stalemate"
		case "white_wins", "black_wins":
			record.Termination = "checkmate"
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
		_, moves, err := store.GetGameHistory(t.Context(), gameID)
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
	if _, _, err := store.GetGameHistory(t.Context(), live); !IsGameNotFound(err) {
		t.Fatalf("idle game kept after leaving memory: %v", err)
	}
}

func TestClaimsSnapshotPlayerNames(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	alice, bob := uuid.NewString(), uuid.NewString()
	for id, name := range map[string]string{alice: "alice", bob: "bob"} {
		if err := store.CreateUser(t.Context(), UserRecord{UserID: id, Username: name, PasswordHash: "hash", CreatedAt: now}); err != nil {
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
	record, _, err := store.GetGameHistory(t.Context(), gameID)
	if err != nil || record.WhiteName != "alice" || record.BlackName != "bob" {
		t.Fatalf("names = %q/%q, %v", record.WhiteName, record.BlackName, err)
	}

	// The snapshot survives a rename and account deletion.
	if err := store.UpdateUserUsername(t.Context(), bob, "robert"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(t.Context(), alice); err != nil {
		t.Fatal(err)
	}
	record, _, err = store.GetGameHistory(t.Context(), gameID)
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
	if record, _, err := store.GetGameHistory(t.Context(), orphan); err != nil || record.WhiteName != "" {
		t.Fatalf("orphan claim name = %q, %v", record.WhiteName, err)
	}
}

func TestDeleteAccountClearsClaimsAndNames(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	now := time.Now().UTC()
	alice, bob := uuid.NewString(), uuid.NewString()
	for id, name := range map[string]string{alice: "alice", bob: "bob"} {
		if err := store.CreateUser(t.Context(), UserRecord{UserID: id, Username: name, PasswordHash: "hash", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateSession(t.Context(), SessionRecord{
		SessionID: uuid.NewString(), UserID: alice, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Alice against Bob, and Alice alone against the computer.
	shared, solo := uuid.NewString(), uuid.NewString()
	for _, record := range []GameRecord{
		{GameID: shared, InitialFEN: "initial",
			WhitePlayerID: alice, WhiteType: 1, WhiteClaimedBy: alice,
			BlackPlayerID: bob, BlackType: 1, BlackClaimedBy: bob, StartTimeUTC: now},
		{GameID: solo, InitialFEN: "initial",
			WhitePlayerID: uuid.NewString(), WhiteType: 2,
			BlackPlayerID: alice, BlackType: 1, BlackClaimedBy: alice, StartTimeUTC: now},
	} {
		if err := store.RecordNewGame(record); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteAccount(t.Context(), alice); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetUserByID(t.Context(), alice); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("account still present: %v", err)
	}
	var sessions int
	if err := store.db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id = $1`, alice).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions left: %d, %v", sessions, err)
	}

	record, _, err := store.GetGameHistory(t.Context(), shared)
	if err != nil || record.WhiteClaimedBy != "" || record.WhiteName != "" ||
		record.BlackClaimedBy != bob || record.BlackName != "bob" {
		t.Fatalf("shared game after deletion: white %q/%q black %q/%q, %v",
			record.WhiteClaimedBy, record.WhiteName, record.BlackClaimedBy, record.BlackName, err)
	}
	// Nobody claims the solo game now, so the inactivity purge takes it.
	if err := store.DeleteAnonymousGames(now.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetGameHistory(t.Context(), solo); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("solo game survived the anonymous purge: %v", err)
	}
	if _, _, err := store.GetGameHistory(t.Context(), shared); err != nil {
		t.Fatalf("a game another user claims must stay: %v", err)
	}

	if err := store.DeleteAccount(t.Context(), alice); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second deletion: %v, want sql.ErrNoRows", err)
	}
	if !store.IsHealthy() {
		t.Fatal("deleting a missing account degraded the writer")
	}
}

// TestCallerContextGatesQueuingNotAQueuedWrite: an ended caller queues no
// write, and one cancelled while its write waits still learns its outcome.
func TestCallerContextGatesQueuingNotAQueuedWrite(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	insert := func(ctx context.Context, tx *sql.Tx) error {
		id := uuid.NewString()
		_, err := tx.ExecContext(ctx, `INSERT INTO users (user_id, username, password_hash, created_at)
			VALUES ($1, $2, 'hash', now())`, id, "u"+id[:8])
		return err
	}
	users := func() (n int) {
		if err := store.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	ended, cancel := context.WithCancel(t.Context())
	cancel()
	for range 20 {
		if err := store.enqueueWait(ended, "ended", insert); !errors.Is(err, context.Canceled) {
			t.Fatalf("ended caller: %v", err)
		}
	}
	if err := store.Flush(t.Context()); err != nil || users() != 0 {
		t.Fatalf("an ended caller queued writes: %d users, %v", users(), err)
	}

	lock, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(`LOCK TABLE users IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	time.AfterFunc(300*time.Millisecond, func() { lock.Rollback() })
	if err := store.enqueueWait(ctx, "cancelled", insert); err != nil || users() != 1 {
		t.Fatalf("cancelled caller of a committed write: %v, %d users", err, users())
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

func TestMigrationV2BackfillsTermination(t *testing.T) {
	dsn := pgtest.DSN(t)
	store, err := NewStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A version 1 database with one game of each stored result.
	if _, err := store.db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO schema_version (version) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, result := range []string{"", "white_wins", "black_wins", "stalemate", "draw"} {
		id := uuid.NewString()
		ids[result] = id
		var end any
		if result != "" {
			end = time.Now()
		}
		if _, err := store.db.Exec(`INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type, result, end_time_utc) VALUES ($1, 'initial', $2, 1, $3, 1, $4, $5)`,
			id, uuid.NewString(), uuid.NewString(), nullableString(result), end); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	for result, want := range map[string]string{
		"": "", "white_wins": "checkmate", "black_wins": "checkmate", "stalemate": "stalemate", "draw": "agreement",
	} {
		var termination sql.NullString
		if err := store.db.QueryRow(`SELECT termination FROM games WHERE game_id = $1`, ids[result]).
			Scan(&termination); err != nil {
			t.Fatal(err)
		}
		if termination.String != want {
			t.Errorf("result %q: termination %q, want %q", result, termination.String, want)
		}
	}

	// The constraints reject a termination that cannot produce the result.
	if _, err := store.db.Exec(`UPDATE games SET termination = 'agreement' WHERE game_id = $1`,
		ids["white_wins"]); err == nil {
		t.Error("games_termination_check accepted a win by agreement")
	}
	if _, err := store.db.Exec(`UPDATE games SET termination = 'resignation' WHERE game_id = $1`,
		ids[""]); err == nil {
		t.Error("games_result_termination_check accepted a termination without a result")
	}
}

func TestMigrationV3BackfillsConcessions(t *testing.T) {
	dsn := pgtest.DSN(t)
	store, err := NewStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A version 2 database: a resignation after two moves, an agreed draw at
	// the start, and a checkmate.
	for _, migration := range migrations[:2] {
		if _, err := store.db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO schema_version (version) VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, row := range [][2]string{
		{"black_wins", "resignation"}, {"draw", "agreement"}, {"white_wins", "checkmate"},
	} {
		id := uuid.NewString()
		ids[row[1]] = id
		if _, err := store.db.Exec(`INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type, result, termination, end_time_utc)
			VALUES ($1, 'initial', $2, 1, $3, 2, $4, $5, now())`,
			id, uuid.NewString(), uuid.NewString(), row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for i, uci := range []string{"e2e4", "e7e5"} {
		if _, err := store.db.Exec(`INSERT INTO moves (game_id, move_number, move_uci, fen_after_move,
			player_color) VALUES ($1, $2, $3, 'fen', $4)`,
			ids["resignation"], i+1, uci, []string{"w", "b"}[i]); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	for termination, want := range map[string]GameRecord{
		"resignation": {ConcessionResult: "black_wins", ConcessionTermination: "resignation", ConcessionPly: 2},
		"agreement":   {ConcessionResult: "draw", ConcessionTermination: "agreement", ConcessionPly: 0},
		"checkmate":   {},
	} {
		records, err := store.QueryGames(t.Context(), ids[termination], "")
		if err != nil || len(records) != 1 {
			t.Fatalf("%s: %v, %d records", termination, err, len(records))
		}
		got := records[0]
		if got.ConcessionResult != want.ConcessionResult ||
			got.ConcessionTermination != want.ConcessionTermination ||
			got.ConcessionPly != want.ConcessionPly ||
			(want.ConcessionResult != "") != (got.ConcessionTimeUTC != nil) {
			t.Errorf("%s: concession %q %q %d %v", termination, got.ConcessionResult,
				got.ConcessionTermination, got.ConcessionPly, got.ConcessionTimeUTC)
		}
	}

	// Only a resignation or an agreed draw is a concession, and its columns
	// are set together.
	for name, query := range map[string]string{
		"checkmate concession": `UPDATE games SET concession_result = 'white_wins',
			concession_termination = 'checkmate', concession_ply = 1, concession_time_utc = now()
			WHERE game_id = $1`,
		"partial concession": `UPDATE games SET concession_result = 'draw' WHERE game_id = $1`,
	} {
		if _, err := store.db.Exec(query, ids["checkmate"]); err == nil {
			t.Errorf("games_concession_check accepted a %s", name)
		}
	}
}

func TestWriteForDeletedGameIsDroppedWithoutDegrading(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	missing := make(chan string, 4)
	store.SetGameDroppedHandler(func(gameID string, _ error) { missing <- gameID })

	gameID := uuid.NewString()
	if err := store.RecordNewGame(GameRecord{
		GameID: gameID, InitialFEN: "initial",
		WhitePlayerID: uuid.NewString(), WhiteType: 1,
		BlackPlayerID: uuid.NewString(), BlackType: 1,
		StartTimeUTC: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// An administrator deletes the game while the server still holds it.
	if _, err := store.db.Exec(`DELETE FROM games WHERE game_id = $1`, gameID); err != nil {
		t.Fatal(err)
	}

	ended := time.Now()
	writes := []func() error{
		func() error {
			return store.RecordMove(MovePersistence{Move: MoveRecord{
				GameID: gameID, MoveNumber: 1, MoveUCI: "e2e4", FENAfterMove: "after",
				PlayerColor: "w", MoveTimeUTC: ended,
			}})
		},
		func() error {
			return store.RecordGameEnd(GameEnd{
				GameID: gameID, Result: "black_wins", Termination: "resignation", EndTimeUTC: ended,
			})
		},
		func() error { return store.RewindGame(gameID, 0) },
	}
	for i, write := range writes {
		if err := write(); err != nil {
			t.Fatalf("write %d not queued: %v", i, err)
		}
		if err := store.Flush(context.Background()); err != nil {
			t.Fatalf("write %d degraded storage: %v", i, err)
		}
		select {
		case id := <-missing:
			if id != gameID {
				t.Fatalf("handler got %s, want %s", id, gameID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("write %d: missing-game handler not called", i)
		}
	}
	if !store.IsHealthy() {
		t.Fatal("storage degraded by writes for a deleted game")
	}

	// Other games are unaffected.
	other := uuid.NewString()
	if err := store.RecordNewGame(GameRecord{
		GameID: other, InitialFEN: "initial",
		WhitePlayerID: uuid.NewString(), WhiteType: 1,
		BlackPlayerID: uuid.NewString(), BlackType: 1,
		StartTimeUTC: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetGameHistory(t.Context(), other); err != nil {
		t.Fatalf("other game: %v", err)
	}
}
