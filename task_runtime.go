package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/interaction"
	"github.com/apexracing/tracklogic-agent/memory"
	taskpkg "github.com/apexracing/tracklogic-agent/task"
	"github.com/apexracing/tracklogic-agent/types"
)

// Task is an in-memory runtime for one caller-owned task ID. It has no storage
// or network transport responsibility; applications provide those concerns by
// consuming EventSink events and returning required acknowledgements.
type Task struct {
	harness    *Harness
	id         string
	sink       taskpkg.EventSink
	retry      taskpkg.RetryConfig
	circuit    taskpkg.CircuitConfig
	summary    taskpkg.SummaryConfig
	summarizer taskpkg.Summarizer

	ctx    context.Context
	cancel context.CancelFunc

	emitMu   sync.Mutex
	sequence uint64
	started  time.Time

	mu            sync.RWMutex
	closed        bool
	turns         map[string]*taskTurnState
	conversations map[string]*taskConversation
	pending       map[string]*pendingInteraction
}

type taskTurnState struct {
	mu      sync.RWMutex
	turn    taskpkg.Turn
	result  *taskpkg.Result
	done    chan struct{}
	cancel  context.CancelFunc
	runtime *turnRuntime
}

type taskConversation struct {
	gate chan struct{}
	mem  memory.Memory
}

type pendingInteraction struct {
	request  interaction.Request
	turnID   string
	answer   chan interaction.Response
	restored bool
}

type turnRuntime struct {
	task      *Task
	turnID    string
	kind      taskpkg.TurnKind
	target    string
	startedAt time.Time

	mu            sync.Mutex
	progressCount int
	checkpoint    taskpkg.Checkpoint
}

// TaskID returns the caller-owned identifier of this runtime instance.
func (t *Task) TaskID() string { return t.id }

// NewTask creates an in-memory task runtime. TaskID must be stable and supplied
// by the caller so its own records can correlate events and checkpoints.
func (h *Harness) NewTask(options taskpkg.Options) (*Task, error) {
	return h.newTask(options, 0, true)
}

// RestoreTask recreates an in-memory task from a checkpoint previously handled
// by the caller. It never reads caller storage itself.
func (h *Harness) RestoreTask(input taskpkg.RestoreInput, sink taskpkg.EventSink) (*Task, error) {
	if strings.TrimSpace(input.TaskID) == "" || input.TaskID != strings.TrimSpace(input.TaskID) {
		return nil, types.NewError(types.ErrInvalidConfig, "task id is required and must not contain surrounding whitespace")
	}
	checkpoint := input.Checkpoint
	if checkpoint.Version != taskpkg.CheckpointVersion {
		return nil, types.NewError(types.ErrInvalidConfig, fmt.Sprintf("unsupported checkpoint version %d", checkpoint.Version))
	}
	if checkpoint.TaskID != input.TaskID {
		return nil, types.NewError(types.ErrInvalidConfig, "checkpoint task id does not match restore input")
	}
	lastSequence := input.LastSequence
	if checkpoint.LastSequence > lastSequence {
		lastSequence = checkpoint.LastSequence
	}
	runtimeTask, err := h.newTask(taskpkg.Options{TaskID: input.TaskID, EventSink: sink}, lastSequence, false)
	if err != nil {
		return nil, err
	}
	if err := runtimeTask.installCheckpoint(checkpoint); err != nil {
		runtimeTask.Close()
		return nil, err
	}
	runtimeTask.continueRestoredAgent(checkpoint)
	return runtimeTask, nil
}

func (h *Harness) newTask(options taskpkg.Options, sequence uint64, emitStarted bool) (*Task, error) {
	if h == nil {
		return nil, types.NewError(types.ErrInvalidConfig, "harness is required")
	}
	if strings.TrimSpace(options.TaskID) == "" || options.TaskID != strings.TrimSpace(options.TaskID) {
		return nil, types.NewError(types.ErrInvalidConfig, "task id is required and must not contain surrounding whitespace")
	}
	retry, err := normalizeRetryConfig(options.Retry)
	if err != nil {
		return nil, err
	}
	circuit, err := normalizeCircuitConfig(options.Circuit)
	if err != nil {
		return nil, err
	}
	summary, err := normalizeSummaryConfig(options.Summary)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtimeTask := &Task{
		harness:       h,
		id:            options.TaskID,
		sink:          options.EventSink,
		retry:         retry,
		circuit:       circuit,
		summary:       summary,
		summarizer:    options.Summarizer,
		ctx:           ctx,
		cancel:        cancel,
		sequence:      sequence,
		started:       time.Now(),
		turns:         make(map[string]*taskTurnState),
		conversations: make(map[string]*taskConversation),
		pending:       make(map[string]*pendingInteraction),
	}
	if emitStarted {
		if err := runtimeTask.emit(ctx, taskpkg.Event{Type: taskpkg.EventTaskStarted, Delivery: taskpkg.DeliveryRequiredAck}); err != nil {
			cancel()
			return nil, types.WrapError(types.ErrEventDelivery, "task start was not acknowledged", err)
		}
	}
	return runtimeTask, nil
}

