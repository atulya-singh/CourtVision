package cluster

import (
	"sync"
	"testing"
	"time"

	"github.com/atulya-singh/CourtVision/internal/audit"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// capturingSink records audit events in memory for assertions.
type capturingSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *capturingSink) Record(e audit.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}
func (c *capturingSink) Close() error { return nil }

func (c *capturingSink) snapshot() []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]audit.Event, len(c.events))
	copy(out, c.events)
	return out
}

// auditedWorker builds an auto-safe worker whose executor is audited to sink,
// exactly as the multi-monitor wiring does.
func auditedWorker(name string, inner *recordingExecutor, sink audit.Sink) *ClusterWorker {
	exec := audit.NewExecutor(inner, sink, name, "mock", false)
	return NewClusterWorker(name, nil, nil, exec, true, time.Minute)
}

func TestWorker_AutoSafe_AuditsExecution(t *testing.T) {
	sink := &capturingSink{}
	rec := &recordingExecutor{}
	w := auditedWorker("prod-us", rec, sink)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionScaleDown)})

	events := sink.snapshot()
	if len(events) != 2 {
		t.Fatalf("auto-safe execution should audit executing+terminal, got %d events", len(events))
	}
	if events[0].Phase != "executing" || events[1].Phase != "executed" {
		t.Errorf("unexpected phases: %q then %q", events[0].Phase, events[1].Phase)
	}
	for _, e := range events {
		if e.Actor != "auto-safe" {
			t.Errorf("auto-safe path should record actor auto-safe, got %q", e.Actor)
		}
		if e.Cluster != "prod-us" {
			t.Errorf("event should carry the worker's cluster, got %q", e.Cluster)
		}
	}
}

func TestWorker_AutoSafe_NonReversibleNotAudited(t *testing.T) {
	sink := &capturingSink{}
	rec := &recordingExecutor{}
	w := auditedWorker("prod-us", rec, sink)

	// evict_and_move stays pending and never reaches the executor, so nothing
	// should be audited.
	w.processDecisions([]types.Decision{mkDecision("1", types.ActionEvictAndMove)})

	if events := sink.snapshot(); len(events) != 0 {
		t.Errorf("non-reversible action must not be executed or audited, got %d events", len(events))
	}
}
