package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"chess/internal/server/core"
	"chess/internal/server/game"
	"chess/internal/server/storage"
)

func TestMoveCommitClaimsSlotPersistsResultAndRejectsStalePosition(t *testing.T) {
	svc := newPersistentTestService(t)
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		"game-1", white, black, "initial", core.ColorWhite, core.StateOngoing,
	); err != nil {
		t.Fatal(err)
	}

	ended := time.Date(2026, 9, 7, 2, 3, 4, 0, time.UTC)
	commit := MoveCommit{
		ExpectedFEN: "initial", ExpectedState: core.StateOngoing, ExpectedTurn: core.ColorWhite,
		ActorUserID: "user-1", MoveUCI: "e2e4", NewFEN: "after", State: core.StateWhiteWins, At: ended,
		Result: &game.MoveResult{Move: "e2e4", PlayerColor: core.ColorWhite, GameState: core.StateWhiteWins},
	}
	if err := svc.ApplyMoveWithState("game-1", commit); err != nil {
		t.Fatal(err)
	}

	view, err := svc.GetGameView("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.WhitePlayer.ClaimedBy != "user-1" || view.State != core.StateWhiteWins {
		t.Fatalf("in-memory move did not settle atomically: %+v", view)
	}
	history, err := svc.GetGameHistory("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if history.Players.White.ClaimedBy != "user-1" || history.Result != "white_wins" || len(history.Moves) != 1 {
		t.Fatalf("history did not settle atomically: %+v", history)
	}

	if err := svc.ApplyMoveWithState("game-1", commit); !errors.Is(err, ErrGameChanged) {
		t.Fatalf("stale move error = %v, want ErrGameChanged", err)
	}
}

func TestUndoClearsDurableTerminalResult(t *testing.T) {
	svc := newPersistentTestService(t)
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		"game-1", white, black, "initial", core.ColorWhite, core.StateOngoing,
	); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyMoveWithState("game-1", MoveCommit{
		ExpectedFEN: "initial", ExpectedState: core.StateOngoing, ExpectedTurn: core.ColorWhite,
		MoveUCI: "e2e4", NewFEN: "after", State: core.StateStalemate,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UndoMoves("game-1", 1); err != nil {
		t.Fatal(err)
	}

	history, err := svc.GetGameHistory("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if history.Result != "" || history.EndTimeUTC != nil || len(history.Moves) != 0 {
		t.Fatalf("undo left terminal persistence: %+v", history)
	}
}

func TestPlayerReconfigurationPreservesClaimAndPersistsConfiguration(t *testing.T) {
	svc := newPersistentTestService(t)
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	white.ID = "user-1"
	white.ClaimedBy = "user-1"
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		"game-1", white, black, "initial", core.ColorWhite, core.StateOngoing,
	); err != nil {
		t.Fatal(err)
	}

	replacementWhite := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	replacementBlack := core.NewPlayer(
		core.PlayerConfig{Type: core.PlayerComputer, Level: 12, SearchTime: 500}, core.ColorBlack,
	)
	if err := svc.UpdatePlayers("game-1", replacementWhite, replacementBlack); err != nil {
		t.Fatal(err)
	}

	history, err := svc.GetGameHistory("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if history.Players.White.ID != "user-1" || history.Players.White.ClaimedBy != "user-1" {
		t.Fatalf("white ownership was replaced: %+v", history.Players.White)
	}
	if history.Players.Black.Type != core.PlayerComputer || history.Players.Black.Level != 12 {
		t.Fatalf("black configuration was not persisted: %+v", history.Players.Black)
	}

	computerWhite := core.NewPlayer(
		core.PlayerConfig{Type: core.PlayerComputer, Level: 8, SearchTime: 300}, core.ColorWhite,
	)
	if err := svc.UpdatePlayers("game-1", computerWhite, replacementBlack); err != nil {
		t.Fatal(err)
	}
	history, err = svc.GetGameHistory("game-1")
	if err != nil {
		t.Fatal(err)
	}
	if history.Players.White.ClaimedBy != "user-1" {
		t.Fatalf("player-type change discarded historical ownership: %+v", history.Players.White)
	}
}

func TestCleanupEvictsOnlyMemoryCopyOfTerminalGame(t *testing.T) {
	svc := newPersistentTestService(t)
	svc.SetFinishedGameTTL(time.Nanosecond)
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(
		"game-1", white, black, "terminal", core.ColorWhite, core.StateStalemate,
	); err != nil {
		t.Fatal(err)
	}

	svc.cleanupFinishedGames(time.Now().UTC().Add(time.Second))
	if _, err := svc.GetGameView("game-1"); !errors.Is(err, ErrGameNotFound) {
		t.Fatalf("terminal game remains in memory: %v", err)
	}
	if history, err := svc.GetGameHistory("game-1"); err != nil || history.Result != "stalemate" {
		t.Fatalf("durable history was removed: history=%+v err=%v", history, err)
	}
}

func newPersistentTestService(t *testing.T) *Service {
	t.Helper()
	store, err := storage.NewStore(filepath.Join(t.TempDir(), "chess.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	svc := New(store, []byte("test-secret-test-secret-test-secret"))
	t.Cleanup(func() {
		if err := svc.Shutdown(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return svc
}
