package engine

import (
	"log/slog"

	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/types"
)

type RunOutput struct {
	RunID     string           `json:"run_id"`
	Content   string           `json:"content"`
	ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`
	Messages  []types.Message  `json:"messages,omitempty"`
	Success   bool             `json:"success"`
	Error     string           `json:"error,omitempty"`
	// Err preserves the structured failure for errors.Is/errors.As. Error is
	// retained as the serialized, backward-compatible representation.
	Err         error `json:"-"`
	TotalTokens int   `json:"total_tokens"`
	LoopCount   int   `json:"loop_count"`
}

type RunOption func(*runConfig)

type runConfig struct {
	maxLoops    int
	streamFunc  func(chunk string)
	temperature float64
	maxTokens   int
	model       model.Model
}

func WithMaxLoops(n int) RunOption {
	return func(c *runConfig) { c.maxLoops = n }
}

func WithStream(fn func(chunk string)) RunOption {
	return func(c *runConfig) { c.streamFunc = fn }
}

func WithTemperature(t float64) RunOption {
	return func(c *runConfig) { c.temperature = t }
}

func WithMaxTokens(n int) RunOption {
	return func(c *runConfig) { c.maxTokens = n }
}

// WithModel overrides the Agent model for one Run call without mutating the
// Agent. It is primarily useful for teams that share a model.
func WithModel(m model.Model) RunOption {
	return func(c *runConfig) { c.model = m }
}

type AgentConfig struct {
	Name         string
	SystemPrompt string
	Model        model.Model
	ToolRegistry *tool.Registry
	// RestrictTools makes AllowedTools an exact per-Agent capability set. When
	// false, the Agent can see every Tool in ToolRegistry for compatibility.
	RestrictTools       bool
	AllowedTools        []string
	Memory              memory.Memory
	MaxLoops            int
	CheckToolPermission func(toolName string) error
	Logger              *slog.Logger
	Reliability         *ReliabilityManager
}
