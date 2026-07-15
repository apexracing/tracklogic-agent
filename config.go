package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/apexracing/tracklogic-agent/model"
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
	Type     string `json:"type"` // buffer, summary
	Capacity int    `json:"capacity"`
}

type MCPClientConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Timeout int    `json:"timeout_seconds"`
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
// BaseURL is never inferred from vendor ? configure it explicitly (vendor and api_format are independent).
func ResolveModelDefaults(cfg *ModelConfig) {
	if cfg == nil {
		return
	}
	cfg.Vendor = strings.ToLower(strings.TrimSpace(cfg.Vendor))
	cfg.APIFormat = strings.ToLower(strings.TrimSpace(cfg.APIFormat))
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
	}
}

func (c ModelConfig) BuildModel() (model.Model, error) {
	timeout := time.Duration(c.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	switch c.APIFormat {
	case "openai_response":
		return model.NewOpenAI(model.OpenAIConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
		}), nil
	case "openai_chat_completions":
		return model.NewOpenAIChat(model.OpenAIConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
		}), nil
	case "anthropic_message":
		return model.NewAnthropic(model.AnthropicConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
		}), nil
	case "mock":
		return model.NewMock(c.ModelID), nil
	default:
		return nil, fmt.Errorf("unsupported api_format: %s", c.APIFormat)
	}
}
