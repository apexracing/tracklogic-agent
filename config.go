package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/apexracing/tracklogic-agent/mcp"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/types"
)

type Config struct {
	Version        string            `json:"version"`
	Name           string            `json:"name"`
	LogLevel       string            `json:"log_level"`
	DefaultModel   ModelConfig       `json:"default_model"`
	AllowedTools   []string          `json:"allowed_tools"`
	MemoryConfig   MemoryConfig      `json:"memory"`
	PermissionMode string            `json:"permission_mode"` // strict, permissive
	MCPClients     []MCPClientConfig `json:"mcp_clients,omitempty"`
	Security       SecurityConfig    `json:"security"`
}

// ModelConfig describes which vendor and API protocol to use.
// Vendor and APIFormat are independent: BuildModel selects the client by
// APIFormat only; BaseURL/APIKey/ModelID come from config as-is.
type ModelConfig struct {
	Vendor    string `json:"vendor"`     // deepseek / openai / anthropic / custom (logging / identity)
	APIFormat string `json:"api_format"` // openai_response / openai_chat_completions / anthropic_message / mock
	BaseURL   string `json:"base_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	ModelID   string `json:"model_id"`
	Timeout   int    `json:"timeout_seconds"`
}

type MemoryConfig struct {
	Type     string `json:"type"` // buffer; reserved for future memory implementations
	Capacity int    `json:"capacity"`
}

type MCPClientConfig struct {
	Name            string `json:"name"`
	BaseURL         string `json:"base_url"`
	Timeout         int    `json:"timeout_seconds"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type SecurityConfig struct {
	MaxInputLength       int  `json:"max_input_length"`
	MaxOutputLength      int  `json:"max_output_length"`
	EnableInjectionCheck bool `json:"enable_injection_check"`
	SanitizePII          bool `json:"sanitize_pii"`
}

func DefaultConfig() Config {
	return Config{
		Version:  "0.2.0",
		Name:     "tracklogic-agent",
		LogLevel: "info",
		DefaultModel: ModelConfig{
			Vendor:    "openai",
			APIFormat: "openai_response",
			ModelID:   "gpt-4o-mini",
			Timeout:   60,
		},
		MemoryConfig: MemoryConfig{
			Type:     "buffer",
			Capacity: 50,
		},
		PermissionMode: "permissive",
		Security: SecurityConfig{
			MaxInputLength:       10000,
			MaxOutputLength:      50000,
			EnableInjectionCheck: false,
			SanitizePII:          false,
		},
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("config file not found, using defaults", "path", path)
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

// ResolveModelDefaults normalizes vendor/api_format and timeout.
// BaseURL is never inferred from vendor; configure it explicitly because
// vendor and api_format are independent.
func ResolveModelDefaults(cfg *ModelConfig) {
	if cfg == nil {
		return
	}
	cfg.Vendor = strings.ToLower(strings.TrimSpace(cfg.Vendor))
	cfg.APIFormat = strings.ToLower(strings.TrimSpace(cfg.APIFormat))
	if cfg.Timeout == 0 {
		cfg.Timeout = 60
	}
}

// Validate rejects ambiguous or unsupported production configuration before
// the Harness starts accepting work.
func (c Config) Validate() error {
	if c.Name == "" || c.Name != strings.TrimSpace(c.Name) {
		return types.NewError(types.ErrInvalidConfig, "config name must be non-empty and must not have surrounding whitespace")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("unsupported log_level %q", c.LogLevel))
	}
	switch c.DefaultModel.APIFormat {
	case "openai_response", "openai_chat_completions", "anthropic_message", "mock":
	default:
		return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("unsupported api_format %q", c.DefaultModel.APIFormat))
	}
	if c.DefaultModel.ModelID == "" || c.DefaultModel.ModelID != strings.TrimSpace(c.DefaultModel.ModelID) {
		return types.NewError(types.ErrInvalidConfig, "default_model.model_id must be non-empty and must not have surrounding whitespace")
	}
	if c.DefaultModel.Timeout < 0 {
		return types.NewError(types.ErrInvalidConfig, "default_model.timeout_seconds must not be negative")
	}
	if c.DefaultModel.BaseURL != "" {
		if err := validateHTTPURL(c.DefaultModel.BaseURL); err != nil {
			return types.WrapError(types.ErrInvalidConfig, "invalid default_model.base_url", err)
		}
	}
	if c.MemoryConfig.Type != "" && c.MemoryConfig.Type != "buffer" {
		return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("unsupported memory.type %q", c.MemoryConfig.Type))
	}
	if c.MemoryConfig.Capacity < 0 {
		return types.NewError(types.ErrInvalidConfig, "memory.capacity must not be negative")
	}
	if c.PermissionMode != "strict" && c.PermissionMode != "permissive" {
		return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("unsupported permission_mode %q", c.PermissionMode))
	}
	if c.Security.MaxInputLength < 0 || c.Security.MaxOutputLength < 0 {
		return types.NewError(types.ErrInvalidConfig, "security length limits must not be negative")
	}

	builtinNames := map[string]struct{}{
		"calculator": {}, "read_file": {}, "write_file": {}, "get_current_time": {},
		"list_dir": {}, "http_get": {}, "json_parse": {},
	}
	seenTools := make(map[string]struct{}, len(c.AllowedTools))
	for _, name := range c.AllowedTools {
		if _, ok := builtinNames[name]; !ok {
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("unknown allowed_tools entry %q", name))
		}
		if _, duplicate := seenTools[name]; duplicate {
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("duplicate allowed_tools entry %q", name))
		}
		seenTools[name] = struct{}{}
	}

	seenMCP := make(map[string]struct{}, len(c.MCPClients))
	for _, client := range c.MCPClients {
		if err := validateMCPClientName(client.Name); err != nil {
			return types.WrapError(types.ErrInvalidConfig, "invalid mcp client name", err)
		}
		if _, duplicate := seenMCP[client.Name]; duplicate {
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("duplicate mcp client name %q", client.Name))
		}
		seenMCP[client.Name] = struct{}{}
		if err := validateHTTPURL(client.BaseURL); err != nil {
			return types.WrapError(types.ErrInvalidConfig, fmt.Sprintf("invalid mcp client %q base_url", client.Name), err)
		}
		if client.Timeout < 0 {
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("mcp client %q timeout_seconds must not be negative", client.Name))
		}
		if client.ProtocolVersion != "" && !mcp.IsSupportedProtocolVersion(client.ProtocolVersion) {
			return types.NewError(types.ErrInvalidConfig, fmt.Sprintf("mcp client %q has unsupported protocol_version %q", client.Name, client.ProtocolVersion))
		}
	}
	return nil
}

