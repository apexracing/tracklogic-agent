package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/tool/builtin"
	"github.com/apexracing/tracklogic-agent/types"
)

type toolThenAnswerModel struct {
	calls int
}

func (*toolThenAnswerModel) Provider() string { return "test" }
func (*toolThenAnswerModel) ModelID() string  { return "tool-then-answer" }

func (m *toolThenAnswerModel) Invoke(context.Context, *model.InvokeRequest) (*model.InvokeResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &model.InvokeResponse{ToolCalls: []types.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      "calculator",
				Arguments: `{"expression":"2 + 2"}`,
			},
		}}}, nil
	}
	return &model.InvokeResponse{Content: "4"}, nil
}

func (m *toolThenAnswerModel) InvokeStream(ctx context.Context, req *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	response, err := m.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan model.ResponseChunk, 1)
	chunks <- model.ResponseChunk{Content: response.Content, Done: true, FinishReason: "stop"}
	close(chunks)
	return chunks, nil
}

func TestAgentRunReturnsToolCallHistory(t *testing.T) {
	registry := tool.NewRegistry()
	registry.MustRegister(builtin.NewCalculator())
	runtimeAgent := NewAgent(AgentConfig{
		Name:         "tool-history",
		Model:        &toolThenAnswerModel{},
		ToolRegistry: registry,
		MaxLoops:     3,
	})

	output := runtimeAgent.Run(context.Background(), "calculate")
	if !output.Success {
		t.Fatalf("run failed: %s", output.Error)
	}
	if len(output.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(output.ToolCalls))
	}
	if output.ToolCalls[0].Function.Name != "calculator" {
		t.Fatalf("tool name = %q, want calculator", output.ToolCalls[0].Function.Name)
	}
}

func TestAgentToolScopeLimitsVisibilityAndExecution(t *testing.T) {
	registry := tool.NewRegistry()
	registry.MustRegister(builtin.NewCalculator())
	registry.MustRegister(builtin.NewJSONParse())
	runtimeAgent := NewAgent(AgentConfig{
		Name: "scoped", Model: model.NewMock("scoped"), ToolRegistry: registry,
		RestrictTools: true, AllowedTools: []string{"calculator"},
	})

	definitions := runtimeAgent.buildToolDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "calculator" {
		t.Fatalf("buildToolDefinitions() = %+v, want calculator only", definitions)
	}
	_, err := runtimeAgent.executeToolCall(context.Background(), types.ToolCall{
		Function: types.ToolCallFunction{Name: "json_parse", Arguments: `{"text":"{}"}`},
	})
	var harnessErr *types.HarnessError
	if !errors.As(err, &harnessErr) || harnessErr.Code != types.ErrSecurityViolation {
		t.Fatalf("executeToolCall() error = %v, want SECURITY_VIOLATION", err)
	}
}
