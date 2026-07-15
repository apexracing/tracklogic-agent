package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/apexracing/tracklogic-agent/interaction"
	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/task"
)

type workflowTracker struct {
	mu        sync.RWMutex
	loops     map[string]int
	branches  map[string]bool
	completed map[string]string
	agents    map[string]*task.AgentCheckpoint
	nodeData  map[string]json.RawMessage
}

func newWorkflowTracker(checkpoint *task.WorkflowCheckpoint) *workflowTracker {
	tracker := &workflowTracker{
		loops: make(map[string]int), branches: make(map[string]bool),
		completed: make(map[string]string), agents: make(map[string]*task.AgentCheckpoint),
		nodeData: make(map[string]json.RawMessage),
	}
	if checkpoint != nil {
		for key, value := range checkpoint.LoopIndexes {
			tracker.loops[key] = value
		}
		for key, value := range checkpoint.Branches {
			tracker.branches[key] = value
		}
		for key, value := range checkpoint.Completed {
			tracker.completed[key] = value
		}
		for key, value := range checkpoint.Agents {
			tracker.agents[key] = value
		}
		for key, value := range checkpoint.NodeData {
			tracker.nodeData[key] = append(json.RawMessage(nil), value...)
		}
	}
	return tracker
}

func (tracker *workflowTracker) snapshot() (map[string]int, map[string]bool, map[string]string, map[string]*task.AgentCheckpoint, map[string]json.RawMessage) {
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	loops := make(map[string]int, len(tracker.loops))
	branches := make(map[string]bool, len(tracker.branches))
	completed := make(map[string]string, len(tracker.completed))
	agents := make(map[string]*task.AgentCheckpoint, len(tracker.agents))
	nodeData := make(map[string]json.RawMessage, len(tracker.nodeData))
	for key, value := range tracker.loops {
		loops[key] = value
	}
	for key, value := range tracker.branches {
		branches[key] = value
	}
	for key, value := range tracker.completed {
		completed[key] = value
	}
	for key, value := range tracker.agents {
		agents[key] = value
	}
	for key, value := range tracker.nodeData {
		nodeData[key] = append(json.RawMessage(nil), value...)
	}
	return loops, branches, completed, agents, nodeData
}

type workflowScope struct {
	id      string
	next    int
	current string
	state   *State
	path    []string
	tracker *workflowTracker
}

type workflowRuntime struct {
	delegate task.Runtime
	scope    workflowScope
}

func withWorkflowScope(ctx context.Context, id string, next int, current string, state *State, path []string, tracker *workflowTracker) context.Context {
	runtime, ok := task.RuntimeFrom(ctx)
	if !ok {
		return ctx
	}
	if scoped, ok := runtime.(*workflowRuntime); ok {
		runtime = scoped.delegate
	}
	return task.WithRuntime(ctx, &workflowRuntime{delegate: runtime, scope: workflowScope{id: id, next: next, current: current, state: state, path: append([]string(nil), path...), tracker: tracker}})
}

func childWorkflowScope(ctx context.Context, childID, current string, state *State) context.Context {
	runtime, ok := task.RuntimeFrom(ctx)
	if !ok {
		return ctx
	}
	scoped, ok := runtime.(*workflowRuntime)
	if !ok {
		return ctx
	}
	path := append(append([]string(nil), scoped.scope.path...), childID)
	return task.WithRuntime(ctx, &workflowRuntime{delegate: scoped.delegate, scope: workflowScope{id: scoped.scope.id, next: scoped.scope.next, current: current, state: state, path: path, tracker: scoped.scope.tracker}})
}

