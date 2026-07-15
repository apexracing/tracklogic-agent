package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/types"
)

type Agent struct {
	mu                  sync.RWMutex
	runGate             chan struct{}
	name                string
	systemPrompt        string
	model               model.Model
	toolRegistry        *tool.Registry
	allowedTools        map[string]struct{}
	memory              memory.Memory
	maxLoops            int
	checkToolPermission func(toolName string) error
	logger              *slog.Logger
}

func NewAgent(cfg AgentConfig) *Agent {
	if cfg.MaxLoops <= 0 {
		cfg.MaxLoops = 10
	}
	if isNilRuntimeValue(cfg.Memory) {
		cfg.Memory = memory.NewBufferMemory(50)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var allowedTools map[string]struct{}
	if cfg.RestrictTools {
		allowedTools = make(map[string]struct{}, len(cfg.AllowedTools))
		for _, name := range cfg.AllowedTools {
			allowedTools[name] = struct{}{}
		}
	}
	return &Agent{
		runGate:             make(chan struct{}, 1),
		name:                cfg.Name,
		systemPrompt:        cfg.SystemPrompt,
		model:               cfg.Model,
		toolRegistry:        cfg.ToolRegistry,
		allowedTools:        allowedTools,
		memory:              cfg.Memory,
		maxLoops:            cfg.MaxLoops,
		checkToolPermission: cfg.CheckToolPermission,
		logger:              logger.With("component", "agent", "name", cfg.Name),
	}
}

func (a *Agent) Name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.name
}

func (a *Agent) SystemPrompt() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.systemPrompt
}

func (a *Agent) Model() model.Model {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.model
}

func (a *Agent) SetModel(m model.Model) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = m
}

func (a *Agent) SetSystemPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.systemPrompt = prompt
}

func (a *Agent) AddSystemMessage(content string) {
	a.mu.RLock()
	mem := a.memory
	a.mu.RUnlock()
	mem.Add(types.Message{Role: types.RoleSystem, Content: content, CreatedAt: time.Now()})
}

