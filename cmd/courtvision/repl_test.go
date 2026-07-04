package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func tabKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyTab} }

// failingExecutor always errors, so runExecutor reports StatusFailed. Used to
// prove a failure turns auto mode off.
type failingExecutor struct{}

func (failingExecutor) Execute(context.Context, *types.Decision) error {
	return fmt.Errorf("boom")
}

// loadReview drives a fresh REPL into review mode with the given decisions.
func loadReview(t *testing.T, exec executor.Executor, decisions []types.Decision) replModel {
	t.Helper()
	m := newREPL(&cobra.Command{})
	model, _ := m.Update(reviewLoadedMsg{decisions: decisions, exec: exec, label: "test"})
	rm := model.(replModel)
	if rm.mode != modeReview {
		t.Fatalf("expected review mode after load")
	}
	return rm
}

// TestREPLReviewFlow drives the REPL's Update loop the way Bubbletea would:
// load decisions, approve one (running it through a mock executor), reject the
// next, and confirm the REPL returns to normal input mode with a summary.
func TestREPLReviewFlow(t *testing.T) {
	m := newREPL(&cobra.Command{})

	decisions := []types.Decision{
		{ID: "1", Action: types.ActionPatchLimits, TargetPod: "p1", Namespace: "ns"},
		{ID: "2", Action: types.ActionNone}, // informational, filtered out
		{ID: "3", Action: types.ActionScaleDown, TargetPod: "p3", Namespace: "ns"},
	}

	model, _ := m.Update(reviewLoadedMsg{decisions: decisions, exec: executor.NewMockExecutor(), label: "mock"})
	m = model.(replModel)
	if m.mode != modeReview {
		t.Fatalf("expected review mode after load")
	}
	if m.session.total() != 2 {
		t.Fatalf("want 2 actionable decisions, got %d", m.session.total())
	}

	// Approve the first decision.
	model, cmd := m.Update(key("a"))
	m = model.(replModel)
	if !m.working {
		t.Error("expected working=true after approve")
	}
	if cmd == nil {
		t.Fatal("expected an executor command after approve")
	}

	// Run the executor command and feed its result back in.
	model, _ = m.Update(cmd())
	m = model.(replModel)
	if m.working {
		t.Error("expected working=false after execution finished")
	}
	if m.session.idx != 1 {
		t.Errorf("cursor should have advanced to 1, got %d", m.session.idx)
	}

	// Reject the second (last) decision; that ends the review.
	model, _ = m.Update(key("r"))
	m = model.(replModel)
	if m.mode != modeInput {
		t.Errorf("expected to return to input mode after last decision, got %v", m.mode)
	}
	if m.session != nil {
		t.Error("session should be cleared after review ends")
	}
}

// TestREPLAutoModeRunsReversible: pressing Tab with a reversible decision
// current auto-executes it, then keeps walking the queue through subsequent
// reversible decisions without further keypresses.
func TestREPLAutoModeRunsReversible(t *testing.T) {
	m := loadReview(t, executor.NewMockExecutor(), []types.Decision{
		{ID: "1", Action: types.ActionPatchLimits, TargetPod: "p1", Namespace: "ns"},
		{ID: "2", Action: types.ActionScaleDown, TargetPod: "p2", Namespace: "ns"},
	})

	// Tab turns auto on and immediately runs the first (reversible) decision.
	model, cmd := m.Update(tabKey())
	m = model.(replModel)
	if !m.auto {
		t.Fatal("Tab should turn auto mode on")
	}
	if !m.working || cmd == nil {
		t.Fatal("auto mode should start executing the reversible decision")
	}

	// Finishing the first execution auto-advances into the second.
	model, cmd = m.Update(cmd())
	m = model.(replModel)
	if m.session.idx != 1 {
		t.Fatalf("cursor should have advanced to 1, got %d", m.session.idx)
	}
	if !m.working || cmd == nil {
		t.Fatal("auto mode should continue into the next reversible decision")
	}

	// Finishing the second completes the queue and leaves review mode.
	model, _ = m.Update(cmd())
	m = model.(replModel)
	if m.mode != modeInput {
		t.Errorf("expected input mode after the queue drained, got %v", m.mode)
	}
}

// TestREPLAutoModePausesOnRisky: auto mode never runs evict_and_move on its own;
// it waits for an explicit keypress, then resumes on the next reversible item.
func TestREPLAutoModePausesOnRisky(t *testing.T) {
	m := loadReview(t, executor.NewMockExecutor(), []types.Decision{
		{ID: "1", Action: types.ActionEvictAndMove, TargetPod: "p1", Namespace: "ns"},
		{ID: "2", Action: types.ActionScaleDown, TargetPod: "p2", Namespace: "ns"},
	})

	// Tab turns auto on but must NOT execute the risky eviction.
	model, cmd := m.Update(tabKey())
	m = model.(replModel)
	if !m.auto {
		t.Fatal("Tab should turn auto mode on")
	}
	if m.working || cmd != nil {
		t.Fatal("auto mode must not auto-run evict_and_move")
	}

	// Operator approves the risky one explicitly; on completion auto resumes.
	model, cmd = m.Update(key("a"))
	m = model.(replModel)
	if !m.working || cmd == nil {
		t.Fatal("explicit approval should execute the eviction")
	}
	model, cmd = m.Update(cmd())
	m = model.(replModel)
	if !m.working || cmd == nil {
		t.Fatal("auto mode should resume on the next reversible decision")
	}
}

// TestREPLAutoModeFailureStops: a failed execution turns auto mode off so the
// operator regains control instead of the queue barreling on.
func TestREPLAutoModeFailureStops(t *testing.T) {
	m := loadReview(t, failingExecutor{}, []types.Decision{
		{ID: "1", Action: types.ActionPatchLimits, TargetPod: "p1", Namespace: "ns"},
		{ID: "2", Action: types.ActionScaleDown, TargetPod: "p2", Namespace: "ns"},
	})

	model, cmd := m.Update(tabKey())
	m = model.(replModel)
	if !m.working || cmd == nil {
		t.Fatal("auto mode should start executing")
	}

	// The execution fails → auto turns off and the queue does not continue.
	model, cmd = m.Update(cmd())
	m = model.(replModel)
	if m.auto {
		t.Error("a failure must turn auto mode off")
	}
	if m.working || cmd != nil {
		t.Error("auto mode should not continue after a failure")
	}
	if m.mode != modeReview {
		t.Error("should still be in review mode, awaiting the operator")
	}
}

// TestREPLReviewNoActionable ensures an all-informational result never enters
// review mode.
func TestREPLReviewNoActionable(t *testing.T) {
	m := newREPL(&cobra.Command{})
	model, _ := m.Update(reviewLoadedMsg{
		decisions: []types.Decision{{ID: "1", Action: types.ActionNone}},
		exec:      executor.NewMockExecutor(),
		label:     "mock",
	})
	m = model.(replModel)
	if m.mode != modeInput {
		t.Error("a non-actionable result must not enter review mode")
	}
}
