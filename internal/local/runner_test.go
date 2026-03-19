package local_test

import (
	"context"
	"testing"

	"github.com/pastorenue/kflow/internal/local"
	"github.com/pastorenue/kflow/pkg/kflow"
)

func TestRunLocal_EndToEnd(t *testing.T) {
	wf := kflow.New("e2e")
	wf.Task("Step1", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		return kflow.Output{"step1": true, "val": input["val"]}, nil
	})
	wf.Task("Step2", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
		return kflow.Output{"step2": true, "val": input["val"]}, nil
	})
	wf.Flow(
		kflow.Step("Step1").Next("Step2"),
		kflow.Step("Step2").End(),
	)

	if err := local.RunLocal(wf, kflow.Input{"val": 42}); err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
}

func TestRunLocal_InvalidWorkflow(t *testing.T) {
	wf := kflow.New("bad")
	// No Flow() call
	if err := local.RunLocal(wf, kflow.Input{}); err == nil {
		t.Fatal("expected error for invalid workflow")
	}
}
