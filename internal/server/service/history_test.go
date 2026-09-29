package service

import (
	"errors"
	"strings"
	"testing"

	"chess/internal/server/chess"
	"chess/internal/server/core"

	"github.com/google/uuid"
)

// playLine creates a game and commits uci as the players' moves, with FENs
// from the rules core and the given final state (reached by checkmate) on the
// last move.
func playLine(t *testing.T, svc *Service, userID string, black core.PlayerConfig, uci []string, final core.State) string {
	t.Helper()
	gameID := uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	blackPlayer := core.NewPlayer(black, core.ColorBlack)
	if err := svc.CreateGame(gameID, white, blackPlayer, chess.StartFEN, core.ColorWhite, core.StateOngoing, core.TermNone); err != nil {
		t.Fatal(err)
	}
	pos, err := chess.ParseFEN(chess.StartFEN)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range uci {
		m, err := pos.ParseUCI(text)
		if err != nil {
			t.Fatal(err)
		}
		next := pos.Play(m)
		state, termination := core.StateOngoing, core.TermNone
		if i == len(uci)-1 && final.IsTerminal() {
			state, termination = final, core.TermCheckmate
		}
		turn, actor := core.ColorWhite, userID
		if i%2 == 1 {
			turn, actor = core.ColorBlack, ""
		}
		if err := svc.ApplyMoveWithState(gameID, MoveCommit{
			ExpectedFEN: pos.FEN(), ExpectedState: core.StateOngoing, ExpectedTurn: turn,
			ActorUserID: actor, MoveUCI: text, NewFEN: next.FEN(), State: state, Termination: termination,
		}); err != nil {
			t.Fatal(err)
		}
		pos = next
	}
	return gameID
}

func TestHistoryNotationAndPGN(t *testing.T) {
	svc := newPersistentTestService(t)
	computer := core.PlayerConfig{Type: core.PlayerComputer, Level: 3, SearchTime: 100}
	gameID := playLine(t, svc, uuid.NewString(), computer,
		[]string{"f2f3", "e7e5", "g2g4", "d8h4"}, core.StateBlackWins)

	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	var san []string
	for _, m := range history.Moves {
		san = append(san, m.SAN)
	}
	if strings.Join(san, " ") != "f3 e5 g4 Qh4#" || history.PGNResult != "0-1" || history.Termination != "checkmate" {
		t.Fatalf("history notation = %v, %q, %q", san, history.PGNResult, history.Termination)
	}

	pgn, err := svc.GetGamePGN(gameID, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[Event \"Casual game\"]\n[Site \"?\"]\n", "[White \"Anonymous\"]\n", "[Black \"Stockfish level 3\"]\n",
		"[Result \"0-1\"]\n", "[GameId \"" + gameID + "\"]\n", "[BlackType \"program\"]\n", "[PlyCount \"4\"]\n",
		"[Termination \"normal\"]\n", "\n\n1. f3 e5 2. g4 Qh4# { Black wins by checkmate. } 0-1\n",
	} {
		if !strings.Contains(pgn.Text, want) {
			t.Errorf("PGN lacks %q:\n%s", want, pgn.Text)
		}
	}
	if !strings.HasPrefix(pgn.Filename, "chess-") || !strings.HasSuffix(pgn.Filename, "-"+gameID[:8]+".pgn") {
		t.Errorf("filename = %q", pgn.Filename)
	}

	partial, err := svc.GetGamePGN(gameID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(partial.Text, "\n\n1. f3 e5 *\n") || strings.Contains(partial.Text, "Termination") ||
		!strings.HasSuffix(partial.Filename, "-ply2.pgn") {
		t.Errorf("partial PGN %q:\n%s", partial.Filename, partial.Text)
	}
	if full, err := svc.GetGamePGN(gameID, 4); err != nil || full.Text != pgn.Text {
		t.Errorf("ply equal to the line length should export the whole game: %v", err)
	}
	if _, err := svc.GetGamePGN(gameID, 5); !errors.Is(err, ErrPlyOutOfRange) {
		t.Errorf("ply 5 error = %v, want ErrPlyOutOfRange", err)
	}
	if _, err := svc.GetGamePGN(uuid.NewString(), -1); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("unknown game error = %v, want ErrGameNotFound", err)
	}
}

func TestUserGamesCursorAndFilters(t *testing.T) {
	svc := newPersistentTestService(t)
	userID := uuid.NewString()
	human := core.PlayerConfig{Type: core.PlayerHuman}
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append([]string{playLine(t, svc, userID, human, []string{"e2e4"}, core.StateOngoing)}, ids...)
	}
	finished := playLine(t, svc, userID, human, []string{"f2f3", "e7e5", "g2g4", "d8h4"}, core.StateBlackWins)
	ids = append([]string{finished}, ids...)

	var got []string
	cursor := ""
	for {
		page, err := svc.GetUserGames(userID, UserGamesOptions{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if cursor != "" && page.NextOffset != nil {
			t.Error("cursor page reported nextOffset")
		}
		for _, g := range page.Games {
			got = append(got, g.GameID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Fatalf("cursor pages = %v, want %v", got, ids)
	}

	page, err := svc.GetUserGames(userID, UserGamesOptions{Limit: 10, Status: "finished", Color: "white"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Games) != 1 || page.Games[0].GameID != finished || page.Games[0].PGNResult != "0-1" {
		t.Fatalf("finished games = %+v", page.Games)
	}
	for _, bad := range []UserGamesOptions{
		{Limit: 1, Cursor: "!!"}, {Limit: 1, Cursor: "bm90LWEtY3Vyc29y"}, {Limit: 1, Offset: 1, Cursor: page.NextCursor + "x"},
		{Limit: 1, Color: "red"}, {Limit: 1, Status: "paused"},
	} {
		if _, err := svc.GetUserGames(userID, bad); !errors.Is(err, ErrInvalidListQuery) {
			t.Errorf("GetUserGames(%+v) error = %v, want ErrInvalidListQuery", bad, err)
		}
	}
}
