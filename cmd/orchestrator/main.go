// Package main is the Control Plane entry point (composition root).
// Flag dispatch selects the execution mode:
//
//	--state=<name>    run a single state handler and exit (K8s Job mode)
//	--service=<name>  run a persistent service or one-shot service (K8s deployment/lambda)
//	(no flag)         start the Control Plane HTTP + gRPC server
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pastorenue/kflow/internal/api"
	"github.com/pastorenue/kflow/internal/config"
	"github.com/pastorenue/kflow/internal/controller"
	"github.com/pastorenue/kflow/internal/engine"
	kflowv1 "github.com/pastorenue/kflow/internal/gen/kflow/v1"
	grpcsrv "github.com/pastorenue/kflow/internal/grpc"
	k8sclient "github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/runner"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	stateName := flag.String("state", "", "state name to execute (K8s Job mode)")
	serviceName := flag.String("service", "", "service name to run")
	flag.Parse()

	if *stateName != "" {
		runStateMode(*stateName)
		return
	}
	if *serviceName != "" {
		runServiceMode(*serviceName)
		return
	}
	runServerMode()
}

// runStateMode is the --state=<name> execution path.
func runStateMode(stateName string) {
	execID := requireEnv("KFLOW_EXECUTION_ID")
	stateToken := requireEnv("KFLOW_STATE_TOKEN")
	grpcEndpoint := requireEnv("KFLOW_GRPC_ENDPOINT")

	log.Printf("state mode: execID=%s state=%s endpoint=%s", execID, stateName, grpcEndpoint)

	conn, err := grpc.NewClient(grpcEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("state mode: dial runner endpoint %s: %v", grpcEndpoint, err)
	}
	defer conn.Close()

	ctx := context.Background()
	client := kflowv1.NewRunnerServiceClient(conn)

	resp, err := client.GetInput(ctx, &kflowv1.GetInputRequest{Token: stateToken})
	if err != nil {
		log.Fatalf("state mode: GetInput: %v", err)
	}

	var input map[string]any
	if resp.GetPayload() != nil {
		input = resp.GetPayload().AsMap()
	}

	log.Printf("state mode: got input keys=%d", len(input))

	// In K8s Job mode, the workflow binary is the container itself.
	// The handler should be registered by the workflow code that embeds this binary.
	// For now, report success with the received input as output.
	// Production workflows override this by calling kflow.Run / kflow.RunService.
	output := input
	if _, err := client.CompleteState(ctx, &kflowv1.CompleteStateRequest{
		Token: stateToken,
	}); err != nil {
		log.Fatalf("state mode: CompleteState: %v", err)
	}

	log.Printf("state mode: completed state=%s execID=%s output_keys=%d", stateName, execID, len(output))
	os.Exit(0)
}

// runServiceMode is the --service=<name> execution path.
// Deployment-mode: starts a gRPC ServiceRunnerService server.
// Lambda-mode: dials RunnerService, runs handler, reports result, exits.
func runServiceMode(serviceName string) {
	grpcEndpoint := os.Getenv("KFLOW_GRPC_ENDPOINT")
	stateToken := os.Getenv("KFLOW_STATE_TOKEN")
	execID := os.Getenv("KFLOW_EXECUTION_ID")

	log.Printf("service mode: name=%s grpcEndpoint=%s execID=%s token_present=%v",
		serviceName, grpcEndpoint, execID, stateToken != "")

	if stateToken != "" && grpcEndpoint != "" {
		// Lambda-mode: run once and exit.
		conn, err := grpc.NewClient(grpcEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("service mode: dial %s: %v", grpcEndpoint, err)
		}
		defer conn.Close()

		ctx := context.Background()
		client := kflowv1.NewRunnerServiceClient(conn)

		resp, err := client.GetInput(ctx, &kflowv1.GetInputRequest{Token: stateToken})
		if err != nil {
			log.Fatalf("service mode: GetInput: %v", err)
		}

		var input map[string]any
		if resp.GetPayload() != nil {
			input = resp.GetPayload().AsMap()
		}
		_ = input

		if _, err := client.CompleteState(ctx, &kflowv1.CompleteStateRequest{Token: stateToken}); err != nil {
			log.Fatalf("service mode: CompleteState: %v", err)
		}
		log.Printf("service mode: lambda service %q completed", serviceName)
		os.Exit(0)
	}

	// Deployment-mode: start a ServiceRunnerService gRPC server and block.
	servicePort := os.Getenv("KFLOW_SERVICE_GRPC_PORT")
	if servicePort == "" {
		servicePort = "9091"
	}
	addr := ":" + servicePort

	grpcServer := grpc.NewServer()
	kflowv1.RegisterServiceRunnerServiceServer(grpcServer, &serviceRunnerServer{name: serviceName})

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("service mode: listen %s: %v", addr, err)
	}
	log.Printf("service mode: deployment service %q listening on %s", serviceName, addr)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("service mode: serve: %v", err)
	}
}

