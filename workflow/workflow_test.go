package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/types"
	"github.com/apexracing/tracklogic-agent/workflow"
)

type stateNode struct{ id string }

func (node stateNode) ID() string         { return node.id }
func (stateNode) Type() workflow.NodeType { return workflow.NodeTypeStep }
func (node stateNode) Execute(_ context.Context, input string, state *workflow.State) (string, error) {
	state.Set(node.id, input)
	return input, nil
}

func TestWorkflowStateIsIsolatedAndConcurrent(t *testing.T) {
	runtimeWorkflow := workflow.NewWorkflow(workflow.WorkflowConfig{Name: "concurrent"})
	runtimeWorkflow.AddNode(workflow.NewParallelNode("parallel",
		stateNode{id: "first"}, stateNode{id: "second"}, stateNode{id: "third"},
	))

	var waitGroup sync.WaitGroup
	for index := 0; index < 10; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			input := fmt.Sprintf("run-%d", index)
			result := runtimeWorkflow.Run(context.Background(), input)
			if !result.Success {
				t.Errorf("workflow failed: %s", result.Error)
				return
			}
			for _, key := range []string{"first", "second", "third"} {
				if result.State[key] != input {
					t.Errorf("state[%s] = %v, want %s", key, result.State[key], input)
				}
			}
		}(index)
	}
	waitGroup.Wait()
}

func TestWorkflowReturnsStructuralErrorsInsteadOfPanicking(t *testing.T) {
	tests := []struct {
		name string
		node workflow.Node
		want string
	}{
		{name: "nil node", node: nil, want: "nil"},
		{name: "step without agent", node: workflow.NewStepNode("step", nil), want: "no agent"},
		{name: "condition without predicate", node: workflow.NewConditionNode("condition", nil, nil, nil), want: "no predicate"},
		{name: "loop without body", node: workflow.NewLoopNode("loop", nil, func(int, string, *workflow.State) (bool, error) { return true, nil }, 1), want: "no body"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeWorkflow := workflow.NewWorkflow(workflow.WorkflowConfig{Name: "invalid"})
			runtimeWorkflow.AddNode(test.node)
			result := runtimeWorkflow.Run(context.Background(), "input")
			if result.Success || !strings.Contains(result.Error, test.want) {
				t.Fatalf("Run() = %+v, want error containing %q", result, test.want)
			}
		})
	}
}

func TestParallelNodeHandlesNilChildWithoutPanic(t *testing.T) {
	node := workflow.NewParallelNode("parallel", nil)
	output, err := node.Execute(context.Background(), "input", workflow.NewState(nil))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(output, "child_0") || !strings.Contains(output, "nil") {
		t.Fatalf("Execute() output = %q", output)
	}
}

func TestNewWorkflowCopiesConfiguredNodes(t *testing.T) {
	nodes := []workflow.Node{stateNode{id: "step"}}
	runtimeWorkflow := workflow.NewWorkflow(workflow.WorkflowConfig{Name: "configured", Nodes: nodes})
	nodes[0] = nil

	if err := runtimeWorkflow.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestTeamAndParallelNodeCopyConfiguredSlices(t *testing.T) {
	agent := engine.NewAgent(engine.AgentConfig{Name: "member"})
	agents := []*engine.Agent{agent}
	team := workflow.NewTeam(workflow.TeamConfig{Name: "team", Mode: workflow.ModeSequential, Agents: agents})
	agents[0] = nil
	if err := team.Validate(); err != nil {
		t.Fatalf("Team.Validate() error after caller mutation = %v", err)
	}

	nodes := []workflow.Node{stateNode{id: "child"}}
	parallel := workflow.NewParallelNode("parallel", nodes...)
	nodes[0] = nil
	output, err := parallel.Execute(context.Background(), "input", workflow.NewState(nil))
	if err != nil || !strings.Contains(output, "child") {
		t.Fatalf("Parallel.Execute() = %q, %v", output, err)
	}
}

func TestParallelTeamPreservesStructuredAgentFailure(t *testing.T) {
	broken := engine.NewAgent(engine.AgentConfig{Name: "broken"})
	team := workflow.NewTeam(workflow.TeamConfig{
		Name: "parallel", Mode: workflow.ModeParallel, Agents: []*engine.Agent{broken},
	})
	result := team.Run(context.Background(), "input")
	if result.Success || result.Err == nil || result.Error == "" {
		t.Fatalf("Run() = %+v, want structured failure", result)
	}
	var harnessErr *types.HarnessError
	if !errors.As(result.Err, &harnessErr) || harnessErr.Code != types.ErrInvalidConfig {
		t.Fatalf("result.Err = %v, want INVALID_CONFIG HarnessError", result.Err)
	}
}
