package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pastorenue/kflow/internal/engine"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// helpers

func taskFn(output kflow.Output) kflow.HandlerFunc {
	return func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		return output, nil
	}
}

func errorFn(err error) kflow.HandlerFunc {
	return func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		return nil, err
	}
}

func passthroughFn() kflow.HandlerFunc {
	return func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		return input, nil
	}
}

func buildLinearWf() *kflow.Workflow {
	wf := kflow.New("linear")
	wf.Task("A", taskFn(kflow.Output{"a": 1}))
	wf.Task("B", passthroughFn())
	wf.Flow(
		kflow.Step("A").Next("B"),
		kflow.Step("B").End(),
	)
	return wf
}

func newExecutor(ms *store.MemoryStore, handlers map[string]kflow.HandlerFunc) *engine.Executor {
	return &engine.Executor{
		Store: ms,
		Handler: func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
			if h, ok := handlers[stateName]; ok {
				return h(ctx, input)
			}
			return input, nil
		},
	}
}

// Graph tests

func TestGraph_Build_Linear(t *testing.T) {
	wf := buildLinearWf()
	g, err := engine.Build(wf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if g.EntryNode() == nil {
		t.Fatal("expected entry node, got nil")
	}
	if g.EntryNode().Name != "A" {
		t.Errorf("expected entry A, got %s", g.EntryNode().Name)
	}
}

func TestGraph_Build_InvalidWorkflow(t *testing.T) {
	wf := kflow.New("empty")
	// no Flow → ErrNoEntryPoint
	_, err := engine.Build(wf)
	if !errors.Is(err, kflow.ErrNoEntryPoint) {
		t.Fatalf("expected ErrNoEntryPoint, got %v", err)
	}
}

func TestGraph_Build_EmptyNext(t *testing.T) {
	wf := kflow.New("bad")
	wf.Task("A", taskFn(nil))
	// Step has no Next and is not End()
	wf.Flow(kflow.Step("A"))
	_, err := engine.Build(wf)
	if err == nil {
		t.Fatal("expected error for step with no Next and not End()")
	}
}

func TestGraph_Next_Terminal(t *testing.T) {
	wf := buildLinearWf()
	g, _ := engine.Build(wf)
	nodeB := g.Node("B")
	next, err := g.Next(nodeB, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next != nil {
		t.Errorf("expected nil for terminal node, got %s", next.Name)
	}
}

func TestGraph_Next_Choice_Valid(t *testing.T) {
	wf := kflow.New("choice-wf")
	wf.Choice("Router", func(ctx context.Context, input kflow.Input) (string, error) {
		return "B", nil
	})
	wf.Task("B", taskFn(kflow.Output{"b": 1}))
	wf.Task("C", taskFn(kflow.Output{"c": 1}))
	wf.Flow(
		kflow.Step("Router").Next("B"),
		kflow.Step("B").End(),
		kflow.Step("C").End(),
	)
	g, err := engine.Build(wf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	routerNode := g.Node("Router")
	next, err := g.Next(routerNode, "B")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next == nil || next.Name != "B" {
		t.Errorf("expected B, got %v", next)
	}
}

func TestGraph_Next_Choice_Unknown(t *testing.T) {
	wf := kflow.New("choice-wf2")
	wf.Choice("Router", func(ctx context.Context, input kflow.Input) (string, error) {
		return "B", nil
	})
	wf.Task("B", taskFn(nil))
	wf.Flow(
		kflow.Step("Router").Next("B"),
		kflow.Step("B").End(),
	)
	g, _ := engine.Build(wf)
	routerNode := g.Node("Router")
	_, err := g.Next(routerNode, "UNKNOWN")
	if err == nil {
		t.Fatal("expected error for unknown choice key")
	}
}

// Executor tests

func TestExecutor_LinearFlow(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e1", Workflow: "linear"})

	wf := buildLinearWf()
	g, _ := engine.Build(wf)

	var bInput kflow.Input
	ex := &engine.Executor{
		Store: ms,
		Handler: func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
			if stateName == "B" {
				bInput = input
			}
			return input, nil
		},
	}

	if err := ex.Run(ctx, "e1", g, kflow.Input{"start": true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// B receives A's output (which is the passthrough of A's input merged from start input)
	// A returns input unchanged via passthroughFn, so B gets {"start": true}
	if bInput == nil {
		t.Fatal("B handler never called")
	}
}

func TestExecutor_RetrySuccess(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-retry", Workflow: "retry-wf"})

	callCount := 0
	wf := kflow.New("retry-wf")
	wf.Task("A", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		callCount++
		if callCount < 3 {
			return nil, errors.New("transient")
		}
		return kflow.Output{"done": true}, nil
	}).Retry(kflow.RetryPolicy{MaxAttempts: 3})
	wf.Flow(kflow.Step("A").End())

	g, _ := engine.Build(wf)
	ex := newExecutor(ms, nil)
	// Override handler with the actual task fn
	ex.Handler = func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
		return wf.Tasks()["A"].Fn()(ctx, input)
	}

	if err := ex.Run(ctx, "e-retry", g, kflow.Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

func TestExecutor_RetryExhausted_NoCatch(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-exhaust", Workflow: "wf"})

	wf := kflow.New("wf")
	wf.Task("A", errorFn(errors.New("always fails"))).Retry(kflow.RetryPolicy{MaxAttempts: 2})
	wf.Flow(kflow.Step("A").End())

	g, _ := engine.Build(wf)
	ex := &engine.Executor{
		Store: ms,
		Handler: func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
			return wf.Tasks()["A"].Fn()(ctx, input)
		},
	}

	err := ex.Run(ctx, "e-exhaust", g, kflow.Input{})
	if err == nil {
		t.Fatal("expected error after all retries exhausted")
	}
}

func TestExecutor_RetryExhausted_WithCatch(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-catch", Workflow: "wf"})

	var catchInput kflow.Input
	wf := kflow.New("wf")
	wf.Task("A", errorFn(errors.New("fail"))).Retry(kflow.RetryPolicy{MaxAttempts: 1}).Catch("Fallback")
	wf.Task("Fallback", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		catchInput = input
		return kflow.Output{"recovered": true}, nil
	})
	wf.Flow(
		kflow.Step("A").Next("Fallback").Catch("Fallback"),
		kflow.Step("Fallback").End(),
	)

	g, _ := engine.Build(wf)
	handlers := map[string]kflow.HandlerFunc{
		"A":        wf.Tasks()["A"].Fn(),
		"Fallback": wf.Tasks()["Fallback"].Fn(),
	}
	ex := newExecutor(ms, handlers)

	if err := ex.Run(ctx, "e-catch", g, kflow.Input{"x": 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if catchInput == nil {
		t.Fatal("Fallback handler never called")
	}
	if catchInput["_error"] == nil {
		t.Error("expected _error key in catch input")
	}
	if catchInput["x"] != 1 {
		t.Errorf("expected original key x=1, got %v", catchInput["x"])
	}
}

func TestExecutor_CatchInput_ErrorKeyOverwrite(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-overwrite", Workflow: "wf"})

	var catchInput kflow.Input
	wf := kflow.New("wf")
	wf.Task("A", errorFn(errors.New("new error"))).Catch("Fallback")
	wf.Task("Fallback", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		catchInput = input
		return kflow.Output{}, nil
	})
	wf.Flow(
		kflow.Step("A").Next("Fallback").Catch("Fallback"),
		kflow.Step("Fallback").End(),
	)

	g, _ := engine.Build(wf)
	handlers := map[string]kflow.HandlerFunc{
		"A":        wf.Tasks()["A"].Fn(),
		"Fallback": wf.Tasks()["Fallback"].Fn(),
	}
	ex := newExecutor(ms, handlers)

	// Input already has _error key
	if err := ex.Run(ctx, "e-overwrite", g, kflow.Input{"_error": "old error"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if catchInput["_error"] != "new error" {
		t.Errorf("expected _error overwritten to 'new error', got %v", catchInput["_error"])
	}
}

func TestExecutor_Choice(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-choice", Workflow: "wf"})

	var chosenState string
	wf := kflow.New("wf")
	wf.Choice("Router", func(ctx context.Context, input kflow.Input) (string, error) {
		return "B", nil
	})
	wf.Task("B", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		chosenState = "B"
		return kflow.Output{}, nil
	})
	wf.Task("C", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		chosenState = "C"
		return kflow.Output{}, nil
	})
	wf.Flow(
		kflow.Step("Router").Next("B"),
		kflow.Step("B").End(),
		kflow.Step("C").End(),
	)

	g, _ := engine.Build(wf)
	ex := &engine.Executor{
		Store: ms,
		Handler: func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
			td := wf.Tasks()[stateName]
			if td.IsChoice() {
				choice, err := td.ChoiceFn()(ctx, input)
				if err != nil {
					return nil, err
				}
				return kflow.Output{"__choice__": choice}, nil
			}
			return td.Fn()(ctx, input)
		},
	}

	if err := ex.Run(ctx, "e-choice", g, kflow.Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if chosenState != "B" {
		t.Errorf("expected B to be chosen, got %s", chosenState)
	}
}

func TestExecutor_Wait(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-wait", Workflow: "wf"})

	wf := kflow.New("wf")
	wf.Wait("W", 10*time.Millisecond)
	wf.Flow(kflow.Step("W").End())

	g, _ := engine.Build(wf)
	ex := newExecutor(ms, nil)

	start := time.Now()
	if err := ex.Run(ctx, "e-wait", g, kflow.Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 10*time.Millisecond {
		t.Errorf("expected wait of ~10ms, elapsed %v", elapsed)
	}
}

func TestExecutor_Idempotency(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-idem", Workflow: "wf"})

	// Pre-seed A as completed
	ms.WriteAheadState(ctx, store.StateRecord{ExecutionID: "e-idem", StateName: "A", Attempt: 1})
	ms.MarkRunning(ctx, "e-idem", "A")
	ms.CompleteState(ctx, "e-idem", "A", kflow.Output{"pre": "seeded"})

	callCount := 0
	wf := kflow.New("wf")
	wf.Task("A", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		callCount++
		return kflow.Output{"fresh": true}, nil
	})
	wf.Flow(kflow.Step("A").End())

	g, _ := engine.Build(wf)
	ex := newExecutor(ms, map[string]kflow.HandlerFunc{
		"A": wf.Tasks()["A"].Fn(),
	})

	if err := ex.Run(ctx, "e-idem", g, kflow.Input{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 0 {
		t.Errorf("expected 0 handler calls (idempotent), got %d", callCount)
	}
}

func TestExecutor_ContextCancel(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	ms.CreateExecution(ctx, store.ExecutionRecord{ID: "e-cancel", Workflow: "wf"})

	wf := kflow.New("wf")
	wf.Task("A", passthroughFn())
	wf.Task("B", passthroughFn())
	wf.Flow(
		kflow.Step("A").Next("B"),
		kflow.Step("B").End(),
	)

	g, _ := engine.Build(wf)
	callCount := 0
	ex := &engine.Executor{
		Store: ms,
		Handler: func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
			callCount++
			if stateName == "A" {
				cancel() // cancel after first state
			}
			return input, nil
		},
	}

	err := ex.Run(ctx, "e-cancel", g, kflow.Input{})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if callCount != 1 {
		t.Errorf("expected 1 handler call before cancel, got %d", callCount)
	}
}

