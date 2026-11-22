package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/game"
	"github.com/lixenwraith/chess/internal/server/metrics"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/lixenwraith/auth"
)

const (
	DefaultMaxUsers    = 100
	SessionTTL         = 7 * 24 * time.Hour
	CleanupJobInterval = 1 * time.Hour
	// RecoveryInterval is how often degraded storage is tried again.
	RecoveryInterval = 5 * time.Second
	FinishedGameTTL  = 1 * time.Hour
	// AnonymousGameTTL is how long a game no registered user claimed survives
	// after its last activity, in memory and in the database.
	AnonymousGameTTL = 24 * time.Hour
	// IdleGameTTL is how long an unfinished game a registered user claimed
	// stays in memory after its last activity; its next request loads it again.
	IdleGameTTL = 7 * 24 * time.Hour
)

type Service struct {
	games        map[string]*game.Game
	loading      map[string]*gameLoad // stored games being read back into games
	mu           sync.RWMutex
	store        *storage.Store
	jwt          *auth.JWT
	kdf          chan struct{} // Argon2id concurrency slots
	waiter       *WaitRegistry
	finishedTTL  time.Duration
	anonymousTTL time.Duration
	idleTTL      time.Duration
	maxUsers     atomic.Int64

	integrityMode   IntegrityMode
	integrityCursor string // last game ID the integrity sweep verified; cleanup goroutine only

	mail *mailer // password recovery; nil when no transport is configured
	site string  // Site tag of exported games played here; empty is PGN's "?"

	kdfWaits    metrics.Timer // waits for an Argon2id slot
	replayReads metrics.Timer // stored-game reads, barrier included
}

// Metrics reports the games in memory, those with a computer side, the
// long-polls waiting, how long passwords waited for a hashing slot and stored
// games took to read, and the store's own measures.
func (s *Service) Metrics() []slog.Attr {
	s.mu.RLock()
	loaded, computer := len(s.games), 0
	for _, g := range s.games {
		if !g.BothHuman() {
			computer++
		}
	}
	s.mu.RUnlock()
	attrs := []slog.Attr{
		slog.Group("games", "loaded", loaded, "with_computer", computer, "longpoll_waiters", s.waiter.Waiting()),
		slog.Group("auth", s.kdfWaits.Attr("kdf_wait")),
		slog.Group("replay", s.replayReads.Attr("read")),
	}
	if s.store != nil {
		attrs = append(attrs, s.store.Metrics()...)
	}
	return attrs
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
		loading:      make(map[string]*gameLoad),
		store:        store,
		jwt:          manager,
		kdf:          make(chan struct{}, MaxConcurrentKDF),
		waiter:       NewWaitRegistry(),
		finishedTTL:  FinishedGameTTL,
		anonymousTTL: AnonymousGameTTL,
		idleTTL:      IdleGameTTL,
	}
	s.maxUsers.Store(DefaultMaxUsers)
	if store != nil {
		store.SetGameDroppedHandler(s.evictDroppedGame)
	}
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

// SetIdleGameTTL configures how long an unfinished game a registered user
// claimed stays in memory after its last activity; its rows stay, and resume
// it. A non-positive duration keeps such games loaded.
func (s *Service) SetIdleGameTTL(ttl time.Duration) {
	s.mu.Lock()
	s.idleTTL = ttl
	s.mu.Unlock()
}

// SetSite names this server in the Site tag of the games it exports, from
// the web client's public address; call it before serving.
func (s *Service) SetSite(site string) {
	s.site = site
}

// SetMaxUsers caps the number of accounts public registration may create;
// zero removes the cap. Accounts created through the CLI are not limited.
func (s *Service) SetMaxUsers(limit int) {
	s.maxUsers.Store(int64(max(limit, 0)))
}

func (s *Service) GetStorageHealth() string {
	if s.store == nil {
		return "disabled"
	}
	if s.store.IsHealthy() {
		return "ok"
	}
	return "degraded"
}

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

// EndWaits wakes every long-poll and refuses new waits, so a server's drain
// need not wait them out; Shutdown ends them too.
func (s *Service) EndWaits(timeout time.Duration) error {
	return s.waiter.Shutdown(timeout)
}