func normalizeRetryConfig(config taskpkg.RetryConfig) (taskpkg.RetryConfig, error) {
	defaults := taskpkg.DefaultRetryConfig()
	if config == (taskpkg.RetryConfig{}) {
		return defaults, nil
	}
	if config.MaxAttempts < 0 || config.BaseDelay < 0 || config.MaxDelay < 0 || config.MaxRetryAfter < 0 {
		return config, types.NewError(types.ErrInvalidConfig, "retry values must not be negative")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaults.MaxAttempts
	}
	if config.MaxAttempts <= 0 {
		return config, types.NewError(types.ErrInvalidConfig, "retry max attempts must be greater than zero")
	}
	if config.BaseDelay <= 0 {
		config.BaseDelay = defaults.BaseDelay
	}
	if config.MaxDelay <= 0 {
		config.MaxDelay = defaults.MaxDelay
	}
	if config.MaxRetryAfter <= 0 {
		config.MaxRetryAfter = defaults.MaxRetryAfter
	}
	if config.MaxDelay < config.BaseDelay {
		return config, types.NewError(types.ErrInvalidConfig, "retry max delay must not be less than base delay")
	}
	return config, nil
}

func normalizeCircuitConfig(config taskpkg.CircuitConfig) (taskpkg.CircuitConfig, error) {
	defaults := taskpkg.DefaultCircuitConfig()
	if config == (taskpkg.CircuitConfig{}) {
		return defaults, nil
	}
	if config.FailureThreshold < 0 || config.OpenDuration < 0 || config.HalfOpenMax < 0 {
		return config, types.NewError(types.ErrInvalidConfig, "circuit values must not be negative")
	}
	if config.FailureThreshold == 0 {
		config.FailureThreshold = defaults.FailureThreshold
	}
	if config.OpenDuration == 0 {
		config.OpenDuration = defaults.OpenDuration
	}
	if config.HalfOpenMax == 0 {
		config.HalfOpenMax = defaults.HalfOpenMax
	}
	return config, nil
}

func normalizeSummaryConfig(config taskpkg.SummaryConfig) (taskpkg.SummaryConfig, error) {
	defaults := taskpkg.DefaultSummaryConfig()
	if config == (taskpkg.SummaryConfig{}) {
		return defaults, nil
	}
	if config.MaxMessages < 0 || config.KeepRecent < 0 || config.HardLimit < 0 {
		return config, types.NewError(types.ErrInvalidConfig, "summary message limits must not be negative")
	}
	if config.MaxMessages == 0 {
		config.MaxMessages = defaults.MaxMessages
	}
	if config.KeepRecent == 0 {
		config.KeepRecent = defaults.KeepRecent
	}
	if config.HardLimit == 0 {
		config.HardLimit = defaults.HardLimit
	}
	if config.KeepRecent >= config.MaxMessages || config.MaxMessages >= config.HardLimit {
		return config, types.NewError(types.ErrInvalidConfig, "summary limits must satisfy keep recent < max messages < hard limit")
	}
	return config, nil
}

