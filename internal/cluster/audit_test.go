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

// auditedWorker builds an auto-safe worker whose executor and proposals are both
// audited to sink, exactly as the multi-monitor wiring does (one shared sink).
func auditedWorker(name string, inner *recordingExecutor, sink audit.Sink) *ClusterWorker {
	exec := audit.NewExecutor(inner, sink, name, "mock", false)
	return NewClusterWorker(name, nil, nil, exec, true, time.Minute, sink)
}

func TestWorker_AutoSafe_AuditsProposalThenExecution(t *testing.T) {
	sink := &capturingSink{}
	rec := &recordingExecutor{}
	w := auditedWorker("prod-us", rec, sink)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionScaleDown)})

	events := sink.snapshot()
	if len(events) != 3 {
		t.Fatalf("reversible auto-safe should audit proposed+executing+executed, got %d events", len(events))
	}
	if events[0].Phase != audit.PhaseProposed || events[1].Phase != audit.PhaseExecuting || events[2].Phase != audit.PhaseExecuted {
		t.Errorf("unexpected phases: %q, %q, %q", events[0].Phase, events[1].Phase, events[2].Phase)
	}
	if events[0].Actor != actorProposer {
		t.Errorf("proposed event should carry the proposer actor, got %q", events[0].Actor)
	}
	for _, e := range events[1:] {
		if e.Actor != "auto-safe" {
			t.Errorf("execution events should record actor auto-safe, got %q", e.Actor)
		}
	}
	for _, e := range events {
		if e.Cluster != "prod-us" {
			t.Errorf("event should carry the worker's cluster, got %q", e.Cluster)
		}
	}
}

// A non-reversible decision never executes, but it must still leave a durable
// "proposed" record — the whole point of auditing the lifecycle, not just runs.
func TestWorker_NonReversible_AuditsProposalOnly(t *testing.T) {
	sink := &capturingSink{}
	rec := &recordingExecutor{}
	w := auditedWorker("prod-us", rec, sink)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionEvictAndMove)})

	events := sink.snapshot()
	if len(events) != 1 || events[0].Phase != audit.PhaseProposed {
		t.Fatalf("non-reversible action should audit exactly one proposed event, got %+v", events)
	}
	if rec.count() != 0 {
		t.Errorf("non-reversible action must not execute, got %d calls", rec.count())
	}
}

// The proposal record is deduplicated by problem signature within the cooldown
// window, so a per-tick re-analysis (fresh IDs, same problem) can't flood the log.
func TestWorker_Proposed_DedupedWithinWindow(t *testing.T) {
	sink := &capturingSink{}
	rec := &recordingExecutor{}
	// auto off so we isolate proposal logging from execution events.
	w := NewClusterWorker("c", nil, nil, rec, false, time.Minute, sink)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionEvictAndMove)})
	w.processDecisions([]types.Decision{mkDecision("2", types.ActionEvictAndMove)}) // same problem, new ID

	if events := sink.snapshot(); len(events) != 1 {
		t.Errorf("same problem within the window should be proposed once, got %d events", len(events))
	}
}
