package agent

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/apexracing/tracklogic-agent/mcp"
)

func TestNewNormalizesModelConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = " MOCK "
	cfg.DefaultModel.Vendor = " TEST "

	harness, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := harness.Config().DefaultModel.APIFormat; got != "mock" {
		t.Fatalf("api format = %q, want mock", got)
	}
	if got := harness.Config().DefaultModel.Vendor; got != "test" {
		t.Fatalf("vendor = %q, want test", got)
	}
}

func TestHarnessOwnsConfigCollections(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.AllowedTools = []string{"calculator"}
	cfg.MCPClients = []MCPClientConfig{{Name: "catalog", BaseURL: "https://example.com"}}

	harness, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cfg.AllowedTools[0] = "http_get"
	cfg.MCPClients[0].Name = "changed"

	snapshot := harness.Config()
	if snapshot.AllowedTools[0] != "calculator" || snapshot.MCPClients[0].Name != "catalog" {
		t.Fatalf("Config() changed through caller input: %+v", snapshot)
	}
	snapshot.AllowedTools[0] = "write_file"
	snapshot.MCPClients[0].Name = "mutated-copy"
	second := harness.Config()
	if second.AllowedTools[0] != "calculator" || second.MCPClients[0].Name != "catalog" {
		t.Fatalf("Config() exposed internal slices: %+v", second)
	}
}

func TestConfigValidateRejectsInvalidProductionValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "log level", mutate: func(c *Config) { c.LogLevel = "verbose" }, want: "log_level"},
		{name: "config name whitespace", mutate: func(c *Config) { c.Name = " service " }, want: "config name"},
		{name: "model id whitespace", mutate: func(c *Config) { c.DefaultModel.ModelID = " model " }, want: "model_id"},
		{name: "negative model timeout", mutate: func(c *Config) { c.DefaultModel.Timeout = -1 }, want: "timeout_seconds"},
		{name: "memory implementation", mutate: func(c *Config) { c.MemoryConfig.Type = "summary" }, want: "memory.type"},
		{name: "permission mode", mutate: func(c *Config) { c.PermissionMode = "typo" }, want: "permission_mode"},
		{name: "model URL", mutate: func(c *Config) { c.DefaultModel.BaseURL = "file:///tmp/model" }, want: "base_url"},
		{name: "duplicate tool", mutate: func(c *Config) { c.AllowedTools = []string{"calculator", "calculator"} }, want: "duplicate"},
		{name: "unknown tool", mutate: func(c *Config) { c.AllowedTools = []string{"missing"} }, want: "unknown"},
		{name: "duplicate MCP client", mutate: func(c *Config) {
			c.MCPClients = []MCPClientConfig{{Name: "catalog", BaseURL: "https://example.com"}, {Name: "catalog", BaseURL: "https://example.org"}}
		}, want: "duplicate"},
		{name: "invalid MCP client name", mutate: func(c *Config) {
			c.MCPClients = []MCPClientConfig{{Name: "live catalog", BaseURL: "https://example.com"}}
		}, want: "mcp client name"},
		{name: "MCP protocol", mutate: func(c *Config) {
			c.MCPClients = []MCPClientConfig{{Name: "catalog", BaseURL: "https://example.com", ProtocolVersion: "2099-01-01"}}
		}, want: "protocol_version"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DefaultModel.APIFormat = "mock"
			test.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestNewRejectsNegativeModelTimeoutBeforeDefaulting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.DefaultModel.Timeout = -1
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Fatalf("New() error = %v, want negative timeout rejection", err)
	}
}

func TestBuildModelValidatesDirectCalls(t *testing.T) {
	for _, cfg := range []ModelConfig{
		{APIFormat: "mock", ModelID: " "},
		{APIFormat: "mock", ModelID: "model", Timeout: -1},
		{APIFormat: "mock", ModelID: "model", BaseURL: "file:///tmp/model"},
	} {
		if _, err := cfg.BuildModel(); err == nil {
			t.Fatalf("BuildModel(%+v) error = nil", cfg)
		}
	}
	if _, err := (ModelConfig{APIFormat: " MOCK ", ModelID: "model"}).BuildModel(); err != nil {
		t.Fatalf("BuildModel() rejected normalizable config: %v", err)
	}
}

func TestNewDoesNotReplaceProcessDefaultLogger(t *testing.T) {
	before := slog.Default()
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.DefaultModel.ModelID = "mock"

	if _, err := New(cfg); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if after := slog.Default(); after != before {
		t.Fatal("New() replaced slog.Default; a library must not mutate process-global logging")
	}
}

func TestWithMCPClientInjectsRuntimeClient(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	client := mcp.NewClient("https://catalog.example", time.Second)

	harness, err := New(cfg, WithMCPClient("catalog", client))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := harness.mcpClients["catalog"]; got != client {
		t.Fatal("injected MCP client was not retained")
	}
}

func TestWithMCPClientRejectsInvalidDependency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	if _, err := New(cfg, WithMCPClient("", nil)); err == nil {
		t.Fatal("New() error = nil, want invalid injected MCP client error")
	}
}
