package engine

import (
	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/types"
)

type RunOutput struct {
	Content     string           `json:"content"`
	ToolCalls   []types.ToolCall `json:"tool_calls,omitempty"`
	Messages    []types.Message  `json:"messages,omitempty"`
	Success     bool             `json:"success"`
	Error       string           `json:"error,omitempty"`
	TotalTokens int              `json:"total_tokens"`
	LoopCount   int              `json:"loop_count"`
}

type RunOption func(*runConfig)

type runConfig struct {
	maxLoops    int
	streamFunc  func(chunk string)
	temperature float64
	maxTokens   int
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

type AgentConfig struct {
	Name                string
	SystemPrompt        string
	Model               model.Model
	ToolRegistry        *tool.Registry
	Memory              memory.Memory
	MaxLoops            int
	CheckToolPermission func(toolName string) error
}
