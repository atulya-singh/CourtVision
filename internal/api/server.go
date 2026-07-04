package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/audit"
	"github.com/atulya-singh/CourtVision/internal/cluster"
	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// Server holds the HTTP server and its dependencies.
//
// It serves two shapes. In single-cluster mode (NewServer) store/exec back the
// classic /api/* routes. In multi-cluster mode (NewMultiServer) store/exec back
// the fleet-level coordinator decisions, and workers adds the per-cluster
// /api/clusters/* routes. The handlers are written against an explicit store so
// both modes reuse the same rendering and approval logic.
type Server struct {
	store   *store.Store
	exec    executor.Executor
	workers map[string]*cluster.ClusterWorker
	order   []string // stable cluster ordering for listing
	port    string
}

func NewServer(st *store.Store, exec executor.Executor, port string) *Server {
	return &Server{store: st, exec: exec, port: port}
}

// NewMultiServer builds the API for a multi-cluster deployment. masterStore
// holds the coordinator's cross-cluster decisions; workers expose each cluster's
// own store and executor.
func NewMultiServer(workers []*cluster.ClusterWorker, masterStore *store.Store, port string) *Server {
	m := make(map[string]*cluster.ClusterWorker, len(workers))
	order := make([]string, 0, len(workers))
	for _, w := range workers {
		m[w.Name()] = w
		order = append(order, w.Name())
	}
	return &Server{store: masterStore, workers: m, order: order, port: port}
}

// Start registers all routes and begins listening. It returns when ctx is
// cancelled, giving in-flight requests up to 5 seconds to complete.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Fleet-level routes. In single-cluster mode these serve the one store; in
	// multi-cluster mode they serve the coordinator's cross-cluster decisions.
	mux.HandleFunc("/api/cluster", s.handleCluster)
	mux.HandleFunc("/api/decisions", s.handleDecisions)
	mux.HandleFunc("/api/events", s.handleSSE)
	mux.HandleFunc("/api/decisions/", s.handleDecisionAction)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Per-cluster routes are only registered when running with workers.
	if s.workers != nil {
		mux.HandleFunc("/api/clusters", s.handleClustersList)
		mux.HandleFunc("/api/clusters/", s.handleClusterScoped)
	}

	srv := &http.Server{
		Addr:    ":" + s.port,
		Handler: corsMiddleware(mux),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	log.Printf("API server starting on :%s", s.port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	renderSnapshot(w, r, s.store)
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	renderDecisions(w, r, s.store)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	streamSSE(w, r, s.store)
}

// renderSnapshot writes a store's current snapshot as JSON.
func renderSnapshot(w http.ResponseWriter, r *http.Request, st *store.Store) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := st.GetSnapshot()
	if snap == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pods":[],"nodes":[],"timestamp":"0001-01-01T00:00:00Z"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

// renderDecisions writes a store's decisions as JSON.
func renderDecisions(w http.ResponseWriter, r *http.Request, st *store.Store) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	decisions := st.GetDecisions()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decisions)
}

