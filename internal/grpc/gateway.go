package grpc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	kflowv1 "github.com/pastorenue/kflow/internal/gen/kflow/v1"
	"github.com/pastorenue/kflow/internal/api"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// NewGatewayMux creates the HTTP handler that serves all public REST API routes
// via grpc-gateway (in-process), the WebSocket hub, and health endpoints.
// Auth middleware is applied to all routes except /healthz and /readyz.
func NewGatewayMux(
	ctx context.Context,
	st store.Store,
	hub *api.WSHub,
	ch *telemetry.Client,
	apiKey string,
	ready *atomic.Bool,
	trigger func(execID string, graph *kflowv1.WorkflowGraph, input kflow.Input),
) (http.Handler, error) {
	gwMux := runtime.NewServeMux()

	wfSrv := &workflowServiceServer{
		store:     st,
		workflows: make(map[string]*kflowv1.WorkflowGraph),
		trigger:   trigger,
	}
	execSrv := &executionServiceServer{store: st}
	svcSrv := &serviceManagementServiceServer{store: st}
	telSrv := &telemetryServiceServer{ch: ch}

	if err := kflowv1.RegisterWorkflowServiceHandlerServer(ctx, gwMux, wfSrv); err != nil {
		return nil, fmt.Errorf("gateway: register WorkflowService: %w", err)
	}
	if err := kflowv1.RegisterExecutionServiceHandlerServer(ctx, gwMux, execSrv); err != nil {
		return nil, fmt.Errorf("gateway: register ExecutionService: %w", err)
	}
	if err := kflowv1.RegisterServiceManagementServiceHandlerServer(ctx, gwMux, svcSrv); err != nil {
		return nil, fmt.Errorf("gateway: register ServiceManagementService: %w", err)
	}
	if err := kflowv1.RegisterTelemetryServiceHandlerServer(ctx, gwMux, telSrv); err != nil {
		return nil, fmt.Errorf("gateway: register TelemetryService: %w", err)
	}

	mux := http.NewServeMux()

	// Auth-exempt endpoints.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// WebSocket endpoint.
	mux.Handle("/api/v1/ws", authMiddleware(apiKey, http.HandlerFunc(hub.ServeWS)))

	// All other routes via grpc-gateway, protected by auth.
	mux.Handle("/", authMiddleware(apiKey, secureHeaders(gwMux)))

	return mux, nil
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func authMiddleware(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := ""
		if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
			provided = auth[7:]
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── WorkflowServiceServer ──────────────────────────────────────────────────

type workflowServiceServer struct {
	kflowv1.UnimplementedWorkflowServiceServer
	store     store.Store
	workflows map[string]*kflowv1.WorkflowGraph
	trigger   func(execID string, graph *kflowv1.WorkflowGraph, input kflow.Input)
}

func (s *workflowServiceServer) RegisterWorkflow(_ context.Context, req *kflowv1.RegisterWorkflowRequest) (*kflowv1.RegisterWorkflowResponse, error) {
	if req.GetGraph() == nil || req.GetGraph().GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow name is required")
	}
	name := req.GetGraph().GetName()
	s.workflows[name] = req.GetGraph()
	return &kflowv1.RegisterWorkflowResponse{WorkflowName: name}, nil
}

func (s *workflowServiceServer) GetWorkflow(_ context.Context, req *kflowv1.GetWorkflowRequest) (*kflowv1.GetWorkflowResponse, error) {
	graph, ok := s.workflows[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "workflow %q not found", req.GetName())
	}
	return &kflowv1.GetWorkflowResponse{Graph: graph}, nil
}

func (s *workflowServiceServer) ListWorkflows(_ context.Context, _ *kflowv1.ListWorkflowsRequest) (*kflowv1.ListWorkflowsResponse, error) {
	graphs := make([]*kflowv1.WorkflowGraph, 0, len(s.workflows))
	for _, g := range s.workflows {
		graphs = append(graphs, g)
	}
	return &kflowv1.ListWorkflowsResponse{Workflows: graphs}, nil
}

