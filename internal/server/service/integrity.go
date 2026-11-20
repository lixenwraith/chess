package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lixenwraith/chess/internal/server/replay"

	"github.com/lixenwraith/auth"
)

// IntegrityMode selects what the periodic integrity sweep does.
type IntegrityMode int

const (
	// IntegrityOff skips the sweep.
	IntegrityOff IntegrityMode = iota
	// IntegrityReport logs what the sweep finds and changes nothing.
	IntegrityReport
	// IntegrityDelete also unloads and deletes what it finds.
	IntegrityDelete
)

// integrityBatch bounds how many stored games one sweep replays against the
// rules; the sweep resumes after the last one next time, so every game is
// checked in turn at a bounded cost per run.
const integrityBatch = 200

// ParseIntegrityMode accepts "off", "report", or "delete".
func ParseIntegrityMode(s string) (IntegrityMode, error) {
	switch s {
	case "", "off":
		return IntegrityOff, nil
	case "report":
		return IntegrityReport, nil
	case "delete":
		return IntegrityDelete, nil
	}
	return IntegrityOff, fmt.Errorf("invalid integrity mode %q (want off, report, or delete)", s)
}

func (m IntegrityMode) String() string {
	switch m {
	case IntegrityReport:
		return "report"
	case IntegrityDelete:
		return "delete"
	}
	return "off"
}

// SetIntegrityMode configures the sweep run with every cleanup job.
func (s *Service) SetIntegrityMode(mode IntegrityMode) {
	s.mu.Lock()
	s.integrityMode = mode
	s.mu.Unlock()
}

// integrityFindings is what one sweep found. Keys are IDs, values reasons.
type integrityFindings struct {
	vanished map[string]string // loaded games without a database row
	games    map[string]string // stored games to delete
	users    map[string]string // accounts to delete
	loaded   map[string]bool
}

// checkIntegrity runs the sweep doc/database.md describes: it logs each
// finding at WARN with its reason and, in delete mode, removes it from memory
// and the database. Loaded games are not replayed: their rows may trail memory.
func (s *Service) checkIntegrity(ctx context.Context, now time.Time) {
	s.mu.RLock()
	mode, anonymousTTL := s.integrityMode, s.anonymousTTL
	loaded := make(map[string]bool, len(s.games))
	for gameID := range s.games {
		loaded[gameID] = true
	}
	s.mu.RUnlock()
	if mode == IntegrityOff || s.store == nil {
		return
	}

	findings, err := s.findIntegrityProblems(ctx, now, loaded, anonymousTTL)
	if err != nil {
		slog.Error("integrity sweep failed", "error", err)
		return
	}
	for gameID, reason := range findings.vanished {
		slog.Warn("integrity: loaded game has no database row", "game_id", gameID, "reason", reason)
	}
	for gameID, reason := range findings.games {
		slog.Warn("integrity: invalid game", "game_id", gameID, "reason", reason)
	}
	for userID, reason := range findings.users {
		slog.Warn("integrity: unusable account", "user_id", userID, "reason", reason)
	}
	summary := []any{"mode", mode.String(), "vanished_games", len(findings.vanished),
		"invalid_games", len(findings.games), "unusable_accounts", len(findings.users)}
	if mode != IntegrityDelete {
		if len(findings.vanished)+len(findings.games)+len(findings.users) > 0 {
			slog.Info("integrity sweep found problems; nothing changed (report mode)", summary...)
		}
		return
	}

	for gameID := range findings.vanished {
		s.evictMissingGame(gameID)
	}
	var games []string
	for gameID := range findings.games {
		if findings.loaded[gameID] {
			s.evictMissingGame(gameID) // unload first: no further writes for it
		}
		games = append(games, gameID)
	}
	if err := s.store.DeleteGames(games); err != nil {
		slog.Error("integrity sweep failed to queue game deletion", "error", err)
	}
	var users []string
	for userID := range findings.users {
		users = append(users, userID)
	}
	if err := s.store.DeleteUsers(ctx, users); err != nil {
		slog.Error("integrity sweep failed to delete accounts", "error", err)
	}
	if len(findings.vanished)+len(games)+len(users) > 0 {
		slog.Info("integrity sweep removed invalid data", summary...)
	}
}

func (s *Service) findIntegrityProblems(ctx context.Context, now time.Time, loaded map[string]bool,
	anonymousTTL time.Duration) (*integrityFindings, error) {
	found := &integrityFindings{
		vanished: map[string]string{},
		games:    map[string]string{},
		users:    map[string]string{},
		loaded:   loaded,
	}
	live := make([]string, 0, len(loaded))
	for gameID := range loaded {
		live = append(live, gameID)
	}

	// Every loaded game's insert was queued before the snapshot was taken, so
	// after this flush a missing row means the row was deleted.
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.store.Flush(flushCtx); err != nil {
		return nil, err
	}
	exists, err := s.store.ExistingGames(ctx, live)
	if err != nil {
		return nil, err
	}
	for _, gameID := range live {
		if !exists[gameID] {
			found.vanished[gameID] = "row deleted outside the server"
		}
	}

	gaps, err := s.store.GamesWithMoveGaps(ctx)
	if err != nil {
		return nil, err
	}
	for _, gameID := range gaps {
		found.games[gameID] = "missing plies: move rows were deleted"
	}

	ids, err := s.store.GameIDsAfter(ctx, s.integrityCursor, integrityBatch)
	if err != nil {
		return nil, err
	}
	s.integrityCursor = ""
	if len(ids) == integrityBatch {
		s.integrityCursor = ids[len(ids)-1]
	}
	for _, gameID := range ids {
		if loaded[gameID] || found.games[gameID] != "" {
			continue
		}
		record, moves, err := s.store.GetGameHistory(ctx, gameID)
		if err != nil {
			continue // deleted since the listing
		}
		if problems := replay.Verify(record, moves); len(problems) > 0 {
			found.games[gameID] = problems[0]
		}
	}

	if anonymousTTL > 0 {
		orphans, err := s.store.OrphanedGames(ctx, now.Add(-anonymousTTL), live)
		if err != nil {
			return nil, err
		}
		for _, gameID := range orphans {
			if found.games[gameID] == "" {
				found.games[gameID] = "all registered players were deleted"
			}
		}
	}

	users, err := s.store.GetAllUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		if err := auth.ValidatePHCHashFormat(user.PasswordHash); err != nil {
			found.users[user.UserID] = fmt.Sprintf("%s: %v", user.Username, err)
		}
	}
	return found, nil
}

// evictMissingGame unloads a game whose database row disappeared, typically
// deleted by hand. It runs when a write for the game finds the row gone.
func (s *Service) evictMissingGame(gameID string) {
	s.mu.Lock()
	_, ok := s.games[gameID]
	if ok {
		delete(s.games, gameID)
	}
	s.mu.Unlock()
	if ok {
		s.waiter.RemoveGame(gameID)
		slog.Warn("game unloaded: its database row no longer exists", "game_id", gameID)
	}
}
