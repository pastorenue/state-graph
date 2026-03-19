package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

func newExec(id string) store.ExecutionRecord {
	return store.ExecutionRecord{
		ID:       id,
		Workflow: "test-workflow",
		Input:    kflow.Input{"key": "val"},
	}
}

func newState(execID, stateName string) store.StateRecord {
	return store.StateRecord{
		ExecutionID: execID,
		StateName:   stateName,
		Input:       kflow.Input{"x": 1},
		Attempt:     1,
	}
}

func TestMemoryStore_CreateAndGet(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	rec := newExec("exec-1")
	if err := ms.CreateExecution(ctx, rec); err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	got, err := ms.GetExecution(ctx, "exec-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.ID != "exec-1" || got.Workflow != "test-workflow" {
		t.Errorf("unexpected record: %+v", got)
	}
	if got.Status != store.StatusPending {
		t.Errorf("expected Pending, got %s", got.Status)
	}
}

func TestMemoryStore_CreateDuplicate(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	rec := newExec("exec-dup")
	if err := ms.CreateExecution(ctx, rec); err != nil {
		t.Fatalf("first CreateExecution: %v", err)
	}
	if err := ms.CreateExecution(ctx, rec); err == nil {
		t.Fatal("expected error on duplicate, got nil")
	}
}

func TestMemoryStore_GetMissing(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	_, err := ms.GetExecution(ctx, "missing")
	if err != store.ErrExecutionNotFound {
		t.Fatalf("expected ErrExecutionNotFound, got %v", err)
	}
}

func TestMemoryStore_UpdateExecution(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-upd")); err != nil {
		t.Fatal(err)
	}
	if err := ms.UpdateExecution(ctx, "exec-upd", store.StatusRunning); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}
	got, _ := ms.GetExecution(ctx, "exec-upd")
	if got.Status != store.StatusRunning {
		t.Errorf("expected Running, got %s", got.Status)
	}
}

func TestMemoryStore_WriteAheadTerminal(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-t")); err != nil {
		t.Fatal(err)
	}
	sr := newState("exec-t", "step-a")
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatal(err)
	}
	if err := ms.MarkRunning(ctx, "exec-t", "step-a"); err != nil {
		t.Fatal(err)
	}
	if err := ms.CompleteState(ctx, "exec-t", "step-a", kflow.Output{"r": 1}); err != nil {
		t.Fatal(err)
	}
	// Second WriteAhead on completed record must fail
	if err := ms.WriteAheadState(ctx, sr); err != store.ErrStateAlreadyTerminal {
		t.Fatalf("expected ErrStateAlreadyTerminal, got %v", err)
	}
}

func TestMemoryStore_WriteAheadOnFailed(t *testing.T) {
	// Failed states can be overwritten (retry scenario); only Completed is terminal.
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-f")); err != nil {
		t.Fatal(err)
	}
	sr := newState("exec-f", "step-b")
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatal(err)
	}
	if err := ms.MarkRunning(ctx, "exec-f", "step-b"); err != nil {
		t.Fatal(err)
	}
	if err := ms.FailState(ctx, "exec-f", "step-b", "boom"); err != nil {
		t.Fatal(err)
	}
	// WriteAheadState on a Failed record must succeed (allows retry)
	sr.Attempt = 2
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatalf("expected WriteAheadState to succeed on Failed record for retry, got %v", err)
	}
}

func TestMemoryStore_WriteAheadOverwrite(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-ow")); err != nil {
		t.Fatal(err)
	}
	sr := newState("exec-ow", "step-c")
	// First write-ahead
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatal(err)
	}
	// Mark Running (non-terminal)
	if err := ms.MarkRunning(ctx, "exec-ow", "step-c"); err != nil {
		t.Fatal(err)
	}
	// Second write-ahead on Running state should overwrite (retry scenario)
	sr.Attempt = 2
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatalf("expected overwrite of non-terminal state, got %v", err)
	}
}

func TestMemoryStore_MarkRunning(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-mr")); err != nil {
		t.Fatal(err)
	}
	sr := newState("exec-mr", "step-d")
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatal(err)
	}
	if err := ms.MarkRunning(ctx, "exec-mr", "step-d"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
}

func TestMemoryStore_CompleteState(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-cs")); err != nil {
		t.Fatal(err)
	}
	sr := newState("exec-cs", "step-e")
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-cs", "step-e")
	if err := ms.CompleteState(ctx, "exec-cs", "step-e", kflow.Output{"out": 42}); err != nil {
		t.Fatalf("CompleteState: %v", err)
	}
	out, err := ms.GetStateOutput(ctx, "exec-cs", "step-e")
	if err != nil {
		t.Fatal(err)
	}
	if out["out"] != 42 {
		t.Errorf("unexpected output: %v", out)
	}
}

func TestMemoryStore_FailState(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	if err := ms.CreateExecution(ctx, newExec("exec-fs")); err != nil {
		t.Fatal(err)
	}
	sr := newState("exec-fs", "step-f")
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-fs", "step-f")
	if err := ms.FailState(ctx, "exec-fs", "step-f", "something went wrong"); err != nil {
		t.Fatalf("FailState: %v", err)
	}
}

func TestMemoryStore_GetStateOutput_OK(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, newExec("exec-go"))
	sr := newState("exec-go", "step-g")
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-go", "step-g")
	ms.CompleteState(ctx, "exec-go", "step-g", kflow.Output{"result": "ok"})
	out, err := ms.GetStateOutput(ctx, "exec-go", "step-g")
	if err != nil {
		t.Fatalf("GetStateOutput: %v", err)
	}
	if out["result"] != "ok" {
		t.Errorf("expected result=ok, got %v", out)
	}
}

func TestMemoryStore_GetStateOutput_NotFound(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	_, err := ms.GetStateOutput(ctx, "exec-nf", "step-z")
	if err != store.ErrStateNotFound {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}
}

func TestMemoryStore_GetStateOutput_NotCompleted(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, newExec("exec-nc"))
	sr := newState("exec-nc", "step-h")
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-nc", "step-h")
	_, err := ms.GetStateOutput(ctx, "exec-nc", "step-h")
	if err != store.ErrStateNotCompleted {
		t.Fatalf("expected ErrStateNotCompleted, got %v", err)
	}
}

func TestMemoryStore_ConcurrentWrites(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("exec-conc-%d", i)
			ms.CreateExecution(ctx, store.ExecutionRecord{ID: id, Workflow: "w"})
			ms.WriteAheadState(ctx, store.StateRecord{ExecutionID: id, StateName: "s", Attempt: 1})
			ms.MarkRunning(ctx, id, "s")
			ms.CompleteState(ctx, id, "s", kflow.Output{"i": i})
		}(i)
	}
	wg.Wait()
}
