package kflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Run registers the workflow with the Control Plane and triggers execution.
// Reads KFLOW_SERVER (default http://localhost:8080) and KFLOW_API_KEY from env.
func Run(wf *Workflow) error {
	if err := wf.Validate(); err != nil {
		return fmt.Errorf("invalid workflow: %w", err)
	}
	server := os.Getenv("KFLOW_SERVER")
	if server == "" {
		server = "http://localhost:8080"
	}
	apiKey := os.Getenv("KFLOW_API_KEY")
	if err := registerWorkflow(server, apiKey, wf); err != nil {
		return err
	}
	return triggerExecution(server, apiKey, wf.Name(), Input{})
}

// RunLocal executes the workflow in-process using an in-memory executor (no Kubernetes).
func RunLocal(wf *Workflow, input Input) error {
	if err := wf.Validate(); err != nil {
		return fmt.Errorf("invalid workflow: %w", err)
	}
	g, err := buildLocalGraph(wf)
	if err != nil {
		return err
	}
	return runLocalGraph(context.Background(), g, input)
}

// RunService registers a Service with the Control Plane.
// Reads KFLOW_SERVER (default http://localhost:8080) and KFLOW_API_KEY from env.
func RunService(svc *ServiceDef) error {
	if err := svc.Validate(); err != nil {
		return fmt.Errorf("invalid service: %w", err)
	}
	server := os.Getenv("KFLOW_SERVER")
	if server == "" {
		server = "http://localhost:8080"
	}
	return registerService(server, os.Getenv("KFLOW_API_KEY"), svc)
}

func registerService(server, apiKey string, svc *ServiceDef) error {
	mode := "deployment"
	if svc.ServiceMode() == Lambda {
		mode = "lambda"
	}
	body, _ := json.Marshal(map[string]any{
		"name":           svc.Name(),
		"mode":           mode,
		"port":           svc.ServicePort(),
		"minScale":       svc.MinScale(),
		"maxScale":       svc.MaxScale(),
		"ingressHost":    svc.IngressHost(),
		"timeoutSeconds": int64(svc.ServiceTimeout().Seconds()),
	})
	req, err := http.NewRequest(http.MethodPost, server+"/api/v1/services", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("register service: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("register service: %w", err)
	}
	defer resp.Body.Close()
	// grpc-gateway: 200 OK on success; 409 AlreadyExists → treat as OK
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusConflict {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("register service: server returned %d: %s", resp.StatusCode, raw)
}

func registerWorkflow(server, apiKey string, wf *Workflow) error {
	g := toRegisterJSON(wf)
	body, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("marshal workflow graph: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, server+"/api/v1/workflows", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build register request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("register workflow: %w", err)
	}
	defer resp.Body.Close()

	// grpc-gateway returns 200 OK for successful unary calls; also accept 201/409.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusConflict {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("register workflow: server returned %d: %s", resp.StatusCode, raw)
}

func triggerExecution(server, apiKey, name string, input Input) error {
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, server+"/api/v1/workflows/"+name+"/run", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build run request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("trigger execution: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("trigger execution: server returned %d: %s", resp.StatusCode, raw)
	}
	return nil
}
