// Package local provides RunLocal: in-process workflow execution using MemoryStore.
// This is the composition root for local development and testing.
// Production code uses kflow.Run() which dispatches via the Control Plane.
package local

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/pastorenue/kflow/internal/engine"
	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// RunLocal executes the workflow in-process using MemoryStore (no Kubernetes).
func RunLocal(wf *kflow.Workflow, input kflow.Input) error {
	g, err := engine.Build(wf)
	if err != nil {
		return fmt.Errorf("kflow.RunLocal: %w", err)
	}

	execID, err := newUUID()
	if err != nil {
		return fmt.Errorf("kflow.RunLocal: uuid: %w", err)
	}

	ms := store.NewMemoryStore()
	ctx := context.Background()

	if err := ms.CreateExecution(ctx, store.ExecutionRecord{
		ID:       execID,
		Workflow: wf.Name(),
		Input:    input,
	}); err != nil {
		return fmt.Errorf("kflow.RunLocal: create execution: %w", err)
	}

	if err := ms.UpdateExecution(ctx, execID, store.StatusRunning); err != nil {
		return fmt.Errorf("kflow.RunLocal: mark running: %w", err)
	}

	ex := &engine.Executor{
		Store: ms,
		Handler: func(ctx context.Context, stateName string, inp kflow.Input) (kflow.Output, error) {
			td := wf.Tasks()[stateName]
			if td == nil {
				return nil, fmt.Errorf("kflow.RunLocal: unknown state %q", stateName)
			}
			if td.IsChoice() {
				choice, err := td.ChoiceFn()(ctx, inp)
				if err != nil {
					return nil, err
				}
				return kflow.Output{"__choice__": choice}, nil
			}
			return td.Fn()(ctx, inp)
		},
	}

	runErr := ex.Run(ctx, execID, g, input)

	finalStatus := store.StatusCompleted
	if runErr != nil {
		finalStatus = store.StatusFailed
	}
	_ = ms.UpdateExecution(ctx, execID, finalStatus)

	return runErr
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
