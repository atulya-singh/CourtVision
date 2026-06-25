package main

import (
	"testing"

	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
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
