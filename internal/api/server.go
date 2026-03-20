// Package api implements the HTTP Control Plane server.
// Routes are registered using Go 1.22's enhanced net/http ServeMux.
// Auth middleware and grpc-gateway wiring are added in Phase 11 and 13.
package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/pastorenue/kflow/internal/controller"
	k8sclient "github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// ExecutionTrigger is called asynchronously after an ExecutionRecord is created.
// It drives the actual workflow execution (e.g. K8sExecutor.Run).
type ExecutionTrigger func(execID, wfName string, input kflow.Input)

// Server is the HTTP Control Plane server.
type Server struct {
	Store      store.Store
	K8s        *k8sclient.Client
	Hub        *WSHub
	Dispatcher *controller.ServiceDispatcher
	Telemetry  *telemetry.Client // nil = telemetry disabled

	// Workflows is the set of workflow names registered with this server.
	Workflows []string

	// Trigger is called asynchronously when POST /workflows/:name/run is invoked.
	// May be nil (execution record is created but not driven).
	Trigger ExecutionTrigger

	// APIKey is the shared bearer token. Empty = auth disabled (dev mode).
	APIKey string

	graphs   map[string]workflowGraph
	graphsMu sync.RWMutex

	ready   atomic.Bool
	mux     *http.ServeMux
	handler http.Handler
}

// NewServer creates a Server and registers all routes.
func NewServer(
	st store.Store,
	k8s *k8sclient.Client,
	hub *WSHub,
	disp *controller.ServiceDispatcher,
	workflows []string,
	trigger ExecutionTrigger,
	apiKey string,
) *Server {
	s := &Server{
		Store:      st,
		K8s:        k8s,
		Hub:        hub,
		Dispatcher: disp,
		Workflows:  workflows,
		Trigger:    trigger,
		APIKey:     apiKey,
		graphs:     make(map[string]workflowGraph),
		mux:        http.NewServeMux(),
	}
	s.registerRoutes()
	s.handler = BearerAuthMiddleware(s.APIKey)(s.mux)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	s.handler.ServeHTTP(w, r)
}

// MarkReady signals that the server is ready to serve traffic.
func (s *Server) MarkReady() { s.ready.Store(true) }

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)

	s.mux.HandleFunc("GET /api/v1/workflows", s.handleListWorkflows)
	s.mux.HandleFunc("POST /api/v1/workflows", s.handleRegisterWorkflow)
	s.mux.HandleFunc("GET /api/v1/workflows/{name}", s.handleGetWorkflow)
	s.mux.HandleFunc("POST /api/v1/workflows/{name}/run", s.handleRunWorkflow)

	s.mux.HandleFunc("GET /api/v1/executions", s.handleListExecutions)
	s.mux.HandleFunc("GET /api/v1/executions/{id}", s.handleGetExecution)
	s.mux.HandleFunc("GET /api/v1/executions/{id}/states", s.handleListStates)

	s.mux.HandleFunc("GET /api/v1/services", s.handleListServices)
	s.mux.HandleFunc("POST /api/v1/services", s.handleCreateService)
	s.mux.HandleFunc("GET /api/v1/services/{name}", s.handleGetService)
	s.mux.HandleFunc("DELETE /api/v1/services/{name}", s.handleDeleteService)

	s.mux.HandleFunc("GET /api/v1/ws", s.Hub.ServeWS)

	s.mux.HandleFunc("POST /api/v1/auth/token", s.handleAuthToken)

	// Telemetry endpoints (no-op when s.Telemetry == nil)
	s.mux.HandleFunc("GET /api/v1/executions/{id}/events", s.handleListEvents)
	s.mux.HandleFunc("GET /api/v1/services/{name}/metrics", s.handleListMetrics)
	s.mux.HandleFunc("GET /api/v1/logs", s.handleListLogs)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeError(w, http.StatusServiceUnavailable, "not ready", "not_ready")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"workflows": s.Workflows})
}

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.workflowRegistered(name) {
		writeError(w, http.StatusNotFound, "workflow not found", "not_found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var input kflow.Input
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}

	execID, err := genUUID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate execution ID", "internal")
		return
	}

	rec := store.ExecutionRecord{ID: execID, Workflow: name, Input: input}
	if err := s.Store.CreateExecution(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create execution", "internal")
		return
	}

	if s.Trigger != nil {
		go s.Trigger(execID, name, input)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"execution_id": execID})
}

func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.ExecutionFilter{
		Workflow: q.Get("workflow"),
		Status:   q.Get("status"),
		Limit:    parseIntParam(q.Get("limit"), 50),
		Offset:   parseIntParam(q.Get("offset"), 0),
	}
	execs, err := s.Store.ListExecutions(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list executions", "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": execs})
}

func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Store.GetExecution(r.Context(), r.PathValue("id"))
	if err == store.ErrExecutionNotFound {
		writeError(w, http.StatusNotFound, "execution not found", "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get execution", "internal")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleListStates(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetExecution(r.Context(), id); err == store.ErrExecutionNotFound {
		writeError(w, http.StatusNotFound, "execution not found", "not_found")
		return
	}
	states, err := s.Store.ListStates(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list states", "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"states": states})
}

func (s *Server) workflowRegistered(name string) bool {
	for _, wf := range s.Workflows {
		if wf == name {
			return true
		}
	}
	s.graphsMu.RLock()
	_, ok := s.graphs[name]
	s.graphsMu.RUnlock()
	return ok
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg, errCode string) {
	writeJSON(w, code, map[string]string{"error": msg, "code": errCode})
}

func parseIntParam(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func genUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
