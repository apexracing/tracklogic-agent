package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/mcp"
	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/security"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/tool/builtin"
	"github.com/apexracing/tracklogic-agent/workflow"
)

type Harness struct {
	mu                sync.RWMutex
	config            Config
	model             model.Model
	toolRegistry      *tool.Registry
	memoryCapacity    int
	permissionManager security.PermissionManager
	inputValidator    security.InputValidator
	outputValidator   security.OutputValidator
	sanitizer         security.Sanitizer
	mcpClients        map[string]*mcp.Client
	agents            map[string]*engine.Agent
	teams             map[string]*workflow.Team
	workflows         map[string]*workflow.Workflow
	logger            *slog.Logger
}

func New(cfg Config, options ...Option) (*Harness, error) {
	deps := harnessOptions{
		permissionManager: security.NewPermissionManager(),
		inputValidator: security.NewInputValidatorWithConfig(
			cfg.Security.MaxInputLength,
			cfg.Security.EnableInjectionCheck,
		),
		outputValidator: security.NewOutputValidatorWithMaxLength(cfg.Security.MaxOutputLength),
		sanitizer:       security.NewSanitizer(),
	}
	for _, option := range options {
		if option != nil {
			option(&deps)
		}
	}
	if deps.permissionManager == nil || deps.inputValidator == nil || deps.outputValidator == nil || deps.sanitizer == nil {
		return nil, fmt.Errorf("security dependencies must not be nil")
	}

	h := &Harness{
		config:            cfg,
		toolRegistry:      tool.NewRegistry(),
		permissionManager: deps.permissionManager,
		inputValidator:    deps.inputValidator,
		outputValidator:   deps.outputValidator,
		sanitizer:         deps.sanitizer,
		mcpClients:        make(map[string]*mcp.Client),
		agents:            make(map[string]*engine.Agent),
		teams:             make(map[string]*workflow.Team),
		workflows:         make(map[string]*workflow.Workflow),
		logger:            slog.With("component", "harness"),
	}

	h.setupLogger(cfg.LogLevel)

	runtimeModel, err := cfg.DefaultModel.BuildModel()
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	h.model = runtimeModel

	capacity := cfg.MemoryConfig.Capacity
	if capacity <= 0 {
		capacity = 50
	}
	h.memoryCapacity = capacity

	if cfg.PermissionMode == "strict" {
		h.permissionManager.SetRole(security.RoleUser)
	}

	for _, mcpConfig := range cfg.MCPClients {
		timeout := time.Duration(mcpConfig.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		h.mcpClients[mcpConfig.Name] = mcp.NewClient(mcpConfig.BaseURL, timeout)
	}

	for _, toolName := range cfg.AllowedTools {
		h.registerBuiltinTool(toolName)
	}

	return h, nil
}

func (h *Harness) Config() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.config
}

func (h *Harness) Model() model.Model {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.model
}

func (h *Harness) ToolRegistry() *tool.Registry { return h.toolRegistry }

func (h *Harness) setupLogger(level string) {
	var configuredLevel slog.Level
	switch level {
	case "debug":
		configuredLevel = slog.LevelDebug
	case "warn":
		configuredLevel = slog.LevelWarn
	case "error":
		configuredLevel = slog.LevelError
	default:
		configuredLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: configuredLevel})))
}

func (h *Harness) registerBuiltinTool(name string) {
	var runtimeTool tool.Tool
	switch name {
	case "calculator":
		runtimeTool = builtin.NewCalculator()
	case "read_file":
		runtimeTool = builtin.NewReadFile(".")
	case "write_file":
		runtimeTool = builtin.NewWriteFile(".")
	case "get_current_time":
		runtimeTool = builtin.NewCurrentTime()
	case "list_dir":
		runtimeTool = builtin.NewListDir(".")
	case "http_get":
		runtimeTool = builtin.NewHTTPGet()
	case "json_parse":
		runtimeTool = builtin.NewJSONParse()
	default:
		h.logger.Warn("unknown builtin tool", "name", name)
		return
	}
	if err := h.RegisterTool(runtimeTool); err != nil {
		h.logger.Warn("failed to register tool", "name", name, "error", err)
	}
}

