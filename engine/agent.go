package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/types"
)

type Agent struct {
	mu                  sync.RWMutex
	ID                  string
	Name                string
	SystemPrompt        string
	Model               model.Model
	ToolRegistry        *tool.Registry
	Memory              memory.Memory
	MaxLoops            int
	CheckToolPermission func(toolName string) error
	logger              *slog.Logger
}

func NewAgent(cfg AgentConfig) *Agent {
	if cfg.MaxLoops <= 0 {
		cfg.MaxLoops = 10
	}
	if cfg.Memory == nil {
		cfg.Memory = memory.NewBufferMemory(50)
	}
	return &Agent{
		Name:                cfg.Name,
		SystemPrompt:        cfg.SystemPrompt,
		Model:               cfg.Model,
		ToolRegistry:        cfg.ToolRegistry,
		Memory:              cfg.Memory,
		MaxLoops:            cfg.MaxLoops,
		CheckToolPermission: cfg.CheckToolPermission,
		logger:              slog.With("component", "agent", "name", cfg.Name),
	}
}

func (a *Agent) SetSystemPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SystemPrompt = prompt
}

func (a *Agent) AddSystemMessage(content string) {
	a.Memory.Add(types.Message{Role: types.RoleSystem, Content: content, CreatedAt: time.Now()})
}

func (a *Agent) Run(ctx context.Context, input string, opts ...RunOption) *RunOutput {
	cfg := &runConfig{
		maxLoops:    a.MaxLoops,
		temperature: 0.7,
		maxTokens:   4096,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	start := time.Now()
	a.logger.Info("agent run started", "input", truncate(input, 100))

	a.Memory.Add(types.Message{Role: types.RoleUser, Content: input, CreatedAt: time.Now()})

	var lastContent string
	totalTokens := 0
	loopCount := 0

	for loopCount < cfg.maxLoops {
		loopCount++
		select {
		case <-ctx.Done():
			a.logger.Warn("run cancelled", "loop", loopCount)
			return &RunOutput{Success: false, Error: types.NewError(types.ErrRunCancelled, "context cancelled").Error(), LoopCount: loopCount}
		default:
		}

		msgs := a.buildMessages()
		toolDefs := a.buildToolDefinitions()

		req := &model.InvokeRequest{
			Messages:    msgs,
			Tools:       toolDefs,
			Temperature: cfg.temperature,
			MaxTokens:   cfg.maxTokens,
			Stream:      cfg.streamFunc != nil,
		}

		resp, err := a.invokeModel(ctx, req, cfg.streamFunc)
		if err != nil {
			a.logger.Error("model invoke failed", "error", err)
			return &RunOutput{Success: false, Error: err.Error(), LoopCount: loopCount}
		}

		if resp.Usage != nil {
			totalTokens += resp.Usage.TotalTokens
		}

		assistantMsg := types.Message{
			Role:      types.RoleAssistant,
			Content:   resp.Content,
			CreatedAt: time.Now(),
		}

		if len(resp.ToolCalls) > 0 {
			assistantMsg.ToolCalls = resp.ToolCalls
			a.Memory.Add(assistantMsg)

			for _, tc := range resp.ToolCalls {
				a.logger.Info("executing tool", "tool", tc.Function.Name)

				result, err := a.executeToolCall(ctx, tc)
				resultStr := result
				if err != nil {
					resultStr = fmt.Sprintf("error: %v", err)
					a.logger.Error("tool execution failed", "tool", tc.Function.Name, "error", err)
				}

				toolMsg := types.Message{
					Role:       types.RoleTool,
					Content:    resultStr,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					CreatedAt:  time.Now(),
				}
				a.Memory.Add(toolMsg)
			}

			continue
		}

		assistantMsg.Content = resp.Content
		a.Memory.Add(assistantMsg)
		lastContent = resp.Content

		a.logger.Info("agent run completed",
			"loops", loopCount,
			"tokens", totalTokens,
			"duration", time.Since(start),
		)

		return &RunOutput{
			Content:     lastContent,
			Messages:    a.Memory.Snapshot(),
			Success:     true,
			TotalTokens: totalTokens,
			LoopCount:   loopCount,
		}
	}

	a.logger.Warn("max loops exceeded")
	return &RunOutput{
		Content:   lastContent,
		Success:   false,
		Error:     types.NewError(types.ErrMaxLoopsExceeded, "max loops exceeded").Error(),
		LoopCount: loopCount,
	}
}

// invokeModel calls Invoke or InvokeStream depending on whether a stream callback is set.
func (a *Agent) invokeModel(ctx context.Context, req *model.InvokeRequest, onChunk func(string)) (*model.InvokeResponse, error) {
	if onChunk == nil {
		return a.Model.Invoke(ctx, req)
	}
	ch, err := a.Model.InvokeStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return consumeStream(ctx, ch, onChunk)
}

func (a *Agent) buildMessages() []types.Message {
	msgs := a.Memory.Snapshot()
	if a.SystemPrompt != "" {
		hasSystem := false
		for _, m := range msgs {
			if m.Role == types.RoleSystem {
				hasSystem = true
				break
			}
		}
		if !hasSystem {
			systemMsg := types.Message{
				Role:      types.RoleSystem,
				Content:   a.SystemPrompt,
				CreatedAt: time.Now(),
			}
			return append([]types.Message{systemMsg}, msgs...)
		}
	}
	return msgs
}

func (a *Agent) buildToolDefinitions() []model.ToolDefinition {
	if a.ToolRegistry == nil {
		return nil
	}
	tools := a.ToolRegistry.List()
	defs := make([]model.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

func (a *Agent) executeToolCall(ctx context.Context, tc types.ToolCall) (string, error) {
	if a.ToolRegistry == nil {
		return "", types.NewError(types.ErrToolError, "no tool registry configured")
	}

	t, ok := a.ToolRegistry.Get(tc.Function.Name)
	if !ok {
		return "", types.NewError(types.ErrToolError, fmt.Sprintf("tool %q not found", tc.Function.Name))
	}

	if a.CheckToolPermission != nil {
		if err := a.CheckToolPermission(tc.Function.Name); err != nil {
			return "", types.WrapError(types.ErrSecurityViolation, "tool permission denied", err)
		}
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return "", types.WrapError(types.ErrInvalidInput, "failed to parse tool arguments", err)
	}

	if err := t.Validate(args); err != nil {
		return "", types.WrapError(types.ErrInvalidInput, "tool argument validation failed", err)
	}

	result, err := t.Execute(ctx, args)
	if err != nil {
		return "", err
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return "", types.WrapError(types.ErrToolError, "failed to marshal tool result", err)
	}

	return string(resultJSON), nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
