package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/task"
)

type NodeType string

const (
	NodeTypeStep      NodeType = "step"
	NodeTypeCondition NodeType = "condition"
	NodeTypeLoop      NodeType = "loop"
	NodeTypeParallel  NodeType = "parallel"
)

type Node interface {
	ID() string
	Type() NodeType
	Execute(ctx context.Context, input string, state *State) (string, error)
}

// CheckpointableNode is required for custom Nodes used by Task mode. Built-in
// Nodes are checkpointed by Workflow itself. Data must describe only the
// custom Node's resumable state; shared State is captured separately.
type CheckpointableNode interface {
	Node
	SaveCheckpoint(context.Context, string, *State) (json.RawMessage, error)
	RestoreCheckpoint(context.Context, json.RawMessage, *State) error
}

// State is the concurrency-safe state shared by nodes within one workflow run.
type State struct {
	mu     sync.RWMutex
	values map[string]any
}

func NewState(initial map[string]any) *State {
	state := &State{values: make(map[string]any, len(initial))}
	for key, value := range initial {
		state.values[key] = value
	}
	return state
}

func (s *State) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func (s *State) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[key]
	return value, ok
}

func (s *State) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]any, len(s.values))
	for key, value := range s.values {
		result[key] = value
	}
	return result
}

type StepNode struct {
	id            string
	Agent         *engine.Agent
	AfterExecute  func(input, output string, state *State)
	InputStateKey string
}

func NewStepNode(id string, agent *engine.Agent, after ...func(input, output string, state *State)) *StepNode {
	n := &StepNode{id: id, Agent: agent}
	if len(after) > 0 {
		n.AfterExecute = after[0]
	}
	return n
}

func (n *StepNode) WithInputFromState(key string) *StepNode {
	n.InputStateKey = key
	return n
}

func (n *StepNode) ID() string     { return n.id }
func (n *StepNode) Type() NodeType { return NodeTypeStep }

func (n *StepNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	if n.Agent == nil {
		return "", fmt.Errorf("step %s has no agent", n.id)
	}
	agentInput := input
	if n.InputStateKey != "" && state != nil {
		if value, exists := state.Get(n.InputStateKey); exists {
			v, ok := value.(string)
			if ok && v != "" {
				agentInput = v
			}
		}
	}
	output := n.Agent.Run(ctx, agentInput)
	if !output.Success {
		if output.Err != nil {
			return "", fmt.Errorf("step %s failed: %w", n.id, output.Err)
		}
		return "", fmt.Errorf("step %s failed: %s", n.id, output.Error)
	}
	if n.AfterExecute != nil && state != nil {
		n.AfterExecute(agentInput, output.Content, state)
	}
	return output.Content, nil
}

type ConditionNode struct {
	id        string
	Condition func(input string, state *State) (bool, error)
	TrueNode  Node
	FalseNode Node
}

func NewConditionNode(id string, condition func(input string, state *State) (bool, error), trueNode, falseNode Node) *ConditionNode {
	return &ConditionNode{
		id: id, Condition: condition,
		TrueNode: trueNode, FalseNode: falseNode,
	}
}

func (n *ConditionNode) ID() string     { return n.id }
func (n *ConditionNode) Type() NodeType { return NodeTypeCondition }

func (n *ConditionNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	if n.Condition == nil {
		return "", fmt.Errorf("condition %s has no predicate", n.id)
	}
	result, err := n.Condition(input, state)
	if err != nil {
		return "", fmt.Errorf("condition %s error: %w", n.id, err)
	}
	if runtime, ok := task.RuntimeFrom(ctx); ok {
		if scoped, scopedOK := runtime.(*workflowRuntime); scopedOK {
			scoped.scope.tracker.mu.Lock()
			scoped.scope.tracker.branches[n.id] = result
			scoped.scope.tracker.mu.Unlock()
		}
	}
	if result && n.TrueNode != nil {
		return executeWorkflowNode(childWorkflowScope(ctx, n.TrueNode.ID(), input, state), n.TrueNode, input, state)
	}
	if !result && n.FalseNode != nil {
		return executeWorkflowNode(childWorkflowScope(ctx, n.FalseNode.ID(), input, state), n.FalseNode, input, state)
	}
	return input, nil
}

