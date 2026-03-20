// Package controller implements application services that orchestrate
// infrastructure components. It must NOT import internal/api.
package controller

import (
	"context"
	"fmt"
	"log"
	"time"

	kflowv1 "github.com/pastorenue/kflow/internal/gen/kflow/v1"
	"github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/runner"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

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
	Metrics           *telemetry.MetricsWriter // nil = no metrics
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

	modeStr := "deployment"
	if rec.Mode == kflow.Lambda {
		modeStr = "lambda"
	}
	log.Printf("dispatcher: invoking service %q (mode=%s)", serviceName, modeStr)

	start := time.Now()
	var output kflow.Output
	var dispErr error

	switch rec.Mode {
	case kflow.Deployment:
		output, dispErr = d.dispatchDeployment(ctx, execID, stateName, serviceName, rec, input)
	case kflow.Lambda:
		output, dispErr = d.dispatchLambda(ctx, execID, stateName, serviceName, input)
	default:
		return nil, fmt.Errorf("dispatcher: unknown service mode %d", rec.Mode)
	}

	durationMs := uint64(time.Since(start).Milliseconds())
	if dispErr != nil {
		log.Printf("dispatcher: service %q failed: %v", serviceName, dispErr)
	} else {
		log.Printf("dispatcher: service %q completed (%dms)", serviceName, durationMs)
	}

	if d.Metrics != nil {
		var statusCode uint16 = 200
		errMsg := ""
		if dispErr != nil {
			statusCode = 500
			errMsg = dispErr.Error()
		}
		d.Metrics.RecordServiceInvocation(ctx, serviceName, execID+":"+stateName, durationMs, statusCode, errMsg)
	}

	return output, dispErr
}

// dispatchDeployment calls ServiceRunnerService.Invoke via gRPC on the service's
// ClusterIP.
func (d *ServiceDispatcher) dispatchDeployment(
	ctx context.Context,
	execID, stateName, serviceName string,
	rec store.ServiceRecord,
	input kflow.Input,
) (kflow.Output, error) {
	port := d.ServiceGRPCPort
	if port == 0 {
		port = 9091
	}

	addr := fmt.Sprintf("%s:%d", rec.ClusterIP, port)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dispatcher: dial service %q at %s: %w", serviceName, addr, err)
	}
	defer conn.Close()

	var payload *structpb.Struct
	if input != nil {
		payload, err = structpb.NewStruct(input)
		if err != nil {
			return nil, fmt.Errorf("dispatcher: encode input for service %q: %w", serviceName, err)
		}
	} else {
		payload = &structpb.Struct{}
	}

	client := kflowv1.NewServiceRunnerServiceClient(conn)
	resp, err := client.Invoke(ctx, &kflowv1.InvokeRequest{Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("dispatcher: invoke service %q (execID=%s state=%s): %w",
			serviceName, execID, stateName, err)
	}

	if resp.GetResult() == nil {
		return kflow.Output{}, nil
	}
	return resp.GetResult().AsMap(), nil
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
		_ = err // best-effort; non-fatal
	}
}
