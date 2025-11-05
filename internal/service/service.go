// FILE: internal/service/service.go
package service

import (
	"sync"

	"chess/internal/game"
	"chess/internal/storage"
)

// Service is a pure state manager for chess games with optional persistence
type Service struct {
	games     map[string]*game.Game
	mu        sync.RWMutex
	store     *storage.Store // nil if persistence disabled
	jwtSecret []byte
}

// New creates a new service instance with optional storage
func New(store *storage.Store, jwtSecret []byte) (*Service, error) {
	return &Service{
		games:     make(map[string]*game.Game),
		store:     store,
		jwtSecret: jwtSecret,
	}, nil
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

// Close cleans up resources
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear all games
	s.games = make(map[string]*game.Game)

	// Close storage if enabled
	if s.store != nil {
		return s.store.Close()
	}

	return nil
}