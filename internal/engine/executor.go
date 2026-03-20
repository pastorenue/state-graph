package engine

import (
	"context"
	"log"
	"time"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/pkg/kflow"
)

type Executor struct {
	Store   store.Store
	Handler func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error)
}

func (e *Executor) Run(ctx context.Context, execID string, g *Graph, input kflow.Input) error {
	log.Printf("executor: execution %q started", execID)
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
			log.Printf("executor: execution %q failed: %v", execID, err)
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
			log.Printf("executor: execution %q failed: %v", execID, nextErr)
			return nextErr
		}
		current = output
	}

	log.Printf("executor: execution %q completed", execID)
	return nil
}

func (e *Executor) executeState(ctx context.Context, execID string, g *Graph, node *Node, input kflow.Input) (kflow.Output, error) {
	log.Printf("executor: [%s] state %q starting", execID, node.Name)

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

	var output kflow.Output
	var handlerErr error

	if node.TaskDef.IsWait() {
		time.Sleep(time.Until(*resumeAt))
		output = kflow.Output{}
	} else {
		output, handlerErr = e.applyRetry(ctx, execID, node, input)
	}

	if handlerErr == nil {
		if err := e.Store.CompleteState(ctx, execID, node.Name, output); err != nil {
			return nil, err
		}
		log.Printf("executor: [%s] state %q completed", execID, node.Name)
		return output, nil
	}

	// handler failed — mark final attempt as failed
	log.Printf("executor: [%s] state %q failed: %v", execID, node.Name, handlerErr)
	if err := e.Store.FailState(ctx, execID, node.Name, handlerErr.Error()); err != nil {
		return nil, err
	}

	if node.Catch != "" {
		log.Printf("executor: [%s] state %q → catch %q", execID, node.Name, node.Catch)
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
			log.Printf("executor: [%s] state %q retry %d/%d", execID, node.Name, attempt, maxAttempts)
			// mark failed before retry write-ahead
			if err := e.Store.FailState(ctx, execID, node.Name, lastErr.Error()); err != nil {
				return nil, err
			}
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