type LoopNode struct {
	id        string
	BodyNode  Node
	Condition func(iteration int, input string, state *State) (bool, error)
	MaxIter   int
}

func NewLoopNode(id string, body Node, condition func(int, string, *State) (bool, error), maxIter int) *LoopNode {
	if maxIter <= 0 {
		maxIter = 10
	}
	return &LoopNode{id: id, BodyNode: body, Condition: condition, MaxIter: maxIter}
}

func (n *LoopNode) ID() string     { return n.id }
func (n *LoopNode) Type() NodeType { return NodeTypeLoop }

func (n *LoopNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	if n.Condition == nil {
		return input, fmt.Errorf("loop %s has no condition", n.id)
	}
	if isNilNode(n.BodyNode) {
		return input, fmt.Errorf("loop %s has no body", n.id)
	}
	current := input
	for i := 0; i < n.MaxIter; i++ {
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		default:
		}
		shouldContinue, err := n.Condition(i, current, state)
		if err != nil {
			return current, err
		}
		if !shouldContinue {
			break
		}
		if runtime, ok := task.RuntimeFrom(ctx); ok {
			if scoped, scopedOK := runtime.(*workflowRuntime); scopedOK {
				scoped.scope.tracker.mu.Lock()
				scoped.scope.tracker.loops[n.id] = i
				scoped.scope.tracker.mu.Unlock()
			}
		}
		result, err := executeWorkflowNode(childWorkflowScope(ctx, n.BodyNode.ID(), current, state), n.BodyNode, current, state)
		if err != nil {
			return result, err
		}
		current = result
	}
	return current, nil
}

type ParallelNode struct {
	id    string
	Nodes []Node
}

func NewParallelNode(id string, nodes ...Node) *ParallelNode {
	return &ParallelNode{id: id, Nodes: append([]Node(nil), nodes...)}
}

func (n *ParallelNode) ID() string     { return n.id }
func (n *ParallelNode) Type() NodeType { return NodeTypeParallel }

func (n *ParallelNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	type nodeResult struct {
		output string
		err    error
	}
	results := make([]nodeResult, len(n.Nodes))
	var wg sync.WaitGroup

	for i, node := range n.Nodes {
		if isNilNode(node) {
			results[i] = nodeResult{err: fmt.Errorf("parallel node %s child %d is nil", n.id, i)}
			continue
		}
		wg.Add(1)
		go func(idx int, nd Node) {
			defer wg.Done()
			childCtx := childWorkflowScope(ctx, nd.ID(), input, state)
			out, err := executeWorkflowNode(childCtx, nd, input, state)
			results[idx] = nodeResult{output: out, err: err}
			if err == nil {
				if runtime, ok := task.RuntimeFrom(childCtx); ok {
					if scoped, scopedOK := runtime.(*workflowRuntime); scopedOK {
						scoped.scope.tracker.mu.Lock()
						scoped.scope.tracker.completed[strings.Join(scoped.scope.path, "/")] = out
						scoped.scope.tracker.mu.Unlock()
						_ = scoped.checkpointState(childCtx, nil)
					}
				}
			}
		}(i, node)
	}
	wg.Wait()

	var parts []string
	for i, r := range results {
		childID := fmt.Sprintf("child_%d", i)
		if !isNilNode(n.Nodes[i]) {
			childID = n.Nodes[i].ID()
		}
		if r.err != nil {
			parts = append(parts, fmt.Sprintf("[%s] error: %v", childID, r.err))
		} else {
			parts = append(parts, fmt.Sprintf("[%s] %s", childID, r.output))
		}
	}
	return strings.Join(parts, "\n"), nil
}

