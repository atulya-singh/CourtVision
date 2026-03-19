package store

import (
	"sync"

	"github.com/atulya-singh/CourtVision/internal/types"
)

type Store struct {
	mu        sync.RWMutex
	snapshot  *types.ClusterSnapshot
	decisions []types.Decision
	listeners []chan types.Decision // sse subscribers
}

func New() *Store {
	return &Store{}
}

// SetSnapshot replaces the current cluster snapshot (called by monitoring loop)
func (s *Store) SetSnapshot(snap *types.ClusterSnapshot) {
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
}

// GetSnapshot returns the latest cluster snapshot (called by API handler)
func (s *Store) GetSnapshot() *types.ClusterSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Store) AddDecision(d types.Decision) {
	s.mu.Lock()
	s.decisions = append(s.decisions, d)

	//send to all SSE listeners (non-blocking)
	for _, ch := range s.listeners {
		select {
		case ch <- d:
		default:
			// default condition makes sure we skip this listener because its buffer is full
		}
	}
	s.mu.Unlock()
}
