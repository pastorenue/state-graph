package kflow

import (
	"context"
	"fmt"
	"time"
)

// Proto JSON wire-format structs for RegisterWorkflow (grpc-gateway camelCase).
// Matches proto/kflow/v1/workflow.proto WorkflowGraph / WorkflowState / WorkflowStep.

type protoStateJSON struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	ServiceTarget string `json:"serviceTarget,omitempty"`
	WaitSeconds   int64  `json:"waitSeconds,omitempty"`
	CatchState    string `json:"catchState,omitempty"`
}

type protoStepJSON struct {
	Name  string `json:"name"`
	Next  string `json:"next,omitempty"`
	Catch string `json:"catch,omitempty"`
	IsEnd bool   `json:"isEnd,omitempty"`
}

type protoGraphJSON struct {
	Name   string           `json:"name"`
	States []protoStateJSON `json:"states"`
	Steps  []protoStepJSON  `json:"steps"`
}

type registerWorkflowRequestJSON struct {
	Graph protoGraphJSON `json:"graph"`
}

func toRegisterJSON(wf *Workflow) registerWorkflowRequestJSON {
	tasks := wf.Tasks()
	steps := wf.Steps()

	states := make([]protoStateJSON, 0, len(steps))
	for _, step := range steps {
		td, ok := tasks[step.Name()]
		if !ok {
			continue
		}
		kind := "task"
		if td.IsChoice() {
			kind = "choice"
		} else if td.IsWait() {
			kind = "wait"
		} else if td.IsParallel() {
			kind = "parallel"
		}
		s := protoStateJSON{
			Name:          td.Name(),
			Kind:          kind,
			ServiceTarget: td.ServiceTarget(),
			CatchState:    td.CatchState(),
		}
		if td.IsWait() {
			s.WaitSeconds = int64(td.WaitDur().Seconds())
		}
		states = append(states, s)
	}

	protoSteps := make([]protoStepJSON, 0, len(steps))
	for _, step := range steps {
		protoSteps = append(protoSteps, protoStepJSON{
			Name:  step.Name(),
			Next:  step.NextState(),
			Catch: step.CatchState(),
			IsEnd: step.IsEnd(),
		})
	}

	return registerWorkflowRequestJSON{
		Graph: protoGraphJSON{
			Name:   wf.Name(),
			States: states,
			Steps:  protoSteps,
		},
	}
}

// localNode is an in-memory node used by the RunLocal executor.
type localNode struct {
	name  string
	task  *TaskDef
	next  string       // from step.NextState(); choice states use ChoiceFn instead
	catch string       // merged from step + task level (step takes precedence)
	retry *RetryPolicy // merged from step + task level (step takes precedence)
}

// localGraph is the compiled in-memory graph for RunLocal.
type localGraph struct {
	entry string
	nodes map[string]*localNode
}

func buildLocalGraph(wf *Workflow) (*localGraph, error) {
	tasks := wf.Tasks()
	steps := wf.Steps()

	if len(steps) == 0 {
		return nil, ErrNoEntryPoint
	}

	nodes := make(map[string]*localNode, len(steps))
	for _, step := range steps {
		td, ok := tasks[step.Name()]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownState, step.Name())
		}

		// Step-level retry/catch takes precedence over task-level.
		retry := td.RetryPolicy()
		if step.RetryPolicy() != nil {
			retry = step.RetryPolicy()
		}
		catch := td.CatchState()
		if step.CatchState() != "" {
			catch = step.CatchState()
		}

		nodes[step.Name()] = &localNode{
			name:  step.Name(),
			task:  td,
			next:  step.NextState(),
			catch: catch,
			retry: retry,
		}
	}

	return &localGraph{
		entry: steps[0].Name(),
		nodes: nodes,
	}, nil
}

func runLocalGraph(ctx context.Context, g *localGraph, input Input) error {
	current := g.entry
	for {
		node, ok := g.nodes[current]
		if !ok {
			return fmt.Errorf("unknown state: %q", current)
		}

		output, err := runLocalState(ctx, node, input)
		if err != nil {
			if node.catch != "" {
				errInput := cloneInput(input)
				errInput["_error"] = err.Error()
				input = errInput
				current = node.catch
				continue
			}
			return fmt.Errorf("state %q failed: %w", current, err)
		}

		next, err := nextLocalNode(node, output)
		if err != nil {
			return err
		}

		if next == "" || isSentinel(next) {
			return nil
		}

		if _, ok := g.nodes[next]; !ok {
			return fmt.Errorf("state %q: next state %q not in flow", current, next)
		}

		input = output
		current = next
	}
}

func runLocalState(ctx context.Context, node *localNode, input Input) (Output, error) {
	td := node.task

	if td.IsWait() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(td.WaitDur()):
			return input, nil
		}
	}

	if td.IsChoice() {
		fn := td.ChoiceFn()
		next, err := fn(ctx, input)
		if err != nil {
			return nil, err
		}
		out := cloneInput(input)
		out["__choice__"] = next
		return out, nil
	}

	fn := td.Fn()
	if fn == nil {
		return nil, fmt.Errorf("state %q uses InvokeService which is not supported in RunLocal", td.Name())
	}

	return runWithRetry(ctx, fn, input, node.retry)
}

func runWithRetry(ctx context.Context, fn HandlerFunc, input Input, rp *RetryPolicy) (Output, error) {
	maxAttempts := 1
	backoff := 0
	if rp != nil && rp.MaxAttempts > 0 {
		maxAttempts = rp.MaxAttempts
		backoff = rp.BackoffSeconds
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && backoff > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(backoff) * time.Second):
			}
		}
		output, err := fn(ctx, input)
		if err == nil {
			return output, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func nextLocalNode(node *localNode, output Output) (string, error) {
	if node.task.IsChoice() {
		choice, ok := output["__choice__"].(string)
		if !ok {
			return "", fmt.Errorf("choice state %q: __choice__ not set in output", node.name)
		}
		return choice, nil
	}
	return node.next, nil
}

func cloneInput(in Input) Input {
	out := make(Input, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
