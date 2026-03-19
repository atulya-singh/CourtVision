package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/atulya-singh/CourtVision/internal/store"
)

// Server holds the HTTP server and its dependencies
type Server struct {
	store *store.Store
	port  string
}

func NewServer(st *store.Store, port string) *Server {
	return &Server{store: st, port: port}
}

// Start registers all routes and begins listening
func (s *Server) Start() error {
	mux := http.NewServeMux()

	//API routes
	mux.HandleFunc("/api/cluster", s.handleCluster)
	mux.HandleFunc("/api/decisions", s.handleDecisions)
	mux.HandleFunc("/api/events", s.handleSSE)

	// Health check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	handler := corsMiddleware(mux)
	log.Printf("API server starting on :%s", s.port)
	return http.ListenAndServe(":"+s.port, handler)
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

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {}
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request)       {}
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
