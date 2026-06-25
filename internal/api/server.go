package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// Server holds the HTTP server and its dependencies
type Server struct {
	store *store.Store
	exec  executor.Executor
	port  string
}

func NewServer(st *store.Store, exec executor.Executor, port string) *Server {
	return &Server{store: st, exec: exec, port: port}
}

// Start registers all routes and begins listening. It returns when ctx is
// cancelled, giving in-flight requests up to 5 seconds to complete.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/cluster", s.handleCluster)
	mux.HandleFunc("/api/decisions", s.handleDecisions)
	mux.HandleFunc("/api/events", s.handleSSE)
	mux.HandleFunc("/api/decisions/", s.handleDecisionAction)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := s.store.GetSnapshot()
	if snap == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pods":[],"nodes":[],"timestamp":"0001-01-01T00:00:00Z"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	decisions := s.store.GetDecisions()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decisions)
}
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.store.Subscribe()
	defer s.store.Unsubscribe(ch)

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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse: /api/decisions/{id}/approve or /api/decisions/{id}/reject
	path := strings.TrimPrefix(r.URL.Path, "/api/decisions/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	id := parts[0]
	action := parts[1]

	if action != "approve" && action != "reject" {
		http.Error(w, "action must be 'approve' or 'reject'", http.StatusBadRequest)
		return
	}

	dec, found := s.store.GetDecision(id)
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
		s.store.UpdateAndBroadcast(id, func(d *types.Decision) {
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
	s.executeDecision(&dec)
	writeJSON(w, map[string]string{"status": string(dec.Status), "error": dec.Error})
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
func (s *Server) executeDecision(dec *types.Decision) {
	s.store.UpdateAndBroadcast(dec.ID, func(d *types.Decision) {
		d.Status = types.StatusExecuting
	})

	execCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := s.exec.Execute(execCtx, dec)
	now := time.Now()

	s.store.UpdateAndBroadcast(dec.ID, func(d *types.Decision) {
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
	if updated, ok := s.store.GetDecision(dec.ID); ok {
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