func validateMCPClientName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("name %q may contain only letters, digits, '_' and '-'", name)
	}
	return nil
}

func validateHTTPURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("URL host is required")
	}
	return nil
}

func (c ModelConfig) BuildModel() (model.Model, error) {
	return c.BuildModelWithLogger(nil)
}

// BuildModelWithLogger builds a model using the application-owned logger.
// The same logger is propagated to the protocol adapter and the model I/O
// wrapper, so dynamically selected models use the same logging pipeline as
// Harness, Agent, tools and MCP clients.
func (c ModelConfig) BuildModelWithLogger(logger *slog.Logger) (model.Model, error) {
	ResolveModelDefaults(&c)
	if c.ModelID == "" || c.ModelID != strings.TrimSpace(c.ModelID) {
		return nil, types.NewError(types.ErrInvalidConfig, "model_id must be non-empty and must not have surrounding whitespace")
	}
	if c.Timeout < 0 {
		return nil, types.NewError(types.ErrInvalidConfig, "timeout_seconds must not be negative")
	}
	if c.BaseURL != "" {
		if err := validateHTTPURL(c.BaseURL); err != nil {
			return nil, types.WrapError(types.ErrInvalidConfig, "invalid base_url", err)
		}
	}
	return c.buildModel(logger)
}

func (c ModelConfig) buildModel(logger *slog.Logger) (model.Model, error) {
	timeout := time.Duration(c.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	var runtimeModel model.Model
	var err error
	switch c.APIFormat {
	case "openai_response":
		runtimeModel = model.NewOpenAI(model.OpenAIConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
			Logger:  logger,
		})
	case "openai_chat_completions":
		runtimeModel = model.NewOpenAIChat(model.OpenAIConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
			Logger:  logger,
		})
	case "anthropic_message":
		runtimeModel = model.NewAnthropic(model.AnthropicConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
			Logger:  logger,
		})
	case "mock":
		runtimeModel = model.NewMock(c.ModelID)
	default:
		err = fmt.Errorf("unsupported api_format: %s", c.APIFormat)
	}
	if err != nil {
		return nil, err
	}
	return model.WithLogger(runtimeModel, logger), nil
}
