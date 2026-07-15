package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/mcp"
	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/security"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/tool/builtin"
	"github.com/apexracing/tracklogic-agent/types"
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
	baseLogger        *slog.Logger
	logger            *slog.Logger
	reliability       *engine.ReliabilityManager
}

func New(cfg Config, options ...Option) (*Harness, error) {
	cfg = cloneConfig(cfg)
	ResolveModelDefaults(&cfg.DefaultModel)
	if cfg.MemoryConfig.Type == "" {
		cfg.MemoryConfig.Type = "buffer"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
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
	if isNilDependency(deps.permissionManager) || isNilDependency(deps.inputValidator) ||
		isNilDependency(deps.outputValidator) || isNilDependency(deps.sanitizer) {
		return nil, fmt.Errorf("security dependencies must not be nil")
	}
	if deps.model != nil && isNilDependency(deps.model) {
		return nil, fmt.Errorf("injected model must not be nil")
	}

	baseLogger := deps.logger
	if baseLogger == nil {
		baseLogger = newConfiguredLogger(cfg.LogLevel)
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
		baseLogger:        baseLogger,
		logger:            baseLogger.With("component", "harness"),
		reliability:       engine.NewReliabilityManager(),
	}

	runtimeModel := deps.model
	if runtimeModel == nil {
		var err error
		runtimeModel, err = cfg.DefaultModel.buildModel(baseLogger)
		if err != nil {
			return nil, fmt.Errorf("build model: %w", err)
		}
	}
	h.model = runtimeModel

	capacity := cfg.MemoryConfig.Capacity
	if capacity <= 0 {
		capacity = 50
	}
	h.memoryCapacity = capacity

	// Strict mode starts with no grants; callers must opt in with
	// AllowPermissions. Permissive mode grants the built-in permission set so
	// local examples can use configured tools without a second policy step.
	if cfg.PermissionMode != "strict" {
		h.permissionManager.SetRole(security.RoleAdmin)
	}

	for _, mcpConfig := range cfg.MCPClients {
		timeout := time.Duration(mcpConfig.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		clientOptions := []mcp.ClientOption{
			mcp.WithLogger(h.baseLogger),
		}
		if mcpConfig.ProtocolVersion != "" {
			clientOptions = append(clientOptions, mcp.WithProtocolVersion(mcpConfig.ProtocolVersion))
		}
		h.mcpClients[mcpConfig.Name] = mcp.NewClient(mcpConfig.BaseURL, timeout, clientOptions...)
	}
	for name, client := range deps.mcpClients {
		if err := validateMCPClientName(name); err != nil {
			return nil, fmt.Errorf("invalid injected MCP client name: %w", err)
		}
		if client == nil {
			return nil, fmt.Errorf("injected MCP client %q is nil", name)
		}
		h.mcpClients[name] = client
	}

	for _, toolName := range cfg.AllowedTools {
		h.registerBuiltinTool(toolName)
	}

	return h, nil
}

func isNilDependency(value any) bool {
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

func (h *Harness) Config() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return cloneConfig(h.config)
}

func cloneConfig(cfg Config) Config {
	cfg.AllowedTools = append([]string(nil), cfg.AllowedTools...)
	cfg.MCPClients = append([]MCPClientConfig(nil), cfg.MCPClients...)
	return cfg
}

func (h *Harness) Model() model.Model {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.model
}

func (h *Harness) ToolRegistry() *tool.Registry { return h.toolRegistry }

func newConfiguredLogger(level string) *slog.Logger {
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
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: configuredLevel}))
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

// NewAgent is the compatibility convenience constructor. It returns nil and
// logs when validation or registration fails. Production code should prefer
// CreateAgent and handle its error.
func (h *Harness) NewAgent(name, systemPrompt string) *engine.Agent {
	runtimeAgent, err := h.CreateAgent(name, systemPrompt)
	if err != nil {
		h.logger.Error("failed to create agent", "name", name, "error", err)
		return nil
	}
	return runtimeAgent
}

// CreateAgent constructs and atomically registers an Agent. Production code
// should prefer this method over NewAgent so invalid or duplicate names cannot
// fail silently.
func (h *Harness) CreateAgent(name, systemPrompt string) (*engine.Agent, error) {
	return h.createAgent(name, systemPrompt, false, nil)
}

// CreateAgentWithTools constructs and atomically registers an Agent whose
// visible and executable Tool set is restricted to the supplied names. An
// empty list creates an Agent with no tools.
func (h *Harness) CreateAgentWithTools(name, systemPrompt string, toolNames ...string) (*engine.Agent, error) {
	return h.createAgent(name, systemPrompt, true, toolNames)
}

