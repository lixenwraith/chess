package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/storage/pgtest"

	"github.com/google/uuid"
)

// archiveTestGame is a finished one-move game with the archive row given.
func archiveTestGame(white, black, eco string, start time.Time, hash byte) ArchiveGame {
	id := uuid.NewString()
	sum := make([]byte, 32)
	sum[0] = hash
	return ArchiveGame{
		Record: GameRecord{
			GameID: id, InitialFEN: "initial", WhitePlayerID: uuid.NewString(), WhiteType: 1, WhiteName: white,
			BlackPlayerID: uuid.NewString(), BlackType: 1, BlackName: black,
			Result: "white_wins", Termination: "resignation", StartTimeUTC: start, EndTimeUTC: &start,
		},
		Moves: []MoveRecord{{
			GameID: id, MoveNumber: 1, MoveUCI: "e2e4", FENAfterMove: "after", PlayerColor: "w", MoveTimeUTC: start,
		}},
		Hash: sum, WhiteKey: white, BlackKey: black, WhiteElo: 2500, ECO: eco,
	}
}

func text(value string) *string { return &value }

// TestArchiveListsPublishedCollectionsByTheirFilters: a collection is listed
// once it is published, which needs its rights; the list filters by
// collection, opening prefix and a player's name prefix on either side, pages
// by cursor, and gives the featured games in their order.
func TestArchiveListsPublishedCollectionsByTheirFilters(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	ctx := t.Context()
	day := func(n int) time.Time { return time.Date(1972, 7, n, 0, 0, 0, 0, time.UTC) }
	yes := true

	if created, err := store.SetCollection(ctx, "match", CollectionChange{Title: text("The match")}); err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	if _, err := store.SetCollection(ctx, "match", CollectionChange{Published: &yes}); !errors.Is(err, ErrCollectionRights) {
		t.Fatalf("publish without rights: %v", err)
	}
	first := archiveTestGame("spassky", "fischer", "E56", day(11), 1)
	second := archiveTestGame("fischer", "spassky", "B04", day(13), 2)
	third := archiveTestGame("spassky", "fischer", "B05", day(16), 3)
	if err := store.ImportArchiveGames(ctx, "match", []ArchiveGame{first, second, third}); err != nil {
		t.Fatal(err)
	}
	held, err := store.ArchivedHashes(ctx, [][]byte{first.Hash, make([]byte, 32)})
	if err != nil || len(held) != 1 || !held[string(first.Hash)] {
		t.Fatalf("held = %v, %v", held, err)
	}

	list := func(q ArchiveQuery) []string {
		t.Helper()
		if q.Limit == 0 {
			q.Limit = 10
		}
		games, err := store.QueryArchive(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(games))
		for i, game := range games {
			ids[i] = game.GameID
			if game.Archive == nil || game.Archive.Collection.Name != "match" || game.Archive.WhiteElo != 2500 ||
				game.MoveCount != 1 || game.FinalFEN != "after" || game.Origin != OriginArchive {
				t.Errorf("row = %+v, archive %+v", game, game.Archive)
			}
		}
		return ids
	}
	same := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d games, want %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: game %d is %s, want %s", name, i, got[i], want[i])
			}
		}
	}

	same("unpublished", list(ArchiveQuery{}))
	if _, err := store.SetCollection(ctx, "match", CollectionChange{
		SourceURL: text("https://example.org/match"), Licence: text("Public domain"),
		Attribution: text("An example"), Published: &yes,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetCollection(ctx, "drafts", CollectionChange{Title: text("Drafts")}); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportArchiveGames(ctx, "drafts", []ArchiveGame{archiveTestGame("x", "y", "A00", day(20), 4)}); err != nil {
		t.Fatal(err)
	}

	same("newest first", list(ArchiveQuery{}), third.Record.GameID, second.Record.GameID, first.Record.GameID)
	same("collection", list(ArchiveQuery{Collection: "drafts"}))
	same("opening prefix", list(ArchiveQuery{ECO: "B0"}), third.Record.GameID, second.Record.GameID)
	same("player on either side", list(ArchiveQuery{Player: "fis"}),
		third.Record.GameID, second.Record.GameID, first.Record.GameID)
	same("no such player", list(ArchiveQuery{Player: "fisz"}))
	same("after the first", list(ArchiveQuery{After: &GameCursor{StartTimeUTC: day(16), GameID: third.Record.GameID}}),
		second.Record.GameID, first.Record.GameID)
	for order, game := range []ArchiveGame{first, third} {
		if err := store.FeatureArchiveGame(ctx, game.Record.GameID, order+1); err != nil {
			t.Fatal(err)
		}
	}
	same("featured", list(ArchiveQuery{Featured: true}), first.Record.GameID, third.Record.GameID)

	collections, err := store.Collections(ctx, true)
	if err != nil || len(collections) != 1 || collections[0].Name != "match" || collections[0].Games != 3 {
		t.Fatalf("published collections = %+v, %v", collections, err)
	}
	record, _, err := store.GetGameHistory(ctx, first.Record.GameID)
	if err != nil || record.Archive == nil || record.Archive.Collection.Attribution != "An example" || record.Archive.ECO != "E56" {
		t.Fatalf("history archive = %+v, %v", record, err)
	}
}

// TestArchiveGamesOutliveThePurgeAndGoWithTheirCollection: no account holds an
// archive game, yet the anonymous purge passes it by; deleting its collection
// removes it.
func TestArchiveGamesOutliveThePurgeAndGoWithTheirCollection(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	ctx := t.Context()
	if _, err := store.SetCollection(ctx, "old", CollectionChange{Title: text("Old games")}); err != nil {
		t.Fatal(err)
	}
	game := archiveTestGame("a", "b", "", time.Date(1858, 1, 1, 0, 0, 0, 0, time.UTC), 9)
	if err := store.ImportArchiveGames(ctx, "old", []ArchiveGame{game}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAnonymousGames(time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetGameHistory(ctx, game.Record.GameID); err != nil {
		t.Fatalf("purged: %v", err)
	}
	if games, err := store.DeleteCollection(ctx, "old"); err != nil || games != 1 {
		t.Fatalf("delete collection: %d, %v", games, err)
	}
	if _, _, err := store.GetGameHistory(ctx, game.Record.GameID); !IsGameNotFound(err) {
		t.Errorf("game kept after its collection: %v", err)
	}
	if _, err := store.DeleteCollection(ctx, "old"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("deleting it again: %v", err)
	}
}

// TestMigrationV7MarksImportsAndKeepsEveryGame upgrades a version 6 database
// that holds a played game, an import and an import its account disowned.
func TestMigrationV7MarksImportsAndKeepsEveryGame(t *testing.T) {
	store, err := NewStore(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, migration := range migrations[:6] {
		if _, err := store.db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO schema_version (version) VALUES (6)`); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, row := range []struct {
		importer, event any
		origin          string
	}{
		{nil, nil, OriginPlayed}, {uuid.NewString(), nil, OriginImported}, {nil, "Some open", OriginImported},
	} {
		id := uuid.NewString()
		want[id] = row.origin
		if _, err := store.db.Exec(`INSERT INTO games (game_id, initial_fen, white_player_id, white_type,
			black_player_id, black_type, imported_by, event) VALUES ($1, 'initial', $2, 1, $3, 1, $4, $5)`,
			id, uuid.NewString(), uuid.NewString(), row.importer, row.event); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	games, err := store.QueryGames(t.Context(), "", "")
	if err != nil || len(games) != len(want) {
		t.Fatalf("%d games, %v", len(games), err)
	}
	for _, game := range games {
		if game.Origin != want[game.GameID] {
			t.Errorf("origin %q, want %q (importer %q, event %q)", game.Origin, want[game.GameID], game.ImportedBy, game.Event)
		}
	}
}
