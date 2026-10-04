package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/core"
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
