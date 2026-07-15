package agent_test

import (
	"context"
	"strings"
	"testing"

	agent "github.com/apexracing/tracklogic-agent"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/types"
)

type staticModel struct{}

func (staticModel) Provider() string { return "custom" }
func (staticModel) ModelID() string  { return "static-test" }

func (staticModel) Invoke(_ context.Context, req *model.InvokeRequest) (*model.InvokeResponse, error) {
	return &model.InvokeResponse{Content: "custom:" + latestUser(req.Messages)}, nil
}

func (m staticModel) InvokeStream(ctx context.Context, req *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	response, err := m.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan model.ResponseChunk, 1)
	chunks <- model.ResponseChunk{Content: response.Content, FinishReason: "stop", Done: true}
	close(chunks)
	return chunks, nil
}

func latestUser(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleUser {
			return messages[i].Content
		}
	}
	return ""
}

type recordingMemory struct {
	messages []types.Message
}

func (m *recordingMemory) Add(message types.Message) { m.messages = append(m.messages, message) }
func (m *recordingMemory) Clear()                    { m.messages = nil }
func (m *recordingMemory) Len() int                  { return len(m.messages) }

func (m *recordingMemory) Get(index int) (types.Message, bool) {
	if index < 0 || index >= len(m.messages) {
		return types.Message{}, false
	}
	return m.messages[index], true
}

func (m *recordingMemory) Recent(count int) []types.Message {
	if count <= 0 {
		return nil
	}
	if count > len(m.messages) {
		count = len(m.messages)
	}
	return append([]types.Message(nil), m.messages[len(m.messages)-count:]...)
}

func (m *recordingMemory) Snapshot() []types.Message {
	return append([]types.Message(nil), m.messages...)
}

type upperTool struct {
	tool.BaseTool
}

func newUpperTool() *upperTool {
	return &upperTool{BaseTool: tool.NewBaseTool("upper", "uppercase text", []model.ToolParameter{{
		Name: "text", Type: "string", Description: "text to convert", Required: true,
	}})}
}

func (*upperTool) Execute(_ context.Context, args map[string]any) (any, error) {
	return strings.ToUpper(args["text"].(string)), nil
}

func TestPublicExtensionInterfaces(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(newUpperTool()); err != nil {
		t.Fatalf("register custom tool: %v", err)
	}

	memory := &recordingMemory{}
	runtimeAgent := agent.NewAgent(agent.AgentConfig{
		Name:         "custom-agent",
		SystemPrompt: "Reply deterministically.",
		Model:        staticModel{},
		ToolRegistry: registry,
		Memory:       memory,
	})

	var streamed strings.Builder
	result := runtimeAgent.Run(context.Background(), "hello",
		agent.WithMaxLoops(2),
		agent.WithStream(func(chunk string) { streamed.WriteString(chunk) }),
		agent.WithTemperature(0.2),
		agent.WithMaxTokens(128),
	)
	if !result.Success || result.Content != "custom:hello" {
		t.Fatalf("unexpected run result: %+v", result)
	}
	if streamed.String() != result.Content {
		t.Fatalf("streamed %q, want %q", streamed.String(), result.Content)
	}
	if memory.Len() < 2 {
		t.Fatalf("custom memory received %d messages, want at least 2", memory.Len())
	}
}

func TestRootHarnessTeamAndWorkflow(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.DefaultModel.ModelID = "public-api-test"

	harness, err := agent.New(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	first := harness.NewAgent("first", "")
	second := harness.NewAgent("second", "")

	team := agent.NewTeam(agent.TeamConfig{
		Name:   "test-team",
		Mode:   agent.ModeSequential,
		Agents: []*agent.Agent{first, second},
	})
	teamResult := team.Run(context.Background(), "team input")
	if !teamResult.Success {
		t.Fatalf("team run failed: %s", teamResult.Error)
	}

	workflow := agent.NewWorkflow(agent.WorkflowConfig{Name: "test-workflow"})
	workflow.AddNode(agent.NewStepNode("answer", first))
	workflowResult := workflow.Run(context.Background(), "workflow input")
	if !workflowResult.Success {
		t.Fatalf("workflow run failed: %s", workflowResult.Error)
	}
}