// StartAgent starts one Agent turn and returns immediately after the required
// start events have been acknowledged.
func (t *Task) StartAgent(ctx context.Context, agentName, input string, options ...engine.RunOption) (*taskpkg.Turn, error) {
	if _, ok := t.harness.Agent(agentName); !ok {
		return nil, types.NewError(types.ErrInvalidConfig, fmt.Sprintf("agent %q not found", agentName))
	}
	return t.startTurn(ctx, taskpkg.TurnAgent, agentName, input, func(runCtx context.Context) taskpkg.Result {
		output := t.harness.RunAgent(runCtx, agentName, input, options...)
		return taskpkg.Result{Kind: taskpkg.TurnAgent, Output: output.Content, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
	})
}

// StartTeam starts one Team turn asynchronously.
func (t *Task) StartTeam(ctx context.Context, teamName, input string) (*taskpkg.Turn, error) {
	if _, ok := t.harness.Team(teamName); !ok {
		return nil, types.NewError(types.ErrInvalidConfig, fmt.Sprintf("team %q not found", teamName))
	}
	return t.startTurn(ctx, taskpkg.TurnTeam, teamName, input, func(runCtx context.Context) taskpkg.Result {
		output := t.harness.RunTeam(runCtx, teamName, input)
		return taskpkg.Result{Kind: taskpkg.TurnTeam, Output: output.FinalOutput, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
	})
}

// StartWorkflow starts one Workflow turn asynchronously.
func (t *Task) StartWorkflow(ctx context.Context, workflowName, input string) (*taskpkg.Turn, error) {
	runtimeWorkflow, ok := t.harness.Workflow(workflowName)
	if !ok {
		return nil, types.NewError(types.ErrInvalidConfig, fmt.Sprintf("workflow %q not found", workflowName))
	}
	if err := runtimeWorkflow.ValidateTaskMode(); err != nil {
		return nil, types.WrapError(types.ErrInvalidConfig, "workflow does not support Task mode", err)
	}
	return t.startTurn(ctx, taskpkg.TurnWorkflow, workflowName, input, func(runCtx context.Context) taskpkg.Result {
		output := t.harness.RunWorkflow(runCtx, workflowName, input)
		return taskpkg.Result{Kind: taskpkg.TurnWorkflow, Output: output.Output, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
	})
}

func (t *Task) startTurn(ctx context.Context, kind taskpkg.TurnKind, target, input string, run func(context.Context) taskpkg.Result) (*taskpkg.Turn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := t.ensureOpen(); err != nil {
		return nil, err
	}
	validatedInput := t.harness.Sanitize(input)
	if err := t.harness.ValidateInput(validatedInput); err != nil {
		return nil, types.WrapError(types.ErrInvalidInput, "input validation failed", err)
	}
	turnID := taskpkg.NewID("turn")
	startedAt := time.Now()
	turn := taskpkg.Turn{ID: turnID, TaskID: t.id, Kind: kind, Target: target, Status: taskpkg.TurnRunning, StartedAt: startedAt}
	runCtx, cancel := context.WithCancel(t.ctx)
	turnState := &taskTurnState{turn: turn, done: make(chan struct{}), cancel: cancel}
	runtime := &turnRuntime{task: t, turnID: turnID, kind: kind, target: target, startedAt: startedAt}
	turnState.runtime = runtime
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancel()
		return nil, types.NewError(types.ErrRunCancelled, "task is closed")
	}
	t.turns[turnID] = turnState
	t.mu.Unlock()

	startEvent := taskpkg.Event{TurnID: turnID, Type: taskpkg.EventTurnStarted, Delivery: taskpkg.DeliveryRequiredAck, Payload: targetPayload(kind, target, taskpkg.TurnRunning)}
	if err := t.emit(ctx, startEvent); err != nil {
		t.removeTurn(turnID)
		cancel()
		return nil, types.WrapError(types.ErrEventDelivery, "turn start was not acknowledged", err)
	}
	if err := t.emit(ctx, taskpkg.Event{TurnID: turnID, Type: taskpkg.EventUserMessage, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Text: validatedInput}}); err != nil {
		t.removeTurn(turnID)
		cancel()
		return nil, types.WrapError(types.ErrEventDelivery, "user message was not acknowledged", err)
	}

	go t.runTurn(turnState, runtime, runCtx, run)
	copy := turn
	return &copy, nil
}

func (t *Task) runTurn(state *taskTurnState, runtime *turnRuntime, ctx context.Context, run func(context.Context) taskpkg.Result) {
	result := run(taskpkg.WithRuntime(ctx, runtime))
	result.TurnID = runtime.turnID
	result.Duration = time.Since(runtime.startedAt)

	status := taskpkg.TurnCompleted
	eventType := taskpkg.EventTurnCompleted
	if !result.Success {
		status = taskpkg.TurnFailed
		eventType = taskpkg.EventTurnFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			status = taskpkg.TurnCancelled
			eventType = taskpkg.EventTurnCancelled
		}
	}
	if result.Success {
		if err := t.emit(t.ctx, taskpkg.Event{TurnID: runtime.turnID, Type: taskpkg.EventAssistantMessageCompleted, Delivery: taskpkg.DeliveryRequiredAck, ElapsedMS: result.Duration.Milliseconds(), Payload: taskpkg.EventPayload{Text: result.Output, DurationMS: result.Duration.Milliseconds()}}); err != nil {
			result.Success = false
			result.Err = types.WrapError(types.ErrEventDelivery, "assistant message was not acknowledged", err)
			result.Error = result.Err.Error()
			status = taskpkg.TurnFailed
			eventType = taskpkg.EventTurnFailed
		}
	}
	payload := taskpkg.EventPayload{Status: status, Error: result.Error, DurationMS: result.Duration.Milliseconds()}
	var harnessError *types.HarnessError
	if errors.As(result.Err, &harnessError) {
		payload.ErrorCode = harnessError.Code
	}
	if err := t.emit(context.Background(), taskpkg.Event{TurnID: runtime.turnID, Type: eventType, Delivery: taskpkg.DeliveryRequiredAck, ElapsedMS: result.Duration.Milliseconds(), Payload: payload}); err != nil {
		result.Success = false
		result.Err = types.WrapError(types.ErrEventDelivery, "turn completion was not acknowledged", err)
		result.Error = result.Err.Error()
		status = taskpkg.TurnFailed
	}

	state.mu.Lock()
	state.turn.Status = status
	state.turn.CompletedAt = time.Now()
	state.result = &result
	close(state.done)
	state.mu.Unlock()
}

