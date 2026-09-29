package service

import (
	"errors"
	"testing"
	"time"

	"chess/internal/server/core"
	"chess/internal/server/game"
	"chess/internal/server/storage"
	"chess/internal/server/storage/pgtest"

	"github.com/google/uuid"
)

func TestMoveCommitClaimsSlotPersistsResultAndRejectsStalePosition(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID, userID := uuid.NewString(), uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		gameID, white, black, "initial", core.ColorWhite, core.StateOngoing, core.TermNone,
	); err != nil {
		t.Fatal(err)
	}

	ended := time.Date(2026, 9, 7, 2, 3, 4, 0, time.UTC)
	commit := MoveCommit{
		ExpectedFEN: "initial", ExpectedState: core.StateOngoing, ExpectedTurn: core.ColorWhite,
		ActorUserID: userID, MoveUCI: "e2e4", NewFEN: "after", State: core.StateWhiteWins, Termination: core.TermCheckmate, At: ended,
		Result: &game.MoveResult{Move: "e2e4", PlayerColor: core.ColorWhite, GameState: core.StateWhiteWins},
	}
	if err := svc.ApplyMoveWithState(gameID, commit); err != nil {
		t.Fatal(err)
	}

	view, err := svc.GetGameView(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if view.WhitePlayer.ClaimedBy != userID || view.State != core.StateWhiteWins {
		t.Fatalf("in-memory move did not settle atomically: %+v", view)
	}
	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Players.White.ClaimedBy != userID || history.Result != "white_wins" || len(history.Moves) != 1 {
		t.Fatalf("history did not settle atomically: %+v", history)
	}

	if err := svc.ApplyMoveWithState(gameID, commit); !errors.Is(err, ErrGameChanged) {
		t.Fatalf("stale move error = %v, want ErrGameChanged", err)
	}
}

func TestUndoClearsDurableTerminalResult(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID := uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		gameID, white, black, "initial", core.ColorWhite, core.StateOngoing, core.TermNone,
	); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyMoveWithState(gameID, MoveCommit{
		ExpectedFEN: "initial", ExpectedState: core.StateOngoing, ExpectedTurn: core.ColorWhite,
		MoveUCI: "e2e4", NewFEN: "after", State: core.StateStalemate, Termination: core.TermStalemate,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UndoMoves(gameID, 1, ""); err != nil {
		t.Fatal(err)
	}

	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Result != "" || history.EndTimeUTC != nil || len(history.Moves) != 0 {
		t.Fatalf("undo left terminal persistence: %+v", history)
	}
}

func TestPlayerReconfigurationPreservesClaimAndPersistsConfiguration(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID, userID := uuid.NewString(), uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	white.ID = userID
	white.ClaimedBy = userID
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		gameID, white, black, "initial", core.ColorWhite, core.StateOngoing, core.TermNone,
	); err != nil {
		t.Fatal(err)
	}

	replacementWhite := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	replacementBlack := core.NewPlayer(
		core.PlayerConfig{Type: core.PlayerComputer, Level: 12, SearchTime: 500}, core.ColorBlack,
	)
	if err := svc.UpdatePlayers(gameID, replacementWhite, replacementBlack, userID); err != nil {
		t.Fatal(err)
	}

	history, err := svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Players.White.ID != userID || history.Players.White.ClaimedBy != userID {
		t.Fatalf("white ownership was replaced: %+v", history.Players.White)
	}
	if history.Players.Black.Type != core.PlayerComputer || history.Players.Black.Level != 12 {
		t.Fatalf("black configuration was not persisted: %+v", history.Players.Black)
	}

	computerWhite := core.NewPlayer(
		core.PlayerConfig{Type: core.PlayerComputer, Level: 8, SearchTime: 300}, core.ColorWhite,
	)
	if err := svc.UpdatePlayers(gameID, computerWhite, replacementBlack, userID); err != nil {
		t.Fatal(err)
	}
	history, err = svc.GetGameHistory(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Players.White.ClaimedBy != userID {
		t.Fatalf("player-type change discarded historical ownership: %+v", history.Players.White)
	}
}

func TestCleanupEvictsOnlyMemoryCopyOfTerminalGame(t *testing.T) {
	svc := newPersistentTestService(t)
	gameID := uuid.NewString()
	svc.SetFinishedGameTTL(time.Nanosecond)
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		gameID, white, black, "terminal", core.ColorWhite, core.StateStalemate, core.TermStalemate,
	); err != nil {
		t.Fatal(err)
	}

	svc.cleanupGames(time.Now().UTC().Add(time.Second))
	if _, err := svc.GetGameView(gameID); !errors.Is(err, ErrGameNotFound) {
		t.Fatalf("terminal game remains in memory: %v", err)
	}
	if history, err := svc.GetGameHistory(gameID); err != nil || history.Result != "stalemate" {
		t.Fatalf("durable history was removed: history=%+v err=%v", history, err)
	}
}

func TestCleanupRemovesIdleAnonymousGamesButKeepsClaimedOnes(t *testing.T) {
	svc := newPersistentTestService(t)
	svc.SetFinishedGameTTL(0)
	user, _, err := svc.RegisterUser("alice", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}

	anonymous, claimed := uuid.NewString(), uuid.NewString()
	for gameID, claimant := range map[string]string{anonymous: "", claimed: user.UserID} {
		white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
		if claimant != "" {
			white.ID, white.ClaimedBy = claimant, claimant
		}
		black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
		if err := svc.CreateGame(gameID, white, black, "initial", core.ColorWhite, core.StateOngoing, core.TermNone); err != nil {
			t.Fatal(err)
		}
	}
	history, err := svc.GetGameHistory(claimed)
	if err != nil || history.Players.White.Name != "alice" {
		t.Fatalf("claimed history name = %+v, %v", history, err)
	}

	// Within the retention window both games stay loaded and stored.
	svc.cleanupGames(time.Now().UTC().Add(time.Hour))
	for _, gameID := range []string{anonymous, claimed} {
		if _, err := svc.GetGameView(gameID); err != nil {
			t.Fatalf("game %s evicted early: %v", gameID, err)
		}
	}

	// Past it, the anonymous game leaves memory and the database; the
	// registered user's game is untouched.
	svc.cleanupGames(time.Now().UTC().Add(AnonymousGameTTL + time.Hour))
	if _, err := svc.GetGameView(anonymous); !errors.Is(err, ErrGameNotFound) {
		t.Fatalf("idle anonymous game still loaded: %v", err)
	}
	if _, err := svc.GetGameHistory(anonymous); !errors.Is(err, ErrGameNotFound) {
		t.Fatalf("idle anonymous game still stored: %v", err)
	}
	if _, err := svc.GetGameView(claimed); err != nil {
		t.Fatalf("claimed game evicted: %v", err)
	}
	if _, err := svc.GetGameHistory(claimed); err != nil {
		t.Fatalf("claimed game deleted: %v", err)
	}
}

func newPersistentTestService(t *testing.T) *Service {
	t.Helper()
	svc, _ := newPersistentTestServiceDSN(t)
	return svc
}

// newPersistentTestServiceDSN also returns the DSN of the test schema, for
// tests that change rows behind the service's back.
func newPersistentTestServiceDSN(t *testing.T) (*Service, string) {
	t.Helper()
	dsn := pgtest.DSN(t)
	store, err := storage.NewStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	svc, err := New(store, testJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.Shutdown(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return svc, dsn
}
