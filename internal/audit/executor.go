package audit

import (
	"context"
	"time"

	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// AuditingExecutor decorates any executor.Executor with a durable audit trail.
// It records an event before the wrapped executor runs ("executing") and another
// once it finishes ("executed" or "failed"), so the log captures both intent and
// outcome — and still shows the intent if the process dies mid-action.
//
// It is the single write path for the audit package: because every mutation in
// CourtVision goes through the Executor interface, wrapping the executor the
// safety switch already chose (dry-run / mock / live) captures the HTTP approval
// path, the multi-cluster auto-safe loop, and the interactive review flow
// without any of them knowing auditing exists — the same composition trick the
// FallbackEngine uses over decision engines.
type AuditingExecutor struct {
	inner   executor.Executor
	sink    Sink
	cluster string // which cluster this executor targets (empty in single-cluster mode)
	mode    string // the safety-switch label carried onto each event: dry-run | mock | live
	dryRun  bool   // true when inner makes no real change, so records are clearly non-mutating
}

// NewExecutor wraps inner so its executions are audited to sink. cluster, mode
// and dryRun are stamped onto every event this executor produces; the actor is
// read per-call from the context (see WithActor). The return type is
// executor.Executor so callers can wrap transparently at construction.
func NewExecutor(inner executor.Executor, sink Sink, cluster, mode string, dryRun bool) executor.Executor {
	// A nil sink would panic on Record; fall back to a no-op so callers can pass
	// whatever they built without guarding.
	if sink == nil {
		sink = NewNopSink()
	}
	return &AuditingExecutor{inner: inner, sink: sink, cluster: cluster, mode: mode, dryRun: dryRun}
}

// Execute records the attempt, runs the wrapped executor, records the outcome,
// and returns the inner error unchanged so the caller's existing lifecycle
// handling (store status transitions, review outcomes) is unaffected.
func (a *AuditingExecutor) Execute(ctx context.Context, d *types.Decision) error {
	actor := ActorFrom(ctx)

	a.sink.Record(a.event(actor, d, "executing", 0, nil))

	start := time.Now()
	err := a.inner.Execute(ctx, d)
	elapsed := time.Since(start)

	phase := "executed"
	if err != nil {
		phase = "failed"
	}
	a.sink.Record(a.event(actor, d, phase, elapsed, err))

	return err
}

// event builds an audit Event from a decision plus the wrapped executor's
// context. cluster falls back to the decision's own ClusterName so single-cluster
// mode (which constructs the executor with an empty cluster) still attributes the
// record correctly.
func (a *AuditingExecutor) event(actor string, d *types.Decision, phase string, elapsed time.Duration, err error) Event {
	cluster := a.cluster
	if cluster == "" {
		cluster = d.ClusterName
	}
	e := Event{
		Time:        time.Now(),
		Actor:       actor,
		Cluster:     cluster,
		DecisionID:  d.ID,
		Action:      d.Action,
		Severity:    d.Severity,
		Namespace:   d.Namespace,
		TargetPod:   d.TargetPod,
		TargetNode:  d.TargetNode,
		NewCPULimit: d.NewCPULimit,
		NewMemLimit: d.NewMemLimit,
		Reasoning:   d.Reasoning,
		Phase:       phase,
		DryRun:      a.dryRun,
		Mode:        a.mode,
	}
	// Duration and error only make sense on the terminal event.
	if phase != "executing" {
		e.DurationMS = elapsed.Milliseconds()
	}
	if err != nil {
		e.Error = err.Error()
	}
	return e
}
