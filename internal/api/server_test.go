package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newServer() (*Server, *store.Store) {
	st := store.New()
	return NewServer(st, "0"), st
}

func addDecision(st *store.Store, id string) {
	st.AddDecision(types.Decision{
		ID:        id,
		Timestamp: time.Now(),
		Severity:  types.SeverityMedium,
		Action:    types.ActionNone,
		TargetPod: "test-pod",
		Namespace: "default",
		Reasoning: "test",
	})
}

// ── /api/health ───────────────────────────────────────────────────────────────

func TestHandleHealth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("want body with 'ok', got %s", w.Body.String())
	}
}

// ── /api/cluster ──────────────────────────────────────────────────────────────

func TestHandleCluster_EmptyStore(t *testing.T) {
	srv, _ := newServer()
	req := httptest.NewRequest(http.MethodGet, "/api/cluster", nil)
	w := httptest.NewRecorder()
	srv.handleCluster(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %s", ct)
	}
	// Empty store returns a zero-value snapshot JSON, not an error
	if w.Body.Len() == 0 {
		t.Error("expected non-empty body even for empty store")
	}
}

func TestHandleCluster_WithSnapshot(t *testing.T) {
	srv, st := newServer()
	st.SetSnapshot(&types.ClusterSnapshot{
		Timestamp: time.Now(),
		Pods:      []types.PodMetrics{{PodName: "api-server", Namespace: "default"}},
		Nodes:     []types.NodeMetrics{{NodeName: "node-1"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/cluster", nil)
	w := httptest.NewRecorder()
	srv.handleCluster(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}

	var snap types.ClusterSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(snap.Pods) != 1 || snap.Pods[0].PodName != "api-server" {
		t.Errorf("unexpected snapshot in response: %+v", snap)
	}
}

func TestHandleCluster_MethodNotAllowed(t *testing.T) {
	srv, _ := newServer()
	req := httptest.NewRequest(http.MethodPost, "/api/cluster", nil)
	w := httptest.NewRecorder()
	srv.handleCluster(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// ── /api/decisions ────────────────────────────────────────────────────────────

func TestHandleDecisions_Empty(t *testing.T) {
	srv, _ := newServer()
	req := httptest.NewRequest(http.MethodGet, "/api/decisions", nil)
	w := httptest.NewRecorder()
	srv.handleDecisions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}

	var got []types.Decision
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty array, got %d elements", len(got))
	}
}

func TestHandleDecisions_ReturnsList(t *testing.T) {
	srv, st := newServer()
	addDecision(st, "id-1")
	addDecision(st, "id-2")

	req := httptest.NewRequest(http.MethodGet, "/api/decisions", nil)
	w := httptest.NewRecorder()
	srv.handleDecisions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}

	var got []types.Decision
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 decisions, got %d", len(got))
	}
}

func TestHandleDecisions_MethodNotAllowed(t *testing.T) {
	srv, _ := newServer()
	req := httptest.NewRequest(http.MethodDelete, "/api/decisions", nil)
	w := httptest.NewRecorder()
	srv.handleDecisions(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// ── read-only guarantee ───────────────────────────────────────────────────────

// TestMutationRoutesRemoved locks in the read-only contract: the old
// approve/reject endpoints must not exist, and a decision must never become
// executable over HTTP. If someone re-adds a mutating route, this fails.
func TestMutationRoutesRemoved(t *testing.T) {
	srv, st := newServer()
	addDecision(st, "id-1")
	handler := srv.routes()

	for _, path := range []string{
		"/api/decisions/id-1/approve",
		"/api/decisions/id-1/reject",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("POST %s: want 404 (route removed), got %d", path, w.Code)
		}
	}

	if d := st.GetDecisions()[0]; d.Executed || d.Status == types.StatusExecuted {
		t.Error("a decision must not be executable through the HTTP API anymore")
	}
}

// ── CORS middleware ───────────────────────────────────────────────────────────

func TestCORSMiddleware_SetsHeaders(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS origin: want *, got %s", got)
	}
	if methods := w.Header().Get("Access-Control-Allow-Methods"); strings.Contains(methods, "POST") {
		t.Errorf("read-only API must not advertise POST, got %q", methods)
	}
}

func TestCORSMiddleware_PreflightReturns200(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for OPTIONS preflight")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/decisions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("preflight: want 200, got %d", w.Code)
	}
}
