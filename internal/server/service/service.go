package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"chess/internal/server/core"
	"chess/internal/server/game"
	"chess/internal/server/storage"

	"github.com/lixenwraith/auth"
)

const (
	MaxComputerGames   = 10
	DefaultMaxUsers    = 100
	SessionTTL         = 7 * 24 * time.Hour
	CleanupJobInterval = 1 * time.Hour
	FinishedGameTTL    = 1 * time.Hour
	// AnonymousGameTTL is how long a game no registered user claimed survives
	// after its last activity, in memory and in the database.
	AnonymousGameTTL = 24 * time.Hour
)

// Service coordinates game state, user management, and storage
type Service struct {
	games        map[string]*game.Game
	mu           sync.RWMutex
	store        *storage.Store
	jwt          *auth.JWT
	kdf          chan struct{} // Argon2id concurrency slots
	waiter       *WaitRegistry
	finishedTTL  time.Duration
	anonymousTTL time.Duration
	maxUsers     atomic.Int64
}

// New creates a service with optional storage. jwtSecret signs session tokens
// and must hold at least 32 bytes of key material.
func New(store *storage.Store, jwtSecret []byte) (*Service, error) {
	manager, err := auth.NewJWT(jwtSecret,
		auth.WithIssuer(JWTIssuer),
		auth.WithAudience([]string{JWTAudience}),
		auth.WithTokenLifetime(SessionTTL),
		// Tokens are minted and verified by this process: no clock skew.
		auth.WithLeeway(0),
	)
	if err != nil {
		return nil, fmt.Errorf("configure JWT: %w", err)
	}
	s := &Service{
		games:        make(map[string]*game.Game),
		store:        store,
		jwt:          manager,
		kdf:          make(chan struct{}, MaxConcurrentKDF),
		waiter:       NewWaitRegistry(),
		finishedTTL:  FinishedGameTTL,
		anonymousTTL: AnonymousGameTTL,
	}
	s.maxUsers.Store(DefaultMaxUsers)
	return s, nil
}

// SetFinishedGameTTL configures how long terminal games remain in memory.
// Durable rows and moves are never removed by this cleanup. A non-positive
// duration disables terminal-game eviction.
func (s *Service) SetFinishedGameTTL(ttl time.Duration) {
	s.mu.Lock()
	s.finishedTTL = ttl
	s.mu.Unlock()
}

// SetAnonymousGameTTL configures how long games without a registered player
// survive after their last activity. Such games are unloaded from memory and
// deleted from the database once idle this long. A non-positive duration
// keeps them indefinitely.
func (s *Service) SetAnonymousGameTTL(ttl time.Duration) {
	s.mu.Lock()
	s.anonymousTTL = ttl
	s.mu.Unlock()
}

// SetMaxUsers caps the number of accounts public registration may create;
// zero removes the cap. Accounts created through the CLI are not limited.
func (s *Service) SetMaxUsers(limit int) {
	s.maxUsers.Store(int64(max(limit, 0)))
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

// CanCreateComputerGame reports whether the computer game limit leaves room
// for another game; CreateGame re-checks under its lock.
func (s *Service) CanCreateComputerGame() bool {
	return s.GetComputerGameCount() < MaxComputerGames
}

// GetComputerGameCount returns the number of unfinished loaded games with a
// computer player. Finished games stay loaded for a while but need no engine,
// so they do not count against MaxComputerGames.
func (s *Service) GetComputerGameCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeComputerGamesLocked()
}

// activeComputerGamesLocked counts as GetComputerGameCount. Caller holds s.mu.
func (s *Service) activeComputerGamesLocked() int {
	active := 0
	for _, g := range s.games {
		if g.HasComputerPlayer() && !g.State().IsTerminal() {
			active++
		}
	}
	return active
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

// RunCleanupJob periodically removes expired sessions, unloads idle games, and
// deletes anonymous games past their retention.
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
	now := time.Now().UTC()
	if s.store != nil {
		if deleted, err := s.store.DeleteExpiredSessions(); err != nil {
			slog.Error("cleanup failed to delete expired sessions", "error", err)
		} else if deleted > 0 {
			slog.Info("cleanup deleted expired sessions", "count", deleted)
		}
	}
	s.cleanupGames(now)
}

// cleanupGames unloads terminal games after the finished-game TTL and
// unclaimed games idle past the anonymous TTL, then queues deletion of
// anonymous games idle past that TTL. The set of games still loaded is taken
// under the same lock as the eviction and excluded from deletion, so no live
// game can lose its row while writes for it are still possible.
func (s *Service) cleanupGames(now time.Time) {
	s.mu.Lock()
	finishedCutoff := now.Add(-s.finishedTTL)
	anonymousTTL := s.anonymousTTL
	anonymousCutoff := now.Add(-anonymousTTL)
	var finished, idle []string
	live := make([]string, 0, len(s.games))
	for gameID, g := range s.games {
		ended := g.EndTimeUTC()
		switch {
		case s.finishedTTL > 0 && g.State().IsTerminal() && ended != nil && ended.Before(finishedCutoff):
			finished = append(finished, gameID)
		case anonymousTTL > 0 && !g.IsClaimed() && g.State() != core.StatePending &&
			g.LastActivity().Before(anonymousCutoff):
			idle = append(idle, gameID)
		default:
			live = append(live, gameID)
			continue
		}
		delete(s.games, gameID)
	}
	s.mu.Unlock()

	for _, gameID := range append(finished, idle...) {
		s.waiter.RemoveGame(gameID)
	}
	if len(finished) > 0 {
		slog.Info("cleanup evicted terminal games from memory",
			"count", len(finished), "retention", s.finishedTTL)
	}
	if len(idle) > 0 {
		slog.Info("cleanup evicted idle anonymous games from memory",
			"count", len(idle), "retention", anonymousTTL)
	}
	if s.store != nil && anonymousTTL > 0 {
		if err := s.store.DeleteAnonymousGames(anonymousCutoff, live); err != nil {
			slog.Error("cleanup failed to queue anonymous game deletion", "error", err)
		}
	}
}
