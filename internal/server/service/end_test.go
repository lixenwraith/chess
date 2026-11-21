package service

import (
	"database/sql"
	"errors"
	"slices"
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
	history, err := svc.GetGameHistory(t.Context(), gameID)
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
	history, err := svc.GetGameHistory(t.Context(), gameID)
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
	history, _ = svc.GetGameHistory(t.Context(), gameID)
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
	history, err := svc.GetGameHistory(t.Context(), gameID)
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
	if _, err := svc.GetGameHistory(t.Context(), gameID); err != nil { // flushes the insert
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

// TestDegradedStorageResyncsFromMemory: a write the database refuses degrades
// storage while play goes on in memory; recovery writes each game that
// changed whole, and its later moves are stored as before.
func TestDegradedStorageResyncsFromMemory(t *testing.T) {
	svc, dsn := newPersistentTestServiceDSN(t)
	ctx := t.Context()
	gameID := newHumanGame(t, svc)
	move(t, svc, gameID, "", "e2e4")
	if _, err := svc.GetGameHistory(ctx, gameID); err != nil { // flushes
		t.Fatal(err)
	}
	// A row in the way of the next ply: its insert fails for good.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO moves (game_id, move_number, move_uci, fen_after_move, player_color)
		VALUES ($1, 2, 'a7a6', 'in the way', 'b')`, gameID); err != nil {
		t.Fatal(err)
	}
	move(t, svc, gameID, "", "e7e5")
	for deadline := time.Now().Add(5 * time.Second); svc.GetStorageHealth() != "degraded"; {
		if time.Now().After(deadline) {
			t.Fatal("the refused write did not degrade storage")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Play goes on in memory, and so does a game that starts now.
	move(t, svc, gameID, "", "g1f3")
	started := newHumanGame(t, svc)
	move(t, svc, started, "", "d2d4")
	if _, err := svc.GetGameHistory(ctx, gameID); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("history while degraded: %v, want ErrStorageUnavailable", err)
	}
	svc.cleanupGames(time.Now().UTC().Add(30 * 24 * time.Hour))
	if _, err := svc.GetGameView(started); err != nil {
		t.Fatalf("cleanup unloaded a game whose moves are in memory alone: %v", err)
	}

	svc.recoverStorage(ctx)
	if health := svc.GetStorageHealth(); health != "ok" {
		t.Fatalf("storage health after recovery = %q", health)
	}
	for id, want := range map[string][]string{gameID: {"e2e4", "e7e5", "g1f3"}, started: {"d2d4"}} {
		history, err := svc.GetGameHistory(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var line []string
		for _, m := range history.Moves {
			line = append(line, m.MoveUCI)
		}
		if !slices.Equal(line, want) {
			t.Errorf("resynced line = %v, want %v", line, want)
		}
	}
	move(t, svc, gameID, "", "b8c6")
	if history, err := svc.GetGameHistory(ctx, gameID); err != nil || len(history.Moves) != 4 {
		t.Fatalf("move after recovery: %+v, %v", history, err)
	}
}
