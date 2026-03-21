package kflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pastorenue/kflow/pkg/kflow"
)

func TestRunLocal_Linear(t *testing.T) {
	order := make([]string, 0, 2)

	wf := kflow.New("linear-wf")
	wf.Task("A", func(_ context.Context, in kflow.Input) (kflow.Output, error) {
		order = append(order, "A")
		return kflow.Output{"x": 1}, nil
	})
	wf.Task("B", func(_ context.Context, in kflow.Input) (kflow.Output, error) {
		order = append(order, "B")
		return kflow.Output{"x": 2}, nil
	})
	wf.Flow(
		kflow.Step("A").Next("B"),
		kflow.Step("B").End(),
	)

	if err := kflow.RunLocal(wf, kflow.Input{}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if len(order) != 2 || order[0] != "A" || order[1] != "B" {
		t.Fatalf("unexpected execution order: %v", order)
	}
}

func TestRunLocal_Choice(t *testing.T) {
	executed := ""

	wf := kflow.New("choice-wf")
	wf.Choice("route", func(_ context.Context, in kflow.Input) (string, error) {
		if in["take"] == "yes" {
			return "yes", nil
		}
		return "no", nil
	})
	wf.Task("yes", func(_ context.Context, _ kflow.Input) (kflow.Output, error) {
		executed = "yes"
		return kflow.Output{}, nil
	})
	wf.Task("no", func(_ context.Context, _ kflow.Input) (kflow.Output, error) {
		executed = "no"
		return kflow.Output{}, nil
	})
	wf.Flow(
		kflow.Step("route"),
		kflow.Step("yes").End(),
		kflow.Step("no").End(),
	)

	if err := kflow.RunLocal(wf, kflow.Input{"take": "yes"}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if executed != "yes" {
		t.Fatalf("want executed=yes, got %q", executed)
	}

	executed = ""
	if err := kflow.RunLocal(wf, kflow.Input{"take": "no"}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if executed != "no" {
		t.Fatalf("want executed=no, got %q", executed)
	}
}

func TestRunLocal_CatchOnError(t *testing.T) {
	wf := kflow.New("catch-wf")
	wf.Task("risky", func(_ context.Context, _ kflow.Input) (kflow.Output, error) {
		return nil, errors.New("boom")
	})
	wf.Task("handle", func(_ context.Context, in kflow.Input) (kflow.Output, error) {
		if in["_error"] != "boom" {
			return nil, errors.New("expected _error=boom")
		}
		return kflow.Output{}, nil
	})
	wf.Flow(
		kflow.Step("risky").Catch("handle"),
		kflow.Step("handle").End(),
	)

	if err := kflow.RunLocal(wf, kflow.Input{}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestRunLocal_InvalidWorkflow(t *testing.T) {
	wf := kflow.New("bad-wf")
	// No Flow() call — should fail validation
	if err := kflow.RunLocal(wf, kflow.Input{}); err == nil {
		t.Fatal("want error, got nil")
	}
}