func targetPayload(kind taskpkg.TurnKind, target string, status taskpkg.TurnStatus) taskpkg.EventPayload {
	payload := taskpkg.EventPayload{Status: status}
	switch kind {
	case taskpkg.TurnAgent:
		payload.AgentID = target
	case taskpkg.TurnTeam:
		payload.TeamID = target
	case taskpkg.TurnWorkflow:
		payload.WorkflowID = target
	}
	return payload
}

// WaitTurn waits for one turn without affecting its lifecycle when the caller
// stops waiting.
func (t *Task) WaitTurn(ctx context.Context, turnID string) (*taskpkg.Result, error) {
	state, err := t.turn(turnID)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-state.done:
		state.mu.RLock()
		defer state.mu.RUnlock()
		if state.result == nil {
			return nil, types.NewError(types.ErrRunInterrupted, "turn ended without a result")
		}
		copy := *state.result
		return &copy, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CancelTurn explicitly cancels a background turn.
func (t *Task) CancelTurn(_ context.Context, turnID string) error {
	state, err := t.turn(turnID)
	if err != nil {
		return err
	}
	state.cancel()
	return nil
}

// AnswerInteraction acknowledges and delivers a response to a waiting turn.
func (t *Task) AnswerInteraction(ctx context.Context, requestID string, answers map[string][]string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.RLock()
	pending, ok := t.pending[requestID]
	t.mu.RUnlock()
	if !ok {
		return types.NewError(types.ErrInteractionNotFound, fmt.Sprintf("interaction %q is not waiting", requestID))
	}
	response := interaction.Response{RequestID: requestID, Answers: answers, AnsweredAt: time.Now()}
	if err := response.ValidateAgainst(pending.request); err != nil {
		return types.WrapError(types.ErrInvalidInput, "interaction response is invalid", err)
	}
	if err := t.emit(ctx, taskpkg.Event{TurnID: pending.turnID, Type: taskpkg.EventInteractionResponded, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Response: &response}}); err != nil {
		return types.WrapError(types.ErrEventDelivery, "interaction response was not acknowledged", err)
	}
	if pending.restored {
		return t.resumeRestoredInteraction(response)
	}
	t.mu.Lock()
	delete(t.pending, requestID)
	t.mu.Unlock()
	select {
	case pending.answer <- response:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.ctx.Done():
		return types.WrapError(types.ErrRunCancelled, "task closed while answering interaction", t.ctx.Err())
	}
}

// Close cancels active turns and releases only in-memory task state.
func (t *Task) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.cancel()
	t.mu.Unlock()
}

func (t *Task) ensureOpen() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return types.NewError(types.ErrRunCancelled, "task is closed")
	}
	return nil
}

