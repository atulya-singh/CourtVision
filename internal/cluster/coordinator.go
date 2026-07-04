package cluster

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// Coordinator is the master agent. On its own (deliberately slower) loop it
// gathers the latest snapshot each worker has cached, asks the LLM to reason
// about the fleet as a whole, and records any cross-cluster decisions in its own
// store. It never touches a cluster directly — cross-cluster decisions land as
// "pending" and are routed to the owning worker's executor when a human approves
// them through the API.
type Coordinator struct {
	workers  []*ClusterWorker
	llm      llm.Generatable
	store    *store.Store
	interval time.Duration

	// busy is a single-flight guard: a tick that fires while a previous analysis
	// is still running (slow LLM call) is skipped rather than queued, so ticks
	// never pile up behind a slow Ollama.
	busy atomic.Bool
}

// NewCoordinator builds the master agent over a set of workers. masterStore
// holds only the cross-cluster decisions the coordinator produces, kept separate
// from each worker's per-cluster store.
func NewCoordinator(workers []*ClusterWorker, llmClient llm.Generatable, masterStore *store.Store, interval time.Duration) *Coordinator {
	return &Coordinator{
		workers:  workers,
		llm:      llmClient,
		store:    masterStore,
		interval: interval,
	}
}

// Store exposes the coordinator's store so the API layer can serve fleet-level
// decisions and route their approvals.
func (c *Coordinator) Store() *store.Store { return c.store }

// Run drives the coordinator's loop until ctx is cancelled. Each tick reads the
// workers' cached snapshots rather than collecting fresh ones, so there is no
// point running faster than the workers themselves.
func (c *Coordinator) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	log.Printf("[coordinator] started (interval %s, %d workers)", c.interval, len(c.workers))

	// wg tracks the in-flight tick goroutine so shutdown waits for it to finish.
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			log.Printf("[coordinator] stopped")
			wg.Wait()
			return
		case <-ticker.C:
			// Skip this tick if a previous analysis is still running — the LLM
			// call can outlast the interval, and reasoning over even fresher
			// snapshots next tick is better than a backlog.
			if !c.busy.CompareAndSwap(false, true) {
				log.Printf("[coordinator] previous analysis still running, skipping tick")
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.busy.Store(false)
				c.tick()
			}()
		}
	}
}

// tick performs a single coordination pass: gather snapshots, reason across
// them, and record the resulting cross-cluster decisions.
func (c *Coordinator) tick() {
	// Collect only the workers that have completed at least one tick. At cold
	// start some snapshots are still nil.
	snapshots := make([]*types.ClusterSnapshot, 0, len(c.workers))
	for _, w := range c.workers {
		if snap := w.LatestSnapshot(); snap != nil {
			snapshots = append(snapshots, snap)
		}
	}

	// Cross-cluster reasoning needs at least two clusters to compare; with fewer
	// there is nothing the per-cluster agents aren't already handling.
	if len(snapshots) < 2 {
		return
	}

	prompt := llm.BuildMultiClusterPrompt(snapshots)
	response, err := c.llm.Generate(prompt)
	if err != nil {
		log.Printf("[coordinator] ERROR generating: %v", err)
		return
	}

	// ParseResponse stamps each decision's ClusterName from the LLM's
	// target_cluster field, so coordinator decisions already carry the cluster
	// they apply to.
	decisions, err := llm.ParseResponse(response)
	if err != nil {
		log.Printf("[coordinator] ERROR parsing: %v", err)
		return
	}

	recordDecisions(c.store, decisions)
}