// runServerMode starts the Control Plane server.
func runServerMode() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	ms, err := store.NewMongoStore(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		log.Fatalf("store: connect: %v", err)
	}
	if err := ms.EnsureIndexes(ctx); err != nil {
		log.Fatalf("store: ensure indexes: %v", err)
	}
	log.Println("store: connected to MongoDB")

	if cfg.ObjectStoreURI != "" {
		objStore, err := store.NewObjectStore(ctx, cfg.ObjectStoreURI)
		if err != nil {
			log.Fatalf("object store: %v", err)
		}
		ms.ObjectStore = objStore
		log.Printf("object store: connected (uri=%s)", cfg.ObjectStoreURI)
	} else {
		log.Println("object store: KFLOW_OBJECT_STORE_URI not set — large output offload disabled")
	}

	k8s, err := k8sclient.NewClient(cfg.Namespace)
	if err != nil {
		log.Printf("k8s: WARNING cannot init client: %v (running without K8s)", err)
		k8s = nil
	} else {
		log.Printf("k8s: client initialised (namespace=%s)", k8s.Namespace())
	}

	hub := api.NewWSHub()

	var chClient *telemetry.Client
	var metricsWriter *telemetry.MetricsWriter
	var eventWriter *telemetry.EventWriter
	var logWriter *telemetry.LogWriter
	if cfg.ClickHouseDSN != "" {
		chClient, err = telemetry.NewClient(ctx, cfg.ClickHouseDSN)
		if err != nil {
			log.Printf("telemetry: WARNING failed to connect to ClickHouse: %v (continuing without telemetry)", err)
		} else {
			if err := chClient.InitSchema(ctx); err != nil {
				log.Printf("telemetry: WARNING failed to init schema: %v (continuing without telemetry)", err)
				chClient = nil
			} else {
				log.Println("telemetry: connected to ClickHouse")
				metricsWriter = telemetry.NewMetricsWriter(chClient)
				eventWriter = telemetry.NewEventWriter(chClient)
				logWriter = telemetry.NewLogWriter(chClient)
			}
		}
	} else {
		log.Println("telemetry: KFLOW_CLICKHOUSE_DSN not set — telemetry disabled")
	}

	disp := &controller.ServiceDispatcher{
		Store:             ms,
		K8s:               k8s,
		RunnerEndpoint:    cfg.RunnerGRPCEndpoint,
		RunnerTokenSecret: cfg.RunnerTokenSecret,
		Metrics:           metricsWriter,
	}

	runnerSrv := runner.NewRunnerServiceServer(ms, cfg.RunnerTokenSecret)

	notify := func(execID, stateName, from, to, errMsg string) {
		hub.Broadcast(api.WSEvent{
			Type:      "state_transition",
			Timestamp: time.Now(),
			Payload: api.StateTransitionPayload{
				ExecutionID: execID,
				StateName:   stateName,
				FromStatus:  from,
				ToStatus:    to,
				Error:       errMsg,
			},
		})
	}

	trigger := func(execID string, graph *kflowv1.WorkflowGraph, input kflow.Input) {
		g, err := engine.BuildFromProto(graph)
		if err != nil {
			log.Printf("trigger: build graph %q: %v", graph.GetName(), err)
			_ = ms.UpdateExecution(context.Background(), execID, store.StatusFailed)
			return
		}

		var runErr error
		if k8s != nil {
			ke := &engine.K8sExecutor{
				Store:             ms,
				K8s:               k8s,
				Image:             cfg.Image,
				RunnerEndpoint:    cfg.RunnerGRPCEndpoint,
				RunnerTokenSecret: cfg.RunnerTokenSecret,
				Dispatcher:        disp,
				Telemetry:         eventWriter,
				LogWriter:         logWriter,
				Notify:            notify,
			}
			runErr = ke.Run(context.Background(), execID, g, input)
		} else {
			ex := &engine.Executor{
				Store: ms,
				Handler: func(_ context.Context, _ string, in kflow.Input) (kflow.Output, error) {
					return kflow.Output(in), nil
				},
				Dispatcher: disp,
				Telemetry:  eventWriter,
				Notify:     notify,
			}
			runErr = ex.Run(context.Background(), execID, g, input)
		}

		if runErr != nil {
			log.Printf("trigger: execution %s failed: %v", execID, runErr)
			_ = ms.UpdateExecution(context.Background(), execID, store.StatusFailed)
		} else {
			_ = ms.UpdateExecution(context.Background(), execID, store.StatusCompleted)
		}
	}

	srv, err := grpcsrv.NewGRPCServer(cfg, ms, k8s, hub, disp, runnerSrv, chClient, trigger)
	if err != nil {
		log.Fatalf("grpc: create server: %v", err)
	}
	srv.MarkReady()

	if err := srv.Serve(ctx); err != nil {
		log.Fatalf("grpc: serve: %v", err)
	}
}

// serviceRunnerServer is a minimal ServiceRunnerService for Deployment-mode pods.
type serviceRunnerServer struct {
	kflowv1.UnimplementedServiceRunnerServiceServer
	name string
}

func (s *serviceRunnerServer) Invoke(_ context.Context, req *kflowv1.InvokeRequest) (*kflowv1.InvokeResponse, error) {
	log.Printf("service %q: Invoke called with payload keys=%d", s.name, len(req.GetPayload().GetFields()))
	return &kflowv1.InvokeResponse{Result: req.GetPayload()}, nil
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "error: required environment variable %s is not set\n", name)
		os.Exit(1)
	}
	return v
}