func (h *Harness) RegisterTool(runtimeTool tool.Tool) error {
	return h.toolRegistry.Register(runtimeTool)
}

func (h *Harness) NewAgent(name, systemPrompt string) *engine.Agent {
	runtimeAgent := engine.NewAgent(engine.AgentConfig{
		Name:                name,
		SystemPrompt:        systemPrompt,
		Model:               h.Model(),
		ToolRegistry:        h.toolRegistry,
		Memory:              memory.NewBufferMemory(h.memoryCapacity),
		CheckToolPermission: h.checkToolPermission,
	})
	h.mu.Lock()
	h.agents[name] = runtimeAgent
	h.mu.Unlock()
	return runtimeAgent
}

func (h *Harness) RegisterAgent(runtimeAgent *engine.Agent) error {
	if runtimeAgent == nil || runtimeAgent.Name() == "" {
		return fmt.Errorf("agent and agent name are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	name := runtimeAgent.Name()
	if _, exists := h.agents[name]; exists {
		return fmt.Errorf("agent %q already registered", name)
	}
	h.agents[name] = runtimeAgent
	return nil
}

func (h *Harness) Agent(name string) (*engine.Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	runtimeAgent, ok := h.agents[name]
	return runtimeAgent, ok
}

func (h *Harness) checkToolPermission(toolName string) error {
	permission, required := security.RequiredPermission(toolName)
	if !required {
		return nil
	}
	return h.permissionManager.Check(permission)
}

func (h *Harness) NewTeam(cfg workflow.TeamConfig) *workflow.Team {
	team := workflow.NewTeam(cfg)
	h.mu.Lock()
	h.teams[cfg.Name] = team
	h.mu.Unlock()
	return team
}

func (h *Harness) RegisterTeam(team *workflow.Team) error {
	if team == nil || team.Name == "" {
		return fmt.Errorf("team and team name are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.teams[team.Name]; exists {
		return fmt.Errorf("team %q already registered", team.Name)
	}
	h.teams[team.Name] = team
	return nil
}

func (h *Harness) Team(name string) (*workflow.Team, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	team, ok := h.teams[name]
	return team, ok
}

func (h *Harness) NewWorkflow(cfg workflow.WorkflowConfig) *workflow.Workflow {
	runtimeWorkflow := workflow.NewWorkflow(cfg)
	h.mu.Lock()
	h.workflows[cfg.Name] = runtimeWorkflow
	h.mu.Unlock()
	return runtimeWorkflow
}

func (h *Harness) RegisterWorkflow(runtimeWorkflow *workflow.Workflow) error {
	if runtimeWorkflow == nil || runtimeWorkflow.Name == "" {
		return fmt.Errorf("workflow and workflow name are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.workflows[runtimeWorkflow.Name]; exists {
		return fmt.Errorf("workflow %q already registered", runtimeWorkflow.Name)
	}
	h.workflows[runtimeWorkflow.Name] = runtimeWorkflow
	return nil
}

func (h *Harness) Workflow(name string) (*workflow.Workflow, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	runtimeWorkflow, ok := h.workflows[name]
	return runtimeWorkflow, ok
}

func (h *Harness) AllowPermissions(permissions ...security.Permission) {
	h.permissionManager.Allow(permissions...)
}

func (h *Harness) DenyPermissions(permissions ...security.Permission) {
	h.permissionManager.Deny(permissions...)
}

func (h *Harness) ValidateInput(input string) error { return h.inputValidator.Validate(input) }

func (h *Harness) ValidateOutput(output string) error { return h.outputValidator.Validate(output) }

func (h *Harness) Sanitize(input string) string {
	if h.Config().Security.SanitizePII {
		return h.sanitizer.Sanitize(input)
	}
	return input
}

func (h *Harness) CheckPermission(permission security.Permission) error {
	return h.permissionManager.Check(permission)
}

func (h *Harness) InitMCPClients(ctx context.Context) error {
	for name, client := range h.mcpClients {
		if err := client.Initialize(ctx); err != nil {
			return fmt.Errorf("initialize MCP client %q: %w", name, err)
		}
		definitions, err := client.ToToolDefinitions(ctx)
		if err != nil {
			return fmt.Errorf("get tools from MCP client %q: %w", name, err)
		}
		for _, definition := range definitions {
			adapter := &mcpToolAdapter{clientName: name, client: client, definition: definition}
			if err := h.RegisterTool(adapter); err != nil {
				h.logger.Warn("failed to register MCP tool", "name", adapter.Name(), "error", err)
			}
		}
	}
	return nil
}

func (h *Harness) RunAgent(ctx context.Context, name, input string, options ...engine.RunOption) *engine.RunOutput {
	runtimeAgent, ok := h.Agent(name)
	if !ok {
		return &engine.RunOutput{Success: false, Error: fmt.Sprintf("agent %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &engine.RunOutput{Success: false, Error: err.Error()}
	}
	output := runtimeAgent.Run(ctx, validatedInput, options...)
	if output.Success {
		if err := h.ValidateOutput(output.Content); err != nil {
			return &engine.RunOutput{Success: false, Error: err.Error(), LoopCount: output.LoopCount}
		}
		output.Content = h.Sanitize(output.Content)
	}
	return output
}

func (h *Harness) RunTeam(ctx context.Context, name, input string) *workflow.TeamOutput {
	team, ok := h.Team(name)
	if !ok {
		return &workflow.TeamOutput{Success: false, Error: fmt.Sprintf("team %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &workflow.TeamOutput{Success: false, Error: err.Error()}
	}
	output := team.Run(ctx, validatedInput)
	if output.Success {
		if err := h.ValidateOutput(output.FinalOutput); err != nil {
			return &workflow.TeamOutput{Success: false, Error: err.Error(), AgentOutputs: output.AgentOutputs}
		}
		output.FinalOutput = h.Sanitize(output.FinalOutput)
	}
	return output
}

func (h *Harness) RunWorkflow(ctx context.Context, name, input string) *workflow.WorkflowResult {
	runtimeWorkflow, ok := h.Workflow(name)
	if !ok {
		return &workflow.WorkflowResult{Success: false, Error: fmt.Sprintf("workflow %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &workflow.WorkflowResult{Success: false, Error: err.Error()}
	}
	result := runtimeWorkflow.Run(ctx, validatedInput)
	if result.Success {
		if err := h.ValidateOutput(result.Output); err != nil {
			return &workflow.WorkflowResult{Success: false, Error: err.Error(), State: result.State, StepLogs: result.StepLogs}
		}
		result.Output = h.Sanitize(result.Output)
	}
	return result
}

func (h *Harness) Close() error { return nil }

type mcpToolAdapter struct {
	clientName string
	client     *mcp.Client
	definition model.ToolDefinition
}

func (adapter *mcpToolAdapter) Name() string {
	return "mcp_" + adapter.clientName + "_" + adapter.definition.Name
}

func (adapter *mcpToolAdapter) Description() string {
	if adapter.definition.Description != "" {
		return adapter.definition.Description
	}
	return "MCP tool " + adapter.definition.Name + " from " + adapter.clientName
}

func (adapter *mcpToolAdapter) Definition() model.ToolDefinition {
	definition := adapter.definition
	definition.Name = adapter.Name()
	return definition
}

func (*mcpToolAdapter) Validate(map[string]any) error { return nil }

func (adapter *mcpToolAdapter) Execute(ctx context.Context, arguments map[string]any) (any, error) {
	return adapter.client.CallTool(ctx, adapter.definition.Name, arguments)
}
