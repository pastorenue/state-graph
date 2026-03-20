package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	ms := store.NewMemoryStore()
	hub := NewWSHub()
	srv := NewServer(ms, nil, hub, nil, []string{"order-workflow"}, nil)
	srv.MarkReady()
	return srv
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: got %d", rec.Code)
	}
}

func TestReadyz_Ready(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz ready: got %d", rec.Code)
	}
}

func TestReadyz_NotReady(t *testing.T) {
	ms := store.NewMemoryStore()
	srv := NewServer(ms, nil, NewWSHub(), nil, nil, nil)
	// not marked ready
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz not-ready: got %d", rec.Code)
	}
}

func TestRunWorkflow_CreatesExecution(t *testing.T) {
	srv := newTestServer(t)

	body, _ := json.Marshal(kflow.Input{"key": "val"})
	req := httptest.NewRequest("POST", "/api/v1/workflows/order-workflow/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("run workflow: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["execution_id"] == "" {
		t.Fatal("expected non-empty execution_id")
	}
}

func TestRunWorkflow_UnknownWorkflow(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(kflow.Input{})
	req := httptest.NewRequest("POST", "/api/v1/workflows/unknown/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestListExecutions(t *testing.T) {
	srv := newTestServer(t)

	body, _ := json.Marshal(kflow.Input{})
	req := httptest.NewRequest("POST", "/api/v1/workflows/order-workflow/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/executions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list executions: got %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	execs, _ := resp["executions"].([]any)
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(execs))
	}
}

func TestGetExecution_NotFound(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/executions/nonexistent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreateService_Conflict(t *testing.T) {
	srv := newTestServer(t)

	svc := store.ServiceRecord{Name: "my-svc", Port: 8080}
	body, _ := json.Marshal(svc)

	req1 := httptest.NewRequest("POST", "/api/v1/services", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create: got %d", rec1.Code)
	}

	body2, _ := json.Marshal(svc)
	req2 := httptest.NewRequest("POST", "/api/v1/services", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("duplicate create: expected 409, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var errResp map[string]string
	_ = json.Unmarshal(rec2.Body.Bytes(), &errResp)
	if errResp["code"] != "name_collision" {
		t.Fatalf("expected name_collision code, got %q", errResp["code"])
	}
}

func TestDeleteService_NotFound(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/services/ghost", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options header")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing X-Frame-Options header")
	}
}