func (t *Task) turn(turnID string) (*taskTurnState, error) {
	t.mu.RLock()
	state, ok := t.turns[turnID]
	t.mu.RUnlock()
	if !ok {
		return nil, types.NewError(types.ErrTurnNotFound, fmt.Sprintf("turn %q not found", turnID))
	}
	return state, nil
}

func (t *Task) removeTurn(turnID string) {
	t.mu.Lock()
	delete(t.turns, turnID)
	t.mu.Unlock()
}

func (t *Task) emit(ctx context.Context, event taskpkg.Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	t.sequence++
	event.Sequence = t.sequence
	event.TaskID = t.id
	if event.ItemID == "" {
		event.ItemID = taskpkg.NewID("item")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if event.ElapsedMS == 0 {
		event.ElapsedMS = event.OccurredAt.Sub(t.started).Milliseconds()
	}
	if event.Payload.Checkpoint != nil {
		event.Payload.Checkpoint.LastSequence = event.Sequence
	}
	if t.sink == nil {
		return nil
	}
	err := t.sink.Emit(ctx, event)
	if event.Delivery == taskpkg.DeliveryBestEffort {
		return nil
	}
	return err
}

func (t *Task) installCheckpoint(checkpoint taskpkg.Checkpoint) error {
	if checkpoint.TurnID == "" || checkpoint.Target == "" {
		return types.NewError(types.ErrInvalidConfig, "checkpoint turn id and target are required")
	}
	state := &taskTurnState{
		turn: taskpkg.Turn{ID: checkpoint.TurnID, TaskID: t.id, Kind: checkpoint.Kind, Target: checkpoint.Target, Status: checkpoint.Status},
		done: make(chan struct{}),
	}
	runtime := &turnRuntime{task: t, turnID: checkpoint.TurnID, kind: checkpoint.Kind, target: checkpoint.Target, startedAt: time.Now(), checkpoint: checkpoint}
	state.runtime = runtime
	state.cancel = func() {}
	t.turns[checkpoint.TurnID] = state
	agentCheckpoint := checkpoint.Agent
	if agentCheckpoint == nil && checkpoint.Team != nil {
		agentCheckpoint = checkpoint.Team.Agent
	}
	if agentCheckpoint == nil && checkpoint.Workflow != nil {
		agentCheckpoint = checkpoint.Workflow.Agent
	}
	if agentCheckpoint != nil {
		mem := memory.NewSummaryMemory(t.summary.KeepRecent)
		mem.Restore(agentCheckpoint.Summary, agentCheckpoint.Messages)
		t.conversations[agentCheckpoint.AgentID] = &taskConversation{gate: make(chan struct{}, 1), mem: mem}
	}
	if checkpoint.Interaction != nil {
		if err := checkpoint.Interaction.Validate(); err != nil {
			return types.WrapError(types.ErrInvalidConfig, "checkpoint interaction is invalid", err)
		}
		t.pending[checkpoint.Interaction.ID] = &pendingInteraction{request: *checkpoint.Interaction, turnID: checkpoint.TurnID, answer: make(chan interaction.Response, 1), restored: true}
	} else {
		if agentCheckpoint != nil && agentCheckpoint.PendingToolCall == nil && agentCheckpoint.LastContent != "" && checkpoint.Kind == taskpkg.TurnAgent {
			state.turn.Status = taskpkg.TurnCompleted
			state.result = &taskpkg.Result{TurnID: checkpoint.TurnID, Kind: checkpoint.Kind, Output: agentCheckpoint.LastContent, Success: true}
		} else if checkpoint.Kind == taskpkg.TurnAgent && agentCheckpoint != nil && agentCheckpoint.PendingToolCall == nil {
			// RestoreTask starts this safe continuation after installation.
			return nil
		} else {
			state.turn.Status = taskpkg.TurnInterrupted
			message := "checkpoint is not a safe automatic continuation point"
			if agentCheckpoint != nil && agentCheckpoint.PendingToolCall != nil {
				message = "external tool result is unknown and will not be replayed"
			}
			err := types.NewError(types.ErrRunInterrupted, message)
			state.result = &taskpkg.Result{TurnID: checkpoint.TurnID, Kind: checkpoint.Kind, Success: false, Error: err.Error()}
		}
		close(state.done)
	}
	return nil
}

func (t *Task) continueRestoredAgent(checkpoint taskpkg.Checkpoint) {
	if checkpoint.Kind != taskpkg.TurnAgent || checkpoint.Agent == nil || checkpoint.Interaction != nil || checkpoint.Agent.PendingToolCall != nil {
		return
	}
	state, err := t.turn(checkpoint.TurnID)
	if err != nil {
		return
	}
	state.mu.RLock()
	alreadyDone := state.result != nil
	state.mu.RUnlock()
	if alreadyDone {
		return
	}
	runtimeAgent, ok := t.harness.Agent(checkpoint.Target)
	if !ok {
		return
	}
	runCtx, cancel := context.WithCancel(t.ctx)
	runCtx = types.WithRunContext(runCtx, &types.RunContext{RunID: checkpoint.Agent.RunID, AgentID: checkpoint.Agent.AgentID})
	state.mu.Lock()
	state.cancel = cancel
	state.turn.Status = taskpkg.TurnRunning
	state.mu.Unlock()
	go t.runTurn(state, state.runtime, runCtx, func(ctx context.Context) taskpkg.Result {
		output := runtimeAgent.Continue(ctx, *checkpoint.Agent)
		return taskpkg.Result{Kind: taskpkg.TurnAgent, Output: output.Content, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
	})
}

func (t *Task) resumeRestoredInteraction(response interaction.Response) error {
	t.mu.Lock()
	pending := t.pending[response.RequestID]
	if pending == nil {
		t.mu.Unlock()
		return types.NewError(types.ErrInteractionNotFound, fmt.Sprintf("interaction %q is not waiting", response.RequestID))
	}
	state := t.turns[pending.turnID]
	delete(t.pending, response.RequestID)
	t.mu.Unlock()
	if state == nil || state.runtime == nil {
		return types.NewError(types.ErrTurnNotFound, "restored turn is unavailable")
	}
	checkpoint := state.runtime.checkpoint
	agentCheckpoint := checkpoint.Agent
	if agentCheckpoint == nil && checkpoint.Team != nil {
		agentCheckpoint = checkpoint.Team.Agent
	}
	if agentCheckpoint == nil && checkpoint.Workflow != nil {
		agentCheckpoint = checkpoint.Workflow.Agent
	}
	if agentCheckpoint == nil || agentCheckpoint.PendingToolCall == nil || agentCheckpoint.PendingToolCall.Function.Name != "request_user_input" {
		return types.NewError(types.ErrRunInterrupted, "checkpoint cannot safely resume this interaction")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return types.WrapError(types.ErrInvalidInput, "encode interaction response", err)
	}
	runCtx, cancel := context.WithCancel(t.ctx)
	runCtx = types.WithRunContext(runCtx, &types.RunContext{RunID: agentCheckpoint.RunID, AgentID: agentCheckpoint.AgentID})
	state.mu.Lock()
	state.cancel = cancel
	state.turn.Status = taskpkg.TurnRunning
	state.mu.Unlock()
	if err := state.runtime.Emit(runCtx, taskpkg.Event{Type: taskpkg.EventTurnStatusChanged, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Status: taskpkg.TurnRunning}}); err != nil {
		cancel()
		return types.WrapError(types.ErrEventDelivery, "resumed turn status was not acknowledged", err)
	}
	var run func(context.Context) taskpkg.Result
	switch checkpoint.Kind {
	case taskpkg.TurnAgent:
		runtimeAgent, ok := t.harness.Agent(checkpoint.Target)
		if !ok {
			cancel()
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("agent %q not found", checkpoint.Target))
		}
		run = func(ctx context.Context) taskpkg.Result {
			output := runtimeAgent.Resume(ctx, *agentCheckpoint, string(encoded))
			return taskpkg.Result{Kind: taskpkg.TurnAgent, Output: output.Content, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
		}
	case taskpkg.TurnTeam:
		runtimeTeam, ok := t.harness.Team(checkpoint.Target)
		if !ok || checkpoint.Team == nil {
			cancel()
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("team %q checkpoint is unavailable", checkpoint.Target))
		}
		run = func(ctx context.Context) taskpkg.Result {
			output := runtimeTeam.Resume(ctx, *checkpoint.Team, string(encoded))
			return taskpkg.Result{Kind: taskpkg.TurnTeam, Output: output.FinalOutput, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
		}
	case taskpkg.TurnWorkflow:
		runtimeWorkflow, ok := t.harness.Workflow(checkpoint.Target)
		if !ok || checkpoint.Workflow == nil {
			cancel()
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("workflow %q checkpoint is unavailable", checkpoint.Target))
		}
		run = func(ctx context.Context) taskpkg.Result {
			output := runtimeWorkflow.Resume(ctx, *checkpoint.Workflow, string(encoded))
			return taskpkg.Result{Kind: taskpkg.TurnWorkflow, Output: output.Output, Success: output.Success, Error: output.Error, Err: output.Err, Value: output}
		}
	default:
		cancel()
		return types.NewError(types.ErrRunInterrupted, "checkpoint kind cannot be resumed")
	}
	go t.runTurn(state, state.runtime, runCtx, run)
	return nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// Runtime implementation used by engine through Context.
func (r *turnRuntime) TaskID() string                       { return r.task.id }
func (r *turnRuntime) TurnID() string                       { return r.turnID }
func (r *turnRuntime) RetryPolicy() taskpkg.RetryConfig     { return r.task.retry }
func (r *turnRuntime) CircuitPolicy() taskpkg.CircuitConfig { return r.task.circuit }
func (r *turnRuntime) SummaryPolicy() taskpkg.SummaryConfig { return r.task.summary }
func (r *turnRuntime) Summarizer() taskpkg.Summarizer       { return r.task.summarizer }
func (r *turnRuntime) Emit(ctx context.Context, event taskpkg.Event) error {
	if event.TurnID == "" {
		event.TurnID = r.turnID
	}
	return r.task.emit(ctx, event)
}

func (r *turnRuntime) AcquireConversation(ctx context.Context, agentID string, fallback memory.Memory) (taskpkg.ConversationLease, error) {
	r.task.mu.Lock()
	conversation := r.task.conversations[agentID]
	if conversation == nil {
		conversation = &taskConversation{gate: make(chan struct{}, 1), mem: memory.NewSummaryMemory(r.task.summary.KeepRecent)}
		r.task.conversations[agentID] = conversation
	}
	r.task.mu.Unlock()
	select {
	case conversation.gate <- struct{}{}:
		return taskpkg.ConversationLease{Memory: conversation.mem, Release: func() { <-conversation.gate }}, nil
	case <-ctx.Done():
		return taskpkg.ConversationLease{}, ctx.Err()
	}
}

func (r *turnRuntime) ReportProgress(ctx context.Context, summary string) error {
	if len([]rune(summary)) > 200 {
		return types.NewError(types.ErrInvalidInput, "progress summary must not exceed 200 Unicode characters")
	}
	r.mu.Lock()
	if r.progressCount >= 20 {
		r.mu.Unlock()
		return types.NewError(types.ErrInvalidInput, "progress summary limit exceeded for this turn")
	}
	r.progressCount++
	r.mu.Unlock()
	return r.Emit(ctx, taskpkg.Event{Type: taskpkg.EventProgressUpdated, Delivery: taskpkg.DeliveryBestEffort, Payload: taskpkg.EventPayload{Text: summary}})
}

func (r *turnRuntime) RequestInput(ctx context.Context, request interaction.Request, checkpoint taskpkg.Checkpoint) (interaction.Response, error) {
	if err := request.Validate(); err != nil {
		return interaction.Response{}, types.WrapError(types.ErrInvalidInput, "interaction request is invalid", err)
	}
	checkpoint.Interaction = &request
	checkpoint.Status = taskpkg.TurnWaitingInput
	if err := r.Checkpoint(ctx, checkpoint); err != nil {
		return interaction.Response{}, err
	}
	pending := &pendingInteraction{request: request, turnID: r.turnID, answer: make(chan interaction.Response, 1)}
	r.task.mu.Lock()
	if _, exists := r.task.pending[request.ID]; exists {
		r.task.mu.Unlock()
		return interaction.Response{}, types.NewError(types.ErrInvalidInput, fmt.Sprintf("interaction %q already exists", request.ID))
	}
	r.task.pending[request.ID] = pending
	r.task.mu.Unlock()
	defer func() {
		r.task.mu.Lock()
		delete(r.task.pending, request.ID)
		r.task.mu.Unlock()
	}()
	if err := r.Emit(ctx, taskpkg.Event{Type: taskpkg.EventTurnStatusChanged, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Status: taskpkg.TurnWaitingInput}}); err != nil {
		return interaction.Response{}, err
	}
	if err := r.Emit(ctx, taskpkg.Event{Type: taskpkg.EventInteractionRequested, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Request: &request}}); err != nil {
		return interaction.Response{}, err
	}
	select {
	case response := <-pending.answer:
		if err := r.Emit(ctx, taskpkg.Event{Type: taskpkg.EventTurnStatusChanged, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Status: taskpkg.TurnRunning}}); err != nil {
			return interaction.Response{}, err
		}
		return response, nil
	case <-ctx.Done():
		return interaction.Response{}, ctx.Err()
	}
}

