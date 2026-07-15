package task

import (
	"context"
	"encoding/json"
	"time"

	"github.com/apexracing/tracklogic-agent/interaction"
	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/types"
)

type EventType string

const (
	EventTaskStarted               EventType = "task.started"
	EventTurnStarted               EventType = "turn.started"
	EventTurnStatusChanged         EventType = "turn.status_changed"
	EventUserMessage               EventType = "message.user"
	EventAssistantMessageDelta     EventType = "message.assistant_delta"
	EventAssistantMessageCompleted EventType = "message.assistant_completed"
	EventProgressUpdated           EventType = "progress.updated"
	EventInteractionRequested      EventType = "interaction.requested"
	EventInteractionResponded      EventType = "interaction.responded"
	EventToolStarted               EventType = "tool.started"
	EventToolCompleted             EventType = "tool.completed"
	EventModelAttemptStarted       EventType = "model.attempt_started"
	EventModelRetryWaiting         EventType = "model.retry_waiting"
	EventContentReset              EventType = "content.reset"
	EventCheckpointReady           EventType = "checkpoint.ready"
	EventTurnCompleted             EventType = "turn.completed"
	EventTurnFailed                EventType = "turn.failed"
	EventTurnCancelled             EventType = "turn.cancelled"
)

type DeliveryMode string

const (
	DeliveryBestEffort  DeliveryMode = "best_effort"
	DeliveryRequiredAck DeliveryMode = "required_ack"
)

type TurnKind string

const (
	TurnAgent    TurnKind = "agent"
	TurnTeam     TurnKind = "team"
	TurnWorkflow TurnKind = "workflow"
)

type TurnStatus string

const (
	TurnQueued       TurnStatus = "queued"
	TurnRunning      TurnStatus = "running"
	TurnWaitingInput TurnStatus = "waiting_input"
	TurnCompleted    TurnStatus = "completed"
	TurnFailed       TurnStatus = "failed"
	TurnCancelled    TurnStatus = "cancelled"
	TurnInterrupted  TurnStatus = "interrupted"
)

type EventPayload struct {
	Text        string                `json:"text,omitempty"`
	Status      TurnStatus            `json:"status,omitempty"`
	Phase       string                `json:"phase,omitempty"`
	AgentID     string                `json:"agent_id,omitempty"`
	TeamID      string                `json:"team_id,omitempty"`
	WorkflowID  string                `json:"workflow_id,omitempty"`
	ToolName    string                `json:"tool_name,omitempty"`
	ToolCallID  string                `json:"tool_call_id,omitempty"`
	Attempt     int                   `json:"attempt,omitempty"`
	MaxAttempts int                   `json:"max_attempts,omitempty"`
	NextAttempt int                   `json:"next_attempt,omitempty"`
	DelayMS     int64                 `json:"delay_ms,omitempty"`
	ErrorCode   types.ErrorCode       `json:"error_code,omitempty"`
	Error       string                `json:"error,omitempty"`
	Request     *interaction.Request  `json:"request,omitempty"`
	Response    *interaction.Response `json:"response,omitempty"`
	Checkpoint  *Checkpoint           `json:"checkpoint,omitempty"`
	TotalTokens int                   `json:"total_tokens,omitempty"`
	LoopCount   int                   `json:"loop_count,omitempty"`
	DurationMS  int64                 `json:"duration_ms,omitempty"`
}

type Event struct {
	Sequence   uint64       `json:"sequence"`
	TaskID     string       `json:"task_id"`
	TurnID     string       `json:"turn_id,omitempty"`
	RunID      string       `json:"run_id,omitempty"`
	ItemID     string       `json:"item_id,omitempty"`
	Type       EventType    `json:"type"`
	Delivery   DeliveryMode `json:"delivery"`
	OccurredAt time.Time    `json:"occurred_at"`
	ElapsedMS  int64        `json:"elapsed_ms,omitempty"`
	Payload    EventPayload `json:"payload,omitempty"`
}

type EventSink interface {
	Emit(ctx context.Context, event Event) error
}

type EventSinkFunc func(context.Context, Event) error

func (f EventSinkFunc) Emit(ctx context.Context, event Event) error { return f(ctx, event) }

type RetryConfig struct {
	MaxAttempts   int           `json:"max_attempts"`
	BaseDelay     time.Duration `json:"base_delay"`
	MaxDelay      time.Duration `json:"max_delay"`
	MaxRetryAfter time.Duration `json:"max_retry_after"`
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second, MaxRetryAfter: 30 * time.Second}
}

type CircuitConfig struct {
	FailureThreshold int           `json:"failure_threshold"`
	OpenDuration     time.Duration `json:"open_duration"`
	HalfOpenMax      int           `json:"half_open_max"`
}

func DefaultCircuitConfig() CircuitConfig {
	return CircuitConfig{FailureThreshold: 3, OpenDuration: 30 * time.Second, HalfOpenMax: 1}
}

type SummaryConfig struct {
	MaxMessages int `json:"max_messages"`
	KeepRecent  int `json:"keep_recent"`
	HardLimit   int `json:"hard_limit"`
}

