package executor

import (
	"context"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
)

func TestMockExecutor_AlwaysSucceeds(t *testing.T) {
	e := NewMockExecutor()
	d := &types.Decision{Action: types.ActionPatchLimits, TargetPod: "api", Namespace: "default"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("mock executor should never error, got %v", err)
	}
}

func TestDryRunExecutor_DoesNothingButSucceeds(t *testing.T) {
	e := NewDryRunExecutor()
	d := &types.Decision{Action: types.ActionScaleDown, TargetPod: "worker", Namespace: "default"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("dry-run executor should never error, got %v", err)
	}
}

// Both safe executors satisfy the Executor interface; this fails to compile if
// either signature drifts.
var _ Executor = (*MockExecutor)(nil)
var _ Executor = (*DryRunExecutor)(nil)
var _ Executor = (*K8sExecutor)(nil)
