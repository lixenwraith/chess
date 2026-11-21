package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/lixenwraith/chess/internal/server/storage"
)

// archiveDatabase holds five games: two good ones, one the rules refuse, a
// third good one, and the second again without its rating and opening tags.
const archiveDatabase = `% a database
[Event "Paris Opera"]
[Site "Paris FRA"]
[Date "1858.??.??"]
[White "Morphy, Paul"]
[Black "Duke Karl / Count Isouard"]
[Result "1-0"]
[ECO "C41"]

1. e4 e5 2. Nf3 d6 3. d4 Bg4 {pinned
[%clk 0:10:00]} 4. dxe5 Bxf3 5. Qxf3 dxe5 6. Bc4 Nf6 7. Qb3 Qe7 8. Nc3 c6
9. Bg5 b5 10. Nxb5 cxb5 11. Bxb5+ Nbd7 12. O-O-O Rd8 13. Rxd7 Rxd7 14. Rd1 Qe6
15. Bxd7+ Nxd7 16. Qb8+ Nxb8 17. Rd8# 1-0

[Event "Casual"]
[Date "2001.03.04"]
[White "Novice, A"]
[Black "Beginner, B"]
[Result "1-0"]
[WhiteElo "1210"]
[BlackElo "?"]
[ECO "C20"]

1. e4 e5 2. Bc4 Nc6 3. Qh5 Nf6 4. Qxf7# 1-0
[Event "Illegal"]
[Result "*"]

1. e4 e5 2. Ke3 *

[Event "Repetition"]
[Date "2002.06.07"]
[White "Shuffler, C"]
[Black "novice, a"]
[Result "1/2-1/2"]

1. Nf3 Nf6 2. Ng1 Ng8 3. Nf3 Nf6 4. Ng1 Ng8 1/2-1/2

[Event "Casual"]
[Date "2001.03.04"]
[White "Novice, A"]
[Black "Beginner, B"]
[Result "1-0"]

1. e4 e5 2. Bc4 Nc6 3. Qh5 Nf6 4. Qxf7# 1-0
`

// TestImportArchiveStoresOnlyWhatIsNew: a dry run writes nothing, a repeated
// import stores nothing, and a game the rules refuse is skipped with its
// place. What is stored replays and exports as an archive game and never
// goes live.
func TestImportArchiveStoresOnlyWhatIsNew(t *testing.T) {
	svc := newPersistentTestService(t)
	ctx := t.Context()
	run := func(dryRun bool) ArchiveImport {
		t.Helper()
		done, err := svc.ImportArchive(ctx, "classics", strings.NewReader(archiveDatabase), dryRun)
		if err != nil {
			t.Fatal(err)
		}
		if done.Games != 5 || len(done.Rejected) != 1 || done.Rejected[0].Index != 3 ||
			!strings.Contains(done.Rejected[0].Reason, "Ke3") {
			t.Fatalf("read %+v", done)
		}
		return done
	}

	if _, err := svc.ImportArchive(ctx, "classics", strings.NewReader(archiveDatabase), false); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("no collection: %v", err)
	}
	title, source, yes := "Classics", "https://example.org/classics", true
	if _, err := svc.store.SetCollection(ctx, "classics", storage.CollectionChange{
		Title: &title, SourceURL: &source, Licence: &title, Attribution: &title, Published: &yes,
	}); err != nil {
		t.Fatal(err)
	}
	if dry := run(true); dry.Stored != 3 || dry.Duplicate != 1 {
		t.Errorf("dry run = %+v", dry)
	}
	if page, err := svc.GetArchive(ctx, ArchiveOptions{Limit: 10}); err != nil || len(page.Games) != 0 {
		t.Fatalf("a dry run stored %+v, %v", page, err)
	}
	if first := run(false); first.Stored != 3 || first.Duplicate != 1 {
		t.Errorf("first import = %+v", first)
	}
	if again := run(false); again.Stored != 0 || again.Duplicate != 4 {
		t.Errorf("second import = %+v", again)
	}

	page, err := svc.GetArchive(ctx, ArchiveOptions{Limit: 10, Player: "NOV", ECO: "c"})
	if err != nil || len(page.Games) != 1 || page.Games[0].Origin != "archive" ||
		page.Games[0].Archive.WhiteElo != 1210 || page.Games[0].Archive.BlackElo != 0 || page.Games[0].Archive.ECO != "C20" {
		t.Fatalf("filtered page = %+v, %v", page, err)
	}
	gameID := page.Games[0].GameID
	history, err := svc.GetGameHistory(ctx, gameID)
	if err != nil || history.Origin != "archive" || history.Archive.Title != "Classics" || len(history.Moves) != 7 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	pgn, err := svc.GetGamePGN(ctx, gameID, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{`[WhiteElo "1210"]`, `[ECO "C20"]`, `[Source "https://example.org/classics"]`, `[Attribution "Classics"]`} {
		if !strings.Contains(pgn.Text, tag) {
			t.Errorf("PGN lacks %s:\n%s", tag, pgn.Text)
		}
	}
	if err := svc.LoadGame(ctx, gameID); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("an archive game went live: %v", err)
	}
}
