package store

import (
	"errors"
	"time"

	"github.com/pastorenue/kflow/pkg/kflow"
)

// ServiceStatus tracks the deployment lifecycle of a Service.
type ServiceStatus string

const (
	ServiceStatusPending ServiceStatus = "Pending" // registered, not yet deployed
	ServiceStatusRunning ServiceStatus = "Running" // Deployment/Job active and healthy
	ServiceStatusFailed  ServiceStatus = "Failed"  // deployment error
	ServiceStatusStopped ServiceStatus = "Stopped" // deregistered and torn down
)

var (
	ErrServiceNotFound       = errors.New("store: service not found")
	ErrDuplicateServiceName  = errors.New("store: service name already registered")
)

// ServiceRecord is the persisted representation of a registered Service.
type ServiceRecord struct {
	Name        string
	Mode        kflow.ServiceMode
	Port        int
	MinScale    int
	MaxScale    int
	IngressHost string
	Timeout     time.Duration
	Status      ServiceStatus
	ClusterIP   string // set after K8s Service is created (Deployment mode)
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ExecutionFilter is used by ListExecutions to narrow results.
type ExecutionFilter struct {
	Workflow string // empty = all workflows
	Status   string // empty = all statuses
	Limit    int    // 0 = default (50)
	Offset   int
}
