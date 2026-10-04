package service

import (
	"github.com/lixenwraith/chess/internal/server/core"
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// WaitTimeout is the maximum time a client can wait for notifications
	WaitTimeout = 30 * time.Second

	// WaitChannelBuffer size for notification channels
	WaitChannelBuffer = 1
)

// WaitRegistry manages clients waiting for game state changes via long-polling
type WaitRegistry struct {
	mu           sync.RWMutex
	waiters      map[string][]*WaitRequest // gameID → waiting clients
	shutdown     chan struct{}
	wg           sync.WaitGroup
	closed       bool
	shutdownOnce sync.Once
}

// WaitRequest represents a single client waiting for game updates
type WaitRequest struct {
	MoveCount int             // Last known move count
	Notify    chan struct{}   // Buffered channel for notifications
	Timer     *time.Timer     // Timeout timer
	Context   context.Context // Client connection context
	GameID    string          // Game being watched
	done      chan struct{}
	finish    sync.Once
}

// NewWaitRegistry creates a new wait registry
func NewWaitRegistry() *WaitRegistry {
	return &WaitRegistry{
		waiters:  make(map[string][]*WaitRequest),
		shutdown: make(chan struct{}),
	}
}

// RegisterWait registers a client to wait for game state changes
func (w *WaitRegistry) RegisterWait(gameID string, moveCount int, ctx context.Context) <-chan struct{} {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		notify := make(chan struct{})
		close(notify)
		return notify
	}

	// Create wait request
	req := &WaitRequest{
		MoveCount: moveCount,
		Notify:    make(chan struct{}, WaitChannelBuffer),
		Context:   ctx,
		GameID:    gameID,
		done:      make(chan struct{}),
	}

	// Setup timeout timer
	req.Timer = time.AfterFunc(WaitTimeout, func() {
		w.complete(req)
	})

	// Add to waiters map
	w.waiters[gameID] = append(w.waiters[gameID], req)

	// Setup cleanup on context cancellation
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		select {
		case <-ctx.Done():
			w.complete(req)
		case <-w.shutdown:
			w.complete(req)
		case <-req.done:
		}
	}()

	return req.Notify
}

// NotifyGame notifies all clients waiting on a game about state change
func (w *WaitRegistry) NotifyGame(gameID string, currentMoveCount int, state core.State) {
	w.mu.RLock()
	waitList := append([]*WaitRequest(nil), w.waiters[gameID]...)
	w.mu.RUnlock()
	if len(waitList) == 0 {
		return
	}
	settled := state != core.StateOngoing && state != core.StatePending
	for _, req := range waitList {
		if settled || req.MoveCount != currentMoveCount {
			w.complete(req)
		}
	}
}

// NotifyAll wakes every client waiting on a game, for changes that neither
// move nor settle it, such as a draw offer.
func (w *WaitRegistry) NotifyAll(gameID string) {
	w.RemoveGame(gameID)
}

// RemoveGame removes all waiters for a game (called before game deletion)
func (w *WaitRegistry) RemoveGame(gameID string) {
	w.mu.RLock()
	waitList := append([]*WaitRequest(nil), w.waiters[gameID]...)
	w.mu.RUnlock()

	// Notify all waiters that game is gone
	for _, req := range waitList {
		w.complete(req)
	}
}

// Shutdown gracefully shuts down the wait registry
func (w *WaitRegistry) Shutdown(timeout time.Duration) error {
	w.shutdownOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.shutdown)
		w.mu.Unlock()
	})

	// Wait for all goroutines with timeout
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("http wait registry shutdown failed")
	}
}

// complete removes a waiter and closes its notification channel exactly once.
// The registry never consumes Notify itself: the HTTP handler is its sole
// consumer, so a state-change signal cannot be lost to a cleanup goroutine.
func (w *WaitRegistry) complete(req *WaitRequest) {
	req.finish.Do(func() {
		req.Timer.Stop()

		w.mu.Lock()
		waitList := w.waiters[req.GameID]
		for i, waiter := range waitList {
			if waiter == req {
				w.waiters[req.GameID] = append(waitList[:i], waitList[i+1:]...)
				break
			}
		}
		if len(w.waiters[req.GameID]) == 0 {
			delete(w.waiters, req.GameID)
		}
		w.mu.Unlock()

		close(req.done)
		close(req.Notify)
	})
}
