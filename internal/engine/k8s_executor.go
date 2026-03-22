package engine

import (
	"context"
	"fmt"
	"log"

	"github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/runner"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// K8sExecutor drives workflow execution by dispatching each state as a
// Kubernetes Job. It wraps Executor with a K8s-backed Handler.
type K8sExecutor struct {
	Store             store.Store
	K8s               *k8s.Client
	Image             string
	RunnerEndpoint    string // KFLOW_GRPC_ENDPOINT injected into Job containers
	RunnerTokenSecret []byte // HMAC key for state token signing
	Telemetry         *telemetry.EventWriter                              // nil = no telemetry
	LogWriter         *telemetry.LogWriter                                // nil = no log capture
	Notify            func(execID, stateName, fromStatus, toStatus, errMsg string) // nil = no WS broadcast
}

// Run drives a full workflow execution using K8s Jobs.
func (e *K8sExecutor) Run(ctx context.Context, execID string, g *Graph, input kflow.Input) error {
	ex := &Executor{
		Store:   e.Store,
		Handler: e.buildHandler(execID),
	}
	return ex.Run(ctx, execID, g, input)
}

// buildHandler returns a HandlerFunc that spawns a K8s Job for each state.
func (e *K8sExecutor) buildHandler(execID string) func(context.Context, string, kflow.Input) (kflow.Output, error) {
	return func(ctx context.Context, stateName string, _ kflow.Input) (kflow.Output, error) {
		name := k8s.JobName(execID, stateName)

		tok, err := runner.GenerateStateToken(execID, stateName, 1, e.RunnerTokenSecret)
		if err != nil {
			return nil, fmt.Errorf("k8s_executor: generate token for %q: %w", stateName, err)
		}

		if e.Telemetry != nil {
			e.Telemetry.RecordStateTransition(ctx, execID, stateName, string(store.StatusPending), string(store.StatusRunning), "")
		}
		if e.Notify != nil {
			e.Notify(execID, stateName, string(store.StatusPending), string(store.StatusRunning), "")
		}

		_, err = e.K8s.CreateJob(ctx, k8s.JobSpec{
			Name:  name,
			Image: e.Image,
			Args:  []string{"--state=" + stateName},
			Env: []k8s.EnvVar{
				{Name: "KFLOW_EXECUTION_ID", Value: execID},
				{Name: "KFLOW_STATE_TOKEN", Value: tok},
				{Name: "KFLOW_GRPC_ENDPOINT", Value: e.RunnerEndpoint},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("k8s_executor: create job for %q: %w", stateName, err)
		}
		log.Printf("k8s_executor: [%s] job %q created for state %q", execID, name, stateName)

		log.Printf("k8s_executor: [%s] waiting for job %q", execID, name)
		result, err := e.K8s.WaitForJob(ctx, name)
		if err != nil {
			e.deleteJobBestEffort(ctx, name)
			return nil, fmt.Errorf("k8s_executor: wait for job %q: %w", name, err)
		}

		// Capture container logs regardless of outcome (best-effort).
		if e.LogWriter != nil {
			telemetry.StreamJobLogs(ctx, e.K8s.Clientset(), e.K8s.Namespace(), name, execID, stateName, e.LogWriter)
		}

		if result.Failed {
			log.Printf("k8s_executor: [%s] job %q failed: %s", execID, name, result.Message)
			e.deleteJobBestEffort(ctx, name)
			if e.Telemetry != nil {
				e.Telemetry.RecordStateTransition(ctx, execID, stateName, string(store.StatusRunning), string(store.StatusFailed), result.Message)
			}
			if e.Notify != nil {
				e.Notify(execID, stateName, string(store.StatusRunning), string(store.StatusFailed), result.Message)
			}
			return nil, fmt.Errorf("k8s_executor: job for %q failed: %s", stateName, result.Message)
		}
		log.Printf("k8s_executor: [%s] job %q succeeded", execID, name)

		output, err := e.Store.GetStateOutput(ctx, execID, stateName)
		if err != nil {
			e.deleteJobBestEffort(ctx, name)
			return nil, fmt.Errorf("k8s_executor: get output for %q: %w", stateName, err)
		}

		e.deleteJobBestEffort(ctx, name)
		if e.Telemetry != nil {
			e.Telemetry.RecordStateTransition(ctx, execID, stateName, string(store.StatusRunning), string(store.StatusCompleted), "")
		}
		if e.Notify != nil {
			e.Notify(execID, stateName, string(store.StatusRunning), string(store.StatusCompleted), "")
		}
		return output, nil
	}
}

func (e *K8sExecutor) deleteJobBestEffort(ctx context.Context, name string) {
	if err := e.K8s.DeleteJob(ctx, name); err != nil {
		log.Printf("k8s_executor: delete job %q (best-effort): %v", name, err)
	}
}
