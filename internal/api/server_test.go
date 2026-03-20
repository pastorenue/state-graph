package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

func encodeB64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerWithKey(t, "")
}

func newTestServerWithKey(t *testing.T, apiKey string) *Server {
	t.Helper()
	ms := store.NewMemoryStore()
	hub := NewWSHub()
	srv := NewServer(ms, nil, hub, nil, []string{"order-workflow"}, nil, apiKey)
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
	srv := NewServer(ms, nil, NewWSHub(), nil, nil, nil, "")
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

	if rec.Code != http.StatusAccepted {
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

// ---- Auth tests ----

const testAPIKey = "super-secret-api-key-for-testing"

func TestAuthMiddleware_DevMode(t *testing.T) {
	srv := newTestServer(t) // apiKey == ""
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/executions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dev mode: expected 200, got %d", rec.Code)
	}
}

func TestAuthMiddleware_Unauthorized(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/executions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}

func TestAuthMiddleware_RawKey(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)
	req := httptest.NewRequest("GET", "/api/v1/executions", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw key: expected 200, got %d", rec.Code)
	}
}

func TestAuthToken_ValidKey(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)
	body, _ := json.Marshal(map[string]string{"api_key": testAPIKey})
	req := httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["token"] == "" {
		t.Fatal("expected non-empty token")
	}
}

func TestAuthToken_InvalidKey(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)
	body, _ := json.Marshal(map[string]string{"api_key": "wrong-key"})
	req := httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: expected 401, got %d", rec.Code)
	}
}

func TestAuthMiddleware_SessionToken(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)

	// Obtain a session token.
	body, _ := json.Marshal(map[string]string{"api_key": testAPIKey})
	req := httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var tokenResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &tokenResp)
	token := tokenResp["token"]

	// Use session token on protected route.
	req2 := httptest.NewRequest("GET", "/api/v1/executions", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("session token: expected 200, got %d", rec2.Code)
	}
}

func TestAuthMiddleware_ExpiredToken(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)

	// Craft an expired token manually.
	expired := tokenPayload{
		IssuedAt:  time.Now().Add(-48 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	payloadBytes, _ := json.Marshal(expired)
	payloadEnc := encodeB64(payloadBytes)
	sig := signPayload(payloadBytes, testAPIKey)
	sigEnc := encodeB64(sig)
	token := payloadEnc + "." + sigEnc

	req := httptest.NewRequest("GET", "/api/v1/executions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: expected 401, got %d", rec.Code)
	}
}

func TestHealthz_NoAuth(t *testing.T) {
	srv := newTestServerWithKey(t, testAPIKey)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz without token: expected 200, got %d", rec.Code)
	}
}