func (a *Agent) Run(ctx context.Context, input string, opts ...RunOption) (output *RunOutput) {
	ctx, runID := ensureRunContext(ctx, a.Name())
	defer func() {
		if output != nil {
			output.RunID = runID
		}
	}()

	if err := a.acquireRun(ctx); err != nil {
		runErr := types.WrapError(types.ErrRunCancelled, "cancelled while waiting for agent", err)
		return &RunOutput{
			Success: false,
			Error:   runErr.Error(),
			Err:     runErr,
		}
	}
	defer a.releaseRun()

	cfg := &runConfig{
		maxLoops:    a.defaultMaxLoops(),
		temperature: 0.7,
		maxTokens:   4096,
		model:       a.Model(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if err := validateRunConfig(cfg); err != nil {
		return &RunOutput{Success: false, Error: err.Error(), Err: err}
	}

	start := time.Now()
	a.logger.Info("agent run started", "input", truncate(input, 100))

	a.mu.RLock()
	mem := a.memory
	a.mu.RUnlock()
	mem.Add(types.Message{Role: types.RoleUser, Content: input, CreatedAt: time.Now()})

	var lastContent string
	var allToolCalls []types.ToolCall
	totalTokens := 0
	loopCount := 0

	for loopCount < cfg.maxLoops {
		loopCount++
		select {
		case <-ctx.Done():
			a.logger.Warn("run cancelled", "loop", loopCount)
			runErr := types.WrapError(types.ErrRunCancelled, "context cancelled", ctx.Err())
			return &RunOutput{
				Success: false, Error: runErr.Error(), Err: runErr,
				ToolCalls: allToolCalls, Messages: mem.Snapshot(),
				TotalTokens: totalTokens, LoopCount: loopCount,
			}
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

		resp, err := invokeModel(ctx, cfg.model, req, cfg.streamFunc)
		if err != nil {
			err = normalizeModelError(err)
			a.logger.Error("model invoke failed", "error", err)
			return &RunOutput{
				Success: false, Error: err.Error(), Err: err,
				ToolCalls: allToolCalls, Messages: mem.Snapshot(),
				TotalTokens: totalTokens, LoopCount: loopCount,
			}
		}
		if resp == nil {
			err := types.NewError(types.ErrAPIError, "model returned a nil response")
			a.logger.Error("model invoke failed", "error", err)
			return &RunOutput{
				Success: false, Error: err.Error(), Err: err,
				ToolCalls: allToolCalls, Messages: mem.Snapshot(),
				TotalTokens: totalTokens, LoopCount: loopCount,
			}
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
			allToolCalls = append(allToolCalls, resp.ToolCalls...)
			assistantMsg.ToolCalls = resp.ToolCalls
			mem.Add(assistantMsg)

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
				mem.Add(toolMsg)
			}

			continue
		}

		assistantMsg.Content = resp.Content
		mem.Add(assistantMsg)
		lastContent = resp.Content

		a.logger.Info("agent run completed",
			"loops", loopCount,
			"tokens", totalTokens,
			"duration", time.Since(start),
		)

		return &RunOutput{
			Content:     lastContent,
			ToolCalls:   allToolCalls,
			Messages:    mem.Snapshot(),
			Success:     true,
			TotalTokens: totalTokens,
			LoopCount:   loopCount,
		}
	}

	a.logger.Warn("max loops exceeded")
	runErr := types.NewError(types.ErrMaxLoopsExceeded, "max loops exceeded")
	return &RunOutput{
		Content:     lastContent,
		ToolCalls:   allToolCalls,
		Messages:    mem.Snapshot(),
		Success:     false,
		Error:       runErr.Error(),
		Err:         runErr,
		TotalTokens: totalTokens,
		LoopCount:   loopCount,
	}
}

func normalizeModelError(err error) error {
	if err == nil {
		return nil
	}
	var harnessErr *types.HarnessError
	if errors.As(err, &harnessErr) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return types.WrapError(types.ErrRunCancelled, "model request cancelled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return types.WrapError(types.ErrModelTimeout, "model request timed out", err)
	default:
		return err
	}
}

func (a *Agent) acquireRun(ctx context.Context) error {
	select {
	case a.runGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) releaseRun() {
	<-a.runGate
}

func validateRunConfig(cfg *runConfig) error {
	if isNilRuntimeValue(cfg.model) {
		return types.NewError(types.ErrInvalidConfig, "agent model is required")
	}
	if cfg.maxLoops <= 0 {
		return types.NewError(types.ErrInvalidConfig, "max loops must be greater than zero")
	}
	if cfg.maxTokens <= 0 {
		return types.NewError(types.ErrInvalidConfig, "max tokens must be greater than zero")
	}
	if cfg.temperature < 0 || cfg.temperature > 2 {
		return types.NewError(types.ErrInvalidConfig, "temperature must be between 0 and 2")
	}
	return nil
}

// invokeModel calls Invoke or InvokeStream depending on whether a stream callback is set.
func invokeModel(ctx context.Context, runModel model.Model, req *model.InvokeRequest, onChunk func(string)) (*model.InvokeResponse, error) {
	if isNilRuntimeValue(runModel) {
		return nil, types.NewError(types.ErrInvalidConfig, "agent model is required")
	}
	if onChunk == nil {
		return runModel.Invoke(ctx, req)
	}
	ch, err := runModel.InvokeStream(ctx, req)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, types.NewError(types.ErrAPIError, "model returned a nil stream")
	}
	return consumeStream(ctx, ch, onChunk)
}

func isNilRuntimeValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (a *Agent) buildMessages() []types.Message {
	a.mu.RLock()
	mem := a.memory
	systemPrompt := a.systemPrompt
	a.mu.RUnlock()
	msgs := mem.Snapshot()
	if systemPrompt != "" {
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
				Content:   systemPrompt,
				CreatedAt: time.Now(),
			}
			return append([]types.Message{systemMsg}, msgs...)
		}
	}
	return msgs
}

func (a *Agent) buildToolDefinitions() []model.ToolDefinition {
	a.mu.RLock()
	registry := a.toolRegistry
	allowedTools := a.allowedTools
	a.mu.RUnlock()
	if registry == nil {
		return nil
	}
	tools := registry.List()
	defs := make([]model.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if allowedTools != nil {
			if _, allowed := allowedTools[t.Name()]; !allowed {
				continue
			}
		}
		defs = append(defs, t.Definition())
	}
	return defs
}

func (a *Agent) executeToolCall(ctx context.Context, tc types.ToolCall) (string, error) {
	a.mu.RLock()
	registry := a.toolRegistry
	allowedTools := a.allowedTools
	checkPermission := a.checkToolPermission
	a.mu.RUnlock()
	if registry == nil {
		return "", types.NewError(types.ErrToolError, "no tool registry configured")
	}
	if allowedTools != nil {
		if _, allowed := allowedTools[tc.Function.Name]; !allowed {
			return "", types.NewError(types.ErrSecurityViolation, fmt.Sprintf("tool %q is outside the agent capability scope", tc.Function.Name))
		}
	}

	t, ok := registry.Get(tc.Function.Name)
	if !ok {
		return "", types.NewError(types.ErrToolError, fmt.Sprintf("tool %q not found", tc.Function.Name))
	}

	if checkPermission != nil {
		if err := checkPermission(tc.Function.Name); err != nil {
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

func (a *Agent) defaultMaxLoops() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.maxLoops
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
