package types

import "testing"

func TestActionType_IsReversible(t *testing.T) {
	tests := []struct {
		action ActionType
		want   bool
	}{
		{ActionCordonNode, true},
		{ActionScaleDown, true},
		{ActionPatchLimits, true},
		{ActionEvictAndMove, false}, // deletes a pod — never auto-run
		{ActionNone, false},
		{ActionType("bogus"), false},
	}
	for _, tt := range tests {
		if got := tt.action.IsReversible(); got != tt.want {
			t.Errorf("%q.IsReversible() = %v, want %v", tt.action, got, tt.want)
		}
	}
}
