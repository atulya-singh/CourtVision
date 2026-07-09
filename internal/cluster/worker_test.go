package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// recordingExecutor satisfies executor.Executor and records every Execute call
// so tests can assert whether (and how often) an auto-safe worker acted.
type recordingExecutor struct {
	mu    sync.Mutex
	calls []types.Decision
	err   error
}

func (r *recordingExecutor) Execute(_ context.Context, d *types.Decision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, *d)
	return r.err
}

func (r *recordingExecutor) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func mkDecision(id string, action types.ActionType) types.Decision {
	return types.Decision{ID: id, Action: action, Namespace: "ns", TargetPod: "p"}
}

func statusOf(t *testing.T, w *ClusterWorker, id string) types.DecisionStatus {
	t.Helper()
	d, ok := w.Store().GetDecision(id)
	if !ok {
		t.Fatalf("decision %s not found in store", id)
	}
	return d.Status
}

func TestWorker_AutoOff_LeavesPending(t *testing.T) {
	rec := &recordingExecutor{}
	w := NewClusterWorker("c", nil, nil, rec, false, time.Minute, nil)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionScaleDown)})

	if rec.count() != 0 {
		t.Errorf("auto off: executor should not run, got %d calls", rec.count())
	}
	if s := statusOf(t, w, "1"); s != types.StatusPending {
		t.Errorf("auto off: decision should stay pending, got %s", s)
	}
}

func TestWorker_AutoOn_ExecutesReversible(t *testing.T) {
	rec := &recordingExecutor{}
	w := NewClusterWorker("c", nil, nil, rec, true, time.Minute, nil)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionScaleDown)})

	if rec.count() != 1 {
		t.Fatalf("auto on: reversible decision should execute once, got %d", rec.count())
	}
	if s := statusOf(t, w, "1"); s != types.StatusExecuted {
		t.Errorf("auto on: decision should be executed, got %s", s)
	}
}

func TestWorker_AutoOn_SkipsEvict(t *testing.T) {
	rec := &recordingExecutor{}
	w := NewClusterWorker("c", nil, nil, rec, true, time.Minute, nil)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionEvictAndMove)})

	if rec.count() != 0 {
		t.Errorf("auto on: evict_and_move must not auto-run, got %d calls", rec.count())
	}
	if s := statusOf(t, w, "1"); s != types.StatusPending {
		t.Errorf("auto on: evict_and_move should stay pending, got %s", s)
	}
}

func TestWorker_Cooldown_SuppressesRepeat(t *testing.T) {
	rec := &recordingExecutor{}
	w := NewClusterWorker("c", nil, nil, rec, true, time.Minute, nil)

	// Same problem recurs on a later tick (different decision ID, same target).
	w.processDecisions([]types.Decision{mkDecision("1", types.ActionScaleDown)})
	w.processDecisions([]types.Decision{mkDecision("2", types.ActionScaleDown)})

	if rec.count() != 1 {
		t.Errorf("cooldown should suppress the repeat: want 1 execution, got %d", rec.count())
	}
	// The suppressed repeat stays pending rather than executing.
	if s := statusOf(t, w, "2"); s != types.StatusPending {
		t.Errorf("cooled-down repeat should stay pending, got %s", s)
	}
}

func TestWorker_Cooldown_ExpiresAfterWindow(t *testing.T) {
	rec := &recordingExecutor{}
	w := NewClusterWorker("c", nil, nil, rec, true, 10*time.Millisecond, nil)

	w.processDecisions([]types.Decision{mkDecision("1", types.ActionScaleDown)})
	time.Sleep(20 * time.Millisecond) // let the cooldown window elapse
	w.processDecisions([]types.Decision{mkDecision("2", types.ActionScaleDown)})

	if rec.count() != 2 {
		t.Errorf("after the cooldown window the action should run again: want 2, got %d", rec.count())
	}
}
