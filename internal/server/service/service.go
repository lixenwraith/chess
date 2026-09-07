package service

import (
	"chess/internal/server/core"
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	FinishedGameTTL    = 1 * time.Hour
)

// Service coordinates game state, user management, and storage
type Service struct {
	games         map[string]*game.Game
	mu            sync.RWMutex
	userMu        sync.Mutex
	store         *storage.Store
	jwtSecret     []byte
	waiter        *WaitRegistry
	computerGames atomic.Int32 // Active games with computer players
	finishedTTL   time.Duration
}

// New creates a new service instance with optional storage
func New(store *storage.Store, jwtSecret []byte) *Service {
	return &Service{
		games:       make(map[string]*game.Game),
		store:       store,
		jwtSecret:   jwtSecret,
		waiter:      NewWaitRegistry(),
		finishedTTL: FinishedGameTTL,
	}
}

// SetFinishedGameTTL configures how long terminal games remain in memory.
// Durable rows and moves are never removed by this cleanup. A non-positive
// duration disables terminal-game eviction.
func (s *Service) SetFinishedGameTTL(ttl time.Duration) {
	s.mu.Lock()
	s.finishedTTL = ttl
	s.mu.Unlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.games[gameID]; !ok {
		notify := make(chan struct{})
		close(notify)
		return notify
	}
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
	if userID == "" {
		return errors.New("claimant user ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return fmt.Errorf("game not found: %s", gameID)
	}

	if err := g.ClaimSlot(color, userID); err != nil {
		return err
	}
	if s.store != nil {
		if err := s.store.RecordSlotClaim(gameID, color.String(), userID); err != nil {
			slog.Error("failed to queue slot claim persistence",
				"game_id", gameID, "color", color.String(), "error", err)
		}
	}
	slog.Debug("game slot claimed", "game_id", gameID, "color", color.String(), "user_id", userID)
	return nil
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
	s.games = make(map[string]*game.Game)
	s.mu.Unlock()

	if s.store != nil {
		if err := s.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("storage: %w", err))
		}
	}

	return errors.Join(errs...)
}

// RunCleanupJob runs periodic cleanup of expired users and sessions
func (s *Service) RunCleanupJob(ctx context.Context, interval time.Duration) {
	s.cleanupExpired()
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
	if s.store != nil {
		if deleted, err := s.store.DeleteExpiredTempUsers(); err != nil {
			slog.Error("cleanup failed to delete expired users", "error", err)
		} else if deleted > 0 {
			slog.Info("cleanup deleted expired temporary users", "count", deleted)
		}

		if deleted, err := s.store.DeleteExpiredSessions(); err != nil {
			slog.Error("cleanup failed to delete expired sessions", "error", err)
		} else if deleted > 0 {
			slog.Info("cleanup deleted expired sessions", "count", deleted)
		}
	}

	s.cleanupFinishedGames(time.Now().UTC())
}

func (s *Service) cleanupFinishedGames(now time.Time) {
	s.mu.Lock()
	if s.finishedTTL <= 0 {
		s.mu.Unlock()
		return
	}
	cutoff := now.Add(-s.finishedTTL)
	removed := make([]string, 0)
	for gameID, g := range s.games {
		ended := g.EndTimeUTC()
		if !g.State().IsTerminal() || ended == nil || ended.After(cutoff) {
			continue
		}
		if g.HasComputerPlayer() {
			s.computerGames.Add(-1)
		}
		delete(s.games, gameID)
		removed = append(removed, gameID)
	}
	s.mu.Unlock()

	for _, gameID := range removed {
		s.waiter.RemoveGame(gameID)
	}
	if len(removed) > 0 {
		slog.Info("cleanup evicted terminal games from memory",
			"count", len(removed), "retention", s.finishedTTL)
	}
}
