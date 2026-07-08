package harness

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go-harness-tutorial/internal/model"
)

type Config struct {
	Version        string             `json:"version"`
	Name           string             `json:"name"`
	LogLevel       string             `json:"log_level"`
	DefaultModel   ModelConfig        `json:"default_model"`
	AllowedTools   []string           `json:"allowed_tools"`
	MemoryConfig   MemoryConfig       `json:"memory"`
	PermissionMode string             `json:"permission_mode"` // strict, permissive
	MCPClients     []MCPClientConfig  `json:"mcp_clients,omitempty"`
	Security       SecurityConfig     `json:"security"`
}

type ModelConfig struct {
	Provider string  `json:"provider"`
	ModelID  string  `json:"model_id"`
	APIKey   string  `json:"api_key,omitempty"`
	BaseURL  string  `json:"base_url,omitempty"`
	Timeout  int     `json:"timeout_seconds"`
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
	MaxInputLength    int      `json:"max_input_length"`
	MaxOutputLength   int      `json:"max_output_length"`
	EnableInjectionCheck bool  `json:"enable_injection_check"`
	SanitizePII       bool     `json:"sanitize_pii"`
}

func DefaultConfig() Config {
	return Config{
		Version:  "1.0.0",
		Name:     "MiniHarness-Go",
		LogLevel: "info",
		DefaultModel: ModelConfig{
			Provider: "openai",
			ModelID:  "gpt-4o-mini",
			Timeout:  60,
		},
		MemoryConfig: MemoryConfig{
			Type:     "buffer",
			Capacity: 50,
		},
		PermissionMode: "permissive",
		Security: SecurityConfig{
			MaxInputLength:    10000,
			MaxOutputLength:   50000,
			EnableInjectionCheck: false,
			SanitizePII:       false,
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

func (c ModelConfig) BuildModel() (model.Model, error) {
	timeout := time.Duration(c.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	switch c.Provider {
	case "openai":
		return model.NewOpenAI(model.OpenAIConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
		}), nil
	case "deepseek":
		return model.NewDeepSeek(model.OpenAIConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
			ModelID: c.ModelID,
			Timeout: timeout,
		}), nil
	case "mock":
		return model.NewMock(c.ModelID), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", c.Provider)
	}
}
