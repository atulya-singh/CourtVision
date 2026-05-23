package store

import (
	"sync"

	"github.com/atulya-singh/CourtVision/internal/types"
)

type Store struct {
	mu        sync.RWMutex
	snapshot  *types.ClusterSnapshot
	decisions *RingBuffer          // ring buffer of last 1000 decisions
	listeners []chan types.Decision // sse subscribers
}

type RingBuffer struct {
	data     []types.Decision
	head     int // next position to write
	tail     int // next position to read
	size     int // current number of elements
	capacity int // would be set to 1000
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		data:     make([]types.Decision, capacity),
		capacity: capacity,
	}
}

func (r *RingBuffer) Write(d types.Decision) {
	r.data[r.head] = d
	r.head = (r.head + 1) % r.capacity

	if r.size < r.capacity {
		r.size++
	} else {
		r.tail = (r.tail + 1) % r.capacity // overwrite oldest
	}
}

// ReadAll returns all decisions in insertion order without consuming them.
func (r *RingBuffer) ReadAll() []types.Decision {
	out := make([]types.Decision, r.size)
	for i := 0; i < r.size; i++ {
		out[i] = r.data[(r.tail+i)%r.capacity]
	}
	return out
}

// FindAndUpdate finds a decision by ID and applies the update function in place.
func (r *RingBuffer) FindAndUpdate(id string, update func(*types.Decision)) bool {
	for i := 0; i < r.size; i++ {
		idx := (r.tail + i) % r.capacity
		if r.data[idx].ID == id {
			update(&r.data[idx])
			return true
		}
	}
	return false
}

func New() *Store {
	return &Store{
		decisions: NewRingBuffer(1000),
	}
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
	s.decisions.Write(d)

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
	return s.decisions.ReadAll()
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
	return s.decisions.FindAndUpdate(id, update)
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