func (s *workflowServiceServer) RunWorkflow(ctx context.Context, req *kflowv1.RunWorkflowRequest) (*kflowv1.RunWorkflowResponse, error) {
	name := req.GetName()
	graph, ok := s.workflows[name]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "workflow %q not found", name)
	}

	var input kflow.Input
	if req.GetInput() != nil {
		input = req.GetInput().AsMap()
	}

	execID, err := genUUID()
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate execution ID")
	}

	rec := store.ExecutionRecord{ID: execID, Workflow: name, Input: input}
	if err := s.store.CreateExecution(ctx, rec); err != nil {
		return nil, status.Errorf(codes.Internal, "create execution: %v", err)
	}

	if s.trigger != nil {
		go s.trigger(execID, graph, input)
	}

	return &kflowv1.RunWorkflowResponse{ExecutionId: execID}, nil
}

// ── ExecutionServiceServer ─────────────────────────────────────────────────

type executionServiceServer struct {
	kflowv1.UnimplementedExecutionServiceServer
	store store.Store
}

func (s *executionServiceServer) ListExecutions(ctx context.Context, req *kflowv1.ListExecutionsRequest) (*kflowv1.ListExecutionsResponse, error) {
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 50
	}
	recs, err := s.store.ListExecutions(ctx, store.ExecutionFilter{
		Workflow: req.GetWorkflow(),
		Status:   req.GetStatus(),
		Limit:    limit,
		Offset:   int(req.GetOffset()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list executions: %v", err)
	}
	protos := make([]*kflowv1.ExecutionRecord, 0, len(recs))
	for _, r := range recs {
		protos = append(protos, execRecordToProto(r))
	}
	return &kflowv1.ListExecutionsResponse{Executions: protos}, nil
}

func (s *executionServiceServer) GetExecution(ctx context.Context, req *kflowv1.GetExecutionRequest) (*kflowv1.GetExecutionResponse, error) {
	rec, err := s.store.GetExecution(ctx, req.GetId())
	if err == store.ErrExecutionNotFound {
		return nil, status.Errorf(codes.NotFound, "execution %q not found", req.GetId())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get execution: %v", err)
	}
	return &kflowv1.GetExecutionResponse{Execution: execRecordToProto(rec)}, nil
}

func (s *executionServiceServer) ListExecutionStates(ctx context.Context, req *kflowv1.ListExecutionStatesRequest) (*kflowv1.ListExecutionStatesResponse, error) {
	if _, err := s.store.GetExecution(ctx, req.GetExecutionId()); err == store.ErrExecutionNotFound {
		return nil, status.Errorf(codes.NotFound, "execution %q not found", req.GetExecutionId())
	}
	states, err := s.store.ListStates(ctx, req.GetExecutionId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list states: %v", err)
	}
	protos := make([]*kflowv1.StateRecord, 0, len(states))
	for _, sr := range states {
		protos = append(protos, stateRecordToProto(sr))
	}
	return &kflowv1.ListExecutionStatesResponse{States: protos}, nil
}

// ── ServiceManagementServiceServer ────────────────────────────────────────

type serviceManagementServiceServer struct {
	kflowv1.UnimplementedServiceManagementServiceServer
	store store.Store
}

func (s *serviceManagementServiceServer) RegisterService(ctx context.Context, req *kflowv1.RegisterServiceRequest) (*kflowv1.RegisterServiceResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "service name is required")
	}
	rec := store.ServiceRecord{
		Name:        req.GetName(),
		Port:        int(req.GetPort()),
		MinScale:    int(req.GetMinScale()),
		MaxScale:    int(req.GetMaxScale()),
		IngressHost: req.GetIngressHost(),
		Timeout:     time.Duration(req.GetTimeoutSeconds()) * time.Second,
	}
	if req.GetMode() == "lambda" {
		rec.Mode = kflow.Lambda
	} else {
		rec.Mode = kflow.Deployment
	}
	if err := s.store.CreateService(ctx, rec); err == store.ErrDuplicateServiceName {
		return nil, status.Errorf(codes.AlreadyExists, "service %q already registered", req.GetName())
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "create service: %v", err)
	}
	return &kflowv1.RegisterServiceResponse{ServiceName: req.GetName()}, nil
}

