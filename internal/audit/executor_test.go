package audit

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// memSink collects events in memory so tests can inspect exactly what the
// decorator recorded.
type memSink struct {
	mu     sync.Mutex
	events []Event
}

func (m *memSink) Record(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}
func (m *memSink) Close() error { return nil }

// fakeExecutor is a stand-in inner executor whose result the test controls.
type fakeExecutor struct {
	err   error
	calls int
}

func (f *fakeExecutor) Execute(context.Context, *types.Decision) error {
	f.calls++
	return f.err
}

func sampleDecision() *types.Decision {
	return &types.Decision{
		ID:        "dec-1",
		Action:    types.ActionScaleDown,
		Namespace: "ns",
		TargetPod: "pod",
	}
}

func TestAuditingExecutor_RecordsExecutingThenExecuted(t *testing.T) {
	sink := &memSink{}
	inner := &fakeExecutor{}
	exec := NewExecutor(inner, sink, "prod-us", "mock", false)

	if err := exec.Execute(context.Background(), sampleDecision()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner executor should run exactly once, ran %d", inner.calls)
	}

	if len(sink.events) != 2 {
		t.Fatalf("want 2 events (executing, executed), got %d", len(sink.events))
	}
	if sink.events[0].Phase != "executing" {
		t.Errorf("first event phase: want executing, got %q", sink.events[0].Phase)
	}
	term := sink.events[1]
	if term.Phase != "executed" {
		t.Errorf("terminal phase: want executed, got %q", term.Phase)
	}
	if term.Cluster != "prod-us" || term.Mode != "mock" || term.DecisionID != "dec-1" {
		t.Errorf("event fields not stamped: %+v", term)
	}
}

func TestAuditingExecutor_RecordsFailureAndReturnsError(t *testing.T) {
	sink := &memSink{}
	wantErr := errors.New("deployment not found")
	exec := NewExecutor(&fakeExecutor{err: wantErr}, sink, "prod-eu", "live", false)

	err := exec.Execute(context.Background(), sampleDecision())
	if !errors.Is(err, wantErr) {
		t.Fatalf("decorator must return the inner error unchanged, got %v", err)
	}

	term := sink.events[len(sink.events)-1]
	if term.Phase != "failed" {
		t.Errorf("terminal phase: want failed, got %q", term.Phase)
	}
	if term.Error != wantErr.Error() {
		t.Errorf("failure error not recorded: %q", term.Error)
	}
}

func TestAuditingExecutor_ReadsActorFromContext(t *testing.T) {
	sink := &memSink{}
	exec := NewExecutor(&fakeExecutor{}, sink, "c", "mock", false)

	ctx := WithActor(context.Background(), "auto-safe")
	_ = exec.Execute(ctx, sampleDecision())

	for _, e := range sink.events {
		if e.Actor != "auto-safe" {
			t.Errorf("event actor: want auto-safe, got %q", e.Actor)
		}
	}
}

func TestAuditingExecutor_DryRunFlagAndClusterFallback(t *testing.T) {
	sink := &memSink{}
	// Empty cluster arg (single-cluster mode) must fall back to the decision's
	// own ClusterName, and dryRun=true must be stamped on every event.
	exec := NewExecutor(&fakeExecutor{}, sink, "", "dry-run", true)

	d := sampleDecision()
	d.ClusterName = "mock-cluster"
	_ = exec.Execute(context.Background(), d)

	for _, e := range sink.events {
		if !e.DryRun {
			t.Errorf("dry-run flag should be set, got false")
		}
		if e.Cluster != "mock-cluster" {
			t.Errorf("cluster should fall back to decision ClusterName, got %q", e.Cluster)
		}
	}
}

func TestAuditingExecutor_NilSinkIsSafe(t *testing.T) {
	// A nil sink must not panic — construction falls back to a NopSink.
	exec := NewExecutor(&fakeExecutor{}, nil, "c", "mock", false)
	if err := exec.Execute(context.Background(), sampleDecision()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
