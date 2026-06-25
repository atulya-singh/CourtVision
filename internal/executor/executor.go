// Package executor turns an approved Decision into a real action.
//
// The monitoring loop and the rule/LLM engines only ever *propose* decisions;
// nothing in the cluster changes until an Executor runs one. Keeping "decide"
// and "act" behind separate types is deliberate: choosing what to do and
// actually doing it have very different blast radii, so they should not live in
// the same place. An Executor is the only component allowed to cause real,
// outward effects, which makes it the single point where safety controls
// (dry-run, approval gating) need to apply.
package executor

import (
	"context"
	"log"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// Executor performs the action described by a Decision. Implementations must be
// safe for concurrent use and should respect ctx cancellation, since a single
// approval may kick off a network call to the Kubernetes API server.
type Executor interface {
	Execute(ctx context.Context, d *types.Decision) error
}

// MockExecutor simulates execution without touching anything real. It is paired
// with the mock metrics provider so the entire propose -> approve -> execute
// flow can be demonstrated on a laptop with no cluster at all.
type MockExecutor struct{}

func NewMockExecutor() *MockExecutor { return &MockExecutor{} }

func (m *MockExecutor) Execute(_ context.Context, d *types.Decision) error {
	log.Printf("[mock-exec] simulated %s on %s/%s", d.Action, d.Namespace, d.TargetPod)
	return nil
}

// DryRunExecutor logs what it *would* do and then does nothing. It exists so an
// operator can point CourtVision at a real cluster and watch its decisions play
// out with zero risk of a real mutation. This is what backs the --dry-run flag,
// which is on by default.
type DryRunExecutor struct{}

func NewDryRunExecutor() *DryRunExecutor { return &DryRunExecutor{} }

func (d *DryRunExecutor) Execute(_ context.Context, dec *types.Decision) error {
	log.Printf("[dry-run] would %s on %s/%s (no changes made)", dec.Action, dec.Namespace, dec.TargetPod)
	return nil
}
