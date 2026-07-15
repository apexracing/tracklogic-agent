package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/model"
	taskpkg "github.com/apexracing/tracklogic-agent/task"
	"github.com/apexracing/tracklogic-agent/types"
	"github.com/apexracing/tracklogic-agent/workflow"
)

type scriptedTaskModel struct {
	mu      sync.Mutex
	calls   int
	handler func(int, *model.InvokeRequest) (*model.InvokeResponse, error)
}

func (m *scriptedTaskModel) Invoke(_ context.Context, request *model.InvokeRequest) (*model.InvokeResponse, error) {
	return m.next(request)
}

func (m *scriptedTaskModel) InvokeStream(ctx context.Context, request *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	response, err := m.next(request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan model.ResponseChunk, 1)
	select {
	case chunks <- model.ResponseChunk{Content: response.Content, ToolCall: firstToolCall(response.ToolCalls), Usage: response.Usage, Done: true}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	close(chunks)
	return chunks, nil
}

func firstToolCall(calls []types.ToolCall) *types.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	copy := calls[0]
	return &copy
}

func (m *scriptedTaskModel) next(request *model.InvokeRequest) (*model.InvokeResponse, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	return m.handler(call, request)
}

func (*scriptedTaskModel) Provider() string { return "test" }
func (*scriptedTaskModel) ModelID() string  { return "task-script" }

type eventLog struct {
	mu     sync.Mutex
	events []taskpkg.Event
	notify chan struct{}
	fail   taskpkg.EventType
}

func newEventLog() *eventLog {
	return &eventLog{notify: make(chan struct{}, 1)}
}

func (log *eventLog) Emit(_ context.Context, event taskpkg.Event) error {
	log.mu.Lock()
	log.events = append(log.events, event)
	fail := event.Type == log.fail
	log.mu.Unlock()
	select {
	case log.notify <- struct{}{}:
	default:
	}
	if fail {
		return errors.New("sink rejected event")
	}
	return nil
}

func (log *eventLog) snapshot() []taskpkg.Event {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]taskpkg.Event(nil), log.events...)
}

func (log *eventLog) waitFor(t *testing.T, eventType taskpkg.EventType) taskpkg.Event {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		for _, event := range log.snapshot() {
			if event.Type == eventType {
				return event
			}
		}
		select {
		case <-log.notify:
		case <-deadline.C:
			t.Fatalf("did not receive event %s", eventType)
		}
	}
}

