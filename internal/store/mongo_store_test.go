package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

func requireMongo(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("KFLOW_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("KFLOW_TEST_MONGO_URI not set; skipping MongoDB integration tests")
	}
	return uri
}

func newMongoStore(t *testing.T) *store.MongoStore {
	t.Helper()
	uri := requireMongo(t)
	dbName := fmt.Sprintf("kflow_test_%d", rand.Int63())
	ctx := context.Background()
	ms, err := store.NewMongoStore(ctx, uri, dbName)
	if err != nil {
		t.Fatalf("NewMongoStore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.DropDatabase(context.Background()); err != nil {
			t.Logf("cleanup: drop db: %v", err)
		}
	})
	return ms
}

func TestMongoCreateAndGetExecution(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	rec := store.ExecutionRecord{
		ID:       "exec-mongo-1",
		Workflow: "my-workflow",
		Input:    kflow.Input{"key": "val"},
	}
	if err := ms.CreateExecution(ctx, rec); err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	got, err := ms.GetExecution(ctx, "exec-mongo-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.ID != "exec-mongo-1" || got.Workflow != "my-workflow" {
		t.Errorf("unexpected record: %+v", got)
	}
	if got.Status != store.StatusPending {
		t.Errorf("expected Pending, got %s", got.Status)
	}
}

func TestMongoUpdateExecution(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-upd", Workflow: "wf"})
	ms.UpdateExecution(ctx, "exec-upd", store.StatusRunning)

	got, _ := ms.GetExecution(ctx, "exec-upd")
	if got.Status != store.StatusRunning {
		t.Errorf("expected Running, got %s", got.Status)
	}

	ms.UpdateExecution(ctx, "exec-upd", store.StatusCompleted)
	got, _ = ms.GetExecution(ctx, "exec-upd")
	if got.Status != store.StatusCompleted {
		t.Errorf("expected Completed, got %s", got.Status)
	}
}

func TestMongoWriteAheadAndComplete(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-wac", Workflow: "wf"})

	sr := store.StateRecord{
		ExecutionID: "exec-wac",
		StateName:   "step-a",
		Input:       kflow.Input{"x": 1},
		Attempt:     1,
	}
	if err := ms.WriteAheadState(ctx, sr); err != nil {
		t.Fatalf("WriteAheadState: %v", err)
	}
	if err := ms.MarkRunning(ctx, "exec-wac", "step-a"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := ms.CompleteState(ctx, "exec-wac", "step-a", kflow.Output{"result": "ok"}); err != nil {
		t.Fatalf("CompleteState: %v", err)
	}

	out, err := ms.GetStateOutput(ctx, "exec-wac", "step-a")
	if err != nil {
		t.Fatalf("GetStateOutput: %v", err)
	}
	if out["result"] != "ok" {
		t.Errorf("expected result=ok, got %v", out)
	}
}

func TestMongoWriteAheadIdempotency(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-idem", Workflow: "wf"})
	sr := store.StateRecord{ExecutionID: "exec-idem", StateName: "s", Attempt: 1}
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-idem", "s")
	ms.CompleteState(ctx, "exec-idem", "s", kflow.Output{"done": true})

	// Second WriteAheadState on completed record must return ErrStateAlreadyTerminal.
	if err := ms.WriteAheadState(ctx, sr); err != store.ErrStateAlreadyTerminal {
		t.Fatalf("expected ErrStateAlreadyTerminal, got %v", err)
	}
}

func TestMongoRetryAttempts(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-retry", Workflow: "wf"})

	// Attempt 1 — fails
	sr1 := store.StateRecord{ExecutionID: "exec-retry", StateName: "s", Attempt: 1}
	ms.WriteAheadState(ctx, sr1)
	ms.MarkRunning(ctx, "exec-retry", "s")
	ms.FailState(ctx, "exec-retry", "s", "transient error")

	// Attempt 2 — fails
	sr2 := store.StateRecord{ExecutionID: "exec-retry", StateName: "s", Attempt: 2}
	if err := ms.WriteAheadState(ctx, sr2); err != nil {
		t.Fatalf("WriteAheadState attempt 2: %v", err)
	}
	ms.MarkRunning(ctx, "exec-retry", "s")
	ms.FailState(ctx, "exec-retry", "s", "still failing")

	// Attempt 3 — succeeds
	sr3 := store.StateRecord{ExecutionID: "exec-retry", StateName: "s", Attempt: 3}
	if err := ms.WriteAheadState(ctx, sr3); err != nil {
		t.Fatalf("WriteAheadState attempt 3: %v", err)
	}
	ms.MarkRunning(ctx, "exec-retry", "s")
	if err := ms.CompleteState(ctx, "exec-retry", "s", kflow.Output{"ok": true}); err != nil {
		t.Fatalf("CompleteState attempt 3: %v", err)
	}

	out, err := ms.GetStateOutput(ctx, "exec-retry", "s")
	if err != nil {
		t.Fatalf("GetStateOutput: %v", err)
	}
	if out["ok"] != true {
		t.Errorf("expected ok=true, got %v", out)
	}
}

func TestMongoGetStateOutput_NotFound(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	_, err := ms.GetStateOutput(ctx, "exec-nf", "s")
	if err != store.ErrStateNotFound {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}
}

func TestMongoGetStateOutput_NotCompleted(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-nc", Workflow: "wf"})
	sr := store.StateRecord{ExecutionID: "exec-nc", StateName: "s", Attempt: 1}
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-nc", "s")

	_, err := ms.GetStateOutput(ctx, "exec-nc", "s")
	if err != store.ErrStateNotCompleted {
		t.Fatalf("expected ErrStateNotCompleted, got %v", err)
	}
}

func TestMongoEnsureIndexes_Idempotent(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	// EnsureIndexes is called by NewMongoStore; call it again — must not error.
	if err := ms.EnsureIndexes(ctx); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
}

func TestMongoStore_LargeOutput_NoObjectStore(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-large", Workflow: "wf"})
	sr := store.StateRecord{ExecutionID: "exec-large", StateName: "s", Attempt: 1}
	ms.WriteAheadState(ctx, sr)
	ms.MarkRunning(ctx, "exec-large", "s")

	// Build an output exceeding 1 MB.
	large := make([]byte, 1<<20+100)
	for i := range large {
		large[i] = 'x'
	}
	bigOutput := kflow.Output{"data": string(large)}

	err := ms.CompleteState(ctx, "exec-large", "s", bigOutput)
	if err != store.ErrOutputTooLarge {
		t.Fatalf("expected ErrOutputTooLarge, got %v", err)
	}
}

// Ensure timestamps are populated.
func TestMongoTimestamps(t *testing.T) {
	ms := newMongoStore(t)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "exec-ts", Workflow: "wf"})
	got, _ := ms.GetExecution(ctx, "exec-ts")
	after := time.Now().Add(time.Second)

	if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
		t.Errorf("CreatedAt out of range: %v", got.CreatedAt)
	}
}
