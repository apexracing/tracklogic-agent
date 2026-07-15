package agent_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	agent "github.com/apexracing/tracklogic-agent"
	"github.com/apexracing/tracklogic-agent/engine"
)

func TestRealModelFromEnvironment(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("TRACKLOGIC_AGENT_API_KEY"))
	if apiKey == "" {
		t.Skip("TRACKLOGIC_AGENT_API_KEY is not set")
	}

	cfg, err := agent.LoadConfig("examples/config.example.json")
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	cfg.DefaultModel.APIKey = apiKey
	cfg.AllowedTools = nil
	agent.ResolveModelDefaults(&cfg.DefaultModel)

	harness, err := agent.New(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer harness.Close()

	runtimeAgent := harness.NewAgent("real-api-smoke", "只用一句简短中文回答。")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Reasoning models may spend part of the output budget on a thinking block
	// before emitting the final text, so keep this smoke-test budget practical.
	result := runtimeAgent.Run(ctx, "回复：连接成功", engine.WithMaxLoops(1), engine.WithMaxTokens(512))
	if !result.Success {
		t.Fatalf("real API request failed: %s", result.Error)
	}
	if strings.TrimSpace(result.Content) == "" {
		t.Fatal("real API returned empty content")
	}
}
