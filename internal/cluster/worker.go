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

	mu     sync.RWMutex
	latest *types.ClusterSnapshot
}

// NewClusterWorker assembles a worker from the per-cluster pieces. Each worker
// owns its own store so per-cluster decisions never mix, and its own executor
// so approved actions land on the right cluster.
func NewClusterWorker(name string, provider metrics.Provider, engine decision.Engine, exec executor.Executor) *ClusterWorker {
	return &ClusterWorker{
		name:     name,
		store:    store.New(),
		provider: provider,
		engine:   engine,
		exec:     exec,
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
			recordDecisions(w.store, decisions)
		}
	}
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