func newTaskHarness(t *testing.T, runtimeModel model.Model) *Harness {
	t.Helper()
	harness, err := New(DefaultConfig(), WithModel(runtimeModel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.CreateAgent("assistant", "Be concise."); err != nil {
		t.Fatal(err)
	}
	return harness
}

func TestTaskRequiredAckPreventsRun(t *testing.T) {
	var calls atomic.Int32
	runtimeModel := &scriptedTaskModel{handler: func(int, *model.InvokeRequest) (*model.InvokeResponse, error) {
		calls.Add(1)
		return &model.InvokeResponse{Content: "should not run"}, nil
	}}
	harness := newTaskHarness(t, runtimeModel)
	sink := newEventLog()
	sink.fail = taskpkg.EventUserMessage
	runtimeTask, err := harness.NewTask(taskpkg.Options{TaskID: "ack-test", EventSink: sink})
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeTask.Close()
	if _, err := runtimeTask.StartAgent(context.Background(), "assistant", "hello"); err == nil {
		t.Fatal("StartAgent succeeded without required acknowledgement")
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls = %d, want 0", calls.Load())
	}
}

func TestTaskRetriesModelFiveAttemptsAndSequencesEvents(t *testing.T) {
	runtimeModel := &scriptedTaskModel{handler: func(call int, _ *model.InvokeRequest) (*model.InvokeResponse, error) {
		if call < 5 {
			return nil, types.WrapError(types.ErrAPIError, "temporary", &model.HTTPError{Provider: "test", StatusCode: 503})
		}
		return &model.InvokeResponse{Content: "done"}, nil
	}}
	harness := newTaskHarness(t, runtimeModel)
	sink := newEventLog()
	retry := taskpkg.DefaultRetryConfig()
	retry.BaseDelay = time.Microsecond
	retry.MaxDelay = time.Microsecond
	runtimeTask, err := harness.NewTask(taskpkg.Options{TaskID: "retry-test", EventSink: sink, Retry: retry})
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeTask.Close()
	turn, err := runtimeTask.StartAgent(context.Background(), "assistant", "hello")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtimeTask.WaitTurn(context.Background(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Output != "done" {
		t.Fatalf("result = %#v", result)
	}
	events := sink.snapshot()
	attempts := 0
	retries := 0
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence = %d", index, event.Sequence)
		}
		switch event.Type {
		case taskpkg.EventModelAttemptStarted:
			attempts++
		case taskpkg.EventModelRetryWaiting:
			retries++
		}
	}
	if attempts != 5 || retries != 4 {
		t.Fatalf("attempts/retries = %d/%d, want 5/4", attempts, retries)
	}
}

func TestTaskSummaryAndHardLimit(t *testing.T) {
	t.Run("compacts", func(t *testing.T) {
		var summaries atomic.Int32
		runtimeModel := &scriptedTaskModel{handler: func(call int, _ *model.InvokeRequest) (*model.InvokeResponse, error) {
			return &model.InvokeResponse{Content: fmt.Sprintf("answer-%d", call)}, nil
		}}
		harness := newTaskHarness(t, runtimeModel)
		sink := newEventLog()
		runtimeTask, err := harness.NewTask(taskpkg.Options{
			TaskID: "summary-success", EventSink: sink,
			Summary: taskpkg.SummaryConfig{MaxMessages: 2, KeepRecent: 1, HardLimit: 4},
			Summarizer: taskpkg.SummarizerFunc(func(context.Context, taskpkg.SummaryInput) (string, error) {
				summaries.Add(1)
				return "compact context", nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer runtimeTask.Close()
		for _, input := range []string{"one", "two"} {
			turn, err := runtimeTask.StartAgent(context.Background(), "assistant", input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runtimeTask.WaitTurn(context.Background(), turn.ID)
			if err != nil || !result.Success {
				t.Fatalf("result/error = %#v / %v", result, err)
			}
		}
		if summaries.Load() != 1 {
			t.Fatalf("summary calls = %d, want 1", summaries.Load())
		}
		found := false
		for _, event := range sink.snapshot() {
			if event.Type == taskpkg.EventCheckpointReady && event.Payload.Checkpoint != nil && event.Payload.Checkpoint.Agent != nil && event.Payload.Checkpoint.Agent.Summary == "compact context" {
				found = true
			}
		}
		if !found {
			t.Fatal("summary checkpoint was not emitted")
		}
	})

	t.Run("fails_at_hard_limit", func(t *testing.T) {
		runtimeModel := &scriptedTaskModel{handler: func(int, *model.InvokeRequest) (*model.InvokeResponse, error) {
			return &model.InvokeResponse{Content: "answer"}, nil
		}}
		harness := newTaskHarness(t, runtimeModel)
		runtimeTask, err := harness.NewTask(taskpkg.Options{
			TaskID: "summary-failure", Summary: taskpkg.SummaryConfig{MaxMessages: 2, KeepRecent: 1, HardLimit: 3},
			Summarizer: taskpkg.SummarizerFunc(func(context.Context, taskpkg.SummaryInput) (string, error) { return "", errors.New("unavailable") }),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer runtimeTask.Close()
		first, _ := runtimeTask.StartAgent(context.Background(), "assistant", "one")
		if result, err := runtimeTask.WaitTurn(context.Background(), first.ID); err != nil || !result.Success {
			t.Fatalf("first = %#v, %v", result, err)
		}
		second, _ := runtimeTask.StartAgent(context.Background(), "assistant", "two")
		result, err := runtimeTask.WaitTurn(context.Background(), second.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Success || !strings.Contains(result.Error, string(types.ErrSummaryFailed)) {
			t.Fatalf("result = %#v", result)
		}
	})
}

func TestTaskCircuitOpensPerModelInstance(t *testing.T) {
	var calls atomic.Int32
	runtimeModel := &scriptedTaskModel{handler: func(int, *model.InvokeRequest) (*model.InvokeResponse, error) {
		calls.Add(1)
		return nil, types.WrapError(types.ErrAPIError, "temporary", &model.HTTPError{Provider: "test", StatusCode: 503})
	}}
	harness := newTaskHarness(t, runtimeModel)
	options := taskpkg.Options{
		Retry:   taskpkg.RetryConfig{MaxAttempts: 1},
		Circuit: taskpkg.CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute, HalfOpenMax: 1},
	}
	for index := 0; index < 2; index++ {
		options.TaskID = fmt.Sprintf("circuit-%d", index)
		runtimeTask, err := harness.NewTask(options)
		if err != nil {
			t.Fatal(err)
		}
		turn, err := runtimeTask.StartAgent(context.Background(), "assistant", "hello")
		if err != nil {
			t.Fatal(err)
		}
		result, err := runtimeTask.WaitTurn(context.Background(), turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Success {
			t.Fatalf("turn %d unexpectedly succeeded", index)
		}
		if index == 1 && !strings.Contains(result.Error, string(types.ErrCircuitOpen)) {
			t.Fatalf("second error = %s", result.Error)
		}
		runtimeTask.Close()
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want 1", calls.Load())
	}
}

type interruptingStreamModel struct{ calls atomic.Int32 }

func (*interruptingStreamModel) Invoke(context.Context, *model.InvokeRequest) (*model.InvokeResponse, error) {
	return nil, errors.New("unexpected Invoke")
}
func (m *interruptingStreamModel) InvokeStream(_ context.Context, _ *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	call := m.calls.Add(1)
	chunks := make(chan model.ResponseChunk, 2)
	if call == 1 {
		chunks <- model.ResponseChunk{Content: "partial"}
		chunks <- model.ResponseChunk{Error: types.WrapError(types.ErrAPIError, "stream ended", fmt.Errorf("%w", io.ErrUnexpectedEOF))}
	} else {
		chunks <- model.ResponseChunk{Content: "complete", Done: true}
	}
	close(chunks)
	return chunks, nil
}
func (*interruptingStreamModel) Provider() string { return "test" }
func (*interruptingStreamModel) ModelID() string  { return "interrupting" }

func TestTaskStreamRetryResetsIncompleteContent(t *testing.T) {
	runtimeModel := &interruptingStreamModel{}
	harness := newTaskHarness(t, runtimeModel)
	sink := newEventLog()
	runtimeTask, err := harness.NewTask(taskpkg.Options{TaskID: "stream-reset", EventSink: sink, Retry: taskpkg.RetryConfig{MaxAttempts: 2, BaseDelay: time.Microsecond, MaxDelay: time.Microsecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeTask.Close()
	turn, err := runtimeTask.StartAgent(context.Background(), "assistant", "hello")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtimeTask.WaitTurn(context.Background(), turn.ID)
	if err != nil || !result.Success || result.Output != "complete" {
		t.Fatalf("result/error = %#v / %v", result, err)
	}
	reset := false
	for _, event := range sink.snapshot() {
		if event.Type == taskpkg.EventContentReset {
			reset = true
		}
	}
	if !reset {
		t.Fatal("content.reset was not emitted")
	}
}

type legacyOnlyNode struct{ id string }

func (node *legacyOnlyNode) ID() string         { return node.id }
func (*legacyOnlyNode) Type() workflow.NodeType { return workflow.NodeTypeStep }
func (*legacyOnlyNode) Execute(_ context.Context, input string, _ *workflow.State) (string, error) {
	return input + "-legacy", nil
}

func TestTaskWorkflowRejectsNonCheckpointableCustomNodeOnlyInTaskMode(t *testing.T) {
	runtimeModel := &scriptedTaskModel{handler: func(int, *model.InvokeRequest) (*model.InvokeResponse, error) {
		return &model.InvokeResponse{Content: "unused"}, nil
	}}
	harness := newTaskHarness(t, runtimeModel)
	if _, err := harness.CreateWorkflow(workflow.WorkflowConfig{Name: "custom", Nodes: []workflow.Node{&legacyOnlyNode{id: "legacy"}}}); err != nil {
		t.Fatal(err)
	}
	legacy := harness.RunWorkflow(context.Background(), "custom", "input")
	if !legacy.Success || legacy.Output != "input-legacy" {
		t.Fatalf("legacy result = %#v", legacy)
	}
	runtimeTask, err := harness.NewTask(taskpkg.Options{TaskID: "custom-node"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeTask.Close()
	if _, err := runtimeTask.StartWorkflow(context.Background(), "custom", "input"); err == nil {
		t.Fatal("Task mode accepted non-checkpointable custom node")
	}
}

func TestTaskInteractionCanRestoreOriginalTurn(t *testing.T) {
	runtimeModel := &scriptedTaskModel{handler: func(call int, request *model.InvokeRequest) (*model.InvokeResponse, error) {
		if call == 1 {
			return &model.InvokeResponse{ToolCalls: []types.ToolCall{
				{ID: "call-question", Type: "function", Function: types.ToolCallFunction{
					Name:      "request_user_input",
					Arguments: `{"title":"Choose","questions":[{"id":"mode","prompt":"Which mode?","options":[{"label":"safe"}],"required":true}]}`,
				}},
			}}, nil
		}
		if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].ToolCallID != "call-question" {
			return nil, fmt.Errorf("interaction answer did not preserve ToolCallID")
		}
		return &model.InvokeResponse{Content: "continued"}, nil
	}}
	harness := newTaskHarness(t, runtimeModel)
	firstSink := newEventLog()
	firstTask, err := harness.NewTask(taskpkg.Options{TaskID: "restore-test", EventSink: firstSink})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := firstTask.StartAgent(context.Background(), "assistant", "start")
	if err != nil {
		t.Fatal(err)
	}
	requested := firstSink.waitFor(t, taskpkg.EventInteractionRequested)
	var checkpoint taskpkg.Checkpoint
	var sequence uint64
	for _, event := range firstSink.snapshot() {
		if event.Type == taskpkg.EventCheckpointReady && event.Payload.Checkpoint != nil && event.Payload.Checkpoint.Interaction != nil {
			checkpoint = *event.Payload.Checkpoint
			sequence = event.Sequence
		}
	}
	if checkpoint.Interaction == nil {
		t.Fatal("interaction checkpoint was not emitted")
	}
	if requested.TurnID != turn.ID || checkpoint.TurnID != turn.ID {
		t.Fatalf("turn ids changed: %s %s %s", turn.ID, requested.TurnID, checkpoint.TurnID)
	}
	firstTask.Close()

	restoredSink := newEventLog()
	restored, err := harness.RestoreTask(taskpkg.RestoreInput{TaskID: "restore-test", Checkpoint: checkpoint, LastSequence: sequence}, restoredSink)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.AnswerInteraction(context.Background(), checkpoint.Interaction.ID, map[string][]string{"mode": {"safe"}}); err != nil {
		t.Fatal(err)
	}
	result, err := restored.WaitTurn(context.Background(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Output != "continued" {
		t.Fatalf("restored result = %#v", result)
	}
}

func TestWorkflowCompositeNodesRestoreInteraction(t *testing.T) {
	tests := []struct {
		name  string
		build func(*engine.Agent, *engine.Agent) workflow.Node
		want  string
	}{
		{name: "condition", build: func(interactive, _ *engine.Agent) workflow.Node {
			return workflow.NewConditionNode("choose", func(string, *workflow.State) (bool, error) { return true, nil }, workflow.NewStepNode("ask", interactive), nil)
		}, want: "continued"},
		{name: "loop", build: func(interactive, _ *engine.Agent) workflow.Node {
			return workflow.NewLoopNode("repeat", workflow.NewStepNode("ask", interactive), func(iteration int, _ string, _ *workflow.State) (bool, error) { return iteration < 1, nil }, 2)
		}, want: "continued"},
		{name: "parallel", build: func(interactive, static *engine.Agent) workflow.Node {
			return workflow.NewParallelNode("both", workflow.NewStepNode("ask", interactive), workflow.NewStepNode("static", static))
		}, want: "continued"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interactiveModel := interactionScriptModel()
			harness, err := New(DefaultConfig(), WithModel(interactiveModel))
			if err != nil {
				t.Fatal(err)
			}
			interactive, err := harness.CreateAgent("interactive", "ask when needed")
			if err != nil {
				t.Fatal(err)
			}
			static, err := harness.CreateAgent("static", "answer")
			if err != nil {
				t.Fatal(err)
			}
			static.SetModel(&scriptedTaskModel{handler: func(int, *model.InvokeRequest) (*model.InvokeResponse, error) {
				return &model.InvokeResponse{Content: "static-result"}, nil
			}})
			workflowName := "flow-" + test.name
			if _, err := harness.CreateWorkflow(workflow.WorkflowConfig{Name: workflowName, Nodes: []workflow.Node{test.build(interactive, static)}}); err != nil {
				t.Fatal(err)
			}
			result := runRestoreWorkflow(t, harness, workflowName, "workflow-"+test.name)
			if !result.Success || !strings.Contains(result.Output, test.want) {
				t.Fatalf("workflow result = %#v", result)
			}
		})
	}
}

func TestTeamModesRestoreInteraction(t *testing.T) {
	tests := []struct {
		name   string
		mode   workflow.TeamMode
		leader bool
		want   string
	}{
		{name: "sequential", mode: workflow.ModeSequential, want: "static-result"},
		{name: "parallel", mode: workflow.ModeParallel, want: "continued"},
		{name: "leader_follower", mode: workflow.ModeLeaderFollower, leader: true, want: "static-result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness, err := New(DefaultConfig(), WithModel(interactionScriptModel()))
			if err != nil {
				t.Fatal(err)
			}
			interactive, err := harness.CreateAgent("interactive", "ask")
			if err != nil {
				t.Fatal(err)
			}
			static, err := harness.CreateAgent("static", "answer")
			if err != nil {
				t.Fatal(err)
			}
			static.SetModel(&scriptedTaskModel{handler: func(int, *model.InvokeRequest) (*model.InvokeResponse, error) {
				return &model.InvokeResponse{Content: "static-result"}, nil
			}})
			config := workflow.TeamConfig{Name: "team-" + test.name, Mode: test.mode, Agents: []*engine.Agent{interactive, static}}
			if test.leader {
				config.Agents = []*engine.Agent{static, interactive}
				config.Leader = static
			}
			if _, err := harness.CreateTeam(config); err != nil {
				t.Fatal(err)
			}
			result := runRestoreTeam(t, harness, config.Name, "team-task-"+test.name)
			if !result.Success || !strings.Contains(result.FinalOutput, test.want) {
				t.Fatalf("team result = %#v", result)
			}
		})
	}
}

func runRestoreTeam(t *testing.T, harness *Harness, teamName, taskID string) *workflow.TeamOutput {
	t.Helper()
	sink := newEventLog()
	running, err := harness.NewTask(taskpkg.Options{TaskID: taskID, EventSink: sink})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := running.StartTeam(context.Background(), teamName, "start")
	if err != nil {
		t.Fatal(err)
	}
	sink.waitFor(t, taskpkg.EventInteractionRequested)
	deadline := time.Now().Add(3 * time.Second)
	var checkpoint taskpkg.Checkpoint
	var sequence uint64
	for time.Now().Before(deadline) {
		for _, event := range sink.snapshot() {
			if event.Type == taskpkg.EventCheckpointReady && event.Payload.Checkpoint != nil && event.Payload.Checkpoint.Interaction != nil {
				checkpoint = *event.Payload.Checkpoint
				sequence = event.Sequence
			}
		}
		if checkpoint.Team != nil && checkpoint.Team.Agent != nil {
			staticCheckpoint := checkpoint.Team.Agents["static"]
			if checkpoint.Team.Mode != string(workflow.ModeParallel) || (staticCheckpoint != nil && staticCheckpoint.LastContent != "") {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if checkpoint.Interaction == nil || checkpoint.Team == nil {
		t.Fatal("team interaction checkpoint was not emitted")
	}
	running.Close()
	restored, err := harness.RestoreTask(taskpkg.RestoreInput{TaskID: taskID, Checkpoint: checkpoint, LastSequence: sequence}, newEventLog())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.AnswerInteraction(context.Background(), checkpoint.Interaction.ID, map[string][]string{"mode": {"safe"}}); err != nil {
		t.Fatal(err)
	}
	result, err := restored.WaitTurn(context.Background(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	teamResult, ok := result.Value.(*workflow.TeamOutput)
	if !ok {
		t.Fatalf("result value type = %T", result.Value)
	}
	return teamResult
}

func interactionScriptModel() model.Model {
	return &scriptedTaskModel{handler: func(call int, request *model.InvokeRequest) (*model.InvokeResponse, error) {
		if call == 1 {
			return &model.InvokeResponse{ToolCalls: []types.ToolCall{
				{ID: "call-question", Type: "function", Function: types.ToolCallFunction{
					Name:      "request_user_input",
					Arguments: `{"questions":[{"id":"mode","prompt":"Which mode?","options":[{"label":"safe"}],"required":true}]}`,
				}},
			}}, nil
		}
		if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].ToolCallID != "call-question" {
			return nil, fmt.Errorf("interaction answer did not preserve ToolCallID")
		}
		return &model.InvokeResponse{Content: "continued"}, nil
	}}
}

func runRestoreWorkflow(t *testing.T, harness *Harness, workflowName, taskID string) *workflow.WorkflowResult {
	t.Helper()
	sink := newEventLog()
	running, err := harness.NewTask(taskpkg.Options{TaskID: taskID, EventSink: sink})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := running.StartWorkflow(context.Background(), workflowName, "start")
	if err != nil {
		t.Fatal(err)
	}
	sink.waitFor(t, taskpkg.EventInteractionRequested)
	deadline := time.Now().Add(3 * time.Second)
	var checkpoint taskpkg.Checkpoint
	var sequence uint64
	for time.Now().Before(deadline) {
		for _, event := range sink.snapshot() {
			if event.Type == taskpkg.EventCheckpointReady && event.Payload.Checkpoint != nil && event.Payload.Checkpoint.Interaction != nil {
				checkpoint = *event.Payload.Checkpoint
				sequence = event.Sequence
			}
		}
		if checkpoint.Workflow != nil && checkpoint.Workflow.Agent != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if checkpoint.Interaction == nil || checkpoint.Workflow == nil {
		t.Fatal("workflow interaction checkpoint was not emitted")
	}
	running.Close()
	restored, err := harness.RestoreTask(taskpkg.RestoreInput{TaskID: taskID, Checkpoint: checkpoint, LastSequence: sequence}, newEventLog())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.AnswerInteraction(context.Background(), checkpoint.Interaction.ID, map[string][]string{"mode": {"safe"}}); err != nil {
		t.Fatal(err)
	}
	result, err := restored.WaitTurn(context.Background(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflowResult, ok := result.Value.(*workflow.WorkflowResult)
	if !ok {
		t.Fatalf("result value type = %T", result.Value)
	}
	return workflowResult
}
