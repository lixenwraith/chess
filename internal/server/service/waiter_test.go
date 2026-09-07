package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"chess/internal/server/core"
)

func TestWaitRegistryDeliversNotificationToCallerAndRemovesWaiter(t *testing.T) {
	registry := NewWaitRegistry()
	notify := registry.RegisterWait("game-1", 0, context.Background())

	registry.NotifyGame("game-1", 1, core.StateOngoing)
	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("game update was consumed before reaching the caller")
	}
	select {
	case _, ok := <-notify:
		if ok {
			t.Fatal("completed notification channel returned another value")
		}
	default:
		t.Fatal("completed notification channel was not closed")
	}

	registry.mu.RLock()
	remaining := len(registry.waiters["game-1"])
	registry.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("completed waiter remains registered: %d", remaining)
	}
	if err := registry.Shutdown(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitRegistryCompletionAndShutdownAreConcurrentAndIdempotent(t *testing.T) {
	registry := NewWaitRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	notify := registry.RegisterWait("game-1", 0, ctx)

	var callers sync.WaitGroup
	callers.Add(3)
	go func() {
		defer callers.Done()
		registry.NotifyGame("game-1", 1, core.StateOngoing)
	}()
	go func() {
		defer callers.Done()
		registry.RemoveGame("game-1")
	}()
	go func() {
		defer callers.Done()
		cancel()
	}()
	callers.Wait()

	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("concurrent completion did not notify caller")
	}
	if err := registry.Shutdown(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := registry.Shutdown(time.Second); err != nil {
		t.Fatalf("second shutdown failed: %v", err)
	}

	afterShutdown := registry.RegisterWait("game-2", 0, context.Background())
	select {
	case <-afterShutdown:
	default:
		t.Fatal("registration after shutdown did not return a closed signal")
	}
}
