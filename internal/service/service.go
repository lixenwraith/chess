// FILE: internal/service/service.go
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"chess/internal/game"
	"chess/internal/storage"
)

// Service is a pure state manager for chess games with optional persistence
type Service struct {
	games     map[string]*game.Game
	mu        sync.RWMutex
	store     *storage.Store // nil if persistence disabled
	jwtSecret []byte
	waiter    *WaitRegistry // Long-polling notification registry
}

// New creates a new service instance with optional storage
func New(store *storage.Store, jwtSecret []byte) *Service {
	return &Service{
		games:     make(map[string]*game.Game),
		store:     store,
		jwtSecret: jwtSecret,
		waiter:    NewWaitRegistry(),
	}
}

// GetStorageHealth returns the storage component status
func (s *Service) GetStorageHealth() string {
	if s.store == nil {
		return "disabled"
	}
	if s.store.IsHealthy() {
		return "ok"
	}
	return "degraded"
}

// RegisterWait registers a client to wait for game state changes
func (s *Service) RegisterWait(gameID string, moveCount int, ctx context.Context) <-chan struct{} {
	return s.waiter.RegisterWait(gameID, moveCount, ctx)
}

// Shutdown gracefully shuts down the service
func (s *Service) Shutdown(timeout time.Duration) error {
	// Collect all errors
	var errs []error

	// Shutdown wait registry
	if err := s.waiter.Shutdown(timeout); err != nil {
		errs = append(errs, fmt.Errorf("wait registry: %w", err))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear all games
	s.games = make(map[string]*game.Game)

	// Close storage if enabled
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("storage: %w", err))
		}
	}

	return errors.Join(errs...)
}