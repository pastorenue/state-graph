package store

import (
	"context"
	"errors"
	"time"

	"github.com/pastorenue/kflow/pkg/kflow"
)

type Status string

const (
	StatusPending   Status = "Pending"
	StatusRunning   Status = "Running"
	StatusCompleted Status = "Completed"
	StatusFailed    Status = "Failed"
)

var (
	ErrExecutionNotFound    = errors.New("store: execution not found")
	ErrStateNotFound        = errors.New("store: state record not found")
	ErrStateNotCompleted    = errors.New("store: state is not in Completed status")
	ErrStateAlreadyTerminal = errors.New("store: state record already in terminal status")
	ErrOutputTooLarge       = errors.New("store: output exceeds 1 MB and no object store is configured")
)

type ExecutionRecord struct {
	ID        string
	Workflow  string
	Status    Status
	Input     kflow.Input
	CreatedAt time.Time
	UpdatedAt time.Time
}

type StateRecord struct {
	ExecutionID string
	StateName   string
	Status      Status
	Input       kflow.Input
	Output      kflow.Output
	Error       string
	Attempt     int
	ResumeAt    *time.Time // non-nil for Wait states only
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Store interface {
	// Execution methods
	CreateExecution(ctx context.Context, record ExecutionRecord) error
	GetExecution(ctx context.Context, execID string) (ExecutionRecord, error)
	UpdateExecution(ctx context.Context, execID string, status Status) error
	ListExecutions(ctx context.Context, filter ExecutionFilter) ([]ExecutionRecord, error)

	// State methods
	WriteAheadState(ctx context.Context, record StateRecord) error
	MarkRunning(ctx context.Context, execID, stateName string) error
	CompleteState(ctx context.Context, execID, stateName string, output kflow.Output) error
	FailState(ctx context.Context, execID, stateName string, errMsg string) error
	GetStateOutput(ctx context.Context, execID, stateName string) (kflow.Output, error)
	ListStates(ctx context.Context, execID string) ([]StateRecord, error)

	// Service methods
	CreateService(ctx context.Context, record ServiceRecord) error
	GetService(ctx context.Context, name string) (ServiceRecord, error)
	ListServices(ctx context.Context) ([]ServiceRecord, error)
	UpdateServiceStatus(ctx context.Context, name string, status ServiceStatus, clusterIP string) error
	DeleteService(ctx context.Context, name string) error
}
