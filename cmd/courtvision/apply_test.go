package main

import (
	"testing"

	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// TestApplyAutoModeRunsReversible mirrors the REPL test for the standalone
// `analyze --apply` model — the one surface that can run a real (LIVE) executor,
// so its auto-mode gate matters most. Tab auto-runs a reversible decision and
// walks into the next; the queue then drains and the model quits.
func TestApplyAutoModeRunsReversible(t *testing.T) {
	m := newApplyModel([]types.Decision{
		{ID: "1", Action: types.ActionCordonNode, TargetNode: "n1"},
		{ID: "2", Action: types.ActionScaleDown, TargetPod: "p2", Namespace: "ns"},
	}, executor.NewMockExecutor(), "mock", nil)

	model, cmd := m.Update(tabKey())
	m = model.(applyModel)
	if !m.auto || !m.working || cmd == nil {
		t.Fatal("Tab should turn auto on and start executing the reversible decision")
	}

	model, cmd = m.Update(cmd())
	m = model.(applyModel)
	if m.session.idx != 1 {
		t.Fatalf("cursor should have advanced to 1, got %d", m.session.idx)
	}
	if !m.working || cmd == nil {
		t.Fatal("auto mode should continue into the next reversible decision")
	}

	model, _ = m.Update(cmd())
	m = model.(applyModel)
	if !m.quitting {
		t.Error("model should quit once the queue drains under auto mode")
	}
}

// TestApplyAutoModePausesOnRisky proves the reversible-only gate holds on the
// LIVE-capable surface: auto mode leaves evict_and_move for explicit approval.
func TestApplyAutoModePausesOnRisky(t *testing.T) {
	m := newApplyModel([]types.Decision{
		{ID: "1", Action: types.ActionEvictAndMove, TargetPod: "p1", Namespace: "ns"},
	}, executor.NewMockExecutor(), "mock", nil)

	model, cmd := m.Update(tabKey())
	m = model.(applyModel)
	if !m.auto {
		t.Fatal("Tab should turn auto mode on")
	}
	if m.working || cmd != nil {
		t.Fatal("auto mode must not auto-run evict_and_move")
	}
}
