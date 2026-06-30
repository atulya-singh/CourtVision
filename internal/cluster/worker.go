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

// Run drives the worker's monitoring loop until ctx is cancelled. It mirrors the
// single-cluster monitor loop: collect a snapshot, analyze it, and record the
// resulting decisions, while also publishing the snapshot for the Coordinator.
func (w *ClusterWorker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[%s] worker started (interval %s)", w.name, interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] worker stopped", w.name)
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

			decisions, err := w.engine.Analyze(snapshot)
			if err != nil {
				log.Printf("[%s] ERROR analyzing: %v", w.name, err)
				continue
			}

			for _, d := range decisions {
				// Decisions that propose a real action wait for human approval;
				// informational ones (action == none) have nothing to approve.
				if d.Action == types.ActionNone {
					d.Status = types.StatusNone
				} else {
					d.Status = types.StatusPending
				}
				w.store.AddDecision(d)
			}
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
