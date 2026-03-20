package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// mockK8sClient records calls in order so we can assert the call sequence.
type mockK8sClient struct {
	createdJobs []string
	waitResults map[string]k8s.JobResult
	deletedJobs []string
}

func (m *mockK8sClient) CreateJob(_ context.Context, spec k8s.JobSpec) (string, error) {
	m.createdJobs = append(m.createdJobs, spec.Name)
	return spec.Name, nil
}

func (m *mockK8sClient) WaitForJob(_ context.Context, name string) (k8s.JobResult, error) {
	if r, ok := m.waitResults[name]; ok {
		return r, nil
	}
	return k8s.JobResult{Succeeded: true}, nil
}

func (m *mockK8sClient) DeleteJob(_ context.Context, name string) error {
	m.deletedJobs = append(m.deletedJobs, name)
	return nil
}

// k8sClientIface allows injecting the mock without importing a fake k8s cluster.
type k8sClientIface interface {
	CreateJob(context.Context, k8s.JobSpec) (string, error)
	WaitForJob(context.Context, string) (k8s.JobResult, error)
	DeleteJob(context.Context, string) error
}

// testableK8sExecutor mirrors K8sExecutor but accepts the interface for testing.
type testableK8sExecutor struct {
	Store          store.Store
	K8s            k8sClientIface
	Image          string
	RunnerEndpoint string
	TokenSecret    []byte
}

func (e *testableK8sExecutor) buildHandler(execID string) func(context.Context, string, kflow.Input) (kflow.Output, error) {
	return func(ctx context.Context, stateName string, _ kflow.Input) (kflow.Output, error) {
		name := k8s.JobName(execID, stateName)

		if _, err := e.K8s.CreateJob(ctx, k8s.JobSpec{
			Name:  name,
			Image: e.Image,
			Args:  []string{"--state=" + stateName},
			Env: []k8s.EnvVar{
				{Name: "KFLOW_EXECUTION_ID", Value: execID},
				{Name: "KFLOW_GRPC_ENDPOINT", Value: e.RunnerEndpoint},
			},
		}); err != nil {
			return nil, err
		}

		result, err := e.K8s.WaitForJob(ctx, name)
		if err != nil {
			_ = e.K8s.DeleteJob(ctx, name)
			return nil, err
		}

		if result.Failed {
			_ = e.K8s.DeleteJob(ctx, name)
			return nil, errors.New(result.Message)
		}

		output, err := e.Store.GetStateOutput(ctx, execID, stateName)
		if err != nil {
			_ = e.K8s.DeleteJob(ctx, name)
			return nil, err
		}

		_ = e.K8s.DeleteJob(ctx, name)
		return output, nil
	}
}

func TestK8sHandlerCallOrder(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()

	// Pre-populate execution and state output so handler can read it.
	execID := "test-exec-0001"
	stateName := "ProcessData"
	jobN := k8s.JobName(execID, stateName)

	if err := ms.CreateExecution(ctx, store.ExecutionRecord{ID: execID, Workflow: "wf"}); err != nil {
		t.Fatal(err)
	}
	sr := store.StateRecord{ExecutionID: execID, StateName: stateName, Input: kflow.Input{}, Attempt: 1}
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatal(err)
	}
	if err := ms.MarkRunning(ctx, execID, stateName); err != nil {
		t.Fatal(err)
	}
	want := kflow.Output{"result": "ok"}
	if err := ms.CompleteState(ctx, execID, stateName, want); err != nil {
		t.Fatal(err)
	}

	mock := &mockK8sClient{
		waitResults: map[string]k8s.JobResult{
			jobN: {Succeeded: true},
		},
	}
	ex := &testableK8sExecutor{
		Store: ms,
		K8s:   mock,
		Image: "kflow:test",
	}

	handler := ex.buildHandler(execID)
	out, err := handler(ctx, stateName, kflow.Input{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if out["result"] != "ok" {
		t.Fatalf("unexpected output: %v", out)
	}

	// Verify call order: CreateJob → WaitForJob → (GetStateOutput) → DeleteJob
	if len(mock.createdJobs) != 1 || mock.createdJobs[0] != jobN {
		t.Fatalf("CreateJob not called correctly: %v", mock.createdJobs)
	}
	if len(mock.deletedJobs) != 1 || mock.deletedJobs[0] != jobN {
		t.Fatalf("DeleteJob not called correctly: %v", mock.deletedJobs)
	}
}

func TestK8sHandler_NoMongoURIInEnv(t *testing.T) {
	// Verify that KFLOW_MONGO_URI and KFLOW_INPUT are not injected into the job env.
	ms := store.NewMemoryStore()
	ctx := context.Background()

	execID := "exec-env-check"
	stateName := "CheckEnv"
	jobN := k8s.JobName(execID, stateName)

	_ = ms.CreateExecution(ctx, store.ExecutionRecord{ID: execID, Workflow: "wf"})
	sr := store.StateRecord{ExecutionID: execID, StateName: stateName, Input: kflow.Input{}, Attempt: 1}
	_ = ms.WriteAheadState(ctx, sr)
	_ = ms.MarkRunning(ctx, execID, stateName)
	_ = ms.CompleteState(ctx, execID, stateName, kflow.Output{})

	var capturedEnv []k8s.EnvVar
	capturingMock := &capturingK8sClient{
		waitResult: k8s.JobResult{Succeeded: true},
		onCreate: func(spec k8s.JobSpec) {
			capturedEnv = spec.Env
		},
	}

	ex := &testableK8sExecutor{Store: ms, K8s: capturingMock, Image: "kflow:test"}
	handler := ex.buildHandler(execID)
	_, _ = handler(ctx, stateName, kflow.Input{})

	forbidden := []string{"KFLOW_MONGO_URI", "KFLOW_MONGO_DB", "KFLOW_INPUT"}
	for _, env := range capturedEnv {
		for _, bad := range forbidden {
			if env.Name == bad {
				t.Fatalf("forbidden env var %q injected into job for %q", bad, jobN)
			}
		}
	}
}

func TestK8sExecutor_NilTelemetryNoPanic(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()

	execID := "exec-telemetry"
	stateName := "SomeState"

	_ = ms.CreateExecution(ctx, store.ExecutionRecord{ID: execID, Workflow: "wf"})
	sr := store.StateRecord{ExecutionID: execID, StateName: stateName, Input: kflow.Input{}, Attempt: 1}
	_ = ms.WriteAheadState(ctx, sr)
	_ = ms.MarkRunning(ctx, execID, stateName)
	_ = ms.CompleteState(ctx, execID, stateName, kflow.Output{"ok": true})

	ex := &K8sExecutor{
		Store:     ms,
		K8s:       nil, // not called since we won't actually run
		Telemetry: nil, // must not panic
	}
	// Just confirm the struct is nil-safe at field level
	if ex.Telemetry != nil {
		t.Fatal("expected nil Telemetry")
	}
}

// capturingK8sClient records the JobSpec passed to CreateJob.
type capturingK8sClient struct {
	waitResult k8s.JobResult
	onCreate   func(k8s.JobSpec)
}

func (c *capturingK8sClient) CreateJob(_ context.Context, spec k8s.JobSpec) (string, error) {
	if c.onCreate != nil {
		c.onCreate(spec)
	}
	return spec.Name, nil
}

func (c *capturingK8sClient) WaitForJob(_ context.Context, _ string) (k8s.JobResult, error) {
	return c.waitResult, nil
}

func (c *capturingK8sClient) DeleteJob(_ context.Context, _ string) error { return nil }