// streamSSE subscribes the caller to a store's decision stream over SSE.
func streamSSE(w http.ResponseWriter, r *http.Request, st *store.Store) {

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := st.Subscribe()
	defer st.Unsubscribe(ch)

	log.Println("SSE client connected")

	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	for {
		select {
		case decision, ok := <-ch:
			if !ok {
				// Channel was closed (store shutting down)
				return
			}
			data, err := json.Marshal(decision)
			if err != nil {
				log.Printf("SSE marshal error: %v", err)
				continue
			}
			// SSE format: "event: <type>\ndata: <json>\n\n"
			fmt.Fprintf(w, "event: decision\ndata: %s\n\n", data)
			flusher.Flush()

		case <-r.Context().Done():
			// Browser disconnected
			log.Println("SSE client disconnected")
			return
		}
	}
}
func (s *Server) handleDecisionAction(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/decisions/{id}/approve or /api/decisions/{id}/reject
	path := strings.TrimPrefix(r.URL.Path, "/api/decisions/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	s.decisionAction(w, r, s.store, parts[0], parts[1])
}

// decisionAction approves or rejects a single decision in the given store.
// Approval resolves the right executor for the decision's cluster, so the same
// logic serves single-cluster, per-cluster, and coordinator decisions.
func (s *Server) decisionAction(w http.ResponseWriter, r *http.Request, st *store.Store, id, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if action != "approve" && action != "reject" {
		http.Error(w, "action must be 'approve' or 'reject'", http.StatusBadRequest)
		return
	}

	dec, found := st.GetDecision(id)
	if !found {
		http.Error(w, "decision not found", http.StatusNotFound)
		return
	}

	// Guard against acting twice on the same decision. Without this, a
	// double-click or a retried request could run a scale-down or eviction
	// twice. A terminal/in-flight decision is off-limits.
	if dec.Status == types.StatusExecuting || dec.Status == types.StatusExecuted {
		http.Error(w, "decision already "+string(dec.Status), http.StatusConflict)
		return
	}

	if action == "reject" {
		now := time.Now()
		st.UpdateAndBroadcast(id, func(d *types.Decision) {
			d.Status = types.StatusRejected
			d.Executed = false
			d.ExecutedAt = &now
			d.Error = "rejected by operator"
		})
		writeJSON(w, map[string]string{"status": "rejected"})
		return
	}

	// approve: this is the "ask first" gate. The decision was only ever a
	// proposal until a human reached this point; now we actually run it.
	exec, err := s.executorFor(&dec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	executeDecision(st, exec, &dec)
	writeJSON(w, map[string]string{"status": string(dec.Status), "error": dec.Error})
}

// executorFor resolves which executor should run a decision. Single-cluster mode
// always has one executor. Multi-cluster mode routes by the decision's cluster —
// this is what lets an approved coordinator (cross-cluster) decision execute
// against the cluster it actually targets.
func (s *Server) executorFor(dec *types.Decision) (executor.Executor, error) {
	if s.exec != nil {
		return s.exec, nil
	}
	w, ok := s.workers[dec.ClusterName]
	if !ok {
		return nil, fmt.Errorf("no worker for cluster %q", dec.ClusterName)
	}
	return w.Executor(), nil
}

// clusterSummary is the per-cluster entry returned by GET /api/clusters.
type clusterSummary struct {
	Name          string `json:"name"`
	PodCount      int    `json:"pod_count"`
	NodeCount     int    `json:"node_count"`
	DecisionCount int    `json:"decision_count"`
}

// handleClustersList serves GET /api/clusters — a roll-up of every cluster the
// fleet is watching, in stable order.
func (s *Server) handleClustersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	summaries := make([]clusterSummary, 0, len(s.order))
	for _, name := range s.order {
		worker := s.workers[name]
		sum := clusterSummary{Name: name, DecisionCount: len(worker.Store().GetDecisions())}
		if snap := worker.LatestSnapshot(); snap != nil {
			sum.PodCount = len(snap.Pods)
			sum.NodeCount = len(snap.Nodes)
		}
		summaries = append(summaries, sum)
	}

	writeJSON(w, summaries)
}

// handleClusterScoped routes the per-cluster subresources:
//
//	GET  /api/clusters/{cluster}/snapshot
//	GET  /api/clusters/{cluster}/decisions
//	GET  /api/clusters/{cluster}/events
//	POST /api/clusters/{cluster}/decisions/{id}/approve|reject
func (s *Server) handleClusterScoped(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/clusters/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	worker, ok := s.workers[parts[0]]
	if !ok {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}
	st := worker.Store()

	switch {
	case parts[1] == "snapshot" && len(parts) == 2:
		renderSnapshot(w, r, st)
	case parts[1] == "events" && len(parts) == 2:
		streamSSE(w, r, st)
	case parts[1] == "decisions" && len(parts) == 2:
		renderDecisions(w, r, st)
	case parts[1] == "decisions" && len(parts) == 4:
		// /api/clusters/{cluster}/decisions/{id}/{action}
		s.decisionAction(w, r, st, parts[2], parts[3])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// executeDecision runs the approved decision through the executor and records
// the outcome. It broadcasts an "executing" state first so the dashboard shows
// the action in flight, runs the (possibly slow) executor without holding any
// lock, then broadcasts the terminal state. The executor is the only place a
// real cluster mutation can happen, so all the safety wrapping lives around
// this single call.
//
// It deliberately uses a fresh background context rather than the request's:
// once we have committed to mutating the cluster, a browser disconnect must not
// cancel the action half-done. The 30s timeout still bounds it.
func executeDecision(st *store.Store, exec executor.Executor, dec *types.Decision) {
	st.UpdateAndBroadcast(dec.ID, func(d *types.Decision) {
		d.Status = types.StatusExecuting
	})

	execCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Tag the actor so the audit trail attributes this to an operator approval
	// through the HTTP API.
	execCtx = audit.WithActor(execCtx, "api-approval")

	err := exec.Execute(execCtx, dec)
	now := time.Now()

	st.UpdateAndBroadcast(dec.ID, func(d *types.Decision) {
		d.ExecutedAt = &now
		if err != nil {
			d.Status = types.StatusFailed
			d.Executed = false
			d.Error = err.Error()
			log.Printf("execution failed for %s (%s on %s): %v", d.ID, d.Action, d.TargetPod, err)
			return
		}
		d.Status = types.StatusExecuted
		d.Executed = true
		d.Error = ""
	})

	// Reflect the terminal state back to the caller of executeDecision.
	if updated, ok := st.GetDecision(dec.ID); ok {
		*dec = updated
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
