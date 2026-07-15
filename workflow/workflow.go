package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
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
	if result && n.TrueNode != nil {
		return n.TrueNode.Execute(ctx, input, state)
	}
	if !result && n.FalseNode != nil {
		return n.FalseNode.Execute(ctx, input, state)
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
		result, err := n.BodyNode.Execute(ctx, current, state)
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
			out, err := nd.Execute(ctx, input, state)
			results[idx] = nodeResult{output: out, err: err}
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

	for _, node := range w.Nodes {
		select {
		case <-ctx.Done():
			runErr := fmt.Errorf("workflow cancelled: %w", ctx.Err())
			return &WorkflowResult{
				Output: current, Success: false, Error: runErr.Error(), Err: runErr,
				State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs,
			}
		default:
		}

		stepStart := time.Now()
		w.logger.Info("executing node", "id", node.ID(), "type", node.Type())

		output, err := node.Execute(ctx, current, state)
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

	w.logger.Info("workflow completed", "duration", time.Since(start))
	return &WorkflowResult{
		Output: current, Success: true,
		State: state.Snapshot(), Duration: time.Since(start), StepLogs: stepLogs,
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
