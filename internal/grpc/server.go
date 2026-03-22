package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync/atomic"

	kflowv1 "github.com/pastorenue/kflow/internal/gen/kflow/v1"
	"github.com/pastorenue/kflow/internal/api"
	"github.com/pastorenue/kflow/internal/config"
	"github.com/pastorenue/kflow/internal/controller"
	k8sclient "github.com/pastorenue/kflow/internal/k8s"
	"github.com/pastorenue/kflow/internal/runner"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

// GRPCServer manages two listeners:
//   - publicServer (HTTP/grpc-gateway) on cfg.GRPCPort (default :8080)
//   - runnerServer (gRPC, RunnerService) on cfg.RunnerGRPCPort (default :9090)
type GRPCServer struct {
	cfg          *config.Config
	httpSrv      *http.Server
	runnerServer *grpclib.Server
	ready        atomic.Bool
}

// NewGRPCServer wires all dependencies and returns a ready-to-start GRPCServer.
func NewGRPCServer(
	cfg *config.Config,
	st store.Store,
	k8s *k8sclient.Client,
	hub *api.WSHub,
	disp *controller.ServiceDispatcher,
	runnerSrv *runner.RunnerServiceServer,
	ch *telemetry.Client,
	trigger func(execID string, graph *kflowv1.WorkflowGraph, input kflow.Input),
) (*GRPCServer, error) {
	s := &GRPCServer{cfg: cfg}

	// ── Runner gRPC server (:9090) ───────────────────────────────────────────
	runnerOpts := []grpclib.ServerOption{
		grpclib.ChainUnaryInterceptor(
			UnaryRecoveryInterceptor(),
			UnaryLoggingInterceptor(),
			// No auth interceptor — RunnerService uses per-call token auth.
		),
	}
	if cfg.GRPCTLSCert != "" && cfg.GRPCTLSKey != "" {
		creds, err := loadTLSCredentials(cfg.GRPCTLSCert, cfg.GRPCTLSKey)
		if err != nil {
			return nil, fmt.Errorf("grpc: load runner TLS credentials: %w", err)
		}
		runnerOpts = append(runnerOpts, grpclib.Creds(creds))
	} else {
		log.Println("grpc: WARNING runner TLS not configured — running in dev mode (insecure)")
	}

	s.runnerServer = grpclib.NewServer(runnerOpts...)
	kflowv1.RegisterRunnerServiceServer(s.runnerServer, runnerSrv)
	reflection.Register(s.runnerServer)

	// ── Public HTTP/gateway server (:8080) ───────────────────────────────────
	gwHandler, err := NewGatewayMux(
		context.Background(),
		st,
		hub,
		ch,
		cfg.APIKey,
		&s.ready,
		trigger,
	)
	if err != nil {
		return nil, fmt.Errorf("grpc: create gateway mux: %w", err)
	}

	s.httpSrv = &http.Server{
		Addr:    ":" + cfg.GRPCPort,
		Handler: gwHandler,
	}

	_ = k8s

	return s, nil
}

// MarkReady signals that the server is ready to serve traffic (/readyz returns 200).
func (s *GRPCServer) MarkReady() { s.ready.Store(true) }

// Serve starts both listeners concurrently. It blocks until ctx is cancelled,
// then gracefully drains both servers.
func (s *GRPCServer) Serve(ctx context.Context) error {
	runnerAddr := ":" + s.cfg.RunnerGRPCPort
	runnerLis, err := net.Listen("tcp", runnerAddr)
	if err != nil {
		return fmt.Errorf("grpc: listen runner %s: %w", runnerAddr, err)
	}

	errCh := make(chan error, 2)

	go func() {
		log.Printf("grpc: runner server listening on %s", runnerAddr)
		if err := s.runnerServer.Serve(runnerLis); err != nil {
			errCh <- fmt.Errorf("runner server: %w", err)
		}
	}()

	go func() {
		log.Printf("grpc: public HTTP server listening on %s", s.httpSrv.Addr)
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("public server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	log.Println("grpc: shutting down")
	s.runnerServer.GracefulStop()
	if err := s.httpSrv.Shutdown(context.Background()); err != nil {
		log.Printf("grpc: shutdown HTTP server: %v", err)
	}
	return nil
}

func loadTLSCredentials(certFile, keyFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}), nil
}
