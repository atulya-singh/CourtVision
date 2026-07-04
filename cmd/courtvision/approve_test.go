package main

import (
	"testing"

	"github.com/atulya-singh/CourtVision/internal/audit"
	"github.com/atulya-singh/CourtVision/internal/types"
)

func TestIsReversible(t *testing.T) {
	tests := []struct {
		action types.ActionType
		want   bool
	}{
		{types.ActionCordonNode, true},
		{types.ActionScaleDown, true},
		{types.ActionPatchLimits, true},
		{types.ActionEvictAndMove, false}, // deletes a pod — never auto-run
		{types.ActionNone, false},
	}
	for _, tt := range tests {
		if got := isReversible(tt.action); got != tt.want {
			t.Errorf("isReversible(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}

func TestNewReviewSession_FiltersNonActionable(t *testing.T) {
	decisions := []types.Decision{
		{ID: "1", Action: types.ActionPatchLimits},
		{ID: "2", Action: types.ActionNone}, // informational, must be dropped
		{ID: "3", Action: types.ActionScaleDown},
	}
	s := newReviewSession(decisions)
	if s.total() != 2 {
		t.Fatalf("want 2 actionable decisions, got %d", s.total())
	}
	if s.current().ID != "1" {
		t.Errorf("want first actionable id 1, got %s", s.current().ID)
	}
}

func TestReviewSession_RecordAdvancesAndTallies(t *testing.T) {
	s := newReviewSession([]types.Decision{
		{ID: "1", Action: types.ActionPatchLimits},
		{ID: "2", Action: types.ActionScaleDown},
		{ID: "3", Action: types.ActionCordonNode},
	})

	s.record(types.StatusExecuted, "")
	if s.current().ID != "2" {
		t.Errorf("cursor should advance to id 2, got %s", s.current().ID)
	}
	s.record(types.StatusRejected, "")
	s.record(types.StatusFailed, "boom")

	if !s.done() {
		t.Error("session should be done after recording all decisions")
	}
	tally := s.tally()
	if tally[types.StatusExecuted] != 1 || tally[types.StatusRejected] != 1 || tally[types.StatusFailed] != 1 {
		t.Errorf("unexpected tally: %+v", tally)
	}
}

func TestBuildExecutor_DryRunWinsOverK8s(t *testing.T) {
	// Even with a k8s source, dry-run must never build a mutating executor; the
	// label proves the dry-run branch short-circuited before touching the cluster.
	exec, label, err := buildExecutor("k8s", true, audit.NewNopSink())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected a non-nil executor")
	}
	if label != "dry-run (no changes)" {
		t.Errorf("want dry-run label, got %q", label)
	}
}

func TestBuildExecutor_MockForNonK8s(t *testing.T) {
	exec, label, err := buildExecutor("mock", false, audit.NewNopSink())
	if err != nil || exec == nil {
		t.Fatalf("unexpected: exec=%v err=%v", exec, err)
	}
	if label != "mock (simulated)" {
		t.Errorf("want mock label, got %q", label)
	}
}
