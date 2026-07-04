// Package cluster wires CourtVision's single-cluster pipeline into a
// multi-cluster, multi-agent topology: one ClusterWorker (a subagent) per
// cluster, plus a Coordinator (the master agent) that reasons across all of
// them.
package cluster

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// ClusterWorker is a subagent responsible for exactly one cluster. It runs the
// same collect → analyze → store pipeline the single-cluster monitor uses, and
// additionally caches its most recent snapshot so the Coordinator can read the
// whole fleet's state without touching any cluster directly.
type ClusterWorker struct {
	name     string
	store    *store.Store
	provider metrics.Provider
	engine   decision.Engine
	exec     executor.Executor

	// auto-safe mode: when auto is set, the worker executes its own reversible
	// decisions instead of leaving them pending. cooldown throttles repeat
	// executions of the same action on the same target, and lastAuto records the
	// last time each target was auto-executed. lastAuto is touched only from the
	// single analyze goroutine, so it needs no lock.
	auto     bool
	cooldown time.Duration
	lastAuto map[string]time.Time

	mu     sync.RWMutex
	latest *types.ClusterSnapshot
}

// NewClusterWorker assembles a worker from the per-cluster pieces. Each worker
// owns its own store so per-cluster decisions never mix, and its own executor
// so approved actions land on the right cluster. When auto is true the worker
// auto-executes its reversible decisions (see autoExecute), throttled by
// cooldown per target.
func NewClusterWorker(name string, provider metrics.Provider, engine decision.Engine, exec executor.Executor, auto bool, cooldown time.Duration) *ClusterWorker {
	return &ClusterWorker{
		name:     name,
		store:    store.New(),
		provider: provider,
		engine:   engine,
		exec:     exec,
		auto:     auto,
		cooldown: cooldown,
		lastAuto: make(map[string]time.Time),
	}
}

// Run drives the worker's monitoring loop until ctx is cancelled. Metrics
// collection is fast but LLM analysis can take many seconds, so the two run on
// separate goroutines: the collection loop below ticks on interval, publishing a
// fresh snapshot each time, while analyze() consumes those snapshots on its own
// schedule. A slow (or hung) Ollama call therefore never stalls collection, and
// the Coordinator and dashboard always see up-to-date cluster state.
func (w *ClusterWorker) Run(ctx context.Context, interval time.Duration) {
	// analyzeCh hands the freshest snapshot to the analysis goroutine. It holds a
	// single slot with drop-latest semantics (see offerLatest): if the analyzer is
	// still busy with a previous snapshot, the waiting one is replaced rather than
	// queued, so we never build a backlog of stale work.
	analyzeCh := make(chan *types.ClusterSnapshot, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.analyze(ctx, analyzeCh)
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[%s] worker started (interval %s)", w.name, interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] worker stopped", w.name)
			// Wait for the analyzer to observe cancellation and exit so we don't
			// leak a goroutine per worker on shutdown.
			wg.Wait()
			return
		case <-ticker.C:
			snapshot, err := w.provider.GetClusterSnapshot()
			if err != nil {
				log.Printf("[%s] ERROR collecting metrics: %v", w.name, err)
				continue
			}

			w.store.SetSnapshot(snapshot)

			// Publish the snapshot so the Coordinator can read a consistent
			// pointer while the next tick is being assembled.
			w.mu.Lock()
			w.latest = snapshot
			w.mu.Unlock()

			// Hand the snapshot off for analysis without blocking. If the analyzer
			// is still working, this replaces the pending snapshot so it always
			// picks up the freshest data next.
			offerLatest(analyzeCh, snapshot)
		}
	}
}

// analyze consumes snapshots and runs the (potentially slow) decision engine on
// them, recording any resulting decisions. It runs on its own goroutine so that a
// slow LLM call never blocks the collection loop in Run.
func (w *ClusterWorker) analyze(ctx context.Context, ch <-chan *types.ClusterSnapshot) {
	for {
		select {
		case <-ctx.Done():
			return
		case snapshot := <-ch:
			decisions, err := w.engine.Analyze(snapshot)
			if err != nil {
				log.Printf("[%s] ERROR analyzing: %v", w.name, err)
				continue
			}
			w.processDecisions(decisions)
		}
	}
}

// processDecisions records every decision (so the dashboard always sees it) and,
// in auto-safe mode, carries out the worker's own reversible decisions.
// Non-reversible ones (evict_and_move) always stay pending for explicit
// approval, and repeats within the cooldown window are skipped.
func (w *ClusterWorker) processDecisions(decisions []types.Decision) {
	recordDecisions(w.store, decisions)
	if !w.auto {
		return
	}
	for _, d := range decisions {
		if d.Action == types.ActionNone || !d.Action.IsReversible() {
			continue
		}
		if w.onCooldown(d) {
			log.Printf("[%s] auto-safe: %s on %s/%s still on cooldown, skipping", w.name, d.Action, d.Namespace, d.TargetPod)
			continue
		}
		w.autoExecute(d)
	}
}

// autoExecute runs a single reversible decision against the worker's own cluster
// executor and records the outcome in the store, mirroring the API server's
// executeDecision so the dashboard sees the same executing → executed/failed
// lifecycle. It runs inline in the analyze goroutine, which is already decoupled
// from metric collection, so a slow executor never stalls the collection loop.
func (w *ClusterWorker) autoExecute(d types.Decision) {
	w.markCooldown(d)

	w.store.UpdateAndBroadcast(d.ID, func(x *types.Decision) {
		x.Status = types.StatusExecuting
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := w.exec.Execute(ctx, &d)
	now := time.Now()

	w.store.UpdateAndBroadcast(d.ID, func(x *types.Decision) {
		x.ExecutedAt = &now
		if err != nil {
			x.Status = types.StatusFailed
			x.Executed = false
			x.Error = err.Error()
			return
		}
		x.Status = types.StatusExecuted
		x.Executed = true
		x.Error = ""
	})

	if err != nil {
		log.Printf("[%s] auto-safe FAILED %s on %s/%s: %v", w.name, d.Action, d.Namespace, d.TargetPod, err)
		return
	}
	log.Printf("[%s] auto-executed %s on %s/%s", w.name, d.Action, d.Namespace, d.TargetPod)
}

// cooldownKey identifies "the same action on the same target" so a problem that
// recurs every tick is fixed at most once per cooldown window.
func cooldownKey(d types.Decision) string {
	target := d.Namespace + "/" + d.TargetPod
	if d.Action == types.ActionCordonNode {
		target = d.TargetNode
	}
	return string(d.Action) + "|" + target
}

func (w *ClusterWorker) onCooldown(d types.Decision) bool {
	last, ok := w.lastAuto[cooldownKey(d)]
	return ok && time.Since(last) < w.cooldown
}

func (w *ClusterWorker) markCooldown(d types.Decision) {
	w.lastAuto[cooldownKey(d)] = time.Now()
}

// Name returns the worker's cluster name (its kubeconfig context).
func (w *ClusterWorker) Name() string { return w.name }

// Store exposes the worker's decision/snapshot store so the API layer can serve
// per-cluster state and route approvals.
func (w *ClusterWorker) Store() *store.Store { return w.store }

// Executor exposes the worker's executor so approved decisions for this cluster
// run against the right cluster.
func (w *ClusterWorker) Executor() executor.Executor { return w.exec }

// LatestSnapshot returns the most recent snapshot this worker collected, or nil
// if it has not completed a tick yet. The Coordinator uses this to read the
// fleet's state without triggering fresh collection.
func (w *ClusterWorker) LatestSnapshot() *types.ClusterSnapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.latest
}