func (s *Service) Shutdown(timeout time.Duration) error {
	var errs []error

	if err := s.waiter.Shutdown(timeout); err != nil {
		errs = append(errs, fmt.Errorf("wait registry: %w", err))
	}

	s.mu.Lock()
	s.games = make(map[string]*game.Game)
	s.mu.Unlock()

	// Queued reset emails need the store.
	if s.mail != nil {
		s.mail.close(timeout)
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("storage: %w", err))
		}
	}

	return errors.Join(errs...)
}

// RunCleanupJob periodically removes expired sessions, unloads idle games,
// deletes anonymous games past their retention, and runs the integrity sweep
// when enabled. Between runs it tries degraded storage again.
func (s *Service) RunCleanupJob(ctx context.Context, interval time.Duration) {
	s.cleanupExpired(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	recovery := time.NewTicker(RecoveryInterval)
	defer recovery.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpired(ctx)
		case <-recovery.C:
			s.recoverStorage(ctx)
		}
	}
}

func (s *Service) cleanupExpired(ctx context.Context) {
	now := time.Now().UTC()
	if s.store != nil {
		if deleted, err := s.store.DeleteExpiredSessions(ctx); err != nil {
			slog.Error("cleanup failed to delete expired sessions", "error", err)
		} else if deleted > 0 {
			slog.Info("cleanup deleted expired sessions", "count", deleted)
		}
		if deleted, err := s.store.DeleteExpiredPasswordResets(ctx); err != nil {
			slog.Error("cleanup failed to delete expired password resets", "error", err)
		} else if deleted > 0 {
			slog.Info("cleanup deleted expired password resets", "count", deleted)
		}
		if err := s.store.DeleteResetMailsBefore(ctx, now.Add(-2*ResetMailLimits.Window)); err != nil {
			slog.Error("cleanup failed to forget old reset mail", "error", err)
		}
	}
	s.cleanupGames(now)
	s.checkIntegrity(ctx, now)
}

// cleanupGames unloads terminal games past the finished TTL and idle games
// past the anonymous TTL (unclaimed) or the idle TTL (claimed, unfinished),
// then queues deletion of the idle anonymous ones. Games loaded or being
// loaded are excluded under the eviction's lock, so no live game loses its
// row while writes for it are still possible.
func (s *Service) cleanupGames(now time.Time) {
	// Memory holds what the rows lack until storage recovers.
	if s.store != nil && !s.store.IsHealthy() {
		return
	}
	s.mu.Lock()
	finishedTTL, anonymousTTL, idleTTL := s.finishedTTL, s.anonymousTTL, s.idleTTL
	finishedCutoff := now.Add(-finishedTTL)
	anonymousCutoff := now.Add(-anonymousTTL)
	idleCutoff := now.Add(-idleTTL)
	var finished, anonymous, claimed []string
	live := make([]string, 0, len(s.games))
	for gameID, g := range s.games {
		ended, state := g.EndTimeUTC(), g.State()
		switch {
		case finishedTTL > 0 && state.IsTerminal() && ended != nil && ended.Before(finishedCutoff):
			finished = append(finished, gameID)
		case anonymousTTL > 0 && !g.IsClaimed() && state != core.StatePending &&
			g.LastActivity().Before(anonymousCutoff):
			anonymous = append(anonymous, gameID)
		case idleTTL > 0 && g.IsClaimed() && !state.IsTerminal() && state != core.StatePending &&
			g.LastActivity().Before(idleCutoff):
			claimed = append(claimed, gameID)
		default:
			live = append(live, gameID)
			continue
		}
		delete(s.games, gameID)
	}
	for gameID := range s.loading {
		live = append(live, gameID)
	}
	// Queued under the lock: a load that starts later reads behind the purge.
	if s.store != nil && anonymousTTL > 0 {
		if err := s.store.DeleteAnonymousGames(anonymousCutoff, live); err != nil {
			slog.Error("cleanup failed to queue anonymous game deletion", "error", err)
		}
	}
	s.mu.Unlock()

	for _, gameID := range slices.Concat(finished, anonymous, claimed) {
		s.waiter.RemoveGame(gameID)
	}
	if len(finished) > 0 {
		slog.Info("cleanup evicted terminal games from memory",
			"count", len(finished), "retention", finishedTTL)
	}
	if len(anonymous) > 0 {
		slog.Info("cleanup evicted idle anonymous games from memory",
			"count", len(anonymous), "retention", anonymousTTL)
	}
	if len(claimed) > 0 {
		slog.Info("cleanup evicted idle claimed games from memory; they resume from their rows",
			"count", len(claimed), "retention", idleTTL)
	}
}
