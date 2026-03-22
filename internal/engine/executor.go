package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
	"github.com/pastorenue/kflow/pkg/kflow"
)

// ServiceInvoker dispatches an InvokeService step. Implemented by controller.ServiceDispatcher.
type ServiceInvoker interface {
	Dispatch(ctx context.Context, execID, stateName, serviceName string, input kflow.Input) (kflow.Output, error)
}

type Executor struct {
	Store      store.Store
	Handler    func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error)
	Dispatcher ServiceInvoker       // nil = no service dispatch
	Telemetry  *telemetry.EventWriter
	LogWriter  *telemetry.LogWriter // nil = no log capture
	Notify     func(execID, stateName, fromStatus, toStatus, errMsg string)
}

func (e *Executor) Run(ctx context.Context, execID string, g *Graph, input kflow.Input) error {
	e.logLine(ctx, execID, "", "INFO", fmt.Sprintf("executor: execution %q started", execID))
	node := g.EntryNode()
	current := input

	for node != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		output, err := e.executeState(ctx, execID, g, node, cloneInput(current))
		if err != nil {
			e.logLine(ctx, execID, "", "ERROR", fmt.Sprintf("executor: execution %q failed: %v", execID, err))
			return err
		}

		var choiceKey string
		if node.TaskDef.IsChoice() {
			if v, ok := output["__choice__"]; ok {
				choiceKey, _ = v.(string)
			}
		}

		var nextErr error
		node, nextErr = g.Next(node, choiceKey)
		if nextErr != nil {
			e.logLine(ctx, execID, "", "ERROR", fmt.Sprintf("executor: execution %q failed: %v", execID, nextErr))
			return nextErr
		}
		current = output
	}

	e.logLine(ctx, execID, "", "INFO", fmt.Sprintf("executor: execution %q completed", execID))
	return nil
}

func (e *Executor) executeState(ctx context.Context, execID string, g *Graph, node *Node, input kflow.Input) (kflow.Output, error) {
	e.logLine(ctx, execID, node.Name, "INFO", fmt.Sprintf("executor: [%s] state %q starting", execID, node.Name))

	var resumeAt *time.Time
	if node.TaskDef.IsWait() {
		t := time.Now().Add(node.TaskDef.WaitDur())
		resumeAt = &t
	}

	sr := store.StateRecord{
		ExecutionID: execID,
		StateName:   node.Name,
		Input:       input,
		Attempt:     1,
		ResumeAt:    resumeAt,
	}

	if err := e.Store.WriteAheadState(ctx, sr); err != nil {
		if err == store.ErrStateAlreadyTerminal {
			// idempotency: state already completed, return stored output
			return e.Store.GetStateOutput(ctx, execID, node.Name)
		}
		return nil, err
	}

	if err := e.Store.MarkRunning(ctx, execID, node.Name); err != nil {
		return nil, err
	}
	e.recordTransition(ctx, execID, node.Name, string(store.StatusPending), string(store.StatusRunning), "")

	var output kflow.Output
	var handlerErr error

	if node.TaskDef.IsWait() {
		time.Sleep(time.Until(*resumeAt))
		output = kflow.Output{}
	} else if target := node.TaskDef.ServiceTarget(); target != "" && e.Dispatcher != nil {
		output, handlerErr = e.Dispatcher.Dispatch(ctx, execID, node.Name, target, input)
	} else {
		output, handlerErr = e.applyRetry(ctx, execID, node, input)
	}

	if handlerErr == nil {
		if err := e.Store.CompleteState(ctx, execID, node.Name, output); err != nil {
			return nil, err
		}
		e.recordTransition(ctx, execID, node.Name, string(store.StatusRunning), string(store.StatusCompleted), "")
		e.logLine(ctx, execID, node.Name, "INFO", fmt.Sprintf("executor: [%s] state %q completed", execID, node.Name))
		return output, nil
	}

	// handler failed — mark final attempt as failed
	e.logLine(ctx, execID, node.Name, "ERROR", fmt.Sprintf("executor: [%s] state %q failed: %v", execID, node.Name, handlerErr))
	if err := e.Store.FailState(ctx, execID, node.Name, handlerErr.Error()); err != nil {
		return nil, err
	}
	e.recordTransition(ctx, execID, node.Name, string(store.StatusRunning), string(store.StatusFailed), handlerErr.Error())

	if node.Catch != "" {
		e.logLine(ctx, execID, node.Name, "WARN", fmt.Sprintf("executor: [%s] state %q → catch %q", execID, node.Name, node.Catch))
		catchInput := mergeErrorKey(input, handlerErr)
		catchNode := g.Node(node.Catch)
		if catchNode == nil {
			return nil, handlerErr
		}
		return e.executeState(ctx, execID, g, catchNode, catchInput)
	}

	return nil, handlerErr
}

func (e *Executor) applyRetry(ctx context.Context, execID string, node *Node, input kflow.Input) (kflow.Output, error) {
	policy := node.Retry
	maxAttempts := 1
	backoff := time.Duration(0)

	if policy != nil {
		if policy.MaxAttempts > 1 {
			maxAttempts = policy.MaxAttempts
		}
		backoff = time.Duration(policy.BackoffSeconds) * time.Second
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			e.logLine(ctx, execID, node.Name, "WARN", fmt.Sprintf("executor: [%s] state %q retry %d/%d", execID, node.Name, attempt, maxAttempts))
			// mark failed before retry write-ahead
			if err := e.Store.FailState(ctx, execID, node.Name, lastErr.Error()); err != nil {
				return nil, err
			}
			e.recordTransition(ctx, execID, node.Name, string(store.StatusRunning), string(store.StatusFailed), lastErr.Error())
			sr := store.StateRecord{
				ExecutionID: execID,
				StateName:   node.Name,
				Input:       input,
				Attempt:     attempt,
			}
			if err := e.Store.WriteAheadState(ctx, sr); err != nil {
				return nil, err
			}
			if err := e.Store.MarkRunning(ctx, execID, node.Name); err != nil {
				return nil, err
			}
			e.recordTransition(ctx, execID, node.Name, string(store.StatusPending), string(store.StatusRunning), "")
			if backoff > 0 {
				time.Sleep(backoff)
			}
		}

		output, err := e.Handler(ctx, node.Name, input)
		if err == nil {
			return output, nil
		}
		lastErr = err
	}

	return nil, lastErr
}

func (e *Executor) logLine(ctx context.Context, execID, stateName, level, msg string) {
	log.Print(msg)
	if e.LogWriter != nil {
		e.LogWriter.Write(ctx, execID, "", stateName, level, msg)
	}
}

func (e *Executor) recordTransition(ctx context.Context, execID, state, from, to, errMsg string) {
	e.Telemetry.RecordStateTransition(ctx, execID, state, from, to, errMsg)
	if e.Notify != nil {
		e.Notify(execID, state, from, to, errMsg)
	}
}

func mergeErrorKey(input kflow.Input, err error) kflow.Input {
	merged := make(kflow.Input, len(input)+1)
	for k, v := range input {
		merged[k] = v
	}
	merged["_error"] = err.Error()
	return merged
}

func cloneInput(input kflow.Input) kflow.Input {
	cp := make(kflow.Input, len(input))
	for k, v := range input {
		cp[k] = v
	}
	return cp
}
