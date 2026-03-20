// Package controller implements application services that orchestrate
// infrastructure components. It must NOT import internal/api.
package controller

import (
	"context"
	"fmt"

	"github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/runner"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// Metrics is the interface ServiceDispatcher uses for recording dispatch events.
// Satisfied by telemetry.MetricsWriter (Phase 6). Nil disables metrics.
type Metrics interface {
	RecordDispatch(ctx context.Context, serviceName, execID, stateName string, status string)
}

// ServiceDispatcher routes InvokeService steps to the appropriate K8s resource.
// Write-ahead is the caller's responsibility (Executor). Dispatch must NOT call
// WriteAheadState or MarkRunning directly.
type ServiceDispatcher struct {
	Store             store.Store
	K8s               *k8s.Client
	Image             string // container image injected into Lambda Job containers
	RunnerEndpoint    string // KFLOW_GRPC_ENDPOINT for Lambda containers
	RunnerTokenSecret []byte
	ServiceGRPCPort   int // KFLOW_SERVICE_GRPC_PORT (default 9091)
	Metrics           Metrics
}

// Dispatch executes an InvokeService step for the given (execID, stateName) pair.
//
// For Deployment-mode services: dials ClusterIP:ServiceGRPCPort and calls
// ServiceRunnerService.Invoke (Phase 13). Currently stubbed.
//
// For Lambda-mode services: creates a K8s Job with --service=<serviceName>,
// waits for completion, and reads output from the store.
func (d *ServiceDispatcher) Dispatch(
	ctx context.Context,
	execID, stateName, serviceName string,
	input kflow.Input,
) (kflow.Output, error) {
	rec, err := d.Store.GetService(ctx, serviceName)
	if err == store.ErrServiceNotFound {
		return nil, fmt.Errorf("dispatcher: service %q not found", serviceName)
	}
	if err != nil {
		return nil, fmt.Errorf("dispatcher: get service %q: %w", serviceName, err)
	}
	if rec.Status != store.ServiceStatusRunning {
		return nil, fmt.Errorf("dispatcher: service %q is not Running (status=%s)", serviceName, rec.Status)
	}

	switch rec.Mode {
	case kflow.Deployment:
		return d.dispatchDeployment(ctx, execID, stateName, serviceName, rec, input)
	case kflow.Lambda:
		return d.dispatchLambda(ctx, execID, stateName, serviceName, input)
	default:
		return nil, fmt.Errorf("dispatcher: unknown service mode %d", rec.Mode)
	}
}

// dispatchDeployment calls ServiceRunnerService.Invoke via gRPC on the service's
// ClusterIP. The proto/gRPC protocol is defined in Phase 13.
func (d *ServiceDispatcher) dispatchDeployment(
	ctx context.Context,
	execID, stateName, serviceName string,
	rec store.ServiceRecord,
	_ kflow.Input,
) (kflow.Output, error) {
	port := d.ServiceGRPCPort
	if port == 0 {
		port = 9091
	}
	// TODO(Phase 13): dial rec.ClusterIP:port, call ServiceRunnerServiceClient.Invoke.
	_ = rec.ClusterIP
	_ = port
	return nil, fmt.Errorf("dispatcher: deployment gRPC dispatch not yet implemented (Phase 13): service=%s execID=%s state=%s",
		serviceName, execID, stateName)
}

// dispatchLambda creates a K8s Job with --service=<serviceName>, waits for
// completion, then reads the output from the store (written by RunnerServiceServer).
func (d *ServiceDispatcher) dispatchLambda(
	ctx context.Context,
	execID, stateName, serviceName string,
	_ kflow.Input,
) (kflow.Output, error) {
	jobN := k8s.JobName(execID, stateName)

	tok, err := runner.GenerateStateToken(execID, stateName, 1, d.RunnerTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: generate token for %q: %w", stateName, err)
	}

	if _, err := d.K8s.CreateJob(ctx, k8s.JobSpec{
		Name:  jobN,
		Image: d.Image,
		Args:  []string{"--service=" + serviceName},
		Env: []k8s.EnvVar{
			{Name: "KFLOW_EXECUTION_ID", Value: execID},
			{Name: "KFLOW_STATE_TOKEN", Value: tok},
			{Name: "KFLOW_GRPC_ENDPOINT", Value: d.RunnerEndpoint},
		},
	}); err != nil {
		return nil, fmt.Errorf("dispatcher: create job for service %q: %w", serviceName, err)
	}

	result, err := d.K8s.WaitForJob(ctx, jobN)
	if err != nil {
		d.deleteJobBestEffort(ctx, jobN)
		return nil, fmt.Errorf("dispatcher: wait for service job %q: %w", serviceName, err)
	}

	if result.Failed {
		d.deleteJobBestEffort(ctx, jobN)
		return nil, fmt.Errorf("dispatcher: service job %q failed: %s", serviceName, result.Message)
	}

	output, err := d.Store.GetStateOutput(ctx, execID, stateName)
	if err != nil {
		d.deleteJobBestEffort(ctx, jobN)
		return nil, fmt.Errorf("dispatcher: get output after service %q: %w", serviceName, err)
	}

	d.deleteJobBestEffort(ctx, jobN)
	return output, nil
}

func (d *ServiceDispatcher) deleteJobBestEffort(ctx context.Context, name string) {
	if err := d.K8s.DeleteJob(ctx, name); err != nil {
		// best-effort; non-fatal
		_ = err
	}
}
