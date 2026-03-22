package kflow

import (
	"context"
	"fmt"
	"os"
	"strings"

	kflowv1 "github.com/pastorenue/kflow/internal/gen/kflow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// Dispatch is the unified workflow entry point.
//
// Priority:
//  1. KFLOW_STATE_TOKEN set → worker mode (K8s Job container executing one state)
//  2. --local flag          → RunLocal in-process
//  3. default               → register + trigger on Control Plane
func Dispatch(wf *Workflow, input Input) error {
	if token := os.Getenv("KFLOW_STATE_TOKEN"); token != "" {
		dispatchWorker(wf, token) // always calls os.Exit
		return nil                // unreachable
	}
	for _, arg := range os.Args[1:] {
		if arg == "--local" {
			return RunLocal(wf, input)
		}
	}
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
	return triggerExecution(server, apiKey, wf.Name(), input)
}

// dispatchWorker executes a single state in K8s Job worker mode.
// Dials RunnerService, fetches input, calls the handler, reports result.
// Always terminates the process via os.Exit.
func dispatchWorker(wf *Workflow, token string) {
	stateName := workerFlagValue("state")
	if stateName == "" {
		fmt.Fprintln(os.Stderr, "kflow: worker mode: --state=<name> is required")
		os.Exit(1)
	}

	endpoint := os.Getenv("KFLOW_GRPC_ENDPOINT")
	if endpoint == "" {
		endpoint = "kflow-cp.kflow.svc.cluster.local:9090"
	}

	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "kflow: dial RunnerService: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	ctx := context.Background()
	client := kflowv1.NewRunnerServiceClient(conn)

	resp, err := client.GetInput(ctx, &kflowv1.GetInputRequest{Token: token})
	if err != nil {
		_, _ = client.FailState(ctx, &kflowv1.FailStateRequest{Token: token, ErrorMessage: err.Error()})
		os.Exit(1)
	}

	handlerInput := Input{}
	if resp.Payload != nil {
		handlerInput = resp.Payload.AsMap()
	}

	td, ok := wf.tasks[stateName]
	if !ok {
		_, _ = client.FailState(ctx, &kflowv1.FailStateRequest{
			Token:        token,
			ErrorMessage: fmt.Sprintf("unknown state: %s", stateName),
		})
		os.Exit(1)
	}

	fn := td.Fn()
	if fn == nil {
		_, _ = client.FailState(ctx, &kflowv1.FailStateRequest{
			Token:        token,
			ErrorMessage: fmt.Sprintf("state %q: no inline handler", stateName),
		})
		os.Exit(1)
	}

	output, handlerErr := fn(ctx, handlerInput)
	if handlerErr != nil {
		_, _ = client.FailState(ctx, &kflowv1.FailStateRequest{Token: token, ErrorMessage: handlerErr.Error()})
		os.Exit(1)
	}

	s, err := structpb.NewStruct(map[string]any(output))
	if err != nil {
		_, _ = client.FailState(ctx, &kflowv1.FailStateRequest{
			Token:        token,
			ErrorMessage: fmt.Sprintf("marshal output: %v", err),
		})
		os.Exit(1)
	}

	if _, err := client.CompleteState(ctx, &kflowv1.CompleteStateRequest{Token: token, Output: s}); err != nil {
		fmt.Fprintf(os.Stderr, "kflow: CompleteState: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// workerFlagValue parses --<name>=<value> or --<name> <value> from os.Args.
func workerFlagValue(name string) string {
	prefix := "--" + name + "="
	args := os.Args[1:]
	for i, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg[len(prefix):]
		}
		if arg == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
