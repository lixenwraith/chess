package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/storage"
)

const importPGN = `[Event "World Championship"]
[Site "Reykjavik"]
[Date "1972.07.11"]
[Round "1"]
[White "Spassky, Boris"]
[Black "Fischer, Robert"]
[Result "1-0"]

1. d4 Nf6 2. c4 e6 3. Nf3 d5 1-0
`

func TestImportGameStoresReplaysAndLists(t *testing.T) {
	svc := newPersistentTestService(t)
	user, _, err := svc.RegisterUser("alice", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}

	imported, err := svc.ImportGame(user.UserID, importPGN)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Plies != 6 || imported.Result != "white_wins" {
		t.Fatalf("imported = %+v", imported)
	}

	history, err := svc.GetGameHistory(imported.GameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Origin != "imported" || history.Event != "World Championship" || history.Site != "Reykjavik" ||
		history.Round != "1" || history.Players.White.Name != "Spassky, Boris" || history.Players.Black.Name != "Fischer, Robert" ||
		history.Termination != "resignation" || history.PGNResult != "1-0" ||
		history.StartTimeUTC.Format("2006-01-02") != "1972-07-11" {
		t.Fatalf("history = %+v", history)
	}
	var san []string
	for _, m := range history.Moves {
		san = append(san, m.SAN)
	}
	if strings.Join(san, " ") != "d4 Nf6 c4 e6 Nf3 d5" {
		t.Errorf("SAN = %v", san)
	}
	pgn, err := svc.GetGamePGN(imported.GameID, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`[Event "World Championship"]`, `[Site "Reykjavik"]`, `[Round "1"]`, `[White "Spassky, Boris"]`, "1. d4 Nf6 2. c4 e6 3. Nf3 d5 { White wins by resignation. } 1-0"} {
		if !strings.Contains(pgn.Text, want) {
			t.Errorf("PGN lacks %q:\n%s", want, pgn.Text)
		}
	}

	page, err := svc.GetUserGames(user.UserID, UserGamesOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Games) != 1 || page.Games[0].GameID != imported.GameID || page.Games[0].Origin != "imported" ||
		page.Games[0].Event != "World Championship" || page.Games[0].MoveCount != 6 {
		t.Fatalf("listing = %+v", page.Games)
	}
	// Imported games have no claimed colour.
	page, _ = svc.GetUserGames(user.UserID, UserGamesOptions{Limit: 10, Color: "white"})
	if len(page.Games) != 0 {
		t.Errorf("imported game listed under a colour filter")
	}

	// Outcomes from the final position.
	for name, tc := range map[string]struct{ pgn, result, termination string }{
		"mate":      {"1. f3 e5 2. g4 Qh4# 0-1", "black_wins", "checkmate"},
		"stalemate": {"[FEN \"7k/8/6K1/5Q2/8/8/8/8 w - - 0 1\"]\n[SetUp \"1\"]\n\n1. Qf7 1/2-1/2", "stalemate", "stalemate"},
		"agreed":    {"1. e4 e5 1/2-1/2", "draw", "agreement"},
		"ongoing":   {"1. e4 e5 *", "", ""},
		"material":  {"[FEN \"4k3/8/8/8/8/8/8/3RK3 w - - 0 1\"]\n[SetUp \"1\"]\n\n1. Rd8+ Kxd8 1/2-1/2", "draw", "insufficient_material"},
	} {
		got, err := svc.ImportGame(user.UserID, tc.pgn)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		h, _ := svc.GetGameHistory(got.GameID)
		if h.Result != tc.result || h.Termination != tc.termination {
			t.Errorf("%s: result %q %q, want %q %q", name, h.Result, h.Termination, tc.result, tc.termination)
		}
	}
	// Illegal moves and impossible results are refused.
	for name, bad := range map[string]string{
		"illegal":        "1. e4 e5 2. Ke2 Ke7 3. Kd3 Qh4",
		"mate but draw":  "1. f3 e5 2. g4 Qh4# 1/2-1/2",
		"mate but white": "1. f3 e5 2. g4 Qh4# 1-0",
		"over but star":  "1. f3 e5 2. g4 Qh4# *",
		"bad fen":        "[FEN \"nonsense\"]\n[SetUp \"1\"]\n\n1. e4 *",
		"not pgn":        "hello world",
	} {
		if _, err := svc.ImportGame(user.UserID, bad); !errors.Is(err, ErrInvalidPGN) {
			t.Errorf("%s: error = %v, want ErrInvalidPGN", name, err)
		}
	}
}

func TestRemoveGameDeletesOwnAndReleasesShared(t *testing.T) {
	svc := newPersistentTestService(t)
	alice, _, err := svc.RegisterUser("alice", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	bob, _, err := svc.RegisterUser("bob", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	human := core.PlayerConfig{Type: core.PlayerHuman}

	// A game alice alone claims: deleted outright and unloaded.
	own := playLine(t, svc, alice.UserID, human, []string{"e2e4"}, core.StateOngoing)
	outcome, err := svc.RemoveGame(own, alice.UserID)
	if err != nil || outcome != storage.RemovedGame {
		t.Fatalf("own game: %v %v", outcome, err)
	}
	if _, err := svc.GetGameView(own); err == nil {
		t.Error("deleted game still in memory")
	}
	if _, err := svc.GetGameHistory(own); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("deleted game history: %v", err)
	}

	// An imported game: deleted by its importer, unknown to anyone else.
	imported, err := svc.ImportGame(alice.UserID, "1. e4 e5 *")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveGame(imported.GameID, bob.UserID); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("bob removing alice's import: %v", err)
	}
	if outcome, err := svc.RemoveGame(imported.GameID, alice.UserID); err != nil || outcome != storage.RemovedGame {
		t.Errorf("alice removing her import: %v %v", outcome, err)
	}

	// A game both claim: alice's removal releases her side; bob keeps it.
	shared := playLine(t, svc, alice.UserID, human, []string{"e2e4"}, core.StateOngoing)
	if err := svc.ApplyMoveWithState(shared, MoveCommit{
		ExpectedFEN: "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1", ExpectedState: core.StateOngoing,
		ExpectedTurn: core.ColorBlack, ActorUserID: bob.UserID, MoveUCI: "e7e5",
		NewFEN: "rnbqkbnr/pppp1ppp/8/4p3/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 0 2", State: core.StateOngoing,
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err = svc.RemoveGame(shared, alice.UserID)
	if err != nil || outcome != storage.RemovedClaim {
		t.Fatalf("shared game: %v %v", outcome, err)
	}
	view, err := svc.GetGameView(shared)
	if err != nil || view.WhitePlayer.ClaimedBy != "" || view.BlackPlayer.ClaimedBy != bob.UserID {
		t.Errorf("shared game after release: %v %+v", err, view.WhitePlayer)
	}
	history, _ := svc.GetGameHistory(shared)
	if history.Players.White.ClaimedBy != "" || history.Players.White.Name != "" || history.Players.Black.Name != "bob" {
		t.Errorf("stored claims after release: %+v %+v", history.Players.White, history.Players.Black)
	}
	if page, _ := svc.GetUserGames(alice.UserID, UserGamesOptions{Limit: 10}); len(page.Games) != 0 {
		t.Errorf("alice still lists %d games", len(page.Games))
	}
	if page, _ := svc.GetUserGames(bob.UserID, UserGamesOptions{Limit: 10}); len(page.Games) != 1 {
		t.Errorf("bob lists %d games", len(page.Games))
	}
	if _, err := svc.RemoveGame(shared, alice.UserID); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("second removal: %v", err)
	}
}