func (r *turnRuntime) Checkpoint(ctx context.Context, checkpoint taskpkg.Checkpoint) error {
	r.mu.Lock()
	previous := r.checkpoint
	r.mu.Unlock()
	if r.kind == taskpkg.TurnTeam && checkpoint.Kind == taskpkg.TurnAgent && checkpoint.Agent != nil {
		teamCheckpoint := previous.Team
		if teamCheckpoint == nil {
			teamCheckpoint = &taskpkg.TeamCheckpoint{TeamID: r.target}
		}
		teamCopy := *teamCheckpoint
		teamCopy.Agents = cloneAgentCheckpoints(teamCheckpoint.Agents)
		agentCopy := *checkpoint.Agent
		teamCopy.Agents[agentCopy.AgentID] = &agentCopy
		if teamCheckpoint.Agent != nil && teamCheckpoint.Agent.PendingToolCall != nil && teamCheckpoint.Agent.AgentID != agentCopy.AgentID {
			teamCopy.Agent = teamCheckpoint.Agent
			teamCopy.PendingAgentID = teamCheckpoint.Agent.AgentID
			checkpoint.Interaction = previous.Interaction
		} else {
			teamCopy.Agent = &agentCopy
			teamCopy.PendingAgentID = agentCopy.AgentID
		}
		checkpoint.Kind = taskpkg.TurnTeam
		checkpoint.Target = r.target
		checkpoint.Team = &teamCopy
		checkpoint.Agent = nil
	}
	if r.kind == taskpkg.TurnWorkflow && checkpoint.Kind == taskpkg.TurnAgent && checkpoint.Agent != nil {
		workflowCheckpoint := previous.Workflow
		if workflowCheckpoint == nil {
			workflowCheckpoint = &taskpkg.WorkflowCheckpoint{WorkflowID: r.target}
		}
		workflowCopy := *workflowCheckpoint
		workflowCopy.Agent = checkpoint.Agent
		checkpoint.Kind = taskpkg.TurnWorkflow
		checkpoint.Target = r.target
		checkpoint.Workflow = &workflowCopy
		checkpoint.Agent = nil
	}
	if r.kind == taskpkg.TurnWorkflow && checkpoint.Kind == taskpkg.TurnWorkflow && checkpoint.Workflow != nil && previous.Interaction != nil && previous.Workflow != nil && previous.Workflow.Agent != nil && previous.Workflow.Agent.PendingToolCall != nil {
		currentAgent := checkpoint.Workflow.Agent
		if currentAgent == nil || currentAgent.AgentID != previous.Workflow.Agent.AgentID {
			workflowCopy := *checkpoint.Workflow
			workflowCopy.Agent = previous.Workflow.Agent
			workflowCopy.NodePath = append([]string(nil), previous.Workflow.NodePath...)
			workflowCopy.Agents = cloneAgentCheckpoints(checkpoint.Workflow.Agents)
			for key, saved := range previous.Workflow.Agents {
				if _, exists := workflowCopy.Agents[key]; !exists && saved != nil {
					copy := *saved
					workflowCopy.Agents[key] = &copy
				}
			}
			checkpoint.Workflow = &workflowCopy
			checkpoint.Interaction = previous.Interaction
		}
	}
	checkpoint.Version = taskpkg.CheckpointVersion
	checkpoint.TaskID = r.task.id
	checkpoint.TurnID = r.turnID
	r.mu.Lock()
	r.checkpoint = checkpoint
	r.mu.Unlock()
	return r.Emit(ctx, taskpkg.Event{Type: taskpkg.EventCheckpointReady, Delivery: taskpkg.DeliveryRequiredAck, Payload: taskpkg.EventPayload{Checkpoint: &checkpoint}})
}

func cloneAgentCheckpoints(source map[string]*taskpkg.AgentCheckpoint) map[string]*taskpkg.AgentCheckpoint {
	result := make(map[string]*taskpkg.AgentCheckpoint, len(source)+1)
	for key, checkpoint := range source {
		if checkpoint == nil {
			continue
		}
		copy := *checkpoint
		result[key] = &copy
	}
	return result
}
