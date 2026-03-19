package engine

import (
	"fmt"

	"github.com/pastorenue/kflow/pkg/kflow"
)

type Node struct {
	Name     string
	TaskDef  *kflow.TaskDef
	Next     string // successor name, kflow.Succeed, kflow.Fail, or ""
	Catch    string
	Retry    *kflow.RetryPolicy
	Terminal bool
}

func (n *Node) IsTerminal() bool { return n.Terminal }

type Graph struct {
	nodes map[string]*Node
	entry string
}

func Build(wf *kflow.Workflow) (*Graph, error) {
	if err := wf.Validate(); err != nil {
		return nil, err
	}

	g := &Graph{nodes: make(map[string]*Node)}
	steps := wf.Steps()

	for i, step := range steps {
		td, ok := wf.Tasks()[step.Name()]
		if !ok {
			return nil, fmt.Errorf("engine: step %q references unknown task", step.Name())
		}

		next := step.NextState()
		if next == "" && !step.IsEnd() {
			return nil, fmt.Errorf("engine: step %q has no Next and is not End()", step.Name())
		}

		// step-level catch overrides task-level
		catch := step.CatchState()
		if catch == "" {
			catch = td.CatchState()
		}

		// step-level retry overrides task-level
		retry := step.RetryPolicy()
		if retry == nil {
			retry = td.RetryPolicy()
		}

		terminal := next == kflow.Succeed || next == kflow.Fail

		node := &Node{
			Name:     step.Name(),
			TaskDef:  td,
			Next:     next,
			Catch:    catch,
			Retry:    retry,
			Terminal: terminal,
		}
		g.nodes[step.Name()] = node

		if i == 0 {
			g.entry = step.Name()
		}
	}

	return g, nil
}

func (g *Graph) EntryNode() *Node {
	return g.nodes[g.entry]
}

func (g *Graph) Node(name string) *Node {
	return g.nodes[name]
}

func (g *Graph) Next(node *Node, key string) (*Node, error) {
	if node.Terminal {
		return nil, nil
	}

	if node.TaskDef.IsChoice() {
		next, ok := g.nodes[key]
		if !ok {
			return nil, fmt.Errorf("engine: choice node %q returned unknown state %q", node.Name, key)
		}
		return next, nil
	}

	next, ok := g.nodes[node.Next]
	if !ok {
		return nil, fmt.Errorf("engine: node %q next state %q not found in graph", node.Name, node.Next)
	}
	return next, nil
}
