package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pastorenue/kflow/internal/api"
)

func newTestServer() *api.Server {
	return api.NewServer(nil, nil, api.NewWSHub(), nil, nil, nil, "")
}

func registerGraph(t *testing.T, srv *api.Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func getGraph(t *testing.T, srv *api.Server, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/"+name, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

const validGraphJSON = `{
	"name": "my-workflow",
	"states": [{"name": "step-a", "type": "task"}],
	"flow": [{"name": "step-a", "is_end": true}]
}`

func TestRegisterWorkflow_Valid(t *testing.T) {
	srv := newTestServer()
	rr := registerGraph(t, srv, validGraphJSON)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["name"] != "my-workflow" {
		t.Errorf("expected name=my-workflow, got %v", resp)
	}
}

func TestRegisterWorkflow_Duplicate(t *testing.T) {
	srv := newTestServer()
	registerGraph(t, srv, validGraphJSON)
	rr := registerGraph(t, srv, validGraphJSON)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRegisterWorkflow_InvalidName(t *testing.T) {
	srv := newTestServer()
	body := `{"name":"INVALID_NAME","states":[{"name":"s","type":"task"}],"flow":[{"name":"s","is_end":true}]}`
	rr := registerGraph(t, srv, body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["code"] != "invalid_graph" {
		t.Errorf("expected code=invalid_graph, got %v", resp)
	}
}

func TestRegisterWorkflow_DuplicateStateNames(t *testing.T) {
	srv := newTestServer()
	body := `{"name":"wf","states":[{"name":"s","type":"task"},{"name":"s","type":"task"}],"flow":[{"name":"s","is_end":true}]}`
	rr := registerGraph(t, srv, body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["code"] != "invalid_graph" {
		t.Errorf("expected code=invalid_graph, got %v", resp)
	}
}

func TestRegisterWorkflow_FlowUnknownState(t *testing.T) {
	srv := newTestServer()
	body := `{"name":"wf","states":[{"name":"s","type":"task"}],"flow":[{"name":"unknown","is_end":true}]}`
	rr := registerGraph(t, srv, body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["code"] != "invalid_graph" {
		t.Errorf("expected code=invalid_graph, got %v", resp)
	}
}

func TestGetWorkflow_Found(t *testing.T) {
	srv := newTestServer()
	registerGraph(t, srv, validGraphJSON)
	rr := getGraph(t, srv, "my-workflow")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var g map[string]any
	json.NewDecoder(rr.Body).Decode(&g)
	if g["name"] != "my-workflow" {
		t.Errorf("expected name=my-workflow in response, got %v", g)
	}
}

func TestGetWorkflow_NotFound(t *testing.T) {
	srv := newTestServer()
	rr := getGraph(t, srv, "does-not-exist")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}