func (h *Harness) createAgent(name, systemPrompt string, restrictTools bool, toolNames []string) (*engine.Agent, error) {
	if name == "" || name != strings.TrimSpace(name) {
		return nil, fmt.Errorf("agent name must be non-empty and must not have surrounding whitespace")
	}
	if restrictTools {
		seen := make(map[string]struct{}, len(toolNames))
		for _, toolName := range toolNames {
			if toolName == "" || toolName != strings.TrimSpace(toolName) {
				return nil, fmt.Errorf("agent tool name must be non-empty and must not have surrounding whitespace")
			}
			if _, duplicate := seen[toolName]; duplicate {
				return nil, fmt.Errorf("agent %q has duplicate tool %q", name, toolName)
			}
			if _, exists := h.toolRegistry.Get(toolName); !exists {
				return nil, fmt.Errorf("agent %q tool %q is not registered", name, toolName)
			}
			seen[toolName] = struct{}{}
		}
	}
	runtimeAgent := engine.NewAgent(engine.AgentConfig{
		Name:                name,
		SystemPrompt:        systemPrompt,
		Model:               h.Model(),
		ToolRegistry:        h.toolRegistry,
		RestrictTools:       restrictTools,
		AllowedTools:        append([]string(nil), toolNames...),
		Memory:              memory.NewBufferMemory(h.memoryCapacity),
		CheckToolPermission: h.checkToolPermission,
		Logger:              h.baseLogger,
		Reliability:         h.reliability,
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.agents[name]; exists {
		return nil, fmt.Errorf("agent %q already registered", name)
	}
	h.agents[name] = runtimeAgent
	return runtimeAgent, nil
}

func (h *Harness) RegisterAgent(runtimeAgent *engine.Agent) error {
	if runtimeAgent == nil || runtimeAgent.Name() == "" || runtimeAgent.Name() != strings.TrimSpace(runtimeAgent.Name()) {
		return fmt.Errorf("agent and agent name are required")
	}
	if isNilDependency(runtimeAgent.Model()) {
		return fmt.Errorf("agent %q model is required", runtimeAgent.Name())
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

// NewTeam is the compatibility convenience constructor. Production code
// should prefer CreateTeam and handle its error.
func (h *Harness) NewTeam(cfg workflow.TeamConfig) *workflow.Team {
	team, err := h.CreateTeam(cfg)
	if err != nil {
		h.logger.Error("failed to create team", "name", cfg.Name, "error", err)
		return nil
	}
	return team
}

// CreateTeam validates, constructs, and atomically registers a Team.
func (h *Harness) CreateTeam(cfg workflow.TeamConfig) (*workflow.Team, error) {
	if cfg.Logger == nil {
		cfg.Logger = h.baseLogger
	}
	team := workflow.NewTeam(cfg)
	if err := team.Validate(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.teams[cfg.Name]; exists {
		return nil, fmt.Errorf("team %q already registered", cfg.Name)
	}
	h.teams[cfg.Name] = team
	return team, nil
}

func (h *Harness) RegisterTeam(team *workflow.Team) error {
	if err := team.Validate(); err != nil {
		return err
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

// NewWorkflow is the compatibility convenience constructor. Production code
// should prefer CreateWorkflow and handle its error.
func (h *Harness) NewWorkflow(cfg workflow.WorkflowConfig) *workflow.Workflow {
	runtimeWorkflow, err := h.CreateWorkflow(cfg)
	if err != nil {
		h.logger.Error("failed to create workflow", "name", cfg.Name, "error", err)
		return nil
	}
	return runtimeWorkflow
}

// CreateWorkflow validates, constructs, and atomically registers a Workflow.
func (h *Harness) CreateWorkflow(cfg workflow.WorkflowConfig) (*workflow.Workflow, error) {
	if cfg.Logger == nil {
		cfg.Logger = h.baseLogger
	}
	runtimeWorkflow := workflow.NewWorkflow(cfg)
	if err := runtimeWorkflow.Validate(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.workflows[cfg.Name]; exists {
		return nil, fmt.Errorf("workflow %q already registered", cfg.Name)
	}
	h.workflows[cfg.Name] = runtimeWorkflow
	return runtimeWorkflow, nil
}

func (h *Harness) RegisterWorkflow(runtimeWorkflow *workflow.Workflow) error {
	if err := runtimeWorkflow.Validate(); err != nil {
		return err
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
	names := make([]string, 0, len(h.mcpClients))
	for name := range h.mcpClients {
		names = append(names, name)
	}
	sort.Strings(names)

	pending := make([]tool.Tool, 0)
	pendingNames := make(map[string]struct{})
	for _, name := range names {
		client := h.mcpClients[name]
		if err := client.Initialize(ctx); err != nil {
			return fmt.Errorf("initialize MCP client %q: %w", name, err)
		}
		definitions, err := client.ToToolDefinitions(ctx)
		if err != nil {
			return fmt.Errorf("get tools from MCP client %q: %w", name, err)
		}
		for _, definition := range definitions {
			adapter := &mcpToolAdapter{clientName: name, client: client, definition: definition}
			adapterName := adapter.Name()
			if _, exists := h.toolRegistry.Get(adapterName); exists {
				return fmt.Errorf("register MCP tool: tool %q already exists", adapterName)
			}
			if _, duplicate := pendingNames[adapterName]; duplicate {
				return fmt.Errorf("register MCP tool: duplicate discovered tool %q", adapterName)
			}
			pendingNames[adapterName] = struct{}{}
			pending = append(pending, adapter)
		}
	}

	registered := make([]string, 0, len(pending))
	for _, runtimeTool := range pending {
		if err := h.RegisterTool(runtimeTool); err != nil {
			for _, name := range registered {
				h.toolRegistry.Unregister(name)
			}
			return fmt.Errorf("register MCP tool %q: %w", runtimeTool.Name(), err)
		}
		registered = append(registered, runtimeTool.Name())
	}
	return nil
}

func (h *Harness) RunAgent(ctx context.Context, name, input string, options ...engine.RunOption) *engine.RunOutput {
	runtimeAgent, ok := h.Agent(name)
	if !ok {
		err := types.NewError(types.ErrInvalidConfig, fmt.Sprintf("agent %q not found", name))
		return &engine.RunOutput{Success: false, Error: err.Error(), Err: err}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		runErr := types.WrapError(types.ErrInvalidInput, "input validation failed", err)
		return &engine.RunOutput{Success: false, Error: runErr.Error(), Err: runErr}
	}
	output := runtimeAgent.Run(ctx, validatedInput, options...)
	if output.Success {
		if err := h.ValidateOutput(output.Content); err != nil {
			runErr := types.WrapError(types.ErrInvalidInput, "output validation failed", err)
			output.Success = false
			output.Error = runErr.Error()
			output.Err = runErr
			return output
		}
		output.Content = h.Sanitize(output.Content)
	}
	return output
}

func (h *Harness) RunTeam(ctx context.Context, name, input string) *workflow.TeamOutput {
	team, ok := h.Team(name)
	if !ok {
		err := types.NewError(types.ErrInvalidConfig, fmt.Sprintf("team %q not found", name))
		return &workflow.TeamOutput{Success: false, Error: err.Error(), Err: err}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		runErr := types.WrapError(types.ErrInvalidInput, "input validation failed", err)
		return &workflow.TeamOutput{Success: false, Error: runErr.Error(), Err: runErr}
	}
	output := team.Run(ctx, validatedInput)
	if output.Success {
		if err := h.ValidateOutput(output.FinalOutput); err != nil {
			runErr := types.WrapError(types.ErrInvalidInput, "output validation failed", err)
			output.Success = false
			output.Error = runErr.Error()
			output.Err = runErr
			return output
		}
		output.FinalOutput = h.Sanitize(output.FinalOutput)
	}
	return output
}

func (h *Harness) RunWorkflow(ctx context.Context, name, input string) *workflow.WorkflowResult {
	runtimeWorkflow, ok := h.Workflow(name)
	if !ok {
		err := types.NewError(types.ErrInvalidConfig, fmt.Sprintf("workflow %q not found", name))
		return &workflow.WorkflowResult{Success: false, Error: err.Error(), Err: err}
	}
	validatedInput := h.Sanitize(input)
	if err := h.ValidateInput(validatedInput); err != nil {
		runErr := types.WrapError(types.ErrInvalidInput, "input validation failed", err)
		return &workflow.WorkflowResult{Success: false, Error: runErr.Error(), Err: runErr}
	}
	result := runtimeWorkflow.Run(ctx, validatedInput)
	if result.Success {
		if err := h.ValidateOutput(result.Output); err != nil {
			runErr := types.WrapError(types.ErrInvalidInput, "output validation failed", err)
			result.Success = false
			result.Error = runErr.Error()
			result.Err = runErr
			return result
		}
		result.Output = h.Sanitize(result.Output)
	}
	return result
}

func (h *Harness) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	names := make([]string, 0, len(h.mcpClients))
	for name := range h.mcpClients {
		names = append(names, name)
	}
	sort.Strings(names)
	closeErrors := make([]error, 0)
	for _, name := range names {
		if err := h.mcpClients[name].Close(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close MCP client %q: %w", name, err))
		}
	}
	return errors.Join(closeErrors...)
}

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
	result, err := adapter.client.CallToolResult(ctx, adapter.definition.Name, arguments)
	if err != nil {
		return nil, err
	}
	if result.StructuredContent != nil {
		return result.StructuredContent, nil
	}
	texts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if content.Type == "text" {
			texts = append(texts, content.Text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n"), nil
	}
	return result.Content, nil
}
