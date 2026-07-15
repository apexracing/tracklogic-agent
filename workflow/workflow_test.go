package workflow_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

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
