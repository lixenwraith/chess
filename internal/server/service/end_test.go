package service

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"

	"github.com/google/uuid"
)

// newHumanGame creates a human-vs-human game at the standard start.
func newHumanGame(t *testing.T, svc *Service) string {
	t.Helper()
	gameID := uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(gameID, white, black, chess.StartFEN, core.ColorWhite,
		core.StateOngoing, core.TermNone); err != nil {
		t.Fatal(err)
	}
	return gameID
}

// move commits one legal move for the side to move.
func move(t *testing.T, svc *Service, gameID, actor, uci string) {
	t.Helper()
	view, err := svc.GetGameView(gameID)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := chess.ParseFEN(view.FEN)
	if err != nil {
		t.Fatal(err)
	}
	m, err := pos.ParseUCI(uci)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyMoveWithState(gameID, MoveCommit{
		ExpectedFEN: view.FEN, ExpectedState: core.StateOngoing, ExpectedTurn: view.NextTurnColor,
		ActorUserID: actor, MoveUCI: uci, NewFEN: pos.Play(m).FEN(), State: core.StateOngoing,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResignationClaimsAndPersists(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID, alice, bob := newHumanGame(t, svc), uuid.NewString(), uuid.NewString()
	move(t, svc, gameID, alice, "e2e4") // alice claims white

	view, _ := svc.GetGameView(gameID)
	resign := EndCommit{
		ExpectedFEN: view.FEN, Color: core.ColorWhite, ActorUserID: bob,
		State: core.StateBlackWins, Termination: core.TermResignation,
	}
	if err := svc.EndGame(gameID, resign); !errors.Is(err, ErrSlotOwner) {
		t.Fatalf("resigning another user's side: %v, want ErrSlotOwner", err)
	}
	stale := resign
	stale.ActorUserID, stale.ExpectedFEN = alice, chess.StartFEN
	if err := svc.EndGame(gameID, stale); !errors.Is(err, ErrGameChanged) {
		t.Fatalf("stale position: %v, want ErrGameChanged", err)
	}
	wrong := resign
	wrong.ActorUserID, wrong.Termination = alice, core.TermAgreement
	if err := svc.EndGame(gameID, wrong); err == nil {
		t.Fatal("a win by agreement was accepted")
	}

	// Black resigns anonymously; an authenticated resignation would claim it.
	resign.Color, resign.ActorUserID, resign.State = core.ColorBlack, bob, core.StateWhiteWins
	if err := svc.EndGame(gameID, resign); err != nil {
		t.Fatal(err)
	}
	if err := svc.EndGame(gameID, resign); !errors.Is(err, ErrGameOver) {
		t.Fatalf("second resignation: %v, want ErrGameOver", err)
	}
	view, _ = svc.GetGameView(gameID)
	if view.State != core.StateWhiteWins || view.Termination != core.TermResignation ||
		view.BlackPlayer.ClaimedBy != bob || view.EndTimeUTC == nil {
		t.Fatalf("view after resignation: %+v", view)
	}
	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Result != "white_wins" || history.Termination != "resignation" ||
		history.Players.Black.ClaimedBy != bob || history.PGNResult != "1-0" {
		t.Fatalf("history after resignation: %+v", history)
	}

	// Between two humans a resignation is final: neither the other claimant
	// nor an anonymous caller can take it back, and the players cannot be
	// reconfigured around it.
	for _, actor := range []string{alice, bob, ""} {
		if err := svc.UndoMoves(gameID, 1, actor); !errors.Is(err, ErrSlotOwner) {
			t.Fatalf("undo by %q after a two-player resignation: %v, want ErrSlotOwner", actor, err)
		}
	}
	computer := core.NewPlayer(core.PlayerConfig{Type: core.PlayerComputer, Level: 1, SearchTime: 100}, core.ColorBlack)
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	if err := svc.UpdatePlayers(gameID, white, computer, alice); !errors.Is(err, ErrSlotOwner) {
		t.Fatalf("reconfiguring around a resignation: %v, want ErrSlotOwner", err)
	}
	if history.Concession == nil || history.Concession.Result != "white_wins" ||
		history.Concession.Termination != "resignation" || history.Concession.Ply != 1 {
		t.Fatalf("concession not recorded: %+v", history.Concession)
	}
}

// A single controller (hotseat or anonymous) is refused only by finality.
func TestConcessionBetweenHumansIsFinal(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID := newHumanGame(t, svc)
	move(t, svc, gameID, "", "e2e4")
	view, _ := svc.GetGameView(gameID)
	if err := svc.OfferDraw(gameID, core.ColorBlack, "", view.FEN); err != nil {
		t.Fatal(err)
	}
	if err := svc.EndGame(gameID, EndCommit{
		ExpectedFEN: view.FEN, Color: core.ColorWhite, State: core.StateDraw,
		Termination: core.TermAgreement, OfferFrom: core.ColorBlack,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UndoMoves(gameID, 1, ""); !errors.Is(err, ErrConcessionFinal) {
		t.Fatalf("undo after an agreed draw between humans: %v, want ErrConcessionFinal", err)
	}
}

// Against the computer an undo reopens a resigned game; the first
// concession stays on record through the rewind, later moves, and a second
// resignation, while result and termination follow the live game.
func TestConcessionAgainstComputerStaysOnRecord(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID, alice := uuid.NewString(), uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerComputer, Level: 1, SearchTime: 100}, core.ColorBlack)
	if err := svc.CreateGame(gameID, white, black, chess.StartFEN, core.ColorWhite,
		core.StateOngoing, core.TermNone); err != nil {
		t.Fatal(err)
	}
	move(t, svc, gameID, alice, "e2e4")
	move(t, svc, gameID, "", "e7e5")
	resign := func() {
		t.Helper()
		view, _ := svc.GetGameView(gameID)
		if err := svc.EndGame(gameID, EndCommit{
			ExpectedFEN: view.FEN, Color: core.ColorWhite, ActorUserID: alice,
			State: core.StateBlackWins, Termination: core.TermResignation,
		}); err != nil {
			t.Fatal(err)
		}
	}
	resign()

	if err := svc.UndoMoves(gameID, 2, ""); !errors.Is(err, ErrSlotOwner) {
		t.Fatalf("anonymous undo of a claimed game: %v, want ErrSlotOwner", err)
	}
	if err := svc.UndoMoves(gameID, 2, alice); err != nil {
		t.Fatal(err)
	}
	view, _ := svc.GetGameView(gameID)
	if view.State != core.StateOngoing || view.Concession == nil ||
		view.Concession.State != core.StateBlackWins || view.Concession.Ply != 2 {
		t.Fatalf("view after undoing a resignation: %+v", view)
	}
	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Result != "" || history.Termination != "" || len(history.Moves) != 0 ||
		history.Concession == nil || history.Concession.Result != "black_wins" ||
		history.Concession.Termination != "resignation" || history.Concession.Ply != 2 {
		t.Fatalf("history after undoing a resignation: %+v", history)
	}

	move(t, svc, gameID, alice, "d2d4")
	resign()
	history, _ = svc.GetGameHistory(gameID)
	if history.Result != "black_wins" || len(history.Moves) != 1 || history.Concession.Ply != 2 {
		t.Fatalf("second resignation replaced the first concession: %+v", history)
	}
}

func TestDrawOffersBetweenHumans(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID := newHumanGame(t, svc)
	move(t, svc, gameID, "", "e2e4")

	view, _ := svc.GetGameView(gameID)
	if err := svc.OfferDraw(gameID, core.ColorBlack, "", view.FEN); err != nil {
		t.Fatal(err)
	}
	if err := svc.OfferDraw(gameID, core.ColorBlack, "", view.FEN); err != nil {
		t.Fatalf("repeating a standing offer: %v", err)
	}
	if view, _ = svc.GetGameView(gameID); view.DrawOffer != core.ColorBlack {
		t.Fatalf("offer not recorded: %+v", view)
	}
	if err := svc.DeclineDraw(gameID, core.ColorBlack, ""); !errors.Is(err, ErrNoDrawOffer) {
		t.Fatalf("declining one's own offer: %v", err)
	}
	if err := svc.DeclineDraw(gameID, core.ColorWhite, ""); err != nil {
		t.Fatal(err)
	}
	// One offer per move: black must move before offering again.
	if err := svc.OfferDraw(gameID, core.ColorBlack, "", view.FEN); !errors.Is(err, ErrOfferLimit) {
		t.Fatalf("second offer in one move: %v, want ErrOfferLimit", err)
	}
	move(t, svc, gameID, "", "e7e5")
	move(t, svc, gameID, "", "g1f3")
	view, _ = svc.GetGameView(gameID)
	if err := svc.OfferDraw(gameID, core.ColorBlack, "", view.FEN); err != nil {
		t.Fatalf("offer after a move: %v", err)
	}
	// The offerer's own move keeps the offer standing ...
	move(t, svc, gameID, "", "b8c6")
	if view, _ = svc.GetGameView(gameID); view.DrawOffer != core.ColorBlack {
		t.Fatalf("offer lapsed on the offerer's own move: %v", view.DrawOffer)
	}
	// ... and the recipient's move instead of an answer declines it.
	move(t, svc, gameID, "", "f1c4")
	if view, _ = svc.GetGameView(gameID); view.DrawOffer != 0 {
		t.Fatalf("offer survived the recipient's move: %v", view.DrawOffer)
	}

	if err := svc.OfferDraw(gameID, core.ColorWhite, "", view.FEN); err != nil {
		t.Fatal(err)
	}
	view, _ = svc.GetGameView(gameID)
	accept := EndCommit{
		ExpectedFEN: view.FEN, Color: core.ColorBlack,
		State: core.StateDraw, Termination: core.TermAgreement, OfferFrom: core.ColorWhite,
	}
	if err := svc.EndGame(gameID, accept); err != nil {
		t.Fatal(err)
	}
	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Result != "draw" || history.Termination != "agreement" || history.PGNResult != "1/2-1/2" {
		t.Fatalf("history after agreement: %+v", history)
	}
}

func TestOfferToMissingOfferIsRejected(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID := newHumanGame(t, svc)
	view, _ := svc.GetGameView(gameID)
	err := svc.EndGame(gameID, EndCommit{
		ExpectedFEN: view.FEN, Color: core.ColorBlack,
		State: core.StateDraw, Termination: core.TermAgreement, OfferFrom: core.ColorWhite,
	})
	if !errors.Is(err, ErrNoDrawOffer) {
		t.Fatalf("accepting without an offer: %v, want ErrNoDrawOffer", err)
	}
}

func TestGameWithDeletedRowIsUnloadedNotDegraded(t *testing.T) {
	svc, dsn := newPersistentTestServiceDSN(t)
	gameID := newHumanGame(t, svc)
	if _, err := svc.GetGameHistory(gameID); err != nil { // flushes the insert
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM games WHERE game_id = $1`, gameID); err != nil {
		t.Fatal(err)
	}

	move(t, svc, gameID, "", "e2e4") // accepted in memory; its write finds no row
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := svc.GetGameView(gameID); errors.Is(err, ErrGameNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("game with a deleted row is still loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if health := svc.GetStorageHealth(); health != "ok" {
		t.Fatalf("storage health %q after a write for a deleted game", health)
	}
}

func TestFinishedGamesDoNotCountAgainstComputerLimit(t *testing.T) {
	svc := newPersistentTestService(t)
	newComputerGame := func() (string, error) {
		gameID := uuid.NewString()
		white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
		black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerComputer, Level: 1, SearchTime: 100}, core.ColorBlack)
		return gameID, svc.CreateGame(gameID, white, black, chess.StartFEN, core.ColorWhite,
			core.StateOngoing, core.TermNone)
	}
	var games []string
	for i := 0; i < MaxComputerGames; i++ {
		gameID, err := newComputerGame()
		if err != nil {
			t.Fatal(err)
		}
		games = append(games, gameID)
	}
	if _, err := newComputerGame(); err == nil {
		t.Fatal("computer game limit not enforced")
	}
	// Resigning one frees its slot while the game stays loaded.
	if err := svc.EndGame(games[0], EndCommit{
		ExpectedFEN: chess.StartFEN, Color: core.ColorWhite,
		State: core.StateBlackWins, Termination: core.TermResignation,
	}); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetComputerGameCount(); got != MaxComputerGames-1 {
		t.Fatalf("active computer games = %d, want %d", got, MaxComputerGames-1)
	}
	if _, err := newComputerGame(); err != nil {
		t.Fatalf("finished game still counted: %v", err)
	}
}
