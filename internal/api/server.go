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
	"github.com/atulya-singh/CourtVision/internal/store"
)

// Server holds the HTTP server and its dependencies.
//
// The API is deliberately read-only: it exposes cluster snapshots, the decision
// feed, and an SSE stream so the dashboard can *observe* what the agent is doing,
// but it never mutates a cluster. Approving or rejecting a decision — the only
// paths that run a real executor — happens exclusively through the CLI
// (analyze --apply, the REPL review flow) or autonomously via
// multi-monitor --auto-safe. That keeps every mutation behind a
// locally-authenticated process holding a kubeconfig, instead of an anonymous
// browser reaching an open HTTP endpoint.
//
// It serves two shapes. In single-cluster mode (NewServer) store backs the
// classic /api/* routes. In multi-cluster mode (NewMultiServer) store backs the
// fleet-level coordinator decisions, and workers add the per-cluster
// /api/clusters/* routes. The handlers are written against an explicit store so
// both modes reuse the same rendering logic.
type Server struct {
	store   *store.Store
	workers map[string]*cluster.ClusterWorker
	order   []string // stable cluster ordering for listing
	audit   audit.Reader
	port    string
}

func NewServer(st *store.Store, port string) *Server {
	return &Server{store: st, port: port}
}

// NewMultiServer builds the API for a multi-cluster deployment. masterStore
// holds the coordinator's cross-cluster decisions; workers expose each cluster's
// own store for read-only inspection. auditReader (may be nil) backs the
// read-only /api/audit endpoints from the shared audit ring.
func NewMultiServer(workers []*cluster.ClusterWorker, masterStore *store.Store, auditReader audit.Reader, port string) *Server {
	m := make(map[string]*cluster.ClusterWorker, len(workers))
	order := make([]string, 0, len(workers))
	for _, w := range workers {
		m[w.Name()] = w
		order = append(order, w.Name())
	}
	return &Server{store: masterStore, workers: m, order: order, audit: auditReader, port: port}
}

// routes builds the full handler tree (wrapped in CORS). It is separated from
// Start so tests can exercise routing — in particular, to assert that no
// mutating route exists.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Fleet-level routes. In single-cluster mode these serve the one store; in
	// multi-cluster mode they serve the coordinator's cross-cluster decisions.
	// All are read-only.
	mux.HandleFunc("/api/cluster", s.handleCluster)
	mux.HandleFunc("/api/decisions", s.handleDecisions)
	mux.HandleFunc("/api/events", s.handleSSE)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Per-cluster routes are only registered when running with workers.
	if s.workers != nil {
		mux.HandleFunc("/api/clusters", s.handleClustersList)
		mux.HandleFunc("/api/clusters/", s.handleClusterScoped)
	}

	// The audit trail is only served when an in-memory reader is wired in
	// (multi-monitor). Single-cluster monitor is observe-only and audits nothing.
	if s.audit != nil {
		mux.HandleFunc("/api/audit", s.handleAudit)
	}

	return corsMiddleware(mux)
}

// Start registers all routes and begins listening. It returns when ctx is
// cancelled, giving in-flight requests up to 5 seconds to complete.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:    ":" + s.port,
		Handler: s.routes(),
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

// handleAudit serves GET /api/audit — the fleet-wide audit trail (every
// recorded execution and lifecycle event), newest first, read-only.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.audit.Snapshot())
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
		_, _ = w.Write([]byte(`{"pods":[],"nodes":[],"timestamp":"0001-01-01T00:00:00Z"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// renderDecisions writes a store's decisions as JSON.
func renderDecisions(w http.ResponseWriter, r *http.Request, st *store.Store) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	decisions := st.GetDecisions()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(decisions)
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

// handleClusterScoped routes the per-cluster read-only subresources:
//
//	GET /api/clusters/{cluster}/snapshot
//	GET /api/clusters/{cluster}/decisions
//	GET /api/clusters/{cluster}/events
//	GET /api/clusters/{cluster}/audit
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
	case parts[1] == "audit" && len(parts) == 2 && s.audit != nil:
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, s.audit.SnapshotForCluster(parts[0]))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
