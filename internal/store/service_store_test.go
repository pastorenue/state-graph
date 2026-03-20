package store

import (
	"context"
	"testing"

	"github.com/pastorenue/kflow/pkg/kflow"
)

func TestMemoryStore_ServiceCRUD(t *testing.T) {
	ms := NewMemoryStore()
	ctx := context.Background()

	rec := ServiceRecord{
		Name:     "pricing-svc",
		Mode:     kflow.Deployment,
		Port:     8080,
		MinScale: 1,
		MaxScale: 3,
	}

	if err := ms.CreateService(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := ms.GetService(ctx, "pricing-svc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "pricing-svc" || got.Status != ServiceStatusPending {
		t.Fatalf("unexpected record: %+v", got)
	}

	if err := ms.UpdateServiceStatus(ctx, "pricing-svc", ServiceStatusRunning, "10.0.0.5"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = ms.GetService(ctx, "pricing-svc")
	if got.Status != ServiceStatusRunning || got.ClusterIP != "10.0.0.5" {
		t.Fatalf("unexpected status after update: %+v", got)
	}

	svcs, err := ms.ListServices(ctx)
	if err != nil || len(svcs) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(svcs))
	}

	if err := ms.DeleteService(ctx, "pricing-svc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ms.GetService(ctx, "pricing-svc"); err != ErrServiceNotFound {
		t.Fatalf("expected ErrServiceNotFound after delete, got %v", err)
	}
}

func TestMemoryStore_DuplicateService(t *testing.T) {
	ms := NewMemoryStore()
	ctx := context.Background()
	rec := ServiceRecord{Name: "svc-a", Mode: kflow.Lambda}
	_ = ms.CreateService(ctx, rec)
	if err := ms.CreateService(ctx, rec); err != ErrDuplicateServiceName {
		t.Fatalf("expected ErrDuplicateServiceName, got %v", err)
	}
}

func TestMemoryStore_ListExecutions(t *testing.T) {
	ms := NewMemoryStore()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		id := "exec-" + string(rune('a'+i))
		wf := "wf-a"
		if i >= 3 {
			wf = "wf-b"
		}
		_ = ms.CreateExecution(ctx, ExecutionRecord{ID: id, Workflow: wf, Input: nil})
	}

	all, _ := ms.ListExecutions(ctx, ExecutionFilter{})
	if len(all) != 5 {
		t.Fatalf("expected 5, got %d", len(all))
	}

	filtered, _ := ms.ListExecutions(ctx, ExecutionFilter{Workflow: "wf-a"})
	if len(filtered) != 3 {
		t.Fatalf("expected 3 wf-a, got %d", len(filtered))
	}
}

func TestMemoryStore_ListStates(t *testing.T) {
	ms := NewMemoryStore()
	ctx := context.Background()

	execID := "exec-states-test"
	_ = ms.CreateExecution(ctx, ExecutionRecord{ID: execID, Workflow: "wf"})

	for _, name := range []string{"StateA", "StateB", "StateC"} {
		_ = ms.WriteAheadState(ctx, StateRecord{ExecutionID: execID, StateName: name, Attempt: 1})
	}

	states, err := ms.ListStates(ctx, execID)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 {
		t.Fatalf("expected 3 states, got %d", len(states))
	}
}