type Workflow struct {
	ID           string
	Name         string
	Nodes        []Node
	initialState *State
	logger       *slog.Logger
}

type WorkflowConfig struct {
	ID     string
	Name   string
	Nodes  []Node
	Logger *slog.Logger
}

func NewWorkflow(cfg WorkflowConfig) *Workflow {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Workflow{
		ID:           cfg.ID,
		Name:         cfg.Name,
		Nodes:        append([]Node(nil), cfg.Nodes...),
		initialState: NewState(nil),
		logger:       logger.With("component", "workflow", "name", cfg.Name),
	}
}

// AddNode appends a node during workflow construction. A Workflow must be
// treated as immutable after it is registered or first run.
func (w *Workflow) AddNode(node Node) {
	w.Nodes = append(w.Nodes, node)
}

// Validate checks invariants that can be established before execution.
func (w *Workflow) Validate() error {
	if w == nil {
		return fmt.Errorf("workflow is required")
	}
	if w.Name == "" || w.Name != strings.TrimSpace(w.Name) {
		return fmt.Errorf("workflow name must be non-empty and must not have surrounding whitespace")
	}
	seen := make(map[string]struct{}, len(w.Nodes))
	for index, node := range w.Nodes {
		if isNilNode(node) {
			return fmt.Errorf("workflow %q node %d is nil", w.Name, index)
		}
		if node.ID() == "" {
			return fmt.Errorf("workflow %q node %d has no id", w.Name, index)
		}
		if _, duplicate := seen[node.ID()]; duplicate {
			return fmt.Errorf("workflow %q has duplicate node id %q", w.Name, node.ID())
		}
		seen[node.ID()] = struct{}{}
	}
	return nil
}

// ValidateTaskMode rejects custom Nodes that cannot describe resumable state.
// It does not affect the legacy synchronous Workflow API.
func (w *Workflow) ValidateTaskMode() error {
	if err := w.Validate(); err != nil {
		return err
	}
	for _, node := range w.Nodes {
		if err := validateTaskNode(node); err != nil {
			return fmt.Errorf("workflow %q: %w", w.Name, err)
		}
	}
	return nil
}

func validateTaskNode(node Node) error {
	switch current := node.(type) {
	case *StepNode:
		if current.Agent == nil {
			return fmt.Errorf("step %s has no agent", current.id)
		}
		return nil
	case *ConditionNode:
		if current.TrueNode != nil {
			if err := validateTaskNode(current.TrueNode); err != nil {
				return err
			}
		}
		if current.FalseNode != nil {
			return validateTaskNode(current.FalseNode)
		}
		return nil
	case *LoopNode:
		return validateTaskNode(current.BodyNode)
	case *ParallelNode:
		for _, child := range current.Nodes {
			if err := validateTaskNode(child); err != nil {
				return err
			}
		}
		return nil
	case CheckpointableNode:
		return nil
	default:
		return fmt.Errorf("custom node %q must implement workflow.CheckpointableNode in Task mode", node.ID())
	}
}

type WorkflowResult struct {
	Output   string
	Success  bool
	Error    string
	Err      error `json:"-"`
	State    map[string]any
	Duration time.Duration
	StepLogs []StepLog
}

type StepLog struct {
	NodeID   string
	NodeType NodeType
	Duration time.Duration
	Success  bool
	Error    string
}

