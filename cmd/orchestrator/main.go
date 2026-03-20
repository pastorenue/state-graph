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
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/pastorenue/kflow/internal/api"
	"github.com/pastorenue/kflow/internal/config"
	"github.com/pastorenue/kflow/internal/controller"
	k8sclient "github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/store"
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
// Full gRPC RunnerService protocol implemented in Phase 13.
func runStateMode(stateName string) {
	execID := requireEnv("KFLOW_EXECUTION_ID")
	stateToken := requireEnv("KFLOW_STATE_TOKEN")
	grpcEndpoint := requireEnv("KFLOW_GRPC_ENDPOINT")

	log.Printf("state mode: execID=%s state=%s endpoint=%s token_present=true", execID, stateName, grpcEndpoint)
	_ = stateToken

	// TODO(Phase 13): dial grpcEndpoint, call RunnerService.GetInput, run handler,
	// call RunnerService.CompleteState or FailState, then exit.
	log.Fatal("state mode: RunnerService gRPC not yet implemented (Phase 13)")
}

// runServiceMode is the --service=<name> execution path.
// Deployment-mode: starts a gRPC ServiceRunnerService server (Phase 13).
// Lambda-mode:     dials RunnerService, runs handler, reports result, exits (Phase 13).
func runServiceMode(serviceName string) {
	grpcEndpoint := os.Getenv("KFLOW_GRPC_ENDPOINT")
	stateToken := os.Getenv("KFLOW_STATE_TOKEN")
	execID := os.Getenv("KFLOW_EXECUTION_ID")

	log.Printf("service mode: name=%s grpcEndpoint=%s execID=%s token_present=%v",
		serviceName, grpcEndpoint, execID, stateToken != "")

	// TODO(Phase 13): detect Lambda vs Deployment mode via env or registered ServiceDef,
	// then either start a gRPC ServiceRunnerService or run single-shot handler.
	log.Fatal("service mode: ServiceRunnerService gRPC not yet implemented (Phase 13)")
}

// runServerMode starts the Control Plane HTTP API server.
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

	k8s, err := k8sclient.NewClient(cfg.Namespace)
	if err != nil {
		log.Fatalf("k8s: %v", err)
	}
	log.Printf("k8s: client initialised (namespace=%s)", k8s.Namespace())

	hub := api.NewWSHub()

	disp := &controller.ServiceDispatcher{
		Store:             ms,
		K8s:               k8s,
		RunnerEndpoint:    cfg.RunnerGRPCEndpoint,
		RunnerTokenSecret: []byte(cfg.RunnerTokenSecret),
	}

	srv := api.NewServer(ms, k8s, hub, disp, nil, nil)
	srv.MarkReady()

	port := os.Getenv("KFLOW_GRPC_PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: srv,
	}

	go func() {
		log.Printf("api: listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("api: server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("orchestrator: shutting down")
	if err := httpSrv.Shutdown(context.Background()); err != nil {
		log.Printf("api: shutdown error: %v", err)
	}
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "error: required environment variable %s is not set\n", name)
		os.Exit(1)
	}
	return v
}
