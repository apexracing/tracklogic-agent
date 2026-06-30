package harness

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go-harness-tutorial/internal/engine"
	"go-harness-tutorial/internal/mcpclient"
	"go-harness-tutorial/internal/memory"
	"go-harness-tutorial/internal/model"
	"go-harness-tutorial/internal/orchestrator"
	"go-harness-tutorial/internal/security"
	"go-harness-tutorial/internal/tool"
	"go-harness-tutorial/internal/tool/builtin"
	"go-harness-tutorial/pkg/types"
)

type Harness struct {
	Config           Config
	Model            model.Model
	ToolRegistry     *tool.Registry
	Memory           memory.Memory
	PermissionMgr    *security.PermissionManager
	InputValidator   *security.InputValidator
	OutputValidator  *security.OutputValidator
	Sanitizer        *security.Sanitizer
	MCPClients       map[string]*mcpclient.Client
	Agents           map[string]*engine.Agent
	Teams            map[string]*orchestrator.Team
	Workflows        map[string]*orchestrator.Workflow
	logger           *slog.Logger
}

func New(cfg Config) (*Harness, error) {
	h := &Harness{
		Config:          cfg,
		ToolRegistry:    tool.NewRegistry(),
		PermissionMgr:   security.NewPermissionManager(),
		InputValidator:  security.NewInputValidator(),
		OutputValidator: security.NewOutputValidator(),
		Sanitizer:       security.NewSanitizer(),
		MCPClients:      make(map[string]*mcpclient.Client),
		Agents:          make(map[string]*engine.Agent),
		Teams:           make(map[string]*orchestrator.Team),
		Workflows:       make(map[string]*orchestrator.Workflow),
		logger:          slog.With("component", "harness"),
	}

	h.setupLogger(cfg.LogLevel)

	model, err := cfg.DefaultModel.BuildModel()
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	h.Model = model

	switch cfg.MemoryConfig.Type {
	case "buffer":
		h.Memory = memory.NewBufferMemory(cfg.MemoryConfig.Capacity)
	default:
		h.Memory = memory.NewBufferMemory(50)
	}

	if cfg.PermissionMode == "strict" {
		h.PermissionMgr.SetRole(security.RoleUser)
	}

	if cfg.Security.MaxInputLength > 0 {
		h.InputValidator = security.NewInputValidator()
	}

	for _, mcpCfg := range cfg.MCPClients {
		timeout := time.Duration(mcpCfg.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		client := mcpclient.NewClient(mcpCfg.BaseURL, timeout)
		h.MCPClients[mcpCfg.Name] = client
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
		Name:         name,
		SystemPrompt: systemPrompt,
		Model:        h.Model,
		ToolRegistry: h.ToolRegistry,
		Memory:       h.Memory,
	})
	h.Agents[name] = agent
	return agent
}

func (h *Harness) NewTeam(cfg orchestrator.TeamConfig) *orchestrator.Team {
	team := orchestrator.NewTeam(cfg)
	h.Teams[cfg.Name] = team
	return team
}

func (h *Harness) NewWorkflow(cfg orchestrator.WorkflowConfig) *orchestrator.Workflow {
	wf := orchestrator.NewWorkflow(cfg)
	h.Workflows[cfg.Name] = wf
	return wf
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
	for name, client := range h.MCPClients {
		if err := client.Initialize(ctx); err != nil {
			return fmt.Errorf("initialize MCP client %q: %w", name, err)
		}
		tools, err := client.ToToolDefinitions(ctx)
		if err != nil {
			return fmt.Errorf("get tools from MCP client %q: %w", name, err)
		}
		mcpTool := &mcpToolAdapter{
			name:   name,
			client: client,
			defs:   tools,
		}
		if err := h.ToolRegistry.Register(mcpTool); err != nil {
			h.logger.Warn("failed to register MCP tool", "name", name, "error", err)
		}
	}
	return nil
}

func (h *Harness) RunAgent(ctx context.Context, name, input string, opts ...engine.RunOption) *engine.RunOutput {
	agent, ok := h.Agents[name]
	if !ok {
		return &engine.RunOutput{Success: false, Error: fmt.Sprintf("agent %q not found", name)}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		return &engine.RunOutput{Success: false, Error: err.Error()}
	}
	output := agent.Run(ctx, validatedInput, opts...)
	if h.Config.Security.SanitizePII && output.Success {
		output.Content = h.Sanitizer.Sanitize(output.Content)
	}
	return output
}

func (h *Harness) RunTeam(ctx context.Context, name, input string) *orchestrator.TeamOutput {
	team, ok := h.Teams[name]
	if !ok {
		return &orchestrator.TeamOutput{Success: false, Error: fmt.Sprintf("team %q not found", name)}
	}
	return team.Run(ctx, h.Sanitize(input))
}

func (h *Harness) RunWorkflow(ctx context.Context, name, input string) *orchestrator.WorkflowResult {
	wf, ok := h.Workflows[name]
	if !ok {
		return &orchestrator.WorkflowResult{Success: false, Error: fmt.Sprintf("workflow %q not found", name)}
	}
	return wf.Run(ctx, h.Sanitize(input))
}

func (h *Harness) Close() error {
	for name, client := range h.MCPClients {
		_ = name
		_ = client
	}
	return nil
}

type mcpToolAdapter struct {
	name   string
	client *mcpclient.Client
	defs   []model.ToolDefinition
}

func (m *mcpToolAdapter) Name() string { return "mcp_" + m.name }
func (m *mcpToolAdapter) Description() string {
	if len(m.defs) > 0 {
		return "MCP tools from " + m.name
	}
	return "MCP client: " + m.name
}
func (m *mcpToolAdapter) Definition() model.ToolDefinition {
	if len(m.defs) > 0 {
		return m.defs[0]
	}
	return model.ToolDefinition{Name: m.Name(), Description: m.Description()}
}
func (m *mcpToolAdapter) Validate(args map[string]any) error { return nil }
func (m *mcpToolAdapter) Execute(ctx context.Context, args map[string]any) (any, error) {
	return m.client.CallTool(ctx, args["tool"].(string), args)
}