func (s *serviceManagementServiceServer) GetService(ctx context.Context, req *kflowv1.GetServiceRequest) (*kflowv1.GetServiceResponse, error) {
	rec, err := s.store.GetService(ctx, req.GetName())
	if err == store.ErrServiceNotFound {
		return nil, status.Errorf(codes.NotFound, "service %q not found", req.GetName())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get service: %v", err)
	}
	return &kflowv1.GetServiceResponse{Service: svcRecordToProto(rec)}, nil
}

func (s *serviceManagementServiceServer) ListServices(ctx context.Context, _ *kflowv1.ListServicesRequest) (*kflowv1.ListServicesResponse, error) {
	recs, err := s.store.ListServices(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list services: %v", err)
	}
	protos := make([]*kflowv1.ServiceRecord, 0, len(recs))
	for _, r := range recs {
		protos = append(protos, svcRecordToProto(r))
	}
	return &kflowv1.ListServicesResponse{Services: protos}, nil
}

func (s *serviceManagementServiceServer) DeleteService(ctx context.Context, req *kflowv1.DeleteServiceRequest) (*emptypb.Empty, error) {
	if err := s.store.DeleteService(ctx, req.GetName()); err == store.ErrServiceNotFound {
		return nil, status.Errorf(codes.NotFound, "service %q not found", req.GetName())
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "delete service: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ── TelemetryServiceServer ─────────────────────────────────────────────────

type telemetryServiceServer struct {
	kflowv1.UnimplementedTelemetryServiceServer
	ch *telemetry.Client
}

func (s *telemetryServiceServer) ListExecutionEvents(ctx context.Context, req *kflowv1.ListExecutionEventsRequest) (*kflowv1.ListExecutionEventsResponse, error) {
	if s.ch == nil {
		return &kflowv1.ListExecutionEventsResponse{}, nil
	}
	var since *time.Time
	if req.GetSince() != nil {
		t := req.GetSince().AsTime()
		since = &t
	}
	rows, err := s.ch.QueryExecutionEvents(ctx, req.GetExecutionId(), since, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query events: %v", err)
	}
	protos := make([]*kflowv1.EventRow, 0, len(rows))
	for _, r := range rows {
		protos = append(protos, &kflowv1.EventRow{
			EventId:     r.EventID,
			ExecutionId: r.ExecutionID,
			StateName:   r.StateName,
			FromStatus:  r.FromStatus,
			ToStatus:    r.ToStatus,
			Error:       r.Error,
			OccurredAt:  timestamppb.New(r.OccurredAt),
		})
	}
	return &kflowv1.ListExecutionEventsResponse{Events: protos}, nil
}

func (s *telemetryServiceServer) ListServiceMetrics(ctx context.Context, req *kflowv1.ListServiceMetricsRequest) (*kflowv1.ListServiceMetricsResponse, error) {
	if s.ch == nil {
		return &kflowv1.ListServiceMetricsResponse{}, nil
	}
	var since, until *time.Time
	if req.GetSince() != nil {
		t := req.GetSince().AsTime()
		since = &t
	}
	if req.GetUntil() != nil {
		t := req.GetUntil().AsTime()
		until = &t
	}
	rows, err := s.ch.QueryServiceMetrics(ctx, req.GetServiceName(), since, until, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query metrics: %v", err)
	}
	protos := make([]*kflowv1.MetricRow, 0, len(rows))
	for _, r := range rows {
		protos = append(protos, &kflowv1.MetricRow{
			MetricId:     r.MetricID,
			ServiceName:  r.ServiceName,
			InvocationId: r.InvocationID,
			DurationMs:   r.DurationMs,
			StatusCode:   uint32(r.StatusCode),
			Error:        r.Error,
			OccurredAt:   timestamppb.New(r.OccurredAt),
		})
	}
	return &kflowv1.ListServiceMetricsResponse{Metrics: protos}, nil
}

func (s *telemetryServiceServer) ListLogs(ctx context.Context, req *kflowv1.ListLogsRequest) (*kflowv1.ListLogsResponse, error) {
	if s.ch == nil {
		return &kflowv1.ListLogsResponse{}, nil
	}
	filter := telemetry.LogFilter{
		ExecutionID: req.GetExecutionId(),
		ServiceName: req.GetServiceName(),
		StateName:   req.GetStateName(),
		Level:       req.GetLevel(),
		Query:       req.GetQuery(),
		Limit:       int(req.GetLimit()),
		Offset:      int(req.GetOffset()),
	}
	if req.GetSince() != nil {
		t := req.GetSince().AsTime()
		filter.Since = &t
	}
	if req.GetUntil() != nil {
		t := req.GetUntil().AsTime()
		filter.Until = &t
	}
	rows, total, err := s.ch.QueryLogs(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query logs: %v", err)
	}
	protos := make([]*kflowv1.LogRow, 0, len(rows))
	for _, r := range rows {
		protos = append(protos, &kflowv1.LogRow{
			LogId:       r.LogID,
			ExecutionId: r.ExecutionID,
			ServiceName: r.ServiceName,
			StateName:   r.StateName,
			Level:       r.Level,
			Message:     r.Message,
			OccurredAt:  timestamppb.New(r.OccurredAt),
		})
	}
	return &kflowv1.ListLogsResponse{Logs: protos, Total: int32(total)}, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func execRecordToProto(r store.ExecutionRecord) *kflowv1.ExecutionRecord {
	st := kflowv1.Status_STATUS_UNSPECIFIED
	switch r.Status {
	case store.StatusPending:
		st = kflowv1.Status_STATUS_PENDING
	case store.StatusRunning:
		st = kflowv1.Status_STATUS_RUNNING
	case store.StatusCompleted:
		st = kflowv1.Status_STATUS_COMPLETED
	case store.StatusFailed:
		st = kflowv1.Status_STATUS_FAILED
	}
	return &kflowv1.ExecutionRecord{
		Id:        r.ID,
		Workflow:  r.Workflow,
		Status:    st,
		CreatedAt: timestamppb.New(r.CreatedAt),
		UpdatedAt: timestamppb.New(r.UpdatedAt),
	}
}

func stateRecordToProto(r store.StateRecord) *kflowv1.StateRecord {
	st := kflowv1.Status_STATUS_UNSPECIFIED
	switch r.Status {
	case store.StatusPending:
		st = kflowv1.Status_STATUS_PENDING
	case store.StatusRunning:
		st = kflowv1.Status_STATUS_RUNNING
	case store.StatusCompleted:
		st = kflowv1.Status_STATUS_COMPLETED
	case store.StatusFailed:
		st = kflowv1.Status_STATUS_FAILED
	}
	return &kflowv1.StateRecord{
		ExecutionId: r.ExecutionID,
		StateName:   r.StateName,
		Status:      st,
		Error:       r.Error,
		Attempt:     int32(r.Attempt),
		CreatedAt:   timestamppb.New(r.CreatedAt),
		UpdatedAt:   timestamppb.New(r.UpdatedAt),
	}
}

func svcRecordToProto(r store.ServiceRecord) *kflowv1.ServiceRecord {
	st := kflowv1.ServiceStatus_SERVICE_STATUS_UNSPECIFIED
	switch r.Status {
	case store.ServiceStatusPending:
		st = kflowv1.ServiceStatus_SERVICE_STATUS_PENDING
	case store.ServiceStatusRunning:
		st = kflowv1.ServiceStatus_SERVICE_STATUS_RUNNING
	case store.ServiceStatusFailed:
		st = kflowv1.ServiceStatus_SERVICE_STATUS_FAILED
	case store.ServiceStatusStopped:
		st = kflowv1.ServiceStatus_SERVICE_STATUS_STOPPED
	}
	mode := "deployment"
	if r.Mode == kflow.Lambda {
		mode = "lambda"
	}
	return &kflowv1.ServiceRecord{
		Name:        r.Name,
		Mode:        mode,
		Port:        int32(r.Port),
		MinScale:    int32(r.MinScale),
		MaxScale:    int32(r.MaxScale),
		IngressHost: r.IngressHost,
		ClusterIp:   r.ClusterIP,
		Status:      st,
		CreatedAt:   timestamppb.New(r.CreatedAt),
		UpdatedAt:   timestamppb.New(r.UpdatedAt),
	}
}

func genUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

