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

func (s *Store) GetDecisions() []types.Decision {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]types.Decision, len(s.decisions))
	copy(out, s.decisions)
	return out
}

//Subscribe creates a new SSE Listener channel
// The caller reads from this channel to get real time decisions

func (s *Store) Subscribe() chan types.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan types.Decision, 20)
	s.listeners = append(s.listeners, ch)
	return ch
}

// UpdateDecision finds a decision by ID and applies the update function
func (s *Store) UpdateDecision(id string, update func(*types.Decision)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.decisions {
		if s.decisions[i].ID == id {
			update(&s.decisions[i])
			return true
		}
	}
	return false
}

func (s *Store) Unsubscribe(ch chan types.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, listener := range s.listeners {
		if listener == ch {
			s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
			close(ch)
			return
		}
	}
}
