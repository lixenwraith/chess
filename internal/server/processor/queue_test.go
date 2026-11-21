package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/service"

	"github.com/google/uuid"
)

func TestEngineQueueShutdownCancelsAndWaitsForCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &EngineQueue{
		tasks:   make(chan EngineTask, 1),
		workers: 1,
		ctx:     ctx,
		cancel:  cancel,
	}
	player := &core.Player{Type: core.PlayerComputer, SearchTime: 10_000}
	var callbackCalled atomic.Bool
	if err := queue.SubmitAsync("game-1", "fen", core.ColorWhite, player, func(EngineResult) {
		callbackCalled.Store(true)
	}); err != nil {
		t.Fatal(err)
	}

	if err := queue.Shutdown(time.Second); err != nil {
		t.Fatal(err)
	}
	if callbackCalled.Load() {
		t.Fatal("shutdown callback mutated live state after cancellation")
	}
	if err := queue.Submit(EngineTask{}); err == nil {
		t.Fatal("submit succeeded after shutdown")
	}
	if err := queue.Shutdown(time.Second); err != nil {
		t.Fatalf("second shutdown was not idempotent: %v", err)
	}
}

func TestFullEngineQueueRefusesComputerMoveAndLeavesGameOngoing(t *testing.T) {
	svc, err := service.New(nil, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &Processor{svc: svc, queue: &EngineQueue{tasks: make(chan EngineTask), ctx: ctx, cancel: cancel}}

	gameID := uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerComputer, Level: 1, SearchTime: 100}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	fen := "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
	if err := svc.CreateGame(gameID, white, black, fen, core.ColorWhite, core.StateOngoing, core.TermNone); err != nil {
		t.Fatal(err)
	}

	resp := p.Execute(t.Context(), NewMakeMoveCommand(gameID, core.MoveRequest{Move: "cccc"}))
	if resp.Success || resp.Error.Code != core.ErrResourceLimit {
		t.Fatalf("full queue answered %+v, want RESOURCE_LIMIT", resp.Error)
	}
	if view, err := svc.GetGameView(gameID); err != nil || view.State != core.StateOngoing {
		t.Fatalf("refused computer move left state %v (%v), want ongoing", view.State, err)
	}
}
