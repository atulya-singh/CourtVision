package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func decision(id string) types.Decision {
	return types.Decision{
		ID:        id,
		Timestamp: time.Now(),
		Severity:  types.SeverityLow,
		Action:    types.ActionNone,
		TargetPod: "test-pod",
		Namespace: "default",
		Reasoning: "test",
	}
}

// ── RingBuffer ────────────────────────────────────────────────────────────────

func TestRingBuffer_BasicWriteReadAll(t *testing.T) {
	rb := NewRingBuffer(5)
	rb.Write(decision("a"))
	rb.Write(decision("b"))
	rb.Write(decision("c"))

	got := rb.ReadAll()
	if len(got) != 3 {
		t.Fatalf("want 3 elements, got %d", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].ID != want {
			t.Errorf("index %d: want %s, got %s", i, want, got[i].ID)
		}
	}
}

func TestRingBuffer_ReadAllEmpty(t *testing.T) {
	rb := NewRingBuffer(10)
	if got := rb.ReadAll(); len(got) != 0 {
		t.Errorf("want empty slice, got %d elements", len(got))
	}
}

func TestRingBuffer_ReadAllIsNonDestructive(t *testing.T) {
	rb := NewRingBuffer(5)
	rb.Write(decision("a"))
	rb.Write(decision("b"))

	_ = rb.ReadAll()
	got := rb.ReadAll()
	if len(got) != 2 {
		t.Errorf("ReadAll is destructive: second call returned %d elements, want 2", len(got))
	}
}

func TestRingBuffer_Wraparound_OldestEvicted(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 1; i <= 5; i++ {
		rb.Write(decision(fmt.Sprintf("id-%d", i)))
	}

	got := rb.ReadAll()

	if len(got) != 3 {
		t.Fatalf("want 3 (capacity), got %d", len(got))
	}
	// Oldest two (id-1, id-2) must be gone; remaining must be in order
	want := []string{"id-3", "id-4", "id-5"}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("index %d: want %s, got %s", i, w, got[i].ID)
		}
	}
}

func TestRingBuffer_ExactlyFull(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 1; i <= 3; i++ {
		rb.Write(decision(fmt.Sprintf("id-%d", i)))
	}

	got := rb.ReadAll()
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].ID != "id-1" || got[2].ID != "id-3" {
		t.Errorf("wrong order at capacity boundary: %v", ids(got))
	}
}

func TestRingBuffer_FindAndUpdate_Found(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write(decision("a"))
	rb.Write(decision("b"))

	found := rb.FindAndUpdate("a", func(d *types.Decision) { d.Executed = true })

	if !found {
		t.Fatal("FindAndUpdate returned false, want true")
	}
	all := rb.ReadAll()
	if !all[0].Executed {
		t.Error("target decision was not updated")
	}
	if all[1].Executed {
		t.Error("non-target decision was incorrectly mutated")
	}
}

func TestRingBuffer_FindAndUpdate_NotFound(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write(decision("a"))

	if rb.FindAndUpdate("ghost", func(d *types.Decision) { d.Executed = true }) {
		t.Error("FindAndUpdate returned true for an ID that doesn't exist")
	}
}

func TestRingBuffer_FindAndUpdate_AfterWraparound(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 1; i <= 5; i++ {
		rb.Write(decision(fmt.Sprintf("id-%d", i)))
	}

	// id-1 and id-2 were evicted
	if rb.FindAndUpdate("id-1", func(d *types.Decision) {}) {
		t.Error("id-1 should have been evicted and not findable")
	}
	if !rb.FindAndUpdate("id-4", func(d *types.Decision) { d.Executed = true }) {
		t.Error("id-4 should still be present")
	}
}

// ── Store ─────────────────────────────────────────────────────────────────────

func TestNew_RingBufferInitialized(t *testing.T) {
	s := New()
	if s.decisions == nil {
		t.Fatal("New() left decisions nil — ring buffer not initialized")
	}
	// Must not panic
	_ = s.GetDecisions()
}

func TestStore_AddAndGetRoundtrip(t *testing.T) {
	s := New()
	s.AddDecision(decision("a"))
	s.AddDecision(decision("b"))

	got := s.GetDecisions()
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("wrong order: %v", ids(got))
	}
}

func TestStore_GetDecisions_NonDestructive(t *testing.T) {
	s := New()
	s.AddDecision(decision("a"))

	_ = s.GetDecisions()
	got := s.GetDecisions()
	if len(got) != 1 {
		t.Errorf("GetDecisions is destructive: second call returned %d, want 1", len(got))
	}
}

func TestStore_UpdateDecision_Found(t *testing.T) {
	s := New()
	s.AddDecision(decision("a"))

	found := s.UpdateDecision("a", func(d *types.Decision) { d.Executed = true })
	if !found {
		t.Fatal("UpdateDecision returned false, want true")
	}
	if got := s.GetDecisions(); !got[0].Executed {
		t.Error("decision was not updated in place")
	}
}

func TestStore_UpdateDecision_NotFound(t *testing.T) {
	s := New()
	if s.UpdateDecision("ghost", func(d *types.Decision) {}) {
		t.Error("UpdateDecision returned true for unknown ID")
	}
}

func TestStore_Subscribe_ReceivesDecision(t *testing.T) {
	s := New()
	ch := s.Subscribe()
	defer s.Unsubscribe(ch)

	s.AddDecision(decision("a"))

	select {
	case got := <-ch:
		if got.ID != "a" {
			t.Errorf("want id=a, got %s", got.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for decision on subscriber channel")
	}
}

func TestStore_Unsubscribe_ClosesChannel(t *testing.T) {
	s := New()
	ch := s.Subscribe()
	s.Unsubscribe(ch)

	if _, open := <-ch; open {
		t.Error("channel should be closed after Unsubscribe")
	}
}

func TestStore_Unsubscribe_StopsDelivery(t *testing.T) {
	s := New()
	ch := s.Subscribe()
	s.Unsubscribe(ch)

	// Adding after unsubscribe must not send to the closed channel (would panic)
	// If this test panics, the store tried to write to a closed channel.
	s.AddDecision(decision("a"))
}

func TestStore_ConcurrentAddDecision(t *testing.T) {
	s := New()
	const n = 200
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.AddDecision(decision(fmt.Sprintf("id-%d", i)))
		}(i)
	}
	wg.Wait()

	// Ring buffer capacity is 1000; all 200 writes must be present.
	got := s.GetDecisions()
	if len(got) != n {
		t.Errorf("after %d concurrent writes: want %d decisions, got %d", n, n, len(got))
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func ids(ds []types.Decision) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}