func (w *Workflow) Run(ctx context.Context, input string) *WorkflowResult {
	start := time.Now()
	if err := w.Validate(); err != nil {
		return &WorkflowResult{Success: false, Error: err.Error(), Err: err, Duration: time.Since(start)}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.logger.Info("workflow started", "nodes", len(w.Nodes))

	state := NewState(w.initialState.Snapshot())
	state.Set("user_input", input)
	current := input
	var stepLogs []StepLog
	var tracker *workflowTracker
	if _, taskMode := task.RuntimeFrom(ctx); taskMode {
		tracker = newWorkflowTracker(nil)
	}

	for index, node := range w.Nodes {
		select {
		case <-ctx.Done():
			runErr := fmt.Errorf("workflow cancelled: %w", ctx.Err())
			return &WorkflowResult{
				Output: current, Success: false, Error: runErr.Error(), Err: runErr,
				State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs,
			}
		default:
		}

		if runtime, taskMode := task.RuntimeFrom(ctx); taskMode {
			checkpoint := task.Checkpoint{
				Kind: task.TurnWorkflow, Target: w.Name, Status: task.TurnRunning,
				Workflow: &task.WorkflowCheckpoint{WorkflowID: w.Name, NodePath: []string{node.ID()}, NextNode: index, Current: current, State: state.Snapshot()},
			}
			if err := runtime.Checkpoint(ctx, checkpoint); err != nil {
				workflowErr := fmt.Errorf("workflow checkpoint before node %s: %w", node.ID(), err)
				return &WorkflowResult{Output: current, Success: false, Error: workflowErr.Error(), Err: workflowErr, State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs}
			}
		}

		stepStart := time.Now()
		w.logger.Info("executing node", "id", node.ID(), "type", node.Type())

		nodeCtx := ctx
		if tracker != nil {
			nodeCtx = withWorkflowScope(ctx, w.Name, index, current, state, []string{node.ID()}, tracker)
		}
		output, err := executeWorkflowNode(nodeCtx, node, current, state)
		duration := time.Since(stepStart)

		if err != nil {
			w.logger.Error("node failed", "id", node.ID(), "error", err, "duration", duration)
			stepLogs = append(stepLogs, StepLog{
				NodeID: node.ID(), NodeType: node.Type(),
				Duration: duration, Success: false, Error: err.Error(),
			})
			workflowErr := fmt.Errorf("node %s failed: %w", node.ID(), err)
			return &WorkflowResult{
				Output: current, Success: false, Error: workflowErr.Error(), Err: workflowErr,
				State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs,
			}
		}

		stepLogs = append(stepLogs, StepLog{
			NodeID: node.ID(), NodeType: node.Type(),
			Duration: duration, Success: true,
		})
		current = output
	}
	if runtime, taskMode := task.RuntimeFrom(ctx); taskMode {
		checkpoint := task.Checkpoint{
			Kind: task.TurnWorkflow, Target: w.Name, Status: task.TurnRunning,
			Workflow: &task.WorkflowCheckpoint{WorkflowID: w.Name, NextNode: len(w.Nodes), Current: current, State: state.Snapshot()},
		}
		if err := runtime.Checkpoint(ctx, checkpoint); err != nil {
			workflowErr := fmt.Errorf("workflow final checkpoint: %w", err)
			return &WorkflowResult{Output: current, Success: false, Error: workflowErr.Error(), Err: workflowErr, State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs}
		}
	}

	w.logger.Info("workflow completed", "duration", time.Since(start))
	return &WorkflowResult{
		Output: current, Success: true,
		State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs,
	}
}

// Resume continues a Workflow whose current Node is suspended at a safe user
// interaction checkpoint. The caller supplies the already-encoded Tool result.
func (w *Workflow) Resume(ctx context.Context, checkpoint task.WorkflowCheckpoint, toolResult string) *WorkflowResult {
	start := time.Now()
	if err := w.ValidateTaskMode(); err != nil {
		return &WorkflowResult{Success: false, Error: err.Error(), Err: err}
	}
	if checkpoint.WorkflowID != w.Name {
		err := fmt.Errorf("workflow checkpoint %q does not match %q", checkpoint.WorkflowID, w.Name)
		return &WorkflowResult{Success: false, Error: err.Error(), Err: err}
	}
	if checkpoint.NextNode < 0 || checkpoint.NextNode >= len(w.Nodes) || len(checkpoint.NodePath) == 0 {
		err := fmt.Errorf("workflow checkpoint node position is invalid")
		return &WorkflowResult{Success: false, Error: err.Error(), Err: err}
	}
	state := NewState(checkpoint.State)
	tracker := newWorkflowTracker(&checkpoint)
	current := checkpoint.Current
	node := w.Nodes[checkpoint.NextNode]
	nodeCtx := withWorkflowScope(ctx, w.Name, checkpoint.NextNode, current, state, []string{node.ID()}, tracker)
	output, err := resumeWorkflowNode(nodeCtx, node, checkpoint.NodePath, &checkpoint, toolResult, current, state)
	logs := []StepLog{{NodeID: node.ID(), NodeType: node.Type(), Success: err == nil}}
	if err != nil {
		logs[0].Error = err.Error()
		wrapped := fmt.Errorf("resume node %s: %w", node.ID(), err)
		return &WorkflowResult{Output: current, Success: false, Error: wrapped.Error(), Err: wrapped, State: state.Snapshot(), Duration: time.Since(start), StepLogs: logs}
	}
	current = output
	for index := checkpoint.NextNode + 1; index < len(w.Nodes); index++ {
		node = w.Nodes[index]
		if runtime, taskMode := task.RuntimeFrom(ctx); taskMode {
			position := task.Checkpoint{Kind: task.TurnWorkflow, Target: w.Name, Status: task.TurnRunning, Workflow: &task.WorkflowCheckpoint{WorkflowID: w.Name, NodePath: []string{node.ID()}, NextNode: index, Current: current, State: state.Snapshot()}}
			if checkpointErr := runtime.Checkpoint(ctx, position); checkpointErr != nil {
				wrapped := fmt.Errorf("workflow checkpoint before node %s: %w", node.ID(), checkpointErr)
				return &WorkflowResult{Output: current, Success: false, Error: wrapped.Error(), Err: wrapped, State: state.Snapshot(), Duration: time.Since(start), StepLogs: logs}
			}
		}
		stepStart := time.Now()
		nodeCtx = withWorkflowScope(ctx, w.Name, index, current, state, []string{node.ID()}, tracker)
		output, err = executeWorkflowNode(nodeCtx, node, current, state)
		logEntry := StepLog{NodeID: node.ID(), NodeType: node.Type(), Duration: time.Since(stepStart), Success: err == nil}
		if err != nil {
			logEntry.Error = err.Error()
			logs = append(logs, logEntry)
			wrapped := fmt.Errorf("node %s failed: %w", node.ID(), err)
			return &WorkflowResult{Output: current, Success: false, Error: wrapped.Error(), Err: wrapped, State: state.Snapshot(), Duration: time.Since(start), StepLogs: logs}
		}
		logs = append(logs, logEntry)
		current = output
	}
	return &WorkflowResult{Output: current, Success: true, State: state.Snapshot(), Duration: time.Since(start), StepLogs: logs}
}

func resumeWorkflowNode(ctx context.Context, node Node, path []string, checkpoint *task.WorkflowCheckpoint, toolResult, input string, state *State) (string, error) {
	if len(path) == 0 || path[0] != node.ID() {
		return input, fmt.Errorf("checkpoint path does not match node %q", node.ID())
	}
	switch current := node.(type) {
	case *StepNode:
		if current.Agent == nil {
			return input, fmt.Errorf("step %s has no agent", current.id)
		}
		agentCheckpoint := checkpoint.Agent
		if scoped, ok := task.RuntimeFrom(ctx); ok {
			if workflowScope, scopeOK := scoped.(*workflowRuntime); scopeOK {
				if saved := checkpoint.Agents[strings.Join(workflowScope.scope.path, "/")]; saved != nil {
					agentCheckpoint = saved
				}
			}
		}
		if agentCheckpoint == nil || agentCheckpoint.PendingToolCall == nil {
			return input, fmt.Errorf("step %s has no pending Agent checkpoint", current.id)
		}
		output := current.Agent.Resume(ctx, *agentCheckpoint, toolResult)
		if !output.Success {
			if output.Err != nil {
				return input, output.Err
			}
			return input, fmt.Errorf("step %s failed: %s", current.id, output.Error)
		}
		agentInput := input
		if current.InputStateKey != "" {
			if value, ok := state.Get(current.InputStateKey); ok {
				if text, textOK := value.(string); textOK && text != "" {
					agentInput = text
				}
			}
		}
		if current.AfterExecute != nil {
			current.AfterExecute(agentInput, output.Content, state)
		}
		return output.Content, nil
	case *ConditionNode:
		selected, ok := checkpoint.Branches[current.id]
		if !ok {
			return input, fmt.Errorf("condition %s branch is missing", current.id)
		}
		child := current.FalseNode
		if selected {
			child = current.TrueNode
		}
		if isNilNode(child) {
			return input, nil
		}
		return resumeWorkflowNode(childWorkflowScope(ctx, child.ID(), input, state), child, path[1:], checkpoint, toolResult, input, state)
	case *LoopNode:
		iteration, ok := checkpoint.LoopIndexes[current.id]
		if !ok {
			return input, fmt.Errorf("loop %s iteration is missing", current.id)
		}
		result, err := resumeWorkflowNode(childWorkflowScope(ctx, current.BodyNode.ID(), input, state), current.BodyNode, path[1:], checkpoint, toolResult, input, state)
		if err != nil {
			return result, err
		}
		for next := iteration + 1; next < current.MaxIter; next++ {
			shouldContinue, conditionErr := current.Condition(next, result, state)
			if conditionErr != nil {
				return result, conditionErr
			}
			if !shouldContinue {
				break
			}
			result, err = executeWorkflowNode(childWorkflowScope(ctx, current.BodyNode.ID(), result, state), current.BodyNode, result, state)
			if err != nil {
				return result, err
			}
		}
		return result, nil
	case *ParallelNode:
		parts := make([]string, len(current.Nodes))
		for index, child := range current.Nodes {
			if isNilNode(child) {
				parts[index] = fmt.Sprintf("[child_%d] error: nil node", index)
				continue
			}
			childCtx := childWorkflowScope(ctx, child.ID(), input, state)
			key := ""
			if scoped, ok := task.RuntimeFrom(childCtx); ok {
				if workflowScope, scopeOK := scoped.(*workflowRuntime); scopeOK {
					key = strings.Join(workflowScope.scope.path, "/")
				}
			}
			childOutput, completed := checkpoint.Completed[key]
			var childErr error
			if !completed && len(path) > 1 && path[1] == child.ID() {
				childOutput, childErr = resumeWorkflowNode(childCtx, child, path[1:], checkpoint, toolResult, input, state)
			} else if !completed {
				childOutput, childErr = executeWorkflowNode(childCtx, child, input, state)
			}
			if childErr != nil {
				parts[index] = fmt.Sprintf("[%s] error: %v", child.ID(), childErr)
			} else {
				parts[index] = fmt.Sprintf("[%s] %s", child.ID(), childOutput)
			}
		}
		return strings.Join(parts, "\n"), nil
	case CheckpointableNode:
		if data := checkpoint.NodeData[current.ID()]; data != nil {
			if err := current.RestoreCheckpoint(ctx, data, state); err != nil {
				return input, err
			}
		}
		return current.Execute(ctx, input, state)
	default:
		return input, fmt.Errorf("node %s is not resumable", node.ID())
	}
}

func isNilNode(node Node) bool {
	if node == nil {
		return true
	}
	value := reflect.ValueOf(node)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (w *Workflow) SetState(key string, value any) {
	w.initialState.Set(key, value)
}

func (w *Workflow) GetState(key string) (any, bool) {
	return w.initialState.Get(key)
}
