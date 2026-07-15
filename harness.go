package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/internal/mcpclient"
	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/orchestrator"
	"github.com/apexracing/tracklogic-agent/security"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/tool/builtin"
)

type Harness struct {
	mu              sync.RWMutex
	Config          Config
	Model           model.Model
	ToolRegistry    *tool.Registry
	Memory          memory.Memory
	memoryCapacity  int
	PermissionMgr   *security.PermissionManager
	InputValidator  *security.InputValidator
	OutputValidator *security.OutputValidator
	Sanitizer       *security.Sanitizer
	mcpClients      map[string]*mcpclient.Client
	Agents          map[string]*engine.Agent
	Teams           map[string]*orchestrator.Team
	Workflows       map[string]*orchestrator.Workflow
	logger          *slog.Logger
}

func New(cfg Config) (*Harness, error) {
	h := &Harness{
		Config:        cfg,
		ToolRegistry:  tool.NewRegistry(),
		PermissionMgr: security.NewPermissionManager(),
		Sanitizer:     security.NewSanitizer(),
		mcpClients:    make(map[string]*mcpclient.Client),
		Agents:        make(map[string]*engine.Agent),
		Teams:         make(map[string]*orchestrator.Team),
		Workflows:     make(map[string]*orchestrator.Workflow),
		logger:        slog.With("component", "harness"),
	}

	h.setupLogger(cfg.LogLevel)

	m, err := cfg.DefaultModel.BuildModel()
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	h.Model = m

	capacity := cfg.MemoryConfig.Capacity
	if capacity <= 0 {
		capacity = 50
	}
	h.memoryCapacity = capacity
	switch cfg.MemoryConfig.Type {
	case "buffer":
		h.Memory = memory.NewBufferMemory(capacity)
	default:
		h.Memory = memory.NewBufferMemory(capacity)
	}

	h.InputValidator = security.NewInputValidatorWithConfig(
		cfg.Security.MaxInputLength,
		cfg.Security.EnableInjectionCheck,
	)
	h.OutputValidator = security.NewOutputValidatorWithMaxLength(cfg.Security.MaxOutputLength)

	if cfg.PermissionMode == "strict" {
		h.PermissionMgr.SetRole(security.RoleUser)
	}

	for _, mcpCfg := range cfg.MCPClients {
		timeout := time.Duration(mcpCfg.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		client := mcpclient.NewClient(mcpCfg.BaseURL, timeout)
		h.mcpClients[mcpCfg.Name] = client
	}

	for _, toolName := range cfg.AllowedTools {
		h.registerBuiltinTool(toolName)
	}

	return h, nil
}

func (h *Harness) setupLogger(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	handler := slog.NewTextHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))
}

func (h *Harness) registerBuiltinTool(name string) {
	var t tool.Tool
	switch name {
	case "calculator":
		t = builtin.NewCalculator()
	case "read_file":
		t = builtin.NewReadFile(".")
	case "write_file":
		t = builtin.NewWriteFile(".")
	case "get_current_time":
		t = builtin.NewCurrentTime()
	case "list_dir":
		t = builtin.NewListDir(".")
	case "http_get":
		t = builtin.NewHTTPGet()
	case "json_parse":
		t = builtin.NewJSONParse()
	default:
		h.logger.Warn("unknown builtin tool", "name", name)
		return
	}
	if err := h.ToolRegistry.Register(t); err != nil {
		h.logger.Warn("failed to register tool", "name", name, "error", err)
	}
}

func (h *Harness) RegisterTool(t tool.Tool) error {
	return h.ToolRegistry.Register(t)
}

func (h *Harness) NewAgent(name, systemPrompt string) *engine.Agent {
	agent := engine.NewAgent(engine.AgentConfig{
		Name:                name,
		SystemPrompt:        systemPrompt,
		Model:               h.Model,
		ToolRegistry:        h.ToolRegistry,
		Memory:              memory.NewBufferMemory(h.memoryCapacity),
		CheckToolPermission: h.checkToolPermission,
	})
	h.mu.Lock()
	h.Agents[name] = agent
	h.mu.Unlock()
	return agent
}

func (h *Harness) checkToolPermission(toolName string) error {
	perm, ok := security.RequiredPermission(toolName)
	if !ok {
		return nil
	}
	return h.PermissionMgr.Check(perm)
}

func (h *Harness) NewTeam(cfg orchestrator.TeamConfig) *orchestrator.Team {
	team := orchestrator.NewTeam(cfg)
	h.mu.Lock()
	h.Teams[cfg.Name] = team
	h.mu.Unlock()
	return team
}

func (h *Harness) NewWorkflow(cfg orchestrator.WorkflowConfig) *orchestrator.Workflow {
	wf := orchestrator.NewWorkflow(cfg)
	h.mu.Lock()
	h.Workflows[cfg.Name] = wf
	h.mu.Unlock()
	return wf
}

