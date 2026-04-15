package projectstate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
)

// Service coordinates cache lookups and background scans for a repo.
type Service struct {
	store *Store

	mu      sync.RWMutex
	current Snapshot
	ready   bool
}

func NewService(db *sql.DB) *Service {
	return &Service{store: NewStore(db)}
}

// Current returns the most recently known snapshot and whether it is loaded.
func (s *Service) Current() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.ready
}

// EnsureSnapshot returns the cached snapshot for cwd. If no cache exists, it
// runs Scan synchronously and stores the result.
func (s *Service) EnsureSnapshot(ctx context.Context, cwd string) (Snapshot, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return Snapshot{}, err
	}
	root := filepath.ToSlash(abs)

	if snap, ok, err := s.store.Get(root); err == nil && ok {
		s.set(snap)
		return snap, nil
	} else if err != nil {
		return Snapshot{}, fmt.Errorf("projectstate get: %w", err)
	}

	snap, err := Scan(cwd)
	if err != nil {
		return Snapshot{}, fmt.Errorf("projectstate scan: %w", err)
	}
	if err := s.store.Upsert(snap); err != nil {
		return Snapshot{}, fmt.Errorf("projectstate upsert: %w", err)
	}
	s.set(snap)
	return snap, nil
}

// Rescan forces a fresh scan and overwrites the cache.
func (s *Service) Rescan(ctx context.Context, cwd string) (Snapshot, error) {
	snap, err := Scan(cwd)
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.store.Upsert(snap); err != nil {
		return Snapshot{}, err
	}
	s.set(snap)
	return snap, nil
}

func (s *Service) set(snap Snapshot) {
	s.mu.Lock()
	s.current = snap
	s.ready = true
	s.mu.Unlock()
}
