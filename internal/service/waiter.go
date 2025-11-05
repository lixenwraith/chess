// FILE: internal/service/waiter.go
package service

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// WaitTimeout is the maximum time a client can wait for notifications
	WaitTimeout = 25 * time.Second

	// WaitChannelBuffer size for notification channels
	WaitChannelBuffer = 1
)

// WaitRegistry manages long-polling clients waiting for game state changes
type WaitRegistry struct {
	mu       sync.RWMutex
	waiters  map[string][]*WaitRequest // gameID → waiting clients
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// WaitRequest represents a single client waiting for game updates
type WaitRequest struct {
	MoveCount int             // Last known move count
	Notify    chan struct{}   // Buffered channel for notifications
	Timer     *time.Timer     // Timeout timer
	Context   context.Context // Client connection context
	GameID    string          // Game being watched
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
	defer w.mu.Unlock()

	// Create wait request
	req := &WaitRequest{
		MoveCount: moveCount,
		Notify:    make(chan struct{}, WaitChannelBuffer),
		Context:   ctx,
		GameID:    gameID,
	}

	// Setup timeout timer
	req.Timer = time.AfterFunc(WaitTimeout, func() {
		w.handleTimeout(req)
	})

	// Add to waiters map
	w.waiters[gameID] = append(w.waiters[gameID], req)

	// Setup cleanup on context cancellation
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		select {
		case <-ctx.Done():
			// Client disconnected
			w.removeWaiter(gameID, req)
		case <-req.Notify:
			// Notification received
			req.Timer.Stop()
			w.removeWaiter(gameID, req)
		case <-w.shutdown:
			// Server shutting down
			req.Timer.Stop()
			close(req.Notify)
		}
	}()

	return req.Notify
}

// NotifyGame notifies all clients waiting on a game about state change
func (w *WaitRegistry) NotifyGame(gameID string, currentMoveCount int) {
	w.mu.RLock()
	waitList := w.waiters[gameID]
	w.mu.RUnlock()

	if len(waitList) == 0 {
		return
	}

	// Non-blocking notification to all waiters
	for _, req := range waitList {
		// Only notify if move count changed
		if req.MoveCount != currentMoveCount {
			select {
			case req.Notify <- struct{}{}:
				// Notification sent
			default:
				// Channel full or closed, skip slow client
			}
		}
	}
}

// RemoveGame removes all waiters for a game (called before game deletion)
func (w *WaitRegistry) RemoveGame(gameID string) {
	w.mu.Lock()
	waitList := w.waiters[gameID]
	delete(w.waiters, gameID)
	w.mu.Unlock()

	// Notify all waiters that game is gone
	for _, req := range waitList {
		select {
		case req.Notify <- struct{}{}:
		default:
		}
	}
}

// Shutdown gracefully shuts down the wait registry
func (w *WaitRegistry) Shutdown(timeout time.Duration) error {
	close(w.shutdown)

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

// handleTimeout handles wait request timeout
func (w *WaitRegistry) handleTimeout(req *WaitRequest) {
	// Send timeout notification
	select {
	case req.Notify <- struct{}{}:
		// Timeout notification sent
	default:
		// Channel full or closed
	}
}

// removeWaiter removes a specific waiter from the registry
func (w *WaitRegistry) removeWaiter(gameID string, req *WaitRequest) {
	w.mu.Lock()
	defer w.mu.Unlock()

	waitList := w.waiters[gameID]
	for i, waiter := range waitList {
		if waiter == req {
			// Remove from slice
			w.waiters[gameID] = append(waitList[:i], waitList[i+1:]...)
			break
		}
	}

	// Clean up empty entries
	if len(w.waiters[gameID]) == 0 {
		delete(w.waiters, gameID)
	}

	// Stop timer if still running
	req.Timer.Stop()
}