func (h *Harness) RegisterTeam(team *orchestrator.Team) error {
	if team == nil || team.Name == "" {
		return fmt.Errorf("team and team name are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.Teams[team.Name]; exists {
		return fmt.Errorf("team %q already registered", team.Name)
	}
	h.Teams[team.Name] = team
	return nil
}

func (h *Harness) RegisterWorkflow(workflow *orchestrator.Workflow) error {
	if workflow == nil || workflow.Name == "" {
		return fmt.Errorf("workflow and workflow name are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.Workflows[workflow.Name]; exists {
		return fmt.Errorf("workflow %q already registered", workflow.Name)
	}
	h.Workflows[workflow.Name] = workflow
	return nil
}

func (h *Harness) AllowPermissions(permissions ...security.Permission) {
	h.PermissionMgr.Allow(permissions...)
}

func (h *Harness) DenyPermissions(permissions ...security.Permission) {
	h.PermissionMgr.Deny(permissions...)
}

func (h *Harness) ValidateInput(input string) error {
	return h.InputValidator.Validate(input)
}

func (h *Harness) ValidateOutput(output string) error {
	return h.OutputValidator.Validate(output)
}

func (h *Harness) Sanitize(input string) string {
	if h.Config.Security.SanitizePII {
		return h.Sanitizer.Sanitize(input)
	}
	return input
}

func (h *Harness) CheckPermission(perm security.Permission) error {
	return h.PermissionMgr.Check(perm)
}

func (h *Harness) InitMCPClients(ctx context.Context) error {
	for name, client := range h.mcpClients {
		if err := client.Initialize(ctx); err != nil {
			return fmt.Errorf("initialize MCP client %q: %w", name, err)
		}
		tools, err := client.ToToolDefinitions(ctx)
		if err != nil {
			return fmt.Errorf("get tools from MCP client %q: %w", name, err)
		}
		for _, def := range tools {
			adapter := &mcpToolAdapter{
				clientName: name,
				client:     client,
				def:        def,
			}
			if err := h.ToolRegistry.Register(adapter); err != nil {
				h.logger.Warn("failed to register MCP tool", "name", adapter.Name(), "error", err)
			}
		}
	}
	return nil
}

func (h *Harness) RunAgent(ctx context.Context, name, input string, opts ...engine.RunOption) *engine.RunOutput {
	h.mu.RLock()
	agent, ok := h.Agents[name]
	h.mu.RUnlock()
	if !ok {
		return &engine.RunOutput{Success: false, Error: fmt.Sprintf("agent %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &engine.RunOutput{Success: false, Error: err.Error()}
	}
	output := agent.Run(ctx, validatedInput, opts...)
	if output.Success {
		if err := h.ValidateOutput(output.Content); err != nil {
			return &engine.RunOutput{Success: false, Error: err.Error(), LoopCount: output.LoopCount}
		}
		if h.Config.Security.SanitizePII {
			output.Content = h.Sanitizer.Sanitize(output.Content)
		}
	}
	return output
}

func (h *Harness) RunTeam(ctx context.Context, name, input string) *orchestrator.TeamOutput {
	h.mu.RLock()
	team, ok := h.Teams[name]
	h.mu.RUnlock()
	if !ok {
		return &orchestrator.TeamOutput{Success: false, Error: fmt.Sprintf("team %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &orchestrator.TeamOutput{Success: false, Error: err.Error()}
	}
	output := team.Run(ctx, validatedInput)
	if output.Success && h.Config.Security.SanitizePII {
		output.FinalOutput = h.Sanitizer.Sanitize(output.FinalOutput)
	}
	return output
}

func (h *Harness) RunWorkflow(ctx context.Context, name, input string) *orchestrator.WorkflowResult {
	h.mu.RLock()
	wf, ok := h.Workflows[name]
	h.mu.RUnlock()
	if !ok {
		return &orchestrator.WorkflowResult{Success: false, Error: fmt.Sprintf("workflow %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &orchestrator.WorkflowResult{Success: false, Error: err.Error()}
	}
	result := wf.Run(ctx, validatedInput)
	if result.Success {
		if err := h.ValidateOutput(result.Output); err != nil {
			return &orchestrator.WorkflowResult{Success: false, Error: err.Error(), State: result.State, StepLogs: result.StepLogs}
		}
		if h.Config.Security.SanitizePII {
			result.Output = h.Sanitizer.Sanitize(result.Output)
		}
	}
	return result
}

func (h *Harness) Close() error {
	return nil
}

type mcpToolAdapter struct {
	clientName string
	client     *mcpclient.Client
	def        model.ToolDefinition
}

func (m *mcpToolAdapter) Name() string { return "mcp_" + m.clientName + "_" + m.def.Name }

func (m *mcpToolAdapter) Description() string {
	if m.def.Description != "" {
		return m.def.Description
	}
	return "MCP tool " + m.def.Name + " from " + m.clientName
}

func (m *mcpToolAdapter) Definition() model.ToolDefinition {
	def := m.def
	def.Name = m.Name()
	return def
}

func (m *mcpToolAdapter) Validate(args map[string]any) error { return nil }

func (m *mcpToolAdapter) Execute(ctx context.Context, args map[string]any) (any, error) {
	return m.client.CallTool(ctx, m.def.Name, args)
}