type SummaryInput struct {
	Previous string          `json:"previous,omitempty"`
	Messages []types.Message `json:"messages"`
}

// Summarizer lets callers replace the default current-Model summarizer without
// adding storage responsibility to the Task runtime.
type Summarizer interface {
	Summarize(context.Context, SummaryInput) (string, error)
}

type SummarizerFunc func(context.Context, SummaryInput) (string, error)

func (function SummarizerFunc) Summarize(ctx context.Context, input SummaryInput) (string, error) {
	return function(ctx, input)
}

func DefaultSummaryConfig() SummaryConfig {
	return SummaryConfig{MaxMessages: 50, KeepRecent: 20, HardLimit: 100}
}

type Options struct {
	TaskID     string
	EventSink  EventSink
	Retry      RetryConfig
	Circuit    CircuitConfig
	Summary    SummaryConfig
	Summarizer Summarizer
}

type Turn struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	Kind        TurnKind   `json:"kind"`
	Target      string     `json:"target"`
	Status      TurnStatus `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at,omitempty"`
}

type Result struct {
	TurnID   string        `json:"turn_id"`
	Kind     TurnKind      `json:"kind"`
	Output   string        `json:"output,omitempty"`
	Success  bool          `json:"success"`
	Error    string        `json:"error,omitempty"`
	Err      error         `json:"-"`
	Duration time.Duration `json:"duration"`
	Value    any           `json:"-"`
}

const CheckpointVersion = 1

type AgentCheckpoint struct {
	AgentID         string           `json:"agent_id"`
	RunID           string           `json:"run_id"`
	Messages        []types.Message  `json:"messages"`
	ToolCalls       []types.ToolCall `json:"tool_calls,omitempty"`
	PendingToolCall *types.ToolCall  `json:"pending_tool_call,omitempty"`
	LastContent     string           `json:"last_content,omitempty"`
	TotalTokens     int              `json:"total_tokens"`
	LoopCount       int              `json:"loop_count"`
	MaxLoops        int              `json:"max_loops"`
	Temperature     float64          `json:"temperature"`
	MaxTokens       int              `json:"max_tokens"`
	Summary         string           `json:"summary,omitempty"`
}

type TeamCheckpoint struct {
	TeamID         string                      `json:"team_id"`
	Mode           string                      `json:"mode"`
	Phase          string                      `json:"phase,omitempty"`
	CurrentIndex   int                         `json:"current_index"`
	CurrentInput   string                      `json:"current_input,omitempty"`
	AgentOutputs   map[string]string           `json:"agent_outputs,omitempty"`
	PendingAgentID string                      `json:"pending_agent_id,omitempty"`
	Agent          *AgentCheckpoint            `json:"agent,omitempty"`
	Agents         map[string]*AgentCheckpoint `json:"agents,omitempty"`
}

type WorkflowCheckpoint struct {
	WorkflowID  string                      `json:"workflow_id"`
	NodePath    []string                    `json:"node_path,omitempty"`
	NextNode    int                         `json:"next_node"`
	Current     string                      `json:"current,omitempty"`
	State       map[string]any              `json:"state,omitempty"`
	LoopIndexes map[string]int              `json:"loop_indexes,omitempty"`
	Branches    map[string]bool             `json:"branches,omitempty"`
	Completed   map[string]string           `json:"completed,omitempty"`
	Agent       *AgentCheckpoint            `json:"agent,omitempty"`
	Agents      map[string]*AgentCheckpoint `json:"agents,omitempty"`
	NodeData    map[string]json.RawMessage  `json:"node_data,omitempty"`
}

type Checkpoint struct {
	Version      int                  `json:"version"`
	TaskID       string               `json:"task_id"`
	TurnID       string               `json:"turn_id"`
	Kind         TurnKind             `json:"kind"`
	Target       string               `json:"target"`
	Status       TurnStatus           `json:"status"`
	LastSequence uint64               `json:"last_sequence"`
	Agent        *AgentCheckpoint     `json:"agent,omitempty"`
	Team         *TeamCheckpoint      `json:"team,omitempty"`
	Workflow     *WorkflowCheckpoint  `json:"workflow,omitempty"`
	Interaction  *interaction.Request `json:"interaction,omitempty"`
}

type RestoreInput struct {
	TaskID       string
	Checkpoint   Checkpoint
	LastSequence uint64
}

// ConversationLease gives one run exclusive access to task-scoped memory.
type ConversationLease struct {
	Memory  memory.Memory
	Release func()
}

// Runtime is implemented by an application-facing Task and consumed by the
// engine through Context. It contains no storage operations.
type Runtime interface {
	TaskID() string
	TurnID() string
	RetryPolicy() RetryConfig
	CircuitPolicy() CircuitConfig
	SummaryPolicy() SummaryConfig
	Summarizer() Summarizer
	Emit(context.Context, Event) error
	AcquireConversation(context.Context, string, memory.Memory) (ConversationLease, error)
	ReportProgress(context.Context, string) error
	RequestInput(context.Context, interaction.Request, Checkpoint) (interaction.Response, error)
	Checkpoint(context.Context, Checkpoint) error
}
