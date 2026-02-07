package service

import (
	"chess/internal/server/core"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"chess/internal/server/game"
	"chess/internal/server/storage"
)

const (
	MaxComputerGames   = 10
	MaxUsers           = 100
	PermanentSlots     = 10
	TempUserTTL        = 24 * time.Hour
	SessionTTL         = 7 * 24 * time.Hour
	CleanupJobInterval = 1 * time.Hour
)

// Service coordinates game state, user management, and storage
type Service struct {
	games         map[string]*game.Game
	mu            sync.RWMutex
	store         *storage.Store
	jwtSecret     []byte
	waiter        *WaitRegistry
	computerGames atomic.Int32 // Active games with computer players
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

// CanCreateComputerGame checks if a new computer game can be created
func (s *Service) CanCreateComputerGame() bool {
	return s.computerGames.Load() < MaxComputerGames
}

// IncrementComputerGames increments the computer game counter
func (s *Service) IncrementComputerGames() {
	s.computerGames.Add(1)
}

// DecrementComputerGames decrements the computer game counter
func (s *Service) DecrementComputerGames() {
	s.computerGames.Add(-1)
}

// GetComputerGameCount returns current computer game count
func (s *Service) GetComputerGameCount() int32 {
	return s.computerGames.Load()
}

// ClaimGameSlot claims a player slot for a user
func (s *Service) ClaimGameSlot(gameID string, color core.Color, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	return g.ClaimSlot(color, userID)
}

// GetSlotOwner returns the user who claimed a slot
func (s *Service) GetSlotOwner(gameID string, color core.Color) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.games[gameID]
	if !ok {
		return "", fmt.Errorf("game not found: %s", gameID)
	}

	return g.GetSlotOwner(color), nil
}

// Shutdown gracefully shuts down the service
func (s *Service) Shutdown(timeout time.Duration) error {
	var errs []error

	if err := s.waiter.Shutdown(timeout); err != nil {
		errs = append(errs, fmt.Errorf("wait registry: %w", err))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.games = make(map[string]*game.Game)

	if s.store != nil {
		if err := s.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("storage: %w", err))
		}
	}

	return errors.Join(errs...)
}

// RunCleanupJob runs periodic cleanup of expired users and sessions
func (s *Service) RunCleanupJob(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpired()
		}
	}
}

func (s *Service) cleanupExpired() {
	if s.store == nil {
		return
	}

	// Cleanup expired temp users
	if deleted, err := s.store.DeleteExpiredTempUsers(); err != nil {
		// Log but don't fail
		fmt.Printf("cleanup: failed to delete expired users: %v\n", err)
	} else if deleted > 0 {
		fmt.Printf("cleanup: deleted %d expired temp users\n", deleted)
	}

	// Cleanup expired sessions
	if deleted, err := s.store.DeleteExpiredSessions(); err != nil {
		fmt.Printf("cleanup: failed to delete expired sessions: %v\n", err)
	} else if deleted > 0 {
		fmt.Printf("cleanup: deleted %d expired sessions\n", deleted)
	}
}