func (runtime *workflowRuntime) wrap(checkpoint task.Checkpoint) task.Checkpoint {
	if checkpoint.Kind != task.TurnAgent || checkpoint.Agent == nil {
		return checkpoint
	}
	loops, branches, completed, agents, nodeData := runtime.scope.tracker.snapshot()
	pathKey := strings.Join(runtime.scope.path, "/")
	agentCopy := *checkpoint.Agent
	runtime.scope.tracker.mu.Lock()
	runtime.scope.tracker.agents[pathKey] = &agentCopy
	runtime.scope.tracker.mu.Unlock()
	agents[pathKey] = &agentCopy
	checkpoint.Kind = task.TurnWorkflow
	checkpoint.Target = runtime.scope.id
	checkpoint.Workflow = &task.WorkflowCheckpoint{
		WorkflowID: runtime.scope.id, NodePath: append([]string(nil), runtime.scope.path...),
		NextNode: runtime.scope.next, Current: runtime.scope.current, State: runtime.scope.state.Snapshot(),
		LoopIndexes: loops, Branches: branches, Completed: completed, Agent: &agentCopy, Agents: agents, NodeData: nodeData,
	}
	checkpoint.Agent = nil
	return checkpoint
}

func (runtime *workflowRuntime) checkpointState(ctx context.Context, nodeData map[string]json.RawMessage) error {
	if len(nodeData) > 0 {
		runtime.scope.tracker.mu.Lock()
		for key, value := range nodeData {
			runtime.scope.tracker.nodeData[key] = append(json.RawMessage(nil), value...)
		}
		runtime.scope.tracker.mu.Unlock()
	}
	loops, branches, completed, agents, savedNodeData := runtime.scope.tracker.snapshot()
	checkpoint := task.Checkpoint{
		Kind: task.TurnWorkflow, Target: runtime.scope.id, Status: task.TurnRunning,
		Workflow: &task.WorkflowCheckpoint{
			WorkflowID: runtime.scope.id, NodePath: append([]string(nil), runtime.scope.path...),
			NextNode: runtime.scope.next, Current: runtime.scope.current, State: runtime.scope.state.Snapshot(),
			LoopIndexes: loops, Branches: branches, Completed: completed, Agents: agents, NodeData: savedNodeData,
		},
	}
	return runtime.delegate.Checkpoint(ctx, checkpoint)
}

func executeWorkflowNode(ctx context.Context, node Node, input string, state *State) (string, error) {
	if checkpointable, ok := node.(CheckpointableNode); ok {
		data, err := checkpointable.SaveCheckpoint(ctx, input, state)
		if err != nil {
			return input, fmt.Errorf("save custom node %s checkpoint: %w", node.ID(), err)
		}
		if runtime, runtimeOK := task.RuntimeFrom(ctx); runtimeOK {
			if scoped, scopedOK := runtime.(*workflowRuntime); scopedOK {
				if err := scoped.checkpointState(ctx, map[string]json.RawMessage{node.ID(): data}); err != nil {
					return input, err
				}
			}
		}
	}
	return node.Execute(ctx, input, state)
}

func (runtime *workflowRuntime) TaskID() string                { return runtime.delegate.TaskID() }
func (runtime *workflowRuntime) TurnID() string                { return runtime.delegate.TurnID() }
func (runtime *workflowRuntime) RetryPolicy() task.RetryConfig { return runtime.delegate.RetryPolicy() }
func (runtime *workflowRuntime) CircuitPolicy() task.CircuitConfig {
	return runtime.delegate.CircuitPolicy()
}
func (runtime *workflowRuntime) SummaryPolicy() task.SummaryConfig {
	return runtime.delegate.SummaryPolicy()
}
func (runtime *workflowRuntime) Summarizer() task.Summarizer { return runtime.delegate.Summarizer() }
func (runtime *workflowRuntime) Emit(ctx context.Context, event task.Event) error {
	return runtime.delegate.Emit(ctx, event)
}
func (runtime *workflowRuntime) AcquireConversation(ctx context.Context, id string, fallback memory.Memory) (task.ConversationLease, error) {
	return runtime.delegate.AcquireConversation(ctx, id, fallback)
}
func (runtime *workflowRuntime) ReportProgress(ctx context.Context, summary string) error {
	return runtime.delegate.ReportProgress(ctx, summary)
}
func (runtime *workflowRuntime) RequestInput(ctx context.Context, request interaction.Request, checkpoint task.Checkpoint) (interaction.Response, error) {
	return runtime.delegate.RequestInput(ctx, request, runtime.wrap(checkpoint))
}
func (runtime *workflowRuntime) Checkpoint(ctx context.Context, checkpoint task.Checkpoint) error {
	return runtime.delegate.Checkpoint(ctx, runtime.wrap(checkpoint))
